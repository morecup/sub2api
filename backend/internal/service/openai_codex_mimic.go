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
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/google/uuid"
)

// 真实 Codex Desktop App 固定头值（基准：Desktop 26.715.61943 / codex-rs 0.145.0-alpha.27 实抓报文）。
const (
	// codexBetaFeaturesValue 对应 x-codex-beta-features 头（实抓：Desktop App 恒定发送该值）。
	codexBetaFeaturesValue = "remote_compaction_v2"
	// codexTurnMetadataSandbox 对应 HTTP POST x-codex-turn-metadata.sandbox 字段（实抓：Desktop App HTTP POST 为 none）。
	codexTurnMetadataSandbox = "none"
	// codexDesktopThreadSource 对应 0.145 turn/prewarm metadata 的 thread_source。
	codexDesktopThreadSource = "user"
	// codexResponsesLiteValue 对应 x-openai-internal-codex-responses-lite 头
	// （0.145 实抓：turn 与 compaction POST 均恒定发送）。
	codexResponsesLiteValue = "true"
	// codexOAIAttestationLite 对应 0.145 turn POST 的 x-oai-attestation
	// （实抓：turn 不再携带 CBOR token，仅 {"v":1,"s":1}；compaction 与 WS prewarm 仍发完整 token）。
	codexOAIAttestationLite = `{"v":1,"s":1}`
	// codexDesktopOriginator 对应 originator 头（实抓：Desktop App 为 "Codex Desktop"）。
	codexDesktopOriginator = "Codex Desktop"
	// codexInstallationID 对应 x-codex-turn-metadata.installation_id 字段的兜底值（实抓固定值），
	// 正常路径按账号确定性派生，仅在无法派生时使用。
	codexInstallationID = "00e9ffcb-88d7-4ee8-aeca-1982d91a1330"
	// Windows 桌面端 attestation signals（当前机器实抓画像）。
	codexAttestationBundleID      = "com.openai.codex"
	codexAttestationLanguage      = "zh-CN"
	codexAttestationTimezone      = "Asia/Shanghai"
	codexAttestationScreenSizeSum = 1967
	codexAttestationScreenScale   = 1.5
)

// Windows 端不使用 Apple DeviceCheck，而是为每个桌面进程生成带 app_session_id 的
// error_code=1 CBOR envelope。保持进程内稳定、进程间变化，比复用旧抓包 token 更贴近 0.145。
var codexOAIAttestation = buildCodexOAIAttestation(uuid.NewString())

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

func buildCodexOAIAttestation(appSessionID string) string {
	// signals: {0:schema,1:languages,2:locale,3:timezone,4:screen sum,5:scale,6:session}
	signals := appendCodexCBORHead(nil, 5, 7)
	signals = appendCodexCBORUnsigned(signals, 0)
	signals = appendCodexCBORUnsigned(signals, 1)
	signals = appendCodexCBORUnsigned(signals, 1)
	signals = appendCodexCBORHead(signals, 4, 1)
	signals = appendCodexCBORText(signals, codexAttestationLanguage)
	signals = appendCodexCBORUnsigned(signals, 2)
	signals = appendCodexCBORText(signals, codexAttestationLanguage)
	signals = appendCodexCBORUnsigned(signals, 3)
	signals = appendCodexCBORText(signals, codexAttestationTimezone)
	signals = appendCodexCBORUnsigned(signals, 4)
	signals = appendCodexCBORUnsigned(signals, codexAttestationScreenSizeSum)
	signals = appendCodexCBORUnsigned(signals, 5)
	signals = appendCodexCBORFloat64(signals, codexAttestationScreenScale)
	signals = appendCodexCBORUnsigned(signals, 6)
	signals = appendCodexCBORText(signals, appSessionID)

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

// codexInstallationIDForAccount 按账号确定性派生 installation_id（UUIDv5），
// 避免所有账号共用同一安装标识形成关联指纹。种子优先取 sub2api 账号 ID，
// 其次上游 chatgpt-account-id；均不可用时回退实抓固定值 codexInstallationID。
func codexInstallationIDForAccount(accountID int64, chatgptAccountID string) string {
	seed := codexAccountSeed(accountID, chatgptAccountID)
	if seed == "" {
		return codexInstallationID
	}
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("sub2api:codex-installation:"+seed)).String()
}

// codexProcessSalt 为网关进程级随机盐：app_session_id 由它参与派生，
// 保证其随网关进程重启而变化（贴近真实桌面应用重启后 app_session_id 变化的语义）。
var codexProcessSalt = uuid.NewString()

// codexAttestationForAccountCache 缓存按账号派生的 attestation envelope，避免每请求重建 CBOR。
var codexAttestationForAccountCache sync.Map

// codexOAIAttestationForAccount 按账号派生 attestation：app_session_id 为
// UUIDv5(进程盐 + 账号种子)，同账号在网关进程生命周期内恒定，跨账号、跨进程均不同，
// 与按账号派生的 installation_id 语义一致（每个账号看起来是独立的一台设备）。
// 无账号种子时回退进程级全局值 codexOAIAttestation。
func codexOAIAttestationForAccount(accountID int64, chatgptAccountID string) string {
	seed := codexAccountSeed(accountID, chatgptAccountID)
	if seed == "" {
		return codexOAIAttestation
	}
	if v, ok := codexAttestationForAccountCache.Load(seed); ok {
		return v.(string)
	}
	appSessionID := uuid.NewSHA1(uuid.NameSpaceURL, []byte("sub2api:codex-app-session:"+codexProcessSalt+":"+seed)).String()
	attestation := buildCodexOAIAttestation(appSessionID)
	codexAttestationForAccountCache.Store(seed, attestation)
	return attestation
}

// generateCodexSessionUUID 由 (accountID, apiKeyID, seed) 确定性派生一个合法 UUIDv7 形态的会话标识。
//
// 设计目标（与真实 Codex 的进程级 session UUID 对齐）：
//   - 同一会话（同 seed）跨多轮请求恒定，保持粘性路由与上游会话连续性；
//   - 不同 API Key 即使原始 seed 相同也派生出不同 UUID，避免跨用户会话碰撞；
//   - 不同上游账号即使 (apiKeyID, seed) 相同也派生出不同 UUID，避免上游按
//     session-id 关联多个账号；故障转移到别的账号时 session-id 会变
//     （可接受，粘性路由正常路径不受影响）；
//   - 零存储：纯哈希派生，无需缓存。
//
// 注：UUIDv7 前 48 位本应为生成时间戳，这里为哈希值而非真实时间；实测上游不校验，风险极低。
// seed 为空返回空串（由调用方决定是否回退随机 UUIDv7）。
func generateCodexSessionUUID(accountID, apiKeyID int64, seed string) string {
	isolated := isolateOpenAISessionIDForAccount(accountID, apiKeyID, seed)
	if isolated == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("codex-session-v7:" + isolated))
	var u uuid.UUID
	copy(u[:], sum[:16])
	u[6] = (u[6] & 0x0f) | 0x70 // version 7
	u[8] = (u[8] & 0x3f) | 0x80 // RFC 4122 variant
	return u.String()
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

// buildCodexTurnMetadata 生成 x-codex-turn-metadata 头的 JSON 值，字段集合与顺序严格对齐真实 Codex
// Desktop App 0.145.0-alpha.27 实抓报文（普通一轮 request_kind=turn）：
// installation_id, session_id, thread_id, turn_id, window_id, request_kind, thread_source,
// sandbox, workspaces, turn_started_at_unix_ms, workspace_kind。
// turn_id 为每请求新生成的 UUIDv7；session_id/thread_id 复用会话 UUID。
// 注意：workspace_kind 仅在 workspaces 非空时出现（实抓恒为 "project"），
// 系统 turn / compaction 等无 workspaces 的场景不发送该字段。
// workspaces 优先使用入站 x-codex-turn-metadata 中的客户端值（代理端无法获知本地 git 信息），
// 未提供时回退空对象 {} 以保持字段集合一致。
func buildCodexTurnMetadata(sessionUUID, windowID string, workspaces map[string]any, installationID string) string {
	turnID := sessionUUID
	if v, err := uuid.NewV7(); err == nil {
		turnID = v.String()
	}
	if workspaces == nil {
		workspaces = map[string]any{}
	}
	if strings.TrimSpace(installationID) == "" {
		installationID = codexInstallationID
	}
	meta := struct {
		InstallationID      string         `json:"installation_id"`
		SessionID           string         `json:"session_id"`
		ThreadID            string         `json:"thread_id"`
		TurnID              string         `json:"turn_id"`
		WindowID            string         `json:"window_id"`
		RequestKind         string         `json:"request_kind"`
		ThreadSource        string         `json:"thread_source"`
		Sandbox             string         `json:"sandbox"`
		Workspaces          map[string]any `json:"workspaces"`
		TurnStartedAtUnixMs int64          `json:"turn_started_at_unix_ms"`
		WorkspaceKind       string         `json:"workspace_kind,omitempty"`
	}{
		InstallationID:      installationID,
		SessionID:           sessionUUID,
		ThreadID:            sessionUUID,
		TurnID:              turnID,
		WindowID:            windowID,
		RequestKind:         "turn",
		ThreadSource:        codexDesktopThreadSource,
		Sandbox:             codexTurnMetadataSandbox,
		Workspaces:          workspaces,
		TurnStartedAtUnixMs: time.Now().UnixMilli(),
	}
	// 0.145 实抓：workspaces 非空时末尾带 workspace_kind="project"；空 workspaces 不出现该字段。
	if len(workspaces) > 0 {
		meta.WorkspaceKind = "project"
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return ""
	}
	return string(b)
}

// buildCodexWSPrewarmMetadata 生成 WS prewarm 的 x-codex-turn-metadata 头 JSON 值，
// 字段集合与顺序对齐 Codex Desktop App 0.145.0-alpha.27 实抓报文：
// installation_id, session_id, thread_id, turn_id, window_id, request_kind,
// thread_source, sandbox, workspaces。prewarm 不含 turn_started_at_unix_ms 与 workspace_kind。
func buildCodexWSPrewarmMetadata(sessionUUID, windowID string, workspaces map[string]any, installationID string) string {
	if workspaces == nil {
		workspaces = map[string]any{}
	}
	if strings.TrimSpace(installationID) == "" {
		installationID = codexInstallationID
	}
	meta := struct {
		InstallationID string         `json:"installation_id"`
		SessionID      string         `json:"session_id"`
		ThreadID       string         `json:"thread_id"`
		TurnID         string         `json:"turn_id"`
		WindowID       string         `json:"window_id"`
		RequestKind    string         `json:"request_kind"`
		ThreadSource   string         `json:"thread_source"`
		Sandbox        string         `json:"sandbox"`
		Workspaces     map[string]any `json:"workspaces"`
	}{
		InstallationID: installationID,
		SessionID:      sessionUUID,
		ThreadID:       sessionUUID,
		TurnID:         "",
		WindowID:       windowID,
		RequestKind:    "prewarm",
		ThreadSource:   codexDesktopThreadSource,
		Sandbox:        codexTurnMetadataSandbox,
		Workspaces:     workspaces,
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return ""
	}
	return string(b)
}

// codexDefaultCompactionProfile 为 0.145.0-alpha.27 实抓手动压缩请求的默认 compaction 对象
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
// 字段集合与顺序对齐 Codex Desktop App 0.145.0-alpha.27 实抓报文：
// installation_id, session_id, thread_id, turn_id, window_id, request_kind="compaction",
// thread_source, sandbox, turn_started_at_unix_ms, compaction。compaction 请求不含 workspaces。
// turn_id 为每请求新生成的 UUIDv7；compaction 对象原样保留入站值，为空时回退实抓默认画像。
func buildCodexCompactionMetadata(sessionUUID, windowID, installationID string, compaction json.RawMessage) string {
	turnID := ""
	if v, err := uuid.NewV7(); err == nil {
		turnID = v.String()
	}
	if strings.TrimSpace(installationID) == "" {
		installationID = codexInstallationID
	}
	if len(compaction) == 0 {
		compaction = json.RawMessage(codexDefaultCompactionProfile)
	}
	meta := struct {
		InstallationID      string          `json:"installation_id"`
		SessionID           string          `json:"session_id"`
		ThreadID            string          `json:"thread_id"`
		TurnID              string          `json:"turn_id"`
		WindowID            string          `json:"window_id"`
		RequestKind         string          `json:"request_kind"`
		ThreadSource        string          `json:"thread_source"`
		Sandbox             string          `json:"sandbox"`
		TurnStartedAtUnixMs int64           `json:"turn_started_at_unix_ms"`
		Compaction          json.RawMessage `json:"compaction"`
	}{
		InstallationID:      installationID,
		SessionID:           sessionUUID,
		ThreadID:            sessionUUID,
		TurnID:              turnID,
		WindowID:            windowID,
		RequestKind:         "compaction",
		ThreadSource:        codexDesktopThreadSource,
		Sandbox:             codexTurnMetadataSandbox,
		TurnStartedAtUnixMs: time.Now().UnixMilli(),
		Compaction:          compaction,
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
func applyCodexOAuthMimicHeaders(req *http.Request, accountID, apiKeyID int64, sessionSeed, originator string, isCompact bool) {
	if req == nil {
		return
	}
	authorization := strings.TrimSpace(req.Header.Get("authorization"))
	chatgptAccountID := strings.TrimSpace(req.Header.Get("chatgpt-account-id"))
	installationID := codexInstallationIDForAccount(accountID, chatgptAccountID)
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

	// User-Agent 无条件强制为 Codex Desktop 画像（忽略入站 UA），后续调用方不得覆盖。
	req.Header.Set("user-agent", codexDesktopUserAgent)
	// 实抓基准：HTTP POST 恒定携带 version 与 x-codex-beta-features。
	req.Header.Set("version", codexDesktopVersion)
	req.Header.Set("x-codex-beta-features", codexBetaFeaturesValue)
	// 0.145 实抓：所有 /codex/responses POST 恒定携带 responses-lite 头；
	// 0.144 的 x-responsesapi-include-timing-metrics 已在新版移除，不再发送。
	req.Header.Set("x-openai-internal-codex-responses-lite", codexResponsesLiteValue)
	// content-type 钉死为 application/json（实抓基准为裸值，不带 charset）。
	req.Header.Set("content-type", "application/json")
	req.Header.Set("originator", codexDesktopOriginator)
	// x-oai-attestation：0.145 实抓 turn POST 仅发 {"v":1,"s":1}；
	// compact POST 与 WS prewarm 仍发完整 CBOR token（app_session_id 按账号派生）。
	if isCompact {
		req.Header.Set("x-oai-attestation", codexOAIAttestationForAccount(accountID, chatgptAccountID))
	} else {
		req.Header.Set("x-oai-attestation", codexOAIAttestationLite)
	}

	if isCompact {
		req.Header.Set("accept", "application/json")
	} else {
		req.Header.Set("accept", "text/event-stream")
	}

	sessUUID := generateCodexSessionUUID(accountID, apiKeyID, sessionSeed)
	if sessUUID == "" {
		if v, err := uuid.NewV7(); err == nil {
			sessUUID = v.String()
		}
	}
	if sessUUID == "" {
		return
	}
	windowID := sessUUID + ":0"
	req.Header.Set("session-id", sessUUID)
	req.Header.Set("thread-id", sessUUID)
	// x-client-request-id 与真实 Codex 一致，取 thread-id（实抓三者同值）。
	req.Header.Set("x-client-request-id", sessUUID)
	req.Header.Set("x-codex-window-id", windowID)
	// 0.145 实抓：手动压缩请求的 metadata request_kind="compaction"、无 workspaces，
	// compaction 对象原样保留入站值；普通一轮仍走 turn 画像。
	if compaction, isCompaction := extractCodexCompactionRequest(inboundTurnMetadata); isCompaction {
		req.Header.Set("x-codex-turn-metadata", buildCodexCompactionMetadata(sessUUID, windowID, installationID, compaction))
	} else {
		req.Header.Set("x-codex-turn-metadata", buildCodexTurnMetadata(sessUUID, windowID, inboundWorkspaces, installationID))
	}
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
func applyCodexOAuthWSMimicHeaders(headers http.Header, accountID, apiKeyID int64, sessionSeed, originator, turnMetadata string) {
	if headers == nil {
		return
	}
	authorization := strings.TrimSpace(headers.Get("authorization"))
	chatgptAccountID := strings.TrimSpace(headers.Get("chatgpt-account-id"))
	installationID := codexInstallationIDForAccount(accountID, chatgptAccountID)
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

	headers.Set("user-agent", codexDesktopUserAgent)
	headers.Set("version", codexDesktopVersion)
	headers.Set("openai-beta", openAIWSBetaV2Value)
	headers.Set("originator", codexDesktopOriginator)
	headers.Set("x-codex-beta-features", codexBetaFeaturesValue)
	// x-oai-attestation 为 Desktop App 特有的证明头（app_session_id 按账号派生）。
	headers.Set("x-oai-attestation", codexOAIAttestationForAccount(accountID, chatgptAccountID))

	sessUUID := generateCodexSessionUUID(accountID, apiKeyID, sessionSeed)
	if sessUUID == "" {
		if v, err := uuid.NewV7(); err == nil {
			sessUUID = v.String()
		}
	}
	if sessUUID == "" {
		return
	}
	windowID := sessUUID + ":0"
	headers.Set("session-id", sessUUID)
	headers.Set("thread-id", sessUUID)
	headers.Set("x-client-request-id", sessUUID)
	headers.Set("x-codex-window-id", windowID)

	metadata := buildCodexWSPrewarmMetadata(sessUUID, windowID, extractCodexWorkspaces(turnMetadata), installationID)
	if metadata != "" {
		headers.Set("x-codex-turn-metadata", metadata)
	}
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
