package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/google/uuid"
)

// 真实 Codex Desktop App 固定头值（基准：Codex Desktop 0.142.0-alpha.6 实抓报文）。
const (
	// codexBetaFeaturesValue 对应 x-codex-beta-features 头（实抓：Desktop App 恒定发送该值）。
	codexBetaFeaturesValue = "remote_compaction_v2"
	// codexTurnMetadataSandbox 对应 HTTP POST x-codex-turn-metadata.sandbox 字段（实抓：Desktop App HTTP POST 为 none）。
	codexTurnMetadataSandbox = "none"
	// codexWSPrewarmSandbox 对应 WS prewarm x-codex-turn-metadata.sandbox 字段（实抓：Desktop App WS 为 windows_elevated）。
	codexWSPrewarmSandbox = "windows_elevated"
	// codexDesktopOriginator 对应 originator 头（实抓：Desktop App 为 "Codex Desktop"）。
	codexDesktopOriginator = "Codex Desktop"
	// codexInstallationID 对应 x-codex-turn-metadata.installation_id 字段（实抓固定值）。
	codexInstallationID = "00e9ffcb-88d7-4ee8-aeca-1982d91a1330"
	// codexOAIAttestation 对应 x-oai-attestation 头（实抓固定值，Desktop App 特有）。
	codexOAIAttestation = `{"v":1,"s":0,"t":"v1.o2plcnJvcl9jb2RlAWlidW5kbGVfaWRwY29tLm9wZW5haS5jb2RleGFmWFanAAEBgWV6aC1DTgJlemgtQ04DbUFzaWEvU2hhbmdoYWkEGQevBfs_-AAAAAAAAAZ4JGVlYjk4ZTFjLTU4OTAtNDc5YS1hOGRiLTM1MTZmYTUzMzhlNg"}`
)

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

// generateCodexSessionUUID 由 (apiKeyID, seed) 确定性派生一个合法 UUIDv7 形态的会话标识。
//
// 设计目标（与真实 Codex 的进程级 session UUID 对齐）：
//   - 同一会话（同 seed）跨多轮请求恒定，保持粘性路由与上游会话连续性；
//   - 不同 API Key 即使原始 seed 相同也派生出不同 UUID，避免跨用户会话碰撞；
//   - 零存储：纯哈希派生，无需缓存。
//
// 注：UUIDv7 前 48 位本应为生成时间戳，这里为哈希值而非真实时间；实测上游不校验，风险极低。
// seed 为空返回空串（由调用方决定是否回退随机 UUIDv7）。
func generateCodexSessionUUID(apiKeyID int64, seed string) string {
	isolated := isolateOpenAISessionID(apiKeyID, seed)
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

// buildCodexTurnMetadata 生成 x-codex-turn-metadata 头的 JSON 值，字段集合与顺序严格对齐真实 Codex
// Desktop App 0.142.0-alpha.6 实抓报文（普通一轮 request_kind=turn）：
// installation_id, session_id, thread_id, turn_id, window_id, request_kind, sandbox, workspaces, turn_started_at_unix_ms, workspace_kind。
// turn_id 为每请求新生成的 UUIDv7；session_id/thread_id 复用会话 UUID。
// 注意：Desktop App 不含 thread_source 字段，sandbox 为 "none"（与 CLI 的 "windows_elevated" 不同）。
// workspaces 为代理端无法获知的客户端本地动态数据，置空对象 {} 以保持字段集合一致。
func buildCodexTurnMetadata(sessionUUID, windowID string) string {
	turnID := sessionUUID
	if v, err := uuid.NewV7(); err == nil {
		turnID = v.String()
	}
	meta := struct {
		InstallationID      string         `json:"installation_id"`
		SessionID           string         `json:"session_id"`
		ThreadID            string         `json:"thread_id"`
		TurnID              string         `json:"turn_id"`
		WindowID            string         `json:"window_id"`
		RequestKind         string         `json:"request_kind"`
		Sandbox             string         `json:"sandbox"`
		Workspaces          map[string]any `json:"workspaces"`
		TurnStartedAtUnixMs int64          `json:"turn_started_at_unix_ms"`
		WorkspaceKind       string         `json:"workspace_kind"`
	}{
		InstallationID:      codexInstallationID,
		SessionID:           sessionUUID,
		ThreadID:            sessionUUID,
		TurnID:              turnID,
		WindowID:            windowID,
		RequestKind:         "turn",
		Sandbox:             codexTurnMetadataSandbox,
		Workspaces:          map[string]any{},
		TurnStartedAtUnixMs: time.Now().UnixMilli(),
		WorkspaceKind:       "project",
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return ""
	}
	return string(b)
}

// buildCodexWSPrewarmMetadata 生成 WS prewarm 的 x-codex-turn-metadata 头 JSON 值，
// 字段集合与顺序对齐 Codex Desktop App 0.142.0-alpha.6 实抓报文：
// installation_id, session_id, thread_id, turn_id, window_id, request_kind, sandbox。
// 注意：Desktop App WS prewarm 不含 thread_source/turn_started_at_unix_ms/workspace_kind，
// sandbox 为 "windows_elevated"（与 HTTP POST 的 "none" 不同）。
func buildCodexWSPrewarmMetadata(sessionUUID, windowID string) string {
	meta := struct {
		InstallationID string `json:"installation_id"`
		SessionID      string `json:"session_id"`
		ThreadID       string `json:"thread_id"`
		TurnID         string `json:"turn_id"`
		WindowID       string `json:"window_id"`
		RequestKind    string `json:"request_kind"`
		Sandbox        string `json:"sandbox"`
	}{
		InstallationID: codexInstallationID,
		SessionID:      sessionUUID,
		ThreadID:       sessionUUID,
		TurnID:         "",
		WindowID:       windowID,
		RequestKind:    "prewarm",
		Sandbox:        codexWSPrewarmSandbox,
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return ""
	}
	return string(b)
}

// applyCodexOAuthMimicHeaders 将 OAuth 上游请求头无条件重建为与真实 Codex Desktop App HTTP POST 一致
// （字段集合 + 取值 + 实抓基准），完全无视入站客户端传入的对应头。不处理 HTTP/2 头发送顺序（按既定范围）。
//
// sessionSeed 为隔离前的原始会话种子；为空时回退随机 UUIDv7，
// 以保证 session-id/thread-id 始终存在（与真实 Codex 行为一致）。
func applyCodexOAuthMimicHeaders(req *http.Request, apiKeyID int64, sessionSeed, originator string, isCompact bool) {
	if req == nil {
		return
	}
	authorization := strings.TrimSpace(req.Header.Get("authorization"))
	chatgptAccountID := strings.TrimSpace(req.Header.Get("chatgpt-account-id"))
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
	// content-type 钉死为 application/json（实抓基准为裸值，不带 charset）。
	req.Header.Set("content-type", "application/json")
	req.Header.Set("originator", codexDesktopOriginator)
	// x-oai-attestation 为 Desktop App 特有的远程证明头（实抓固定值）。
	req.Header.Set("x-oai-attestation", codexOAIAttestation)

	if isCompact {
		req.Header.Set("accept", "application/json")
	} else {
		req.Header.Set("accept", "text/event-stream")
	}

	sessUUID := generateCodexSessionUUID(apiKeyID, sessionSeed)
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
	req.Header.Set("x-codex-turn-metadata", buildCodexTurnMetadata(sessUUID, windowID))
}

// applyCodexOAuthWSMimicHeaders 将 OAuth 上游 WebSocket 握手业务头重建为 Codex Desktop App 画像。
// WebSocket 协议层头（Host/Upgrade/Sec-WebSocket-*）由底层 WS 库生成；这里仅处理
// Codex/OpenAI 业务头，避免把 HTTP 兼容头（session_id/conversation_id 等）带到握手里。
func applyCodexOAuthWSMimicHeaders(headers http.Header, apiKeyID int64, sessionSeed, originator, turnMetadata string) {
	if headers == nil {
		return
	}
	authorization := strings.TrimSpace(headers.Get("authorization"))
	chatgptAccountID := strings.TrimSpace(headers.Get("chatgpt-account-id"))
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
	_ = turnMetadata

	headers.Set("user-agent", codexDesktopUserAgent)
	headers.Set("version", codexDesktopVersion)
	headers.Set("openai-beta", openAIWSBetaV2Value)
	headers.Set("originator", codexDesktopOriginator)
	headers.Set("x-codex-beta-features", codexBetaFeaturesValue)
	// x-oai-attestation 为 Desktop App 特有的远程证明头（实抓固定值）。
	headers.Set("x-oai-attestation", codexOAIAttestation)

	sessUUID := generateCodexSessionUUID(apiKeyID, sessionSeed)
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

	metadata := buildCodexWSPrewarmMetadata(sessUUID, windowID)
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
