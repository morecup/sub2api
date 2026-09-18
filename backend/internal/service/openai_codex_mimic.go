package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/google/uuid"
)

// 真实 Codex Desktop App 固定头值（2026-09-17 Work 0.155 实抓）。
const (
	// codexBetaFeaturesValue 对应 x-codex-beta-features 头（实抓：Desktop App 恒定发送该值）。
	codexBetaFeaturesValue = "realtime_conversation,remote_compaction_v2"
	// codexTurnMetadataSandbox 对应 HTTP POST x-codex-turn-metadata.sandbox 字段（实抓：Desktop App HTTP POST 为 none）。
	codexTurnMetadataSandbox = "none"
	// codexDesktopThreadSource 对应普通用户 turn/prewarm metadata 的 thread_source。
	codexDesktopThreadSource = "user"
	// codexDesktopAgentName 是根任务的 agent_name；若入站 metadata 携带合法的
	// 子 agent 路径则保留入站值。
	codexDesktopAgentName = "/root"
	// codexDesktopTurnTrigger 是普通用户 turn 的触发来源。
	codexDesktopTurnTrigger = "composer"
	// codexTurnMetadataSandboxMode 是本地无沙箱用户 turn 的新版 sandbox_mode。
	codexTurnMetadataSandboxMode = "danger-full-access"
	// codexResponsesLiteValue 对应 x-openai-internal-codex-responses-lite 头
	// （上游仅对 responses lite 模型发送该头，见 codexResponsesLiteModels）。
	codexResponsesLiteValue = "true"
	// codexDesktopOriginator 对应 originator 头（实抓：Desktop App 为 "Codex Desktop"）。
	codexDesktopOriginator = "Codex Desktop"
	// codexInstallationID 对应 x-codex-turn-metadata.installation_id 字段的兜底值（实抓固定值），
	// 正常路径按账号确定性派生，仅在无法派生时使用。
	codexInstallationID = "00e9ffcb-88d7-4ee8-aeca-1982d91a1330"
	// Windows 桌面端 attestation signals。以下值来自同一次 0.151 实抓，
	// 必须与 WebView 的 oai-language / accept-language 保持为同一设备画像。
	codexAttestationBundleID      = "com.openai.codex"
	codexAttestationLanguage      = "zh-CN"
	codexAttestationTimezone      = "Asia/Shanghai"
	codexAttestationScreenSizeSum = 4880
	codexAttestationScreenScale   = 1.0

	codexSessionUUIDCacheTTL        = 24 * time.Hour
	codexSessionUUIDCacheMaxEntries = 8192
	codexUUIDV7MinUnixMilli         = int64(1577836800000) // 2020-01-01T00:00:00Z
)

// codexDeviceProfile 是一台 Codex Desktop 安装在当前应用进程中的稳定画像。
// installation_id 是账号稳定的 UUIDv4 外形；app_session_id 是进程启动后为该账号
// 随机生成的 UUIDv4。二者通过同一缓存取得，避免 metadata 与 attestation 串台。
type codexDeviceProfile struct {
	InstallationID string
	AppSessionID   string
	Languages      []string
	Locale         string
	Timezone       string
	ScreenSizeSum  uint64
	ScreenScale    float64
	Attestation    string
}

var codexDeviceProfiles = struct {
	sync.Mutex
	byAccount map[string]*codexDeviceProfile
	fallback  *codexDeviceProfile
}{
	byAccount: make(map[string]*codexDeviceProfile),
}

// codexOAIAttestation 保留无账号调用方使用的进程级兜底值。
var codexOAIAttestation = codexDeviceProfileForAccount(0, "").Attestation

// codexResponsesLiteModels 为 responses lite 模型名单（use_responses_lite=true）。
// 来源：2026-09-17 的 0.155 WS/HTTP 矩阵，sol/astra/terra/luna=true、5.5=false。
// 名单外的非 gpt-6-* 模型（gpt-5.5、gpt-5.4、gpt-5.4-mini、
// gpt-5.2、codex-auto-review 等）均为 false。
var codexResponsesLiteModels = map[string]bool{
	"gpt-5.6-sol":   true,
	"gpt-5.6-terra": true,
	"gpt-5.6-luna":  true,
}

// isCodexResponsesLiteModel 判定模型是否为 responses lite 模型。
// 大小写/空白归一后，gpt-6-* 按前缀匹配，其他模型使用精确白名单。
func isCodexResponsesLiteModel(model string) bool {
	normalizedModel := strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(normalizedModel, "gpt-6-") || codexResponsesLiteModels[normalizedModel]
}

func appendCodexCBORHead(dst []byte, major byte, value uint64) []byte {
	switch {
	case value < 24:
		return append(dst, major<<5|byte(value))
	case value <= math.MaxUint8:
		return append(dst, major<<5|24, byte(value))
	case value <= math.MaxUint16:
		dst = append(dst, major<<5|25)
		return binary.BigEndian.AppendUint16(dst, uint16(value))
	case value <= math.MaxUint32:
		dst = append(dst, major<<5|26)
		return binary.BigEndian.AppendUint32(dst, uint32(value))
	default:
		dst = append(dst, major<<5|27)
		return binary.BigEndian.AppendUint64(dst, value)
	}
}

func appendCodexCBORUnsigned(dst []byte, value uint64) []byte {
	return appendCodexCBORHead(dst, 0, value)
}

func appendCodexCBORText(dst []byte, value string) []byte {
	dst = appendCodexCBORHead(dst, 3, uint64(len(value)))
	return append(dst, value...)
}

func appendCodexCBORBytes(dst, value []byte) []byte {
	dst = appendCodexCBORHead(dst, 2, uint64(len(value)))
	return append(dst, value...)
}

func appendCodexCBORFloat64(dst []byte, value float64) []byte {
	dst = append(dst, 0xfb)
	return binary.BigEndian.AppendUint64(dst, math.Float64bits(value))
}

func buildCodexOAIAttestation(profile *codexDeviceProfile) string {
	if profile == nil {
		return ""
	}
	// signals: {0:schema,1:languages,2:locale,3:timezone,4:screen sum,5:scale,6:session}
	signals := appendCodexCBORHead(nil, 5, 7)
	signals = appendCodexCBORUnsigned(signals, 0)
	signals = appendCodexCBORUnsigned(signals, 1)
	signals = appendCodexCBORUnsigned(signals, 1)
	signals = appendCodexCBORHead(signals, 4, uint64(len(profile.Languages)))
	for _, language := range profile.Languages {
		signals = appendCodexCBORText(signals, language)
	}
	signals = appendCodexCBORUnsigned(signals, 2)
	signals = appendCodexCBORText(signals, profile.Locale)
	signals = appendCodexCBORUnsigned(signals, 3)
	signals = appendCodexCBORText(signals, profile.Timezone)
	signals = appendCodexCBORUnsigned(signals, 4)
	signals = appendCodexCBORUnsigned(signals, profile.ScreenSizeSum)
	signals = appendCodexCBORUnsigned(signals, 5)
	signals = appendCodexCBORFloat64(signals, profile.ScreenScale)
	signals = appendCodexCBORUnsigned(signals, 6)
	signals = appendCodexCBORText(signals, profile.AppSessionID)

	payload := appendCodexCBORHead(nil, 5, 3)
	payload = appendCodexCBORText(payload, "error_code")
	payload = appendCodexCBORUnsigned(payload, 1)
	payload = appendCodexCBORText(payload, "bundle_id")
	payload = appendCodexCBORText(payload, codexAttestationBundleID)
	payload = appendCodexCBORText(payload, "f")
	payload = appendCodexCBORBytes(payload, signals)

	header, _ := json.Marshal(struct {
		Version int    `json:"v"`
		Status  int    `json:"s"`
		Token   string `json:"t"`
	}{
		Version: 1,
		Status:  0,
		Token:   "v1." + base64.RawURLEncoding.EncodeToString(payload),
	})
	return string(header)
}

// codexMimicStripInboundHeaders 列出可能经请求头白名单（openaiAllowedHeaders /
// openaiPassthroughAllowedHeaders）无条件透传进来、但真实 Codex CLI HTTP POST 并不发送的头。
// 在伪装时统一删除，确保上游请求头是与 Codex 一致的“固定集合”，不被入站客户端污染。
//
// 不包含 x-stainless-* / *-timeout 等超时头：它们由运营级开关
// gateway.openai_passthrough_allow_timeout_headers 显式控制（默认关闭即不透传），
// 开启时属于运营方主动选择转发，伪装层不应越权删除。
// 注：必须使用小写键，net/http 在 HTTP/2 下按小写规范化，且 Header.Del 本身大小写不敏感。
var codexMimicStripInboundHeaders = []string{
	"accept-language",
	"x-codex-turn-state",
}

// codexAccountSeed 返回账号维度派生种子：优先 sub2api 账号 ID，其次上游
// chatgpt-account-id；均不可用时返回空串（由调用方决定回退行为）。
func codexAccountSeed(accountID int64, chatgptAccountID string) string {
	if accountID > 0 {
		return fmt.Sprintf("account:%d", accountID)
	}
	if trimmed := strings.TrimSpace(chatgptAccountID); trimmed != "" {
		return "chatgpt:" + trimmed
	}
	return ""
}

func codexUUIDv4FromSeed(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	var value uuid.UUID
	copy(value[:], sum[:16])
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return value.String()
}

func newCodexDeviceProfile(seed string) *codexDeviceProfile {
	installationID := codexInstallationID
	if seed != "" {
		installationID = codexUUIDv4FromSeed("sub2api:codex-installation:" + seed)
	}
	profile := &codexDeviceProfile{
		InstallationID: installationID,
		AppSessionID:   uuid.NewString(),
		Languages:      []string{codexAttestationLanguage},
		Locale:         codexAttestationLanguage,
		Timezone:       codexAttestationTimezone,
		ScreenSizeSum:  codexAttestationScreenSizeSum,
		ScreenScale:    codexAttestationScreenScale,
	}
	profile.Attestation = buildCodexOAIAttestation(profile)
	return profile
}

func codexDeviceProfileForAccount(accountID int64, chatgptAccountID string) *codexDeviceProfile {
	seed := codexAccountSeed(accountID, chatgptAccountID)
	codexDeviceProfiles.Lock()
	defer codexDeviceProfiles.Unlock()
	if seed == "" {
		if codexDeviceProfiles.fallback == nil {
			codexDeviceProfiles.fallback = newCodexDeviceProfile("")
		}
		return codexDeviceProfiles.fallback
	}
	if profile := codexDeviceProfiles.byAccount[seed]; profile != nil {
		return profile
	}
	profile := newCodexDeviceProfile(seed)
	codexDeviceProfiles.byAccount[seed] = profile
	return profile
}

// codexInstallationIDForAccount 返回账号稳定的 UUIDv4 外形安装标识。
func codexInstallationIDForAccount(accountID int64, chatgptAccountID string) string {
	return codexDeviceProfileForAccount(accountID, chatgptAccountID).InstallationID
}

// codexOAIAttestationForAccount 返回同一账号、同一进程稳定的 s=0 envelope。
func codexOAIAttestationForAccount(accountID int64, chatgptAccountID string) string {
	return codexDeviceProfileForAccount(accountID, chatgptAccountID).Attestation
}

type codexSessionUUIDCacheEntry struct {
	value     string
	expiresAt time.Time
	lastUsed  time.Time
}

type codexSessionUUIDCache struct {
	mu         sync.Mutex
	entries    map[string]codexSessionUUIDCacheEntry
	ttl        time.Duration
	maxEntries int
}

var defaultCodexSessionUUIDCache = &codexSessionUUIDCache{
	entries:    make(map[string]codexSessionUUIDCacheEntry),
	ttl:        codexSessionUUIDCacheTTL,
	maxEntries: codexSessionUUIDCacheMaxEntries,
}

// Fixed IDs are account namespaces. Entries still include the downstream task
// and API key, so a stable account seed cannot merge unrelated task histories.
var fixedCodexSessionUUIDCache = &codexSessionUUIDCache{
	entries:    make(map[string]codexSessionUUIDCacheEntry),
	maxEntries: codexSessionUUIDCacheMaxEntries,
}

var codexUUIDFallbackCounter atomic.Uint64

func codexUUIDV7Timestamp(value uuid.UUID) int64 {
	return int64(value[0])<<40 |
		int64(value[1])<<32 |
		int64(value[2])<<24 |
		int64(value[3])<<16 |
		int64(value[4])<<8 |
		int64(value[5])
}

func parsePlausibleCodexUUIDV7(raw string, now time.Time) (uuid.UUID, bool) {
	value, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil || value.Version() != 7 {
		return uuid.Nil, false
	}
	timestamp := codexUUIDV7Timestamp(value)
	if timestamp < codexUUIDV7MinUnixMilli || timestamp > now.Add(24*time.Hour).UnixMilli() {
		return uuid.Nil, false
	}
	return value, true
}

func isolatedCodexUUIDV7(source uuid.UUID, isolated string) string {
	sum := sha256.Sum256([]byte("codex-session-v7:" + isolated))
	var value uuid.UUID
	copy(value[:6], source[:6])
	copy(value[6:], sum[:10])
	value[6] = (value[6] & 0x0f) | 0x70
	value[8] = (value[8] & 0x3f) | 0x80
	return value.String()
}

func newCodexUUIDV7() string {
	if value, err := uuid.NewV7(); err == nil {
		return value.String()
	}
	// crypto/rand 失败时仍保持真实毫秒时间语义，避免降级成 UUIDv4。
	now := time.Now()
	sum := sha256.Sum256([]byte(fmt.Sprintf(
		"codex-session-fallback:%d:%d",
		now.UnixNano(),
		codexUUIDFallbackCounter.Add(1),
	)))
	var value uuid.UUID
	timestamp := now.UnixMilli()
	value[0] = byte(timestamp >> 40)
	value[1] = byte(timestamp >> 32)
	value[2] = byte(timestamp >> 24)
	value[3] = byte(timestamp >> 16)
	value[4] = byte(timestamp >> 8)
	value[5] = byte(timestamp)
	copy(value[6:], sum[:10])
	value[6] = (value[6] & 0x0f) | 0x70
	value[8] = (value[8] & 0x3f) | 0x80
	return value.String()
}

func (cache *codexSessionUUIDCache) getOrCreate(key string, now time.Time) string {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if entry, ok := cache.entries[key]; ok && (entry.expiresAt.IsZero() || !entry.expiresAt.Before(now)) {
		if cache.ttl > 0 {
			entry.expiresAt = now.Add(cache.ttl)
		}
		entry.lastUsed = now
		cache.entries[key] = entry
		return entry.value
	}
	value := newCodexUUIDV7()
	expiresAt := time.Time{}
	if cache.ttl > 0 {
		expiresAt = now.Add(cache.ttl)
	}
	cache.entries[key] = codexSessionUUIDCacheEntry{value: value, expiresAt: expiresAt, lastUsed: now}
	cache.evictLocked(now)
	return value
}

func (cache *codexSessionUUIDCache) evictLocked(now time.Time) {
	if cache.maxEntries <= 0 || len(cache.entries) <= cache.maxEntries {
		return
	}
	for key, entry := range cache.entries {
		if !entry.expiresAt.IsZero() && entry.expiresAt.Before(now) {
			delete(cache.entries, key)
		}
	}
	if len(cache.entries) <= cache.maxEntries {
		return
	}
	keys := make([]string, 0, len(cache.entries))
	for key := range cache.entries {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return cache.entries[keys[i]].lastUsed.Before(cache.entries[keys[j]].lastUsed)
	})
	for _, key := range keys[:len(cache.entries)-cache.maxEntries] {
		delete(cache.entries, key)
	}
}

// generateCodexSessionUUID 保持账号/API Key 隔离和同 seed 稳定，同时生成真实 UUIDv7：
// 入站 seed 已是合理 v7 时保留其 48 位时间戳，仅重派生随机位；其他 seed 在有界
// 进程缓存中首次生成随机 v7，后续复用。seed 为空由调用方生成无状态随机 v7。
func generateCodexSessionUUID(accountID, apiKeyID int64, seed string) string {
	isolated := isolateOpenAISessionIDForAccount(accountID, apiKeyID, seed)
	if isolated == "" {
		return ""
	}
	now := time.Now()
	if source, ok := parsePlausibleCodexUUIDV7(seed, now); ok {
		return isolatedCodexUUIDV7(source, isolated)
	}
	return defaultCodexSessionUUIDCache.getOrCreate(isolated, now)
}

func resolveCodexSessionUUID(accountID, apiKeyID int64, sessionSeed, fixedSessionID string) string {
	if fixed := strings.TrimSpace(fixedSessionID); fixed != "" {
		// Without a task seed there is no evidence that two requests belong to
		// the same task. Do not make the account-wide fixed ID a shared thread.
		if strings.TrimSpace(sessionSeed) == "" {
			return newCodexUUIDV7()
		}
		fixedKey := isolateOpenAISessionIDForAccount(accountID, apiKeyID, "fixed:"+fixed+":task:"+sessionSeed)
		if source, ok := parsePlausibleCodexUUIDV7(sessionSeed, time.Now()); ok {
			return isolatedCodexUUIDV7(source, fixedKey)
		}
		return fixedCodexSessionUUIDCache.getOrCreate(fixedKey, time.Now())
	}
	if value := generateCodexSessionUUID(accountID, apiKeyID, sessionSeed); value != "" {
		return value
	}
	return newCodexUUIDV7()
}

// extractCodexWorkspaces 从入站 x-codex-turn-metadata 中安全提取 workspaces 对象。
// 仅保留 workspaces 字段，其余字段由伪装层强制重写；解析失败或非对象时返回 nil。
func extractCodexWorkspaces(turnMetadata string) map[string]any {
	if turnMetadata == "" {
		return nil
	}
	var payload struct {
		Workspaces map[string]any `json:"workspaces"`
	}
	if err := json.Unmarshal([]byte(turnMetadata), &payload); err != nil {
		return nil
	}
	if payload.Workspaces == nil {
		return nil
	}
	return payload.Workspaces
}

type codexTurnMetadataProfile struct {
	TurnID                     string
	TurnStartedAtUnixMs        int64
	WindowNumber               int
	HasWindowNumber            bool
	ContextWindowID            string
	AgentName                  string
	ThreadSource               string
	TurnTrigger                string
	Sandbox                    string
	SandboxMode                string
	RootTurnID                 string
	WorkspaceKind              string
	AutoReviewEnabled          bool
	NodeReplAutoReviewRequired bool
	NodeReplDisabled           bool
}

// codexTurnMetadataProfileFromInbound 保留会改变请求语义的少量官方 metadata，
// 同时把旧版 system 标识归一为 0.151 使用的 thread_title。未知值不透传，
// 避免入站客户端把任意字符串混入固定 Desktop 画像。
func codexTurnMetadataProfileFromInbound(turnMetadata string) codexTurnMetadataProfile {
	profile := codexTurnMetadataProfile{
		AgentName:    codexDesktopAgentName,
		ThreadSource: codexDesktopThreadSource,
		TurnTrigger:  codexDesktopTurnTrigger,
		Sandbox:      codexTurnMetadataSandbox,
		SandboxMode:  codexTurnMetadataSandboxMode,
	}
	if strings.TrimSpace(turnMetadata) == "" {
		return profile
	}

	var inbound struct {
		TurnID                     string `json:"turn_id"`
		TurnStartedAtUnixMs        int64  `json:"turn_started_at_unix_ms"`
		WindowNumber               *int   `json:"window_number"`
		ContextWindowID            string `json:"context_window_id"`
		AgentName                  string `json:"agent_name"`
		ThreadSource               string `json:"thread_source"`
		TurnTrigger                string `json:"turn_trigger"`
		Sandbox                    string `json:"sandbox"`
		SandboxMode                string `json:"sandbox_mode"`
		RootTurnID                 string `json:"root_turn_id"`
		WorkspaceKind              string `json:"workspace_kind"`
		AutoReviewEnabled          bool   `json:"auto_review_enabled"`
		NodeReplAutoReviewRequired bool   `json:"node_repl_auto_review_required"`
		NodeReplDisabled           bool   `json:"node_repl_disabled"`
	}
	if err := json.Unmarshal([]byte(turnMetadata), &inbound); err != nil {
		return profile
	}
	profile.TurnID = validCodexMetadataUUID(inbound.TurnID)
	profile.ContextWindowID = validCodexMetadataUUID(inbound.ContextWindowID)
	if inbound.TurnStartedAtUnixMs > 0 && inbound.TurnStartedAtUnixMs <= time.Now().Add(24*time.Hour).UnixMilli() {
		profile.TurnStartedAtUnixMs = inbound.TurnStartedAtUnixMs
	}
	if inbound.WindowNumber != nil && *inbound.WindowNumber >= 0 && *inbound.WindowNumber <= 1_000_000 {
		profile.WindowNumber, profile.HasWindowNumber = *inbound.WindowNumber, true
	}

	agentName := strings.TrimSpace(inbound.AgentName)
	if strings.HasPrefix(agentName, "/") && len(agentName) <= 128 {
		profile.AgentName = agentName
	}
	isThreadTitle := inbound.ThreadSource == "thread_title" ||
		inbound.ThreadSource == "system" || inbound.TurnTrigger == "thread_title"
	if isThreadTitle {
		profile.ThreadSource = "thread_title"
		profile.TurnTrigger = "thread_title"
		profile.Sandbox = "windows_elevated"
		profile.SandboxMode = "read-only"
	}
	if inbound.ThreadSource == codexDesktopThreadSource && !isThreadTitle {
		profile.ThreadSource = codexDesktopThreadSource
	}
	if inbound.TurnTrigger == codexDesktopTurnTrigger && !isThreadTitle {
		profile.TurnTrigger = codexDesktopTurnTrigger
	}
	if !isThreadTitle {
		switch inbound.Sandbox {
		case "none", "windows_elevated":
			profile.Sandbox = inbound.Sandbox
		}
		switch inbound.SandboxMode {
		case "read-only", "workspace-write", "danger-full-access":
			profile.SandboxMode = inbound.SandboxMode
		}
	}
	if rootTurnID := strings.TrimSpace(inbound.RootTurnID); rootTurnID != "" {
		if _, err := uuid.Parse(rootTurnID); err == nil {
			profile.RootTurnID = rootTurnID
		}
	}
	profile.AutoReviewEnabled = inbound.AutoReviewEnabled
	switch inbound.WorkspaceKind {
	case "project", "projectless":
		profile.WorkspaceKind = inbound.WorkspaceKind
	}
	profile.NodeReplAutoReviewRequired = inbound.NodeReplAutoReviewRequired
	profile.NodeReplDisabled = inbound.NodeReplDisabled
	return profile
}

// generateCodexContextWindowUUID 为同一 session 派生稳定且不同的 UUIDv7 形态标识。
// 保留 session 的前 48 位时间戳，使两者外观与真实 Desktop 同时创建的 UUID 一致。
func generateCodexContextWindowUUID(sessionUUID string) string {
	sum := sha256.Sum256([]byte("codex-context-window:" + sessionUUID))
	var contextWindowID uuid.UUID
	if sessionID, err := uuid.Parse(strings.TrimSpace(sessionUUID)); err == nil {
		copy(contextWindowID[:6], sessionID[:6])
	} else {
		copy(contextWindowID[:6], sum[:6])
	}
	copy(contextWindowID[6:], sum[:10])
	contextWindowID[6] = (contextWindowID[6] & 0x0f) | 0x70
	contextWindowID[8] = (contextWindowID[8] & 0x3f) | 0x80
	return contextWindowID.String()
}

// buildCodexTurnMetadata 生成 x-codex-turn-metadata 头的 JSON 值，字段集合与顺序严格对齐真实 Codex
// Desktop App 0.155.0-alpha.2.6 实抓报文（普通一轮 request_kind=turn）：
// installation_id, session_id, thread_id, agent_name, turn_id, window_id, window_number,
// context_window_id, request_kind, root_turn_id, thread_source, turn_trigger, sandbox,
// sandbox_mode, auto_review_enabled, node_repl_auto_review_required, node_repl_disabled,
// workspaces, turn_started_at_unix_ms, workspace_kind。
// 同一 turn 的工具续接保留客户端 turn_id 与开始时间；缺失时才生成。
// 0.155 实抓：workspace_kind=project 的普通请求也可省略 workspaces。
// 项目类型保留入站值；目录详情未知时不合成路径或 Git 状态。
// workspaces 优先使用入站 x-codex-turn-metadata 中的客户端值（代理端无法获知本地 git 信息），
// 未提供目录明细时省略 workspaces。
func buildCodexTurnMetadata(sessionUUID, windowID string, workspaces map[string]any, installationID string, inboundMetadata ...string) string {
	if strings.TrimSpace(installationID) == "" {
		installationID = codexInstallationID
	}
	profile := codexTurnMetadataProfileFromInbound("")
	if len(inboundMetadata) > 0 {
		profile = codexTurnMetadataProfileFromInbound(inboundMetadata[0])
	}
	turnID, startedAt := codexMetadataTurn(profile)
	windowID, windowNumber, contextWindowID := codexMetadataWindow(sessionUUID, windowID, profile)
	rootTurnID := turnID
	if profile.RootTurnID != "" {
		rootTurnID = profile.RootTurnID
	}
	meta := struct {
		InstallationID             string         `json:"installation_id"`
		SessionID                  string         `json:"session_id"`
		ThreadID                   string         `json:"thread_id"`
		AgentName                  string         `json:"agent_name"`
		TurnID                     string         `json:"turn_id"`
		WindowID                   string         `json:"window_id"`
		WindowNumber               int            `json:"window_number"`
		ContextWindowID            string         `json:"context_window_id"`
		RequestKind                string         `json:"request_kind"`
		RootTurnID                 string         `json:"root_turn_id"`
		ThreadSource               string         `json:"thread_source"`
		TurnTrigger                string         `json:"turn_trigger"`
		Sandbox                    string         `json:"sandbox"`
		SandboxMode                string         `json:"sandbox_mode"`
		AutoReviewEnabled          bool           `json:"auto_review_enabled"`
		NodeReplAutoReviewRequired bool           `json:"node_repl_auto_review_required"`
		NodeReplDisabled           bool           `json:"node_repl_disabled"`
		Workspaces                 map[string]any `json:"workspaces,omitempty"`
		TurnStartedAtUnixMs        int64          `json:"turn_started_at_unix_ms"`
		WorkspaceKind              string         `json:"workspace_kind,omitempty"`
	}{
		InstallationID:             installationID,
		SessionID:                  sessionUUID,
		ThreadID:                   sessionUUID,
		AgentName:                  profile.AgentName,
		TurnID:                     turnID,
		WindowID:                   windowID,
		WindowNumber:               windowNumber,
		ContextWindowID:            contextWindowID,
		RequestKind:                "turn",
		RootTurnID:                 rootTurnID,
		ThreadSource:               profile.ThreadSource,
		TurnTrigger:                profile.TurnTrigger,
		Sandbox:                    profile.Sandbox,
		SandboxMode:                profile.SandboxMode,
		AutoReviewEnabled:          profile.AutoReviewEnabled,
		NodeReplAutoReviewRequired: profile.NodeReplAutoReviewRequired,
		NodeReplDisabled:           profile.NodeReplDisabled,
		Workspaces:                 workspaces,
		TurnStartedAtUnixMs:        startedAt,
	}
	// Non-empty local directories establish a project even if an older client
	// supplied a contradictory projectless hint. Titles have no workspace kind.
	if profile.ThreadSource == codexDesktopThreadSource && profile.TurnTrigger == codexDesktopTurnTrigger {
		meta.WorkspaceKind = profile.WorkspaceKind
		if len(workspaces) > 0 {
			meta.WorkspaceKind = "project"
		}
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return ""
	}
	return string(b)
}

// buildCodexWSPrewarmMetadata 生成 WS prewarm 的 x-codex-turn-metadata 头 JSON 值，
// 字段集合与顺序对齐 Codex Desktop App 0.151.0-alpha.7.1 实抓报文。
// prewarm 不含 root_turn_id、turn_trigger、workspaces、turn_started_at_unix_ms 与 workspace_kind。
func buildCodexWSPrewarmMetadata(sessionUUID, windowID, installationID, inboundMetadata string) string {
	if strings.TrimSpace(installationID) == "" {
		installationID = codexInstallationID
	}
	profile := codexTurnMetadataProfileFromInbound(inboundMetadata)
	windowID, windowNumber, contextWindowID := codexMetadataWindow(sessionUUID, windowID, profile)
	meta := struct {
		InstallationID             string `json:"installation_id"`
		SessionID                  string `json:"session_id"`
		ThreadID                   string `json:"thread_id"`
		AgentName                  string `json:"agent_name"`
		TurnID                     string `json:"turn_id"`
		WindowID                   string `json:"window_id"`
		WindowNumber               int    `json:"window_number"`
		ContextWindowID            string `json:"context_window_id"`
		RequestKind                string `json:"request_kind"`
		ThreadSource               string `json:"thread_source"`
		Sandbox                    string `json:"sandbox"`
		SandboxMode                string `json:"sandbox_mode"`
		AutoReviewEnabled          bool   `json:"auto_review_enabled"`
		NodeReplAutoReviewRequired bool   `json:"node_repl_auto_review_required"`
		NodeReplDisabled           bool   `json:"node_repl_disabled"`
	}{
		InstallationID:             installationID,
		SessionID:                  sessionUUID,
		ThreadID:                   sessionUUID,
		AgentName:                  profile.AgentName,
		TurnID:                     "",
		WindowID:                   windowID,
		WindowNumber:               windowNumber,
		ContextWindowID:            contextWindowID,
		RequestKind:                "prewarm",
		ThreadSource:               profile.ThreadSource,
		Sandbox:                    profile.Sandbox,
		SandboxMode:                profile.SandboxMode,
		AutoReviewEnabled:          profile.AutoReviewEnabled,
		NodeReplAutoReviewRequired: profile.NodeReplAutoReviewRequired,
		NodeReplDisabled:           profile.NodeReplDisabled,
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return ""
	}
	return string(b)
}

// codexDefaultCompactionProfile 为 0.151.0-alpha.7.1 实抓手动压缩请求的默认 compaction 对象
// （入站 metadata 未携带 compaction 字段时回退使用）。
const codexDefaultCompactionProfile = `{"trigger":"manual","reason":"user_requested","implementation":"responses_compaction_v2","phase":"standalone_turn","strategy":"memento"}`

// extractCodexCompactionRequest 解析入站 x-codex-turn-metadata 是否为手动压缩请求
// （request_kind="compaction"）；是则返回其 compaction 对象原值（缺省或为 null 时返回 nil，
// 由调用方回退默认画像）。
func extractCodexCompactionRequest(turnMetadata string) (json.RawMessage, bool) {
	if turnMetadata == "" {
		return nil, false
	}
	var payload struct {
		RequestKind string          `json:"request_kind"`
		Compaction  json.RawMessage `json:"compaction"`
	}
	if err := json.Unmarshal([]byte(turnMetadata), &payload); err != nil {
		return nil, false
	}
	if payload.RequestKind != "compaction" {
		return nil, false
	}
	compaction := payload.Compaction
	if len(compaction) == 0 || string(compaction) == "null" {
		compaction = nil
	}
	return compaction, true
}

// buildCodexCompactionMetadata 生成手动压缩请求的 x-codex-turn-metadata 头 JSON 值，
// 0.155 手动 compaction 含 root_turn_id，不含 turn_trigger 与 workspaces。
// 自动 compaction 保留入站的 turn_trigger/workspace_kind。
// turn_id/开始时间保留合法入站值；compaction 对象原样保留，为空时回退实抓默认画像。
func buildCodexCompactionMetadata(sessionUUID, windowID, installationID string, compaction json.RawMessage, inboundMetadata ...string) string {
	if strings.TrimSpace(installationID) == "" {
		installationID = codexInstallationID
	}
	if len(compaction) == 0 {
		compaction = json.RawMessage(codexDefaultCompactionProfile)
	}
	profile := codexTurnMetadataProfileFromInbound("")
	if len(inboundMetadata) > 0 {
		profile = codexTurnMetadataProfileFromInbound(inboundMetadata[0])
	}
	turnID, startedAt := codexMetadataTurn(profile)
	windowID, windowNumber, contextWindowID := codexMetadataWindow(sessionUUID, windowID, profile)
	rootTurnID := profile.RootTurnID
	if rootTurnID == "" {
		rootTurnID = turnID
	}
	meta := struct {
		InstallationID             string          `json:"installation_id"`
		SessionID                  string          `json:"session_id"`
		ThreadID                   string          `json:"thread_id"`
		AgentName                  string          `json:"agent_name"`
		TurnID                     string          `json:"turn_id"`
		WindowID                   string          `json:"window_id"`
		WindowNumber               int             `json:"window_number"`
		ContextWindowID            string          `json:"context_window_id"`
		RequestKind                string          `json:"request_kind"`
		RootTurnID                 string          `json:"root_turn_id"`
		ThreadSource               string          `json:"thread_source"`
		TurnTrigger                string          `json:"turn_trigger,omitempty"`
		WorkspaceKind              string          `json:"workspace_kind,omitempty"`
		Sandbox                    string          `json:"sandbox"`
		SandboxMode                string          `json:"sandbox_mode"`
		AutoReviewEnabled          bool            `json:"auto_review_enabled"`
		NodeReplAutoReviewRequired bool            `json:"node_repl_auto_review_required"`
		NodeReplDisabled           bool            `json:"node_repl_disabled"`
		TurnStartedAtUnixMs        int64           `json:"turn_started_at_unix_ms"`
		Compaction                 json.RawMessage `json:"compaction"`
	}{
		InstallationID:             installationID,
		SessionID:                  sessionUUID,
		ThreadID:                   sessionUUID,
		AgentName:                  profile.AgentName,
		TurnID:                     turnID,
		WindowID:                   windowID,
		WindowNumber:               windowNumber,
		ContextWindowID:            contextWindowID,
		RequestKind:                "compaction",
		RootTurnID:                 rootTurnID,
		ThreadSource:               profile.ThreadSource,
		Sandbox:                    profile.Sandbox,
		SandboxMode:                profile.SandboxMode,
		AutoReviewEnabled:          profile.AutoReviewEnabled,
		NodeReplAutoReviewRequired: profile.NodeReplAutoReviewRequired,
		NodeReplDisabled:           profile.NodeReplDisabled,
		TurnStartedAtUnixMs:        startedAt,
		Compaction:                 compaction,
	}
	var trigger struct {
		Trigger string `json:"trigger"`
	}
	if json.Unmarshal(compaction, &trigger) == nil && trigger.Trigger == "auto" {
		meta.TurnTrigger = profile.TurnTrigger
		meta.WorkspaceKind = profile.WorkspaceKind
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return ""
	}
	return string(b)
}

// （字段集合 + 取值 + 实抓基准），完全无视入站客户端传入的对应头。不处理 HTTP/2 头发送顺序（按既定范围）。
//
// applyCodexOAuthMimicHeaders 将 OAuth 上游请求头无条件重建为与真实 Codex Desktop App HTTP POST 一致
// （字段集合 + 取值 + 实抓基准），完全无视入站客户端传入的对应头。不处理 HTTP/2 头发送顺序（按既定范围）。
//
// sessionSeed 为隔离前的原始会话种子；为空时回退随机 UUIDv7，
// 以保证 session-id/thread-id 始终存在（与真实 Codex 行为一致）。
//
// responsesLite 控制是否发送 x-openai-internal-codex-responses-lite 头；合成路径
// 按最终模型判定，透传路径也允许入站标记作为前向兼容信号。model 是可选的
// 最终上游模型，用于发送 x-codex-routing-hint: model=<实际模型>。
func applyCodexOAuthMimicHeaders(req *http.Request, accountID, apiKeyID int64, sessionSeed, fixedSessionID, originator string, isCompact bool, responsesLite bool, model ...string) {
	if req == nil {
		return
	}
	applyCodexOAuthMimicHeadersWithProfile(req, accountID, apiKeyID, sessionSeed, fixedSessionID, originator, isCompact, responsesLite,
		codexDeviceProfileForAccount(accountID, req.Header.Get("chatgpt-account-id")), defaultCodexClientProfile(), model...)
}

func applyCodexOAuthMimicHeadersForAccount(req *http.Request, account *Account, apiKeyID int64, sessionSeed, fixedSessionID, originator string, isCompact bool, responsesLite bool, model ...string) {
	if req == nil || account == nil {
		return
	}
	*req = *req.WithContext(withCodexClientProfile(req.Context(), account))
	applyCodexOAuthMimicHeadersWithProfile(req, account.ID, apiKeyID, sessionSeed, fixedSessionID, originator, isCompact, responsesLite,
		codexAccountDeviceProfile(account), codexClientProfileForAccount(account), model...)
}

func applyCodexOAuthMimicHeadersWithProfile(req *http.Request, accountID, apiKeyID int64, sessionSeed, fixedSessionID, originator string, isCompact bool, responsesLite bool, deviceProfile *codexDeviceProfile, clientProfile CodexClientProfile, model ...string) {
	if req == nil {
		return
	}
	authorization := strings.TrimSpace(req.Header.Get("authorization"))
	chatgptAccountID := strings.TrimSpace(req.Header.Get("chatgpt-account-id"))
	installationID := deviceProfile.InstallationID
	inboundTurnMetadata := req.Header.Get("x-codex-turn-metadata")
	inboundWorkspaces := extractCodexWorkspaces(inboundTurnMetadata)
	req.Header = make(http.Header)
	if authorization != "" {
		req.Header.Set("authorization", authorization)
	}
	if chatgptAccountID != "" {
		req.Header.Set("chatgpt-account-id", chatgptAccountID)
	}
	_ = originator
	actualModel := ""
	if len(model) > 0 {
		actualModel = strings.TrimSpace(model[0])
	}

	// User-Agent 无条件强制为 Codex Desktop 画像（忽略入站 UA），后续调用方不得覆盖。
	req.Header.Set("user-agent", clientProfile.UserAgent())
	// 实抓基准：HTTP POST 恒定携带 version 与 x-codex-beta-features。
	req.Header.Set("version", clientProfile.CodexVersion)
	req.Header.Set("x-codex-beta-features", codexBetaFeaturesValue)
	// responses-lite 头仅对 lite 模型发送（对齐上游 add_responses_lite_header）；
	// 非 lite 不发送。0.144 的 x-responsesapi-include-timing-metrics 已在新版移除，不再发送。
	if responsesLite {
		req.Header.Set("x-openai-internal-codex-responses-lite", codexResponsesLiteValue)
	}
	if actualModel != "" {
		req.Header.Set("x-codex-routing-hint", "model="+actualModel)
	}
	// content-type 钉死为 application/json（实抓基准为裸值，不带 charset）。
	req.Header.Set("content-type", "application/json")
	req.Header.Set("originator", codexDesktopOriginator)
	// 0.151 实抓：普通 turn、手动 compaction 与 WS prewarm 均恢复完整
	// s=0 CBOR token（app_session_id 按账号派生）。
	req.Header.Set("x-oai-attestation", deviceProfile.Attestation)

	if isCompact {
		req.Header.Set("accept", "application/json")
	} else {
		req.Header.Set("accept", "text/event-stream")
	}

	sessUUID := resolveCodexSessionUUID(accountID, apiKeyID, codexSessionSeedFromMetadata(sessionSeed, inboundTurnMetadata), fixedSessionID)
	if sessUUID == "" {
		return
	}
	windowID := sessUUID + ":0"
	req.Header.Set("session-id", sessUUID)
	req.Header.Set("thread-id", sessUUID)
	// x-client-request-id 与真实 Codex 一致，取 thread-id（实抓三者同值）。
	req.Header.Set("x-client-request-id", sessUUID)
	req.Header.Set("x-codex-window-id", windowID)
	// 0.151 实抓：手动压缩请求的 metadata request_kind="compaction"、无 workspaces，
	// compaction 对象原样保留入站值；普通一轮仍走 turn 画像。
	if compaction, isCompaction := extractCodexCompactionRequest(inboundTurnMetadata); isCompaction {
		req.Header.Set("x-codex-turn-metadata", buildCodexCompactionMetadata(sessUUID, windowID, installationID, compaction, inboundTurnMetadata))
	} else {
		req.Header.Set("x-codex-turn-metadata", buildCodexTurnMetadata(sessUUID, windowID, inboundWorkspaces, installationID, inboundTurnMetadata))
	}
	syncCodexMetadataWindowHeader(req.Header)
}

// syncCodexOAuthMimicRequestBody 将非 compact OAuth 请求体中的 client_metadata
// 与 applyCodexOAuthMimicHeaders 生成的 Desktop turn metadata 对齐。调用方必须在
// header 重建后、请求体压缩前调用；compact 请求保持原有专用 body 形态不变。
func syncCodexOAuthMimicRequestBody(req *http.Request, body []byte, isCompact bool) ([]byte, error) {
	if req == nil || isCompact {
		return body, nil
	}

	updatedBody, modified, err := applyCodexClientMetadataBytes(body, req.Header.Get("x-codex-turn-metadata"))
	if err != nil {
		return body, err
	}
	if !modified {
		return body, nil
	}

	req.Body = io.NopCloser(bytes.NewReader(updatedBody))
	req.ContentLength = int64(len(updatedBody))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(updatedBody)), nil
	}
	return updatedBody, nil
}

// applyCodexOAuthWSMimicHeaders 将 OAuth 上游 WebSocket 握手业务头重建为 Codex Desktop App 画像。
// WebSocket 协议层头（Host/Upgrade/Sec-WebSocket-*）由底层 WS 库生成；这里仅处理
// Codex/OpenAI 业务头，避免把 HTTP 兼容头（session_id/conversation_id 等）带到握手里。
func applyCodexOAuthWSMimicHeaders(headers http.Header, accountID, apiKeyID int64, sessionSeed, fixedSessionID, originator, turnMetadata string, model ...string) {
	applyCodexOAuthWSMimicHeadersWithProfile(headers, accountID, apiKeyID, sessionSeed, fixedSessionID, originator, turnMetadata,
		codexDeviceProfileForAccount(accountID, headers.Get("chatgpt-account-id")), defaultCodexClientProfile(), model...)
}

func applyCodexOAuthWSMimicHeadersForAccount(headers http.Header, account *Account, apiKeyID int64, sessionSeed, fixedSessionID, originator, turnMetadata string, model ...string) {
	if account == nil {
		return
	}
	applyCodexOAuthWSMimicHeadersWithProfile(headers, account.ID, apiKeyID, sessionSeed, fixedSessionID, originator, turnMetadata,
		codexAccountDeviceProfile(account), codexClientProfileForAccount(account), model...)
}

func applyCodexOAuthWSMimicHeadersWithProfile(headers http.Header, accountID, apiKeyID int64, sessionSeed, fixedSessionID, originator, turnMetadata string, deviceProfile *codexDeviceProfile, clientProfile CodexClientProfile, model ...string) {
	if headers == nil {
		return
	}
	authorization := strings.TrimSpace(headers.Get("authorization"))
	chatgptAccountID := strings.TrimSpace(headers.Get("chatgpt-account-id"))
	installationID := deviceProfile.InstallationID
	for key := range headers {
		delete(headers, key)
	}
	if authorization != "" {
		headers.Set("authorization", authorization)
	}
	if chatgptAccountID != "" {
		headers.Set("chatgpt-account-id", chatgptAccountID)
	}
	_ = originator

	headers.Set("user-agent", clientProfile.UserAgent())
	headers.Set("version", clientProfile.CodexVersion)
	headers.Set("openai-beta", openAIWSBetaV2Value)
	headers.Set("originator", codexDesktopOriginator)
	headers.Set("x-codex-beta-features", codexBetaFeaturesValue)
	if len(model) > 0 {
		if actualModel := strings.TrimSpace(model[0]); actualModel != "" {
			headers.Set("x-codex-routing-hint", "model="+actualModel)
		}
	}
	// x-oai-attestation 为 Desktop App 特有的证明头（app_session_id 按账号派生）。
	headers.Set("x-oai-attestation", deviceProfile.Attestation)

	sessUUID := resolveCodexSessionUUID(accountID, apiKeyID, codexSessionSeedFromMetadata(sessionSeed, turnMetadata), fixedSessionID)
	if sessUUID == "" {
		return
	}
	windowID := sessUUID + ":0"
	headers.Set("session-id", sessUUID)
	headers.Set("thread-id", sessUUID)
	headers.Set("x-client-request-id", sessUUID)
	headers.Set("x-codex-window-id", windowID)

	metadata := buildCodexWSPrewarmMetadata(sessUUID, windowID, installationID, turnMetadata)
	if metadata != "" {
		headers.Set("x-codex-turn-metadata", metadata)
	}
	syncCodexMetadataWindowHeader(headers)
}

// applyCodexWSRequestClientMetadata 对齐 0.151 WS response.create 的传输专用
// client_metadata。Lite 标记不再出现在握手头中，而是只写入 payload。
func applyCodexWSRequestClientMetadata(reqBody map[string]any, model string) bool {
	if reqBody == nil {
		return false
	}
	var clientMetadata map[string]any
	switch existing := reqBody["client_metadata"].(type) {
	case map[string]any:
		clientMetadata = existing
	case map[string]string:
		clientMetadata = make(map[string]any, len(existing)+2)
		for key, value := range existing {
			clientMetadata[key] = value
		}
	default:
		clientMetadata = make(map[string]any, 2)
	}

	modified := false
	if value, ok := clientMetadata["x-codex-ws-stream-request-start-ms"].(string); !ok || strings.TrimSpace(value) == "" {
		clientMetadata["x-codex-ws-stream-request-start-ms"] = strconv.FormatInt(time.Now().UnixMilli(), 10)
		modified = true
	}
	if actualModel := strings.TrimSpace(model); actualModel != "" {
		if isCodexResponsesLiteModel(actualModel) {
			if value, ok := clientMetadata[responsesLiteWSMetadataKey].(string); !ok || value != codexResponsesLiteValue {
				clientMetadata[responsesLiteWSMetadataKey] = codexResponsesLiteValue
				modified = true
			}
		} else if _, exists := clientMetadata[responsesLiteWSMetadataKey]; exists {
			delete(clientMetadata, responsesLiteWSMetadataKey)
			modified = true
		}
	}
	if !modified {
		return false
	}
	reqBody["client_metadata"] = clientMetadata
	return true
}

// codexRequestCompressionEnabled 是否对 OAuth Codex 上游请求体启用 zstd 压缩（默认启用）。
func (s *OpenAIGatewayService) codexRequestCompressionEnabled() bool {
	if s == nil || s.cfg == nil {
		return true
	}
	return !s.cfg.Gateway.OpenAICodexRequestCompressionDisabled
}

// applyCodexRequestCompression 用 zstd 压缩请求体并设置 content-encoding（匹配真实 Codex CLI）。
// 仅改写 req.Body / ContentLength / GetBody，不影响外部用于计费与日志的原始 body 切片。
func (s *OpenAIGatewayService) applyCodexRequestCompression(req *http.Request, body []byte) {
	if req == nil || !s.codexRequestCompressionEnabled() {
		return
	}
	applyCodexRequestCompressionRaw(req, body)
}

func applyCodexRequestCompressionRaw(req *http.Request, body []byte) {
	if req == nil {
		return
	}
	compressed := httputil.CompressZstd(body)
	req.Body = io.NopCloser(bytes.NewReader(compressed))
	req.ContentLength = int64(len(compressed))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(compressed)), nil
	}
	req.Header.Set("content-encoding", "zstd")
}
