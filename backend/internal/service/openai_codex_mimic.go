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

// 真实 Codex CLI HTTP POST 固定头值（基准：codex 0.139.0 实抓报文）。
const (
	// codexBetaFeaturesValue 对应 x-codex-beta-features 头（实抓：HTTP POST 恒定发送该值）。
	codexBetaFeaturesValue = "terminal_resize_reflow"
	// codexTurnMetadataSandbox 对应 x-codex-turn-metadata.sandbox 字段。
	codexTurnMetadataSandbox = "windows_elevated"
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
// 0.139.0 实抓报文（普通一轮 request_kind=turn）：
// session_id, thread_id, thread_source, turn_id, sandbox, turn_started_at_unix_ms, request_kind, window_id。
// turn_id 为每请求新生成的 UUIDv7；session_id/thread_id 复用会话 UUID。
func buildCodexTurnMetadata(sessionUUID, windowID string) string {
	turnID := sessionUUID
	if v, err := uuid.NewV7(); err == nil {
		turnID = v.String()
	}
	meta := struct {
		SessionID           string `json:"session_id"`
		ThreadID            string `json:"thread_id"`
		ThreadSource        string `json:"thread_source"`
		TurnID              string `json:"turn_id"`
		Sandbox             string `json:"sandbox"`
		TurnStartedAtUnixMs int64  `json:"turn_started_at_unix_ms"`
		RequestKind         string `json:"request_kind"`
		WindowID            string `json:"window_id"`
	}{
		SessionID:           sessionUUID,
		ThreadID:            sessionUUID,
		ThreadSource:        "user",
		TurnID:              turnID,
		Sandbox:             codexTurnMetadataSandbox,
		TurnStartedAtUnixMs: time.Now().UnixMilli(),
		RequestKind:         "turn",
		WindowID:            windowID,
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return ""
	}
	return string(b)
}

// applyCodexOAuthMimicHeaders 将 OAuth 上游请求头无条件重建为与真实 Codex CLI HTTP POST 一致
// （字段集合 + 取值 + 实抓基准），完全无视入站客户端传入的对应头。不处理 HTTP/2 头发送顺序（按既定范围）。
//
// sessionSeed 为隔离前的原始会话种子；为空时回退随机 UUIDv7，
// 以保证 session-id/thread-id 始终存在（与真实 Codex 行为一致）。
func applyCodexOAuthMimicHeaders(req *http.Request, apiKeyID int64, sessionSeed, originator string, isCompact bool) {
	if req == nil {
		return
	}
	// 删除入站/历史变体：真实 Codex HTTP POST 使用 session-id/thread-id（连字符），
	// 且 HTTP 路径不发送 OpenAI-Beta（该头仅出现在 WebSocket 升级请求中）。
	req.Header.Del("session_id")
	req.Header.Del("conversation_id")
	req.Header.Del("OpenAI-Beta")

	// 清理经请求头白名单透传进来的“非 Codex”噪声头：真实 Codex CLI 的 HTTP POST 实抓中
	// 不发送以下头。删除它们以保证上游请求头是与 Codex 一致的“固定集合”，不受入站客户端影响。
	for _, noisy := range codexMimicStripInboundHeaders {
		req.Header.Del(noisy)
	}

	// User-Agent 无条件强制为 Codex CLI 画像（忽略入站 UA）；账号自定义 UA 仍可由调用方后置覆盖。
	req.Header.Set("user-agent", codexCLIUserAgent)
	// 实抓基准：HTTP POST 恒定携带 version 与 x-codex-beta-features。
	req.Header.Set("version", codexCLIVersion)
	req.Header.Set("x-codex-beta-features", codexBetaFeaturesValue)
	// content-type 钉死为 application/json（实抓基准为裸值，不带 charset）。
	req.Header.Set("content-type", "application/json")
	if strings.TrimSpace(originator) != "" {
		req.Header.Set("originator", originator)
	}

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
	compressed := httputil.CompressZstd(body)
	req.Body = io.NopCloser(bytes.NewReader(compressed))
	req.ContentLength = int64(len(compressed))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(compressed)), nil
	}
	req.Header.Set("content-encoding", "zstd")
}
