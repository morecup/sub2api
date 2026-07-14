package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func f64p(v float64) *float64 { return &v }

type httpUpstreamRecorder struct {
	lastReq      *http.Request
	lastBody     []byte
	lastProxyURL string
	requests     []*http.Request
	bodies       [][]byte

	resp      *http.Response
	responses []*http.Response
	err       error
}

type passthroughErrReadCloser struct {
	err error
}

func (r passthroughErrReadCloser) Read(_ []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	return 0, io.ErrUnexpectedEOF
}

func (r passthroughErrReadCloser) Close() error {
	return nil
}

func (u *httpUpstreamRecorder) Do(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
	u.lastReq = req
	u.lastProxyURL = proxyURL
	if req != nil && req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		_ = req.Body.Close()
		// 保持 req.Body 与 Content-Encoding 头原样（高保真），仅在记录用快照时按需解压，
		// 使既有针对 lastBody 的 JSON 断言在默认开启 zstd 压缩后依然有效。
		req.Body = io.NopCloser(bytes.NewReader(b))
		decoded := decodeRecorderRequestBody(req.Header.Get("Content-Encoding"), b)
		u.lastBody = decoded
		u.bodies = append(u.bodies, append([]byte(nil), decoded...))
	}
	u.requests = append(u.requests, req)
	if u.err != nil {
		return nil, u.err
	}
	if len(u.responses) > 0 {
		resp := u.responses[0]
		u.responses = u.responses[1:]
		return resp, nil
	}
	return u.resp, nil
}

func (u *httpUpstreamRecorder) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, accountConcurrency)
}

// decodeRecorderRequestBody 解压记录用的请求体快照（仅支持 zstd），失败或非压缩时原样返回。
func decodeRecorderRequestBody(encoding string, raw []byte) []byte {
	if !strings.EqualFold(strings.TrimSpace(encoding), "zstd") {
		return raw
	}
	dec, err := zstd.NewReader(nil)
	if err != nil {
		return raw
	}
	defer dec.Close()
	out, err := dec.DecodeAll(raw, nil)
	if err != nil {
		return raw
	}
	return out
}

func requireCodexDesktopBodyMetadataMatchesHeaders(t *testing.T, req *http.Request, body []byte) {
	t.Helper()
	require.NotNil(t, req)

	turnMetadata := req.Header.Get("X-Codex-Turn-Metadata")
	sessionID := req.Header.Get("Session-Id")
	threadID := req.Header.Get("Thread-Id")
	windowID := req.Header.Get("X-Codex-Window-Id")
	require.NotEmpty(t, turnMetadata)
	require.NotEmpty(t, sessionID)
	require.Equal(t, sessionID, threadID)
	require.Equal(t, sessionID, gjson.Get(turnMetadata, "session_id").String())
	require.Equal(t, threadID, gjson.Get(turnMetadata, "thread_id").String())
	require.Equal(t, windowID, gjson.Get(turnMetadata, "window_id").String())
	require.NotEmpty(t, gjson.Get(turnMetadata, "turn_id").String())

	require.Equal(t, sessionID, gjson.GetBytes(body, "client_metadata.session_id").String())
	require.Equal(t, threadID, gjson.GetBytes(body, "client_metadata.thread_id").String())
	require.Equal(t, gjson.Get(turnMetadata, "turn_id").String(), gjson.GetBytes(body, "client_metadata.turn_id").String())
	require.Equal(t, windowID, gjson.GetBytes(body, "client_metadata.x-codex-window-id").String())
	require.Equal(t, codexInstallationID, gjson.GetBytes(body, "client_metadata.x-codex-installation-id").String())
	require.Equal(t, turnMetadata, gjson.GetBytes(body, "client_metadata.x-codex-turn-metadata").String())
}

func TestOpenAIBuildUpstreamRequestOAuthCodexMimicHeadersAndZstd(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gpt-5.5","input":[{"type":"message","role":"user","content":"hi"}]}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	// 入站噪声头：accept-language / x-codex-turn-state 在请求头白名单内，验证伪装时被剥离（固定请求头）。
	c.Request.Header.Set("Accept-Language", "en-US,en;q=0.9")
	c.Request.Header.Set("X-Codex-Turn-State", "inbound-should-be-stripped")
	c.Request.Header.Set("session_id", "inbound-session-should-be-ignored")
	c.Request.Header.Set("conversation_id", "inbound-conversation-should-be-ignored")
	c.Request.Header.Set("originator", "Codex Desktop")
	// 0.144 仅保留入站 workspaces；旧 workspace_kind 与伪造 thread_source 均由画像重建。
	c.Request.Header.Set("X-Codex-Turn-Metadata", `{"workspaces":{"/foo/bar":{"associated_remote_urls":{"origin":"https://github.com/foo/bar"},"latest_git_commit_hash":"abc123","has_changes":true}},"workspace_kind":"projectless","session_id":"inbound-should-be-overwritten","thread_source":"inbound-should-be-removed"}`)

	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "chatgpt-acc", "user_agent": "custom-ua-should-not-override"}}

	req, err := svc.buildUpstreamRequest(c.Request.Context(), c, account, body, "token", true, "sess-seed-1", true)
	require.NoError(t, err)

	// 1:1 Codex 伪装头：session-id/thread-id/x-client-request-id 三者同值（连字符命名）。
	sessionID := req.Header.Get("Session-Id")
	require.Len(t, sessionID, 36)
	require.Equal(t, sessionID, req.Header.Get("Thread-Id"))
	require.Equal(t, sessionID, req.Header.Get("X-Client-Request-Id"))
	require.Equal(t, sessionID+":0", req.Header.Get("X-Codex-Window-Id"))
	require.Equal(t, codexDesktopVersion, req.Header.Get("Version"))
	// 实抓基准：HTTP POST 恒定发送 x-codex-beta-features=remote_compaction_v2。
	require.Equal(t, "remote_compaction_v2", req.Header.Get("X-Codex-Beta-Features"))
	require.Equal(t, "true", req.Header.Get("X-Responsesapi-Include-Timing-Metrics"))
	require.Equal(t, "text/event-stream", req.Header.Get("Accept"))
	require.Equal(t, "Codex Desktop", req.Header.Get("Originator"))
	// UA 无条件强制为 Codex Desktop 画像（忽略入站 UA）。
	require.Equal(t, codexDesktopUserAgent, req.Header.Get("User-Agent"))
	// HTTP 路径不发送 OpenAI-Beta / x-codex-installation-id，且旧下划线 session_id 变体已移除。
	require.Empty(t, req.Header.Get("OpenAI-Beta"))
	require.Empty(t, req.Header.Get("Session_Id"))
	require.Empty(t, req.Header.Get("X-Codex-Installation-Id"))
	// 固定请求头：经白名单透传进来的非 Codex 噪声头被剥离，content-type 钉死为裸 application/json。
	require.Empty(t, req.Header.Get("Accept-Language"))
	require.Empty(t, req.Header.Get("X-Codex-Turn-State"))
	require.Equal(t, "application/json", req.Header.Get("Content-Type"))

	// x-codex-turn-metadata：字段与 Desktop 26.707.31123 / codex-rs 0.144.0-alpha.4 对齐。
	meta := req.Header.Get("X-Codex-Turn-Metadata")
	require.Equal(t, sessionID, gjson.Get(meta, "session_id").String())
	require.Equal(t, sessionID, gjson.Get(meta, "thread_id").String())
	require.Equal(t, "user", gjson.Get(meta, "thread_source").String())
	require.Equal(t, codexInstallationID, gjson.Get(meta, "installation_id").String())
	require.Equal(t, "none", gjson.Get(meta, "sandbox").String())
	require.Equal(t, "turn", gjson.Get(meta, "request_kind").String())
	require.Equal(t, sessionID+":0", gjson.Get(meta, "window_id").String())
	require.NotEmpty(t, gjson.Get(meta, "turn_id").String())
	require.Greater(t, gjson.Get(meta, "turn_started_at_unix_ms").Int(), int64(0))
	require.False(t, gjson.Get(meta, "workspace_kind").Exists())
	require.True(t, gjson.Get(meta, "workspaces").IsObject())
	require.Equal(t, "https://github.com/foo/bar", gjson.Get(meta, "workspaces./foo/bar.associated_remote_urls.origin").String())
	require.Equal(t, "abc123", gjson.Get(meta, "workspaces./foo/bar.latest_git_commit_hash").String())
	require.True(t, gjson.Get(meta, "workspaces./foo/bar.has_changes").Bool())
	// x-oai-attestation 为 Desktop App 特有的远程证明头。
	require.NotEmpty(t, req.Header.Get("X-Oai-Attestation"))

	// 请求体 zstd 压缩，解压后包含与 header 同源的完整 client_metadata。
	require.Equal(t, "zstd", req.Header.Get("Content-Encoding"))
	require.NotNil(t, req.Body)
	compressed, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.Equal(t, int64(len(compressed)), req.ContentLength)
	decodedBody := decodeRecorderRequestBody("zstd", compressed)
	require.Equal(t, "gpt-5.5", gjson.GetBytes(decodedBody, "model").String())
	require.Equal(t, sessionID, gjson.GetBytes(decodedBody, "client_metadata.session_id").String())
	require.Equal(t, sessionID, gjson.GetBytes(decodedBody, "client_metadata.thread_id").String())
	require.Equal(t, gjson.Get(meta, "turn_id").String(), gjson.GetBytes(decodedBody, "client_metadata.turn_id").String())
	require.Equal(t, codexInstallationID, gjson.GetBytes(decodedBody, "client_metadata.x-codex-installation-id").String())
	require.Equal(t, sessionID+":0", gjson.GetBytes(decodedBody, "client_metadata.x-codex-window-id").String())
	require.Equal(t, meta, gjson.GetBytes(decodedBody, "client_metadata.x-codex-turn-metadata").String())

	// 确定性派生：相同 (apiKeyID, seed) 复算 session-id 不变。
	require.Equal(t, sessionID, generateCodexSessionUUID(0, "sess-seed-1"))
	require.NotEqual(t, sessionID, generateCodexSessionUUID(0, "inbound-session-should-be-ignored"))
}

func TestOpenAIGatewayService_ResponsesUnknownModelDoesNotFallbackToGPT54(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	originalBody := []byte(`{"model":"gpt6","stream":false,"instructions":"local-test-instructions","input":[{"type":"text","text":"hi"}]}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(originalBody))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"rid_unknown_model"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","message":"model not found"}}`)),
	}}

	svc := &OpenAIGatewayService{
		cfg:          &config.Config{},
		httpUpstream: upstream,
	}
	account := &Account{
		ID:          123,
		Name:        "acc",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":       "oauth-token",
			"chatgpt_account_id": "chatgpt-acc",
		},
		Status:      StatusActive,
		Schedulable: true,
	}

	result, err := svc.Forward(context.Background(), c, account, originalBody)
	require.Error(t, err)
	require.Nil(t, result)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, "https://chatgpt.com/backend-api/codex/responses", upstream.lastReq.URL.String())
	require.Equal(t, "gpt6", gjson.GetBytes(upstream.lastBody, "model").String())
	require.NotEqual(t, "gpt-5.4", gjson.GetBytes(upstream.lastBody, "model").String())
	require.True(t, rec.Code >= http.StatusBadRequest)
}

func TestOpenAIGatewayService_NativeResponsesBodyModificationPreservesHTMLChars(t *testing.T) {
	gin.SetMode(gin.TestMode)

	payloadText := strings.Repeat(`<tag>&value</tag>`, 128)
	originalBody := []byte(fmt.Sprintf(`{"model":"gpt-5.5","stream":false,"max_output_tokens":100,"previous_response_id":"resp_prev","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":%q}]}]}`, payloadText))
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(originalBody))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"rid_native_reencode"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","message":"stop after capture"}}`)),
	}}
	svc := &OpenAIGatewayService{
		cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{
			Enabled:           false,
			AllowInsecureHTTP: true,
		}}},
		httpUpstream: upstream,
	}
	account := &Account{
		ID:          456,
		Name:        "openai-apikey",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "sk-test",
			"base_url": "http://upstream.example",
		},
		Extra: map[string]any{
			openai_compat.ExtraKeyResponsesMode:      string(openai_compat.ResponsesSupportModeAuto),
			openai_compat.ExtraKeyResponsesSupported: true,
		},
		Status:      StatusActive,
		Schedulable: true,
	}

	result, err := svc.Forward(context.Background(), c, account, originalBody)
	require.Error(t, err)
	require.Nil(t, result)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, "http://upstream.example/v1/responses", upstream.lastReq.URL.String())
	require.Contains(t, string(upstream.lastBody), payloadText)
	require.NotContains(t, string(upstream.lastBody), `\\u003c`)
	require.NotContains(t, string(upstream.lastBody), `\\u003e`)
	require.NotContains(t, string(upstream.lastBody), `\\u0026`)
}

func TestOpenAIGatewayService_OAuthMessagesBridgeDoesNotInjectDefaultInstructions(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	originalBody := []byte(`{"model":"gpt-5.5","stream":true,"prompt_cache_key":"anthropic-metadata-session-1","input":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"<sub2api-claude-code-todo-guard>"}]},{"type":"message","role":"user","content":"hello"}]}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(originalBody))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"rid_bridge"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","message":"bridge stop"}}`)),
	}}
	svc := &OpenAIGatewayService{
		cfg:          &config.Config{},
		httpUpstream: upstream,
	}
	account := &Account{
		ID:          123,
		Name:        "acc",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":       "oauth-token",
			"chatgpt_account_id": "chatgpt-acc",
		},
		Status:      StatusActive,
		Schedulable: true,
	}

	result, err := svc.Forward(context.Background(), c, account, originalBody)
	require.Error(t, err)
	require.Nil(t, result)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, "", gjson.GetBytes(upstream.lastBody, "instructions").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "prompt_cache_key").Exists())
	require.NotEmpty(t, upstream.lastReq.Header.Get("Session_Id"))
	require.Empty(t, upstream.lastReq.Header.Get("Conversation_Id"))
	require.Empty(t, upstream.lastReq.Header.Get("OpenAI-Beta"))
	require.Empty(t, upstream.lastReq.Header.Get("originator"))
}

type openAIPassthroughFailoverRepo struct {
	stubOpenAIAccountRepo
	rateLimitCalls []time.Time
	overloadCalls  []time.Time
}

func (r *openAIPassthroughFailoverRepo) SetRateLimited(_ context.Context, _ int64, resetAt time.Time) error {
	r.rateLimitCalls = append(r.rateLimitCalls, resetAt)
	return nil
}

func (r *openAIPassthroughFailoverRepo) SetOverloaded(_ context.Context, _ int64, until time.Time) error {
	r.overloadCalls = append(r.overloadCalls, until)
	return nil
}

var structuredLogCaptureMu sync.Mutex

type inMemoryLogSink struct {
	mu     sync.Mutex
	events []*logger.LogEvent
}

func (s *inMemoryLogSink) WriteLogEvent(event *logger.LogEvent) {
	if event == nil {
		return
	}
	cloned := *event
	if event.Fields != nil {
		cloned.Fields = make(map[string]any, len(event.Fields))
		for k, v := range event.Fields {
			cloned.Fields[k] = v
		}
	}
	s.mu.Lock()
	s.events = append(s.events, &cloned)
	s.mu.Unlock()
}

func (s *inMemoryLogSink) ContainsMessage(substr string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ev := range s.events {
		if ev != nil && strings.Contains(ev.Message, substr) {
			return true
		}
	}
	return false
}

func (s *inMemoryLogSink) ContainsMessageAtLevel(substr, level string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	wantLevel := strings.ToLower(strings.TrimSpace(level))
	for _, ev := range s.events {
		if ev == nil {
			continue
		}
		if strings.Contains(ev.Message, substr) && strings.ToLower(strings.TrimSpace(ev.Level)) == wantLevel {
			return true
		}
	}
	return false
}

func (s *inMemoryLogSink) ContainsFieldValue(field, substr string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ev := range s.events {
		if ev == nil || ev.Fields == nil {
			continue
		}
		if v, ok := ev.Fields[field]; ok && strings.Contains(fmt.Sprint(v), substr) {
			return true
		}
	}
	return false
}

func (s *inMemoryLogSink) ContainsField(field string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ev := range s.events {
		if ev == nil || ev.Fields == nil {
			continue
		}
		if _, ok := ev.Fields[field]; ok {
			return true
		}
	}
	return false
}

func captureStructuredLog(t *testing.T) (*inMemoryLogSink, func()) {
	t.Helper()
	structuredLogCaptureMu.Lock()

	err := logger.Init(logger.InitOptions{
		Level:       "debug",
		Format:      "json",
		ServiceName: "sub2api",
		Environment: "test",
		Output: logger.OutputOptions{
			ToStdout: true,
			ToFile:   false,
		},
		Sampling: logger.SamplingOptions{Enabled: false},
	})
	require.NoError(t, err)

	sink := &inMemoryLogSink{}
	logger.SetSink(sink)
	return sink, func() {
		logger.SetSink(nil)
		structuredLogCaptureMu.Unlock()
	}
}

func TestOpenAIGatewayService_OAuthPassthrough_StreamKeepsToolNameAndBodyNormalized(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.1.0")
	c.Request.Header.Set("Authorization", "Bearer inbound-should-not-forward")
	c.Request.Header.Set("Cookie", "secret=1")
	c.Request.Header.Set("X-Api-Key", "sk-inbound")
	c.Request.Header.Set("X-Goog-Api-Key", "goog-inbound")
	c.Request.Header.Set("Accept-Encoding", "gzip")
	c.Request.Header.Set("Proxy-Authorization", "Basic abc")
	c.Request.Header.Set("X-Test", "keep")
	c.Request.Header.Set("x-codex-beta-features", "remote_compaction_v2")

	originalBody := []byte(`{"model":"gpt-5.2","stream":true,"store":true,"instructions":"local-test-instructions","input":[{"type":"text","text":"hi"}]}`)

	upstreamSSE := strings.Join([]string{
		`data: {"type":"response.output_item.added","item":{"type":"tool_call","tool_calls":[{"function":{"name":"apply_patch"}}]}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid"}},
		Body:       io.NopCloser(strings.NewReader(upstreamSSE)),
	}
	upstream := &httpUpstreamRecorder{resp: resp}

	svc := &OpenAIGatewayService{
		cfg:          &config.Config{Gateway: config.GatewayConfig{ForceCodexCLI: false}},
		httpUpstream: upstream,
		openAITokenProvider: &OpenAITokenProvider{ // minimal: will be bypassed by nil cache/service, but GetAccessToken uses provider only if non-nil
			accountRepo: nil,
		},
	}

	account := &Account{
		ID:             123,
		Name:           "acc",
		Platform:       PlatformOpenAI,
		Type:           AccountTypeOAuth,
		Concurrency:    1,
		Credentials:    map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-acc"},
		Extra:          map[string]any{"openai_passthrough": true, "openai_device_id": "dev-should-not-leak"},
		Status:         StatusActive,
		Schedulable:    true,
		RateMultiplier: f64p(1),
	}

	// Use the gateway method that reads token from credentials when provider is nil.
	svc.openAITokenProvider = nil

	result, err := svc.Forward(context.Background(), c, account, originalBody)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Stream)

	// 1) 透传 OAuth 请求体与旧链路关键行为保持一致：store=false + stream=true。
	require.Equal(t, false, gjson.GetBytes(upstream.lastBody, "store").Bool())
	require.Equal(t, true, gjson.GetBytes(upstream.lastBody, "stream").Bool())
	require.Equal(t, "local-test-instructions", strings.TrimSpace(gjson.GetBytes(upstream.lastBody, "instructions").String()))
	// 其余关键字段保持原值。
	require.Equal(t, "gpt-5.2", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, "hi", gjson.GetBytes(upstream.lastBody, "input.0.text").String())
	require.Equal(t, codexInstallationID, gjson.GetBytes(upstream.lastBody, "client_metadata.x-codex-installation-id").String())
	require.NotEqual(t, "dev-should-not-leak", gjson.GetBytes(upstream.lastBody, "client_metadata.x-codex-installation-id").String())
	requireCodexDesktopBodyMetadataMatchesHeaders(t, upstream.lastReq, upstream.lastBody)

	// 2) only auth is replaced; inbound auth/cookie are not forwarded
	require.Equal(t, "Bearer oauth-token", upstream.lastReq.Header.Get("Authorization"))
	// User-Agent 无条件强制为 Codex Desktop 画像，忽略入站 UA（此处入站为 codex_cli_rs/0.1.0）。
	require.Equal(t, codexDesktopUserAgent, upstream.lastReq.Header.Get("User-Agent"))
	require.Empty(t, upstream.lastReq.Header.Get("Cookie"))
	require.Empty(t, upstream.lastReq.Header.Get("X-Api-Key"))
	require.Empty(t, upstream.lastReq.Header.Get("X-Goog-Api-Key"))
	require.Empty(t, upstream.lastReq.Header.Get("Accept-Encoding"))
	require.Empty(t, upstream.lastReq.Header.Get("Proxy-Authorization"))
	require.Empty(t, upstream.lastReq.Header.Get("X-Test"))
	require.Equal(t, "remote_compaction_v2", upstream.lastReq.Header.Get("x-codex-beta-features"))

	// 3) required OAuth headers are present
	require.Equal(t, "chatgpt.com", upstream.lastReq.Host)
	require.Equal(t, "chatgpt-acc", upstream.lastReq.Header.Get("chatgpt-account-id"))

	// 4) downstream SSE keeps tool name (no toolCorrector)
	body := rec.Body.String()
	require.Contains(t, body, "apply_patch")
	require.NotContains(t, body, "\"name\":\"edit\"")
}

func TestOpenAIGatewayService_OAuthPassthrough_CompactUsesJSONAndKeepsNonStreaming(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", bytes.NewReader(nil))
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.1.0")
	c.Request.Header.Set("Content-Type", "application/json")

	originalBody := []byte(`{"model":"gpt-5.1-codex","stream":true,"store":true,"instructions":"local-test-instructions","input":[{"type":"text","text":"compact me"}]}`)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"rid-compact"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":"cmp_123","usage":{"input_tokens":11,"output_tokens":22}}`)),
	}
	upstream := &httpUpstreamRecorder{resp: resp}

	svc := &OpenAIGatewayService{
		cfg:          &config.Config{Gateway: config.GatewayConfig{ForceCodexCLI: false}},
		httpUpstream: upstream,
	}

	account := &Account{
		ID:             123,
		Name:           "acc",
		Platform:       PlatformOpenAI,
		Type:           AccountTypeOAuth,
		Concurrency:    1,
		Credentials:    map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-acc"},
		Extra:          map[string]any{"openai_passthrough": true},
		Status:         StatusActive,
		Schedulable:    true,
		RateMultiplier: f64p(1),
	}

	result, err := svc.Forward(context.Background(), c, account, originalBody)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.Stream)

	require.False(t, gjson.GetBytes(upstream.lastBody, "store").Exists())
	require.False(t, gjson.GetBytes(upstream.lastBody, "stream").Exists())
	require.Equal(t, "gpt-5.1-codex", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, "compact me", gjson.GetBytes(upstream.lastBody, "input.0.text").String())
	require.Equal(t, "local-test-instructions", strings.TrimSpace(gjson.GetBytes(upstream.lastBody, "instructions").String()))
	require.Equal(t, "application/json", upstream.lastReq.Header.Get("Accept"))
	require.Equal(t, codexDesktopVersion, upstream.lastReq.Header.Get("Version"))
	require.NotEmpty(t, upstream.lastReq.Header.Get("Session-Id"))
	require.Equal(t, "chatgpt.com", upstream.lastReq.Host)
	require.Equal(t, "chatgpt-acc", upstream.lastReq.Header.Get("chatgpt-account-id"))
	require.Contains(t, rec.Body.String(), `"id":"cmp_123"`)
}

func TestOpenAIGatewayService_OAuthPassthrough_UpstreamRequestIgnoresClientCancel(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	reqCtx, cancel := context.WithCancel(context.Background())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil)).WithContext(reqCtx)
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.1.0")
	cancel()

	originalBody := []byte(`{"model":"gpt-5.2","stream":true,"store":true,"instructions":"local-test-instructions","input":[{"type":"text","text":"hi"}]}`)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_passthrough_ctx"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`data: {"type":"response.completed","response":{"usage":{"input_tokens":2,"output_tokens":1}}}`,
			"",
			"data: [DONE]",
			"",
		}, "\n"))),
	}}

	svc := &OpenAIGatewayService{
		cfg:          &config.Config{Gateway: config.GatewayConfig{ForceCodexCLI: false}},
		httpUpstream: upstream,
	}
	account := &Account{
		ID:             123,
		Name:           "acc",
		Platform:       PlatformOpenAI,
		Type:           AccountTypeOAuth,
		Concurrency:    1,
		Credentials:    map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-acc"},
		Extra:          map[string]any{"openai_passthrough": true, "openai_oauth_responses_websockets_v2_mode": OpenAIWSIngressModeOff},
		Status:         StatusActive,
		Schedulable:    true,
		RateMultiplier: f64p(1),
	}

	result, err := svc.Forward(reqCtx, c, account, originalBody)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, upstream.lastReq)
	require.NoError(t, upstream.lastReq.Context().Err())
}

func TestOpenAIGatewayService_OAuthPassthrough_CodexMissingInstructionsRejectedBeforeUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logSink, restore := captureStructuredLog(t)
	defer restore()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses?trace=1", bytes.NewReader(nil))
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.98.0 (Windows 10.0.19045; x86_64) unknown")
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("OpenAI-Beta", "responses=experimental")

	// Codex 模型且缺少 instructions，应在本地直接 403 拒绝，不触达上游。
	originalBody := []byte(`{"model":"gpt-5.1-codex-max","stream":false,"store":true,"input":[{"type":"text","text":"hi"}]}`)

	upstream := &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"rid"}},
			Body:       io.NopCloser(strings.NewReader(`{"output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)),
		},
	}

	svc := &OpenAIGatewayService{
		cfg:          &config.Config{Gateway: config.GatewayConfig{ForceCodexCLI: false}},
		httpUpstream: upstream,
	}

	account := &Account{
		ID:             123,
		Name:           "acc",
		Platform:       PlatformOpenAI,
		Type:           AccountTypeOAuth,
		Concurrency:    1,
		Credentials:    map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-acc"},
		Extra:          map[string]any{"openai_passthrough": true},
		Status:         StatusActive,
		Schedulable:    true,
		RateMultiplier: f64p(1),
	}

	result, err := svc.Forward(context.Background(), c, account, originalBody)
	require.Error(t, err)
	require.Nil(t, result)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "requires a non-empty instructions field")
	require.Nil(t, upstream.lastReq)

	require.True(t, logSink.ContainsMessage("OpenAI passthrough 本地拦截：Codex 请求缺少有效 instructions"))
	require.True(t, logSink.ContainsFieldValue("request_user_agent", "codex_cli_rs/0.98.0 (Windows 10.0.19045; x86_64) unknown"))
	require.True(t, logSink.ContainsFieldValue("reject_reason", "instructions_missing"))
}

func TestOpenAIGatewayService_OAuthPassthrough_DisabledUsesLegacyTransform(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.1.0")

	// store=true + stream=false should be forced to store=false + stream=true by applyCodexOAuthTransform (OAuth legacy path)
	inputBody := []byte(`{"model":"gpt-5.2","stream":false,"store":true,"input":[{"type":"text","text":"hi"}]}`)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid"}},
		Body:       io.NopCloser(strings.NewReader("data: [DONE]\n\n")),
	}
	upstream := &httpUpstreamRecorder{resp: resp}

	svc := &OpenAIGatewayService{
		cfg:          &config.Config{Gateway: config.GatewayConfig{ForceCodexCLI: false}},
		httpUpstream: upstream,
	}

	account := &Account{
		ID:             123,
		Name:           "acc",
		Platform:       PlatformOpenAI,
		Type:           AccountTypeOAuth,
		Concurrency:    1,
		Credentials:    map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-acc"},
		Extra:          map[string]any{"openai_passthrough": false},
		Status:         StatusActive,
		Schedulable:    true,
		RateMultiplier: f64p(1),
	}

	_, err := svc.Forward(context.Background(), c, account, inputBody)
	require.NoError(t, err)

	// legacy path rewrites request body (not byte-equal)
	require.NotEqual(t, inputBody, upstream.lastBody)
	require.Contains(t, string(upstream.lastBody), `"store":false`)
	require.Contains(t, string(upstream.lastBody), `"stream":true`)
}

func TestOpenAIGatewayService_OAuthLegacy_UpstreamRequestIgnoresClientCancel(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	reqCtx, cancel := context.WithCancel(context.Background())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil)).WithContext(reqCtx)
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.1.0")
	cancel()

	originalBody := []byte(`{"model":"gpt-5.2","stream":false,"store":true,"input":[{"type":"text","text":"hi"}]}`)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_legacy_ctx"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1}}}`,
			"",
			"data: [DONE]",
			"",
		}, "\n"))),
	}}

	svc := &OpenAIGatewayService{
		cfg:          &config.Config{Gateway: config.GatewayConfig{ForceCodexCLI: false}},
		httpUpstream: upstream,
	}
	account := &Account{
		ID:             123,
		Name:           "acc",
		Platform:       PlatformOpenAI,
		Type:           AccountTypeOAuth,
		Concurrency:    1,
		Credentials:    map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-acc"},
		Extra:          map[string]any{"openai_passthrough": false, "openai_oauth_responses_websockets_v2_mode": OpenAIWSIngressModeOff},
		Status:         StatusActive,
		Schedulable:    true,
		RateMultiplier: f64p(1),
	}

	result, err := svc.Forward(reqCtx, c, account, originalBody)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, upstream.lastReq)
	require.NoError(t, upstream.lastReq.Context().Err())
}

func TestOpenAIGatewayService_OAuthLegacy_CompositeCodexUAUsesCodexOriginator(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))
	// 复合 UA（前缀不是 codex_cli_rs），历史实现会误判为非 Codex 并走 opencode。
	c.Request.Header.Set("User-Agent", "Mozilla/5.0 codex_cli_rs/0.1.0")

	inputBody := []byte(`{"model":"gpt-5.2","stream":true,"store":false,"input":[{"type":"text","text":"hi"}]}`)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid"}},
		Body:       io.NopCloser(strings.NewReader("data: [DONE]\n\n")),
	}
	upstream := &httpUpstreamRecorder{resp: resp}

	svc := &OpenAIGatewayService{
		cfg:          &config.Config{Gateway: config.GatewayConfig{ForceCodexCLI: false}},
		httpUpstream: upstream,
	}

	account := &Account{
		ID:             123,
		Name:           "acc",
		Platform:       PlatformOpenAI,
		Type:           AccountTypeOAuth,
		Concurrency:    1,
		Credentials:    map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-acc"},
		Extra:          map[string]any{"openai_passthrough": false},
		Status:         StatusActive,
		Schedulable:    true,
		RateMultiplier: f64p(1),
	}

	_, err := svc.Forward(context.Background(), c, account, inputBody)
	require.NoError(t, err)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, "Codex Desktop", upstream.lastReq.Header.Get("originator"))
	require.NotEqual(t, "opencode", upstream.lastReq.Header.Get("originator"))
}

func TestOpenAIGatewayService_OAuthPassthrough_ResponseHeadersAllowXCodex(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.1.0")

	originalBody := []byte(`{"model":"gpt-5.2","stream":true,"input":[{"type":"text","text":"hi"}]}`)

	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("x-request-id", "rid")
	headers.Set("x-codex-primary-used-percent", "12")
	headers.Set("x-codex-secondary-used-percent", "34")
	headers.Set("x-codex-primary-window-minutes", "300")
	headers.Set("x-codex-secondary-window-minutes", "10080")
	headers.Set("x-codex-primary-reset-after-seconds", "1")

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     headers,
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`data: {"type":"response.output_text.delta","delta":"h"}`,
			"",
			`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1,"input_tokens_details":{"cached_tokens":0}}}}`,
			"",
			"data: [DONE]",
			"",
		}, "\n"))),
	}
	upstream := &httpUpstreamRecorder{resp: resp}

	svc := &OpenAIGatewayService{
		cfg:          &config.Config{Gateway: config.GatewayConfig{ForceCodexCLI: false}},
		httpUpstream: upstream,
	}

	account := &Account{
		ID:             123,
		Name:           "acc",
		Platform:       PlatformOpenAI,
		Type:           AccountTypeOAuth,
		Concurrency:    1,
		Credentials:    map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-acc"},
		Extra:          map[string]any{"openai_passthrough": true},
		Status:         StatusActive,
		Schedulable:    true,
		RateMultiplier: f64p(1),
	}

	_, err := svc.Forward(context.Background(), c, account, originalBody)
	require.NoError(t, err)

	require.Equal(t, "12", rec.Header().Get("x-codex-primary-used-percent"))
	require.Equal(t, "34", rec.Header().Get("x-codex-secondary-used-percent"))
}

func TestOpenAIGatewayService_OAuthPassthrough_UpstreamErrorIncludesPassthroughFlag(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.1.0")

	originalBody := []byte(`{"model":"gpt-5.2","stream":false,"input":[{"type":"text","text":"hi"}]}`)

	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"rid"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"bad"}}`)),
	}
	upstream := &httpUpstreamRecorder{resp: resp}

	svc := &OpenAIGatewayService{
		cfg:          &config.Config{Gateway: config.GatewayConfig{ForceCodexCLI: false}},
		httpUpstream: upstream,
	}

	account := &Account{
		ID:             123,
		Name:           "acc",
		Platform:       PlatformOpenAI,
		Type:           AccountTypeOAuth,
		Concurrency:    1,
		Credentials:    map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-acc"},
		Extra:          map[string]any{"openai_passthrough": true},
		Status:         StatusActive,
		Schedulable:    true,
		RateMultiplier: f64p(1),
	}

	_, err := svc.Forward(context.Background(), c, account, originalBody)
	require.Error(t, err)
	require.True(t, c.Writer.Written(), "非 429/529 的 passthrough 错误应继续原样写回客户端")
	require.Equal(t, http.StatusBadRequest, rec.Code)

	// should append an upstream error event with passthrough=true
	v, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	arr, ok := v.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.NotEmpty(t, arr)
	require.True(t, arr[len(arr)-1].Passthrough)
	require.Equal(t, "http_error", arr[len(arr)-1].Kind)
}

func TestOpenAIGatewayService_OpenAIPassthrough_RetryableStatusesTriggerFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalBody := []byte(`{"model":"gpt-5.2","stream":false,"instructions":"local-test-instructions","input":[{"type":"text","text":"hi"}]}`)

	newAccount := func(accountType string) *Account {
		account := &Account{
			ID:             123,
			Name:           "acc",
			Platform:       PlatformOpenAI,
			Type:           accountType,
			Concurrency:    1,
			Extra:          map[string]any{"openai_passthrough": true},
			Status:         StatusActive,
			Schedulable:    true,
			RateMultiplier: f64p(1),
		}
		switch accountType {
		case AccountTypeOAuth:
			account.Credentials = map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-acc"}
		case AccountTypeAPIKey:
			account.Credentials = map[string]any{"api_key": "sk-test"}
		}
		return account
	}

	testCases := []struct {
		name           string
		accountType    string
		statusCode     int
		body           string
		expectFailover bool
		assertRepo     func(t *testing.T, repo *openAIPassthroughFailoverRepo, start time.Time)
	}{
		{
			name:        "oauth_429_rate_limit",
			accountType: AccountTypeOAuth,
			statusCode:  http.StatusTooManyRequests,
			body: func() string {
				resetAt := time.Now().Add(7 * 24 * time.Hour).Unix()
				return fmt.Sprintf(`{"error":{"message":"The usage limit has been reached","type":"usage_limit_reached","resets_at":%d}}`, resetAt)
			}(),
			expectFailover: true,
			assertRepo: func(t *testing.T, repo *openAIPassthroughFailoverRepo, _ time.Time) {
				require.Len(t, repo.rateLimitCalls, 1)
				require.Empty(t, repo.overloadCalls)
				require.True(t, time.Until(repo.rateLimitCalls[0]) > 24*time.Hour)
			},
		},
		{
			name:           "oauth_529_overload",
			accountType:    AccountTypeOAuth,
			statusCode:     529,
			body:           `{"error":{"message":"server overloaded","type":"server_error"}}`,
			expectFailover: true,
			assertRepo: func(t *testing.T, repo *openAIPassthroughFailoverRepo, start time.Time) {
				require.Empty(t, repo.rateLimitCalls)
				require.Len(t, repo.overloadCalls, 1)
				require.WithinDuration(t, start.Add(10*time.Minute), repo.overloadCalls[0], 5*time.Second)
			},
		},
		{
			name:           "oauth_502_bad_gateway",
			accountType:    AccountTypeOAuth,
			statusCode:     http.StatusBadGateway,
			body:           `{"error":{"message":"bad gateway","type":"server_error"}}`,
			expectFailover: false,
			assertRepo: func(t *testing.T, repo *openAIPassthroughFailoverRepo, _ time.Time) {
				require.Empty(t, repo.rateLimitCalls)
				require.Empty(t, repo.overloadCalls)
			},
		},
		{
			name:           "oauth_503_unavailable",
			accountType:    AccountTypeOAuth,
			statusCode:     http.StatusServiceUnavailable,
			body:           `{"error":{"message":"service unavailable","type":"server_error"}}`,
			expectFailover: false,
			assertRepo: func(t *testing.T, repo *openAIPassthroughFailoverRepo, _ time.Time) {
				require.Empty(t, repo.rateLimitCalls)
				require.Empty(t, repo.overloadCalls)
			},
		},
		{
			name:           "oauth_504_gateway_timeout",
			accountType:    AccountTypeOAuth,
			statusCode:     http.StatusGatewayTimeout,
			body:           `{"error":{"message":"gateway timeout","type":"server_error"}}`,
			expectFailover: false,
			assertRepo: func(t *testing.T, repo *openAIPassthroughFailoverRepo, _ time.Time) {
				require.Empty(t, repo.rateLimitCalls)
				require.Empty(t, repo.overloadCalls)
			},
		},
		{
			name:        "apikey_429_rate_limit",
			accountType: AccountTypeAPIKey,
			statusCode:  http.StatusTooManyRequests,
			body: func() string {
				resetAt := time.Now().Add(7 * 24 * time.Hour).Unix()
				return fmt.Sprintf(`{"error":{"message":"The usage limit has been reached","type":"usage_limit_reached","resets_at":%d}}`, resetAt)
			}(),
			expectFailover: true,
			assertRepo: func(t *testing.T, repo *openAIPassthroughFailoverRepo, _ time.Time) {
				require.Len(t, repo.rateLimitCalls, 1)
				require.Empty(t, repo.overloadCalls)
				require.True(t, time.Until(repo.rateLimitCalls[0]) > 24*time.Hour)
			},
		},
		{
			name:           "apikey_529_overload",
			accountType:    AccountTypeAPIKey,
			statusCode:     529,
			body:           `{"error":{"message":"server overloaded","type":"server_error"}}`,
			expectFailover: true,
			assertRepo: func(t *testing.T, repo *openAIPassthroughFailoverRepo, start time.Time) {
				require.Empty(t, repo.rateLimitCalls)
				require.Len(t, repo.overloadCalls, 1)
				require.WithinDuration(t, start.Add(10*time.Minute), repo.overloadCalls[0], 5*time.Second)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))
			c.Request.Header.Set("User-Agent", "codex_cli_rs/0.1.0")

			resp := &http.Response{
				StatusCode: tc.statusCode,
				Header: http.Header{
					"Content-Type": []string{"application/json"},
					"x-request-id": []string{"rid-failover"},
				},
				Body: io.NopCloser(strings.NewReader(tc.body)),
			}
			upstream := &httpUpstreamRecorder{resp: resp}
			repo := &openAIPassthroughFailoverRepo{}
			rateSvc := &RateLimitService{
				accountRepo: repo,
				cfg: &config.Config{
					RateLimit: config.RateLimitConfig{OverloadCooldownMinutes: 10},
				},
			}

			svc := &OpenAIGatewayService{
				cfg:              &config.Config{Gateway: config.GatewayConfig{ForceCodexCLI: false}},
				httpUpstream:     upstream,
				rateLimitService: rateSvc,
			}

			account := newAccount(tc.accountType)
			start := time.Now()
			_, err := svc.Forward(context.Background(), c, account, originalBody)
			require.Error(t, err)

			var failoverErr *UpstreamFailoverError
			if tc.expectFailover {
				require.ErrorAs(t, err, &failoverErr)
				require.Equal(t, tc.statusCode, failoverErr.StatusCode)
				require.False(t, c.Writer.Written(), "retryable passthrough 错误应返回 failover 错误给上层换号，而不是直接向客户端写响应")
			} else {
				require.False(t, errors.As(err, &failoverErr))
				require.True(t, c.Writer.Written(), "非 failover 的 passthrough http 错误应直接写回客户端")
				require.Equal(t, tc.statusCode, rec.Code)
			}

			v, ok := c.Get(OpsUpstreamErrorsKey)
			require.True(t, ok)
			arr, ok := v.([]*OpsUpstreamErrorEvent)
			require.True(t, ok)
			require.NotEmpty(t, arr)
			require.True(t, arr[len(arr)-1].Passthrough)
			if tc.expectFailover {
				require.Equal(t, "failover", arr[len(arr)-1].Kind)
			} else {
				require.Equal(t, "http_error", arr[len(arr)-1].Kind)
			}
			require.Equal(t, tc.statusCode, arr[len(arr)-1].UpstreamStatusCode)

			tc.assertRepo(t, repo, start)
		})
	}
}

func TestOpenAIGatewayService_OpenAIPassthrough_CompactNetworkErrorsTriggerFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name           string
		resp           *http.Response
		err            error
		expectFailover bool
	}{
		{
			name:           "request_error",
			err:            errors.New("stream disconnected before completion"),
			expectFailover: true,
		},
		{
			name: "read_error",
			resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"rid-compact"}},
				Body:       passthroughErrReadCloser{err: io.ErrUnexpectedEOF},
			},
			expectFailover: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", bytes.NewReader(nil))
			c.Request.Header.Set("User-Agent", "codex_cli_rs/0.1.0")

			upstream := &httpUpstreamRecorder{resp: tt.resp, err: tt.err}
			svc := &OpenAIGatewayService{
				cfg:          &config.Config{Gateway: config.GatewayConfig{ForceCodexCLI: false}},
				httpUpstream: upstream,
			}
			account := &Account{
				ID:             123,
				Name:           "acc",
				Platform:       PlatformOpenAI,
				Type:           AccountTypeOAuth,
				Concurrency:    1,
				Credentials:    map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-acc"},
				Extra:          map[string]any{"openai_passthrough": true},
				Status:         StatusActive,
				Schedulable:    true,
				RateMultiplier: f64p(1),
			}
			body := []byte(`{"model":"gpt-5.5","instructions":"local-test-instructions","input":[{"type":"text","text":"compact me"}]}`)

			_, err := svc.Forward(context.Background(), c, account, body)
			require.Error(t, err)
			var failoverErr *UpstreamFailoverError
			if tt.expectFailover {
				require.ErrorAs(t, err, &failoverErr)
				require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
				require.False(t, c.Writer.Written(), "compact 网络错误应交给外层 failover，而不是直接写回客户端")
			} else {
				require.False(t, errors.As(err, &failoverErr))
				require.ErrorIs(t, err, io.ErrUnexpectedEOF)
				require.False(t, c.Writer.Written())
			}
		})
	}
}

func TestOpenAIGatewayService_OAuthPassthrough_NonCodexUAFallbackToCodexUA(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))
	// Non-Codex UA
	c.Request.Header.Set("User-Agent", "curl/8.0")

	inputBody := []byte(`{"model":"gpt-5.2","stream":false,"store":true,"input":[{"type":"text","text":"hi"}]}`)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid"}},
		Body:       io.NopCloser(strings.NewReader("data: [DONE]\n\n")),
	}
	upstream := &httpUpstreamRecorder{resp: resp}

	svc := &OpenAIGatewayService{
		cfg:          &config.Config{Gateway: config.GatewayConfig{ForceCodexCLI: false}},
		httpUpstream: upstream,
	}

	account := &Account{
		ID:             123,
		Name:           "acc",
		Platform:       PlatformOpenAI,
		Type:           AccountTypeOAuth,
		Concurrency:    1,
		Credentials:    map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-acc"},
		Extra:          map[string]any{"openai_passthrough": true},
		Status:         StatusActive,
		Schedulable:    true,
		RateMultiplier: f64p(1),
	}

	_, err := svc.Forward(context.Background(), c, account, inputBody)
	require.NoError(t, err)
	require.Equal(t, false, gjson.GetBytes(upstream.lastBody, "store").Bool())
	require.Equal(t, true, gjson.GetBytes(upstream.lastBody, "stream").Bool())
	require.Equal(t, codexDesktopUserAgent, upstream.lastReq.Header.Get("User-Agent"))
}

// OAuth 透传继续遵循 morecup 的固定 Desktop 画像：入站 TUI 身份不得泄漏到上游，
// 最终 UA 与 originator 必须保持 Desktop 配套。
func TestOpenAIGatewayService_OAuthPassthrough_CodexTuiIdentityRebuiltAsDesktop(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const tuiUA = "codex-tui/0.140.2 (Mac OS X 14.0; arm64) iTerm (codex-tui; 0.140.2)"

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))
	c.Request.Header.Set("User-Agent", tuiUA)
	// 客户端携带错配的 originator，也必须按最终 UA 重配。
	c.Request.Header.Set("originator", "codex_cli_rs")

	inputBody := []byte(`{"model":"gpt-5.2","stream":false,"store":true,"input":[{"type":"text","text":"hi"}]}`)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid"}},
		Body:       io.NopCloser(strings.NewReader("data: [DONE]\n\n")),
	}
	upstream := &httpUpstreamRecorder{resp: resp}

	svc := &OpenAIGatewayService{
		cfg:          &config.Config{Gateway: config.GatewayConfig{ForceCodexCLI: false}},
		httpUpstream: upstream,
	}

	account := &Account{
		ID:             123,
		Name:           "acc",
		Platform:       PlatformOpenAI,
		Type:           AccountTypeOAuth,
		Concurrency:    1,
		Credentials:    map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-acc"},
		Extra:          map[string]any{"openai_passthrough": true},
		Status:         StatusActive,
		Schedulable:    true,
		RateMultiplier: f64p(1),
	}

	_, err := svc.Forward(context.Background(), c, account, inputBody)
	require.NoError(t, err)
	require.NotNil(t, upstream.lastReq)
	require.NotEqual(t, tuiUA, upstream.lastReq.Header.Get("User-Agent"))
	require.Equal(t, codexDesktopUserAgent, upstream.lastReq.Header.Get("User-Agent"))
	require.Equal(t, codexDesktopOriginator, upstream.lastReq.Header.Get("originator"))
}

func TestOpenAIGatewayService_CodexCLIOnly_RejectsNonCodexClient(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))
	c.Request.Header.Set("User-Agent", "curl/8.0")

	inputBody := []byte(`{"model":"gpt-5.2","stream":false,"store":true,"input":[{"type":"text","text":"hi"}]}`)

	svc := &OpenAIGatewayService{
		cfg: &config.Config{Gateway: config.GatewayConfig{ForceCodexCLI: false}},
	}

	account := &Account{
		ID:             123,
		Name:           "acc",
		Platform:       PlatformOpenAI,
		Type:           AccountTypeOAuth,
		Concurrency:    1,
		Credentials:    map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-acc"},
		Extra:          map[string]any{"openai_passthrough": true, "codex_cli_only": true},
		Status:         StatusActive,
		Schedulable:    true,
		RateMultiplier: f64p(1),
	}

	_, err := svc.Forward(context.Background(), c, account, inputBody)
	require.Error(t, err)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "Codex official clients")
}

func TestOpenAIGatewayService_CodexCLIOnly_AllowOfficialClientFamilies(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name       string
		ua         string
		originator string
	}{
		{name: "codex_cli_rs", ua: "codex_cli_rs/0.99.0", originator: ""},
		{name: "codex_vscode", ua: "codex_vscode/1.0.0", originator: ""},
		{name: "codex_app", ua: "codex_app/2.1.0", originator: ""},
		// req②：codex_cli_only 下 UA 须能解析出引擎版本；originator 命中路径用可解析的非官方前缀 UA。
		{name: "originator_codex_chatgpt_desktop", ua: "myterm/0.141.0", originator: "codex_chatgpt_desktop"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))
			c.Request.Header.Set("User-Agent", tt.ua)
			if tt.originator != "" {
				c.Request.Header.Set("originator", tt.originator)
			}
			// 引擎指纹头：真实官方客户端必带。本测试用 nil settingService 构造 gateway，
			// detectCodexClientRestriction 会兜底默认种子指纹信号（只勾 x-codex-），与生产默认策略一致，
			// 故官方家族也须携带 x-codex-* 才能过门（对齐 TestDetect_EngineFingerprintSignals）。
			c.Request.Header.Set("x-codex-window-id", "1")

			inputBody := []byte(`{"model":"gpt-5.2","stream":false,"store":true,"input":[{"type":"text","text":"hi"}]}`)

			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid"}},
				Body:       io.NopCloser(strings.NewReader("data: [DONE]\n\n")),
			}
			upstream := &httpUpstreamRecorder{resp: resp}

			svc := &OpenAIGatewayService{
				cfg:          &config.Config{Gateway: config.GatewayConfig{ForceCodexCLI: false}},
				httpUpstream: upstream,
			}

			account := &Account{
				ID:             123,
				Name:           "acc",
				Platform:       PlatformOpenAI,
				Type:           AccountTypeOAuth,
				Concurrency:    1,
				Credentials:    map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-acc"},
				Extra:          map[string]any{"openai_passthrough": true, "codex_cli_only": true},
				Status:         StatusActive,
				Schedulable:    true,
				RateMultiplier: f64p(1),
			}

			_, err := svc.Forward(context.Background(), c, account, inputBody)
			require.NoError(t, err)
			require.NotNil(t, upstream.lastReq)
		})
	}
}

func TestOpenAIGatewayService_OAuthPassthrough_StreamingSetsFirstTokenMs(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.1.0")

	originalBody := []byte(`{"model":"gpt-5.2","stream":true,"service_tier":"fast","input":[{"type":"text","text":"hi"}]}`)

	upstreamSSE := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"h"}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid"}},
		Body:       io.NopCloser(strings.NewReader(upstreamSSE)),
	}
	upstream := &httpUpstreamRecorder{resp: resp}

	svc := &OpenAIGatewayService{
		cfg:          &config.Config{Gateway: config.GatewayConfig{ForceCodexCLI: false}},
		httpUpstream: upstream,
	}

	account := &Account{
		ID:             123,
		Name:           "acc",
		Platform:       PlatformOpenAI,
		Type:           AccountTypeOAuth,
		Concurrency:    1,
		Credentials:    map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-acc"},
		Extra:          map[string]any{"openai_passthrough": true},
		Status:         StatusActive,
		Schedulable:    true,
		RateMultiplier: f64p(1),
	}

	start := time.Now()
	result, err := svc.Forward(context.Background(), c, account, originalBody)
	require.NoError(t, err)
	// sanity: duration after start
	require.GreaterOrEqual(t, time.Since(start), time.Duration(0))
	require.NotNil(t, result.FirstTokenMs)
	require.GreaterOrEqual(t, *result.FirstTokenMs, 0)
	require.NotNil(t, result.ServiceTier)
	require.Equal(t, "priority", *result.ServiceTier)
}

func TestOpenAIGatewayService_OAuthPassthrough_StreamClientDisconnectStillCollectsUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.1.0")
	// 首次写入成功，后续写入失败，模拟客户端中途断开。
	c.Writer = &failingGinWriter{ResponseWriter: c.Writer, failAfter: 1}

	originalBody := []byte(`{"model":"gpt-5.2","stream":true,"input":[{"type":"text","text":"hi"}]}`)

	upstreamSSE := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"h"}`,
		"",
		`data: {"type":"response.completed","response":{"usage":{"input_tokens":11,"output_tokens":7,"input_tokens_details":{"cached_tokens":3}}}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid"}},
		Body:       io.NopCloser(strings.NewReader(upstreamSSE)),
	}
	upstream := &httpUpstreamRecorder{resp: resp}

	svc := &OpenAIGatewayService{
		cfg:          &config.Config{Gateway: config.GatewayConfig{ForceCodexCLI: false}},
		httpUpstream: upstream,
	}

	account := &Account{
		ID:             123,
		Name:           "acc",
		Platform:       PlatformOpenAI,
		Type:           AccountTypeOAuth,
		Concurrency:    1,
		Credentials:    map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-acc"},
		Extra:          map[string]any{"openai_passthrough": true},
		Status:         StatusActive,
		Schedulable:    true,
		RateMultiplier: f64p(1),
	}

	result, err := svc.Forward(context.Background(), c, account, originalBody)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Stream)
	require.NotNil(t, result.FirstTokenMs)
	require.Equal(t, 11, result.Usage.InputTokens)
	require.Equal(t, 7, result.Usage.OutputTokens)
	require.Equal(t, 3, result.Usage.CacheReadInputTokens)
}

func TestOpenAIGatewayService_APIKeyPassthrough_PreservesBodyAndUsesResponsesEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))
	c.Request.Header.Set("User-Agent", "curl/8.0")
	c.Request.Header.Set("X-Test", "keep")
	c.Request.Header.Set("x-codex-beta-features", "remote_compaction_v2")

	originalBody := []byte(`{"model":"gpt-5.2","stream":false,"service_tier":"flex","max_output_tokens":128,"input":[{"type":"text","text":"hi"}]}`)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"rid"}},
		Body:       io.NopCloser(strings.NewReader(`{"output":[],"usage":{"input_tokens":1,"output_tokens":1,"input_tokens_details":{"cached_tokens":0}}}`)),
	}
	upstream := &httpUpstreamRecorder{resp: resp}

	svc := &OpenAIGatewayService{
		cfg:          &config.Config{Gateway: config.GatewayConfig{ForceCodexCLI: false}},
		httpUpstream: upstream,
	}

	account := &Account{
		ID:             456,
		Name:           "apikey-acc",
		Platform:       PlatformOpenAI,
		Type:           AccountTypeAPIKey,
		Concurrency:    1,
		Credentials:    map[string]any{"api_key": "sk-api-key", "base_url": "https://api.openai.com"},
		Extra:          map[string]any{"openai_passthrough": true},
		Status:         StatusActive,
		Schedulable:    true,
		RateMultiplier: f64p(1),
	}

	result, err := svc.Forward(context.Background(), c, account, originalBody)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.ServiceTier)
	require.Equal(t, "flex", *result.ServiceTier)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, originalBody, upstream.lastBody)
	require.Equal(t, "https://api.openai.com/v1/responses", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer sk-api-key", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "curl/8.0", upstream.lastReq.Header.Get("User-Agent"))
	require.Equal(t, "remote_compaction_v2", upstream.lastReq.Header.Get("x-codex-beta-features"))
	require.Empty(t, upstream.lastReq.Header.Get("X-Test"))
}

func TestOpenAIGatewayService_OAuthPassthrough_WarnOnTimeoutHeadersForStream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logSink, restore := captureStructuredLog(t)
	defer restore()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.1.0")
	c.Request.Header.Set("x-stainless-timeout", "10000")

	originalBody := []byte(`{"model":"gpt-5.2","stream":true,"input":[{"type":"text","text":"hi"}]}`)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "X-Request-Id": []string{"rid-timeout"}},
		Body:       io.NopCloser(strings.NewReader("data: [DONE]\n\n")),
	}
	upstream := &httpUpstreamRecorder{resp: resp}
	svc := &OpenAIGatewayService{
		cfg:          &config.Config{Gateway: config.GatewayConfig{ForceCodexCLI: false}},
		httpUpstream: upstream,
	}
	account := &Account{
		ID:             321,
		Name:           "acc",
		Platform:       PlatformOpenAI,
		Type:           AccountTypeOAuth,
		Concurrency:    1,
		Credentials:    map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-acc"},
		Extra:          map[string]any{"openai_passthrough": true},
		Status:         StatusActive,
		Schedulable:    true,
		RateMultiplier: f64p(1),
	}

	_, err := svc.Forward(context.Background(), c, account, originalBody)
	require.NoError(t, err)
	require.True(t, logSink.ContainsMessage("检测到超时相关请求头，将按配置过滤以降低断流风险"))
	require.True(t, logSink.ContainsFieldValue("timeout_headers", "x-stainless-timeout=10000"))
}

func TestOpenAIGatewayService_OAuthPassthrough_InfoWhenStreamEndsWithoutDone(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logSink, restore := captureStructuredLog(t)
	defer restore()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.1.0")

	originalBody := []byte(`{"model":"gpt-5.2","stream":true,"input":[{"type":"text","text":"hi"}]}`)
	// 注意：刻意不发送 [DONE]，模拟上游中途断流。
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "X-Request-Id": []string{"rid-truncate"}},
		Body:       io.NopCloser(strings.NewReader("data: {\"type\":\"response.output_text.delta\",\"delta\":\"h\"}\n\n")),
	}
	upstream := &httpUpstreamRecorder{resp: resp}
	svc := &OpenAIGatewayService{
		cfg:          &config.Config{Gateway: config.GatewayConfig{ForceCodexCLI: false}},
		httpUpstream: upstream,
	}
	account := &Account{
		ID:             654,
		Name:           "acc",
		Platform:       PlatformOpenAI,
		Type:           AccountTypeOAuth,
		Concurrency:    1,
		Credentials:    map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-acc"},
		Extra:          map[string]any{"openai_passthrough": true},
		Status:         StatusActive,
		Schedulable:    true,
		RateMultiplier: f64p(1),
	}

	_, err := svc.Forward(context.Background(), c, account, originalBody)
	require.EqualError(t, err, "stream usage incomplete: missing terminal event")
	require.True(t, logSink.ContainsMessage("上游流在未收到 [DONE] 时结束，疑似断流"))
	require.True(t, logSink.ContainsMessageAtLevel("上游流在未收到 [DONE] 时结束，疑似断流", "info"))
	require.True(t, logSink.ContainsFieldValue("upstream_request_id", "rid-truncate"))
}

func TestOpenAIGatewayService_OAuthPassthrough_DefaultFiltersTimeoutHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.1.0")
	c.Request.Header.Set("x-stainless-timeout", "120000")
	c.Request.Header.Set("X-Test", "keep")

	originalBody := []byte(`{"model":"gpt-5.2","stream":true,"input":[{"type":"text","text":"hi"}]}`)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "X-Request-Id": []string{"rid-filter-default"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1,"input_tokens_details":{"cached_tokens":0}}}}`,
			"",
			"data: [DONE]",
			"",
		}, "\n"))),
	}
	upstream := &httpUpstreamRecorder{resp: resp}
	svc := &OpenAIGatewayService{
		cfg:          &config.Config{Gateway: config.GatewayConfig{ForceCodexCLI: false}},
		httpUpstream: upstream,
	}
	account := &Account{
		ID:             111,
		Name:           "acc",
		Platform:       PlatformOpenAI,
		Type:           AccountTypeOAuth,
		Concurrency:    1,
		Credentials:    map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-acc"},
		Extra:          map[string]any{"openai_passthrough": true},
		Status:         StatusActive,
		Schedulable:    true,
		RateMultiplier: f64p(1),
	}

	_, err := svc.Forward(context.Background(), c, account, originalBody)
	require.NoError(t, err)
	require.NotNil(t, upstream.lastReq)
	require.Empty(t, upstream.lastReq.Header.Get("x-stainless-timeout"))
	require.Empty(t, upstream.lastReq.Header.Get("X-Test"))
}

func TestOpenAIGatewayService_OAuthPassthrough_CodexMimicStripsTimeoutHeadersWhenConfigured(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.1.0")
	c.Request.Header.Set("x-stainless-timeout", "120000")
	c.Request.Header.Set("X-Test", "keep")

	originalBody := []byte(`{"model":"gpt-5.2","stream":true,"input":[{"type":"text","text":"hi"}]}`)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "X-Request-Id": []string{"rid-filter-allow"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1,"input_tokens_details":{"cached_tokens":0}}}}`,
			"",
			"data: [DONE]",
			"",
		}, "\n"))),
	}
	upstream := &httpUpstreamRecorder{resp: resp}
	svc := &OpenAIGatewayService{
		cfg: &config.Config{Gateway: config.GatewayConfig{
			ForceCodexCLI:                        false,
			OpenAIPassthroughAllowTimeoutHeaders: true,
		}},
		httpUpstream: upstream,
	}
	account := &Account{
		ID:             222,
		Name:           "acc",
		Platform:       PlatformOpenAI,
		Type:           AccountTypeOAuth,
		Concurrency:    1,
		Credentials:    map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-acc"},
		Extra:          map[string]any{"openai_passthrough": true},
		Status:         StatusActive,
		Schedulable:    true,
		RateMultiplier: f64p(1),
	}

	_, err := svc.Forward(context.Background(), c, account, originalBody)
	require.NoError(t, err)
	require.NotNil(t, upstream.lastReq)
	require.Empty(t, upstream.lastReq.Header.Get("x-stainless-timeout"))
	require.Empty(t, upstream.lastReq.Header.Get("X-Test"))
}
