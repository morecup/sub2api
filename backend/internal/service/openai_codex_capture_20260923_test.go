package service

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCodexSeptember23HTTPMetadataMatchesFinalRequest(t *testing.T) {
	for _, model := range []string{"gpt-6-sol", "gpt-6-luna"} {
		for _, kind := range []string{"turn", "thread_title", "compaction"} {
			t.Run(model+"/"+kind, func(t *testing.T) {
				inbound := map[string]any{"analytics_enabled": false, "model": "stale-model", "reasoning_effort": "low"}
				if kind == "compaction" {
					inbound["request_kind"], inbound["compaction"] = kind, map[string]any{"trigger": "manual"}
				} else if kind == "thread_title" {
					inbound["thread_source"] = kind
				}
				inboundJSON, _ := json.Marshal(inbound)
				body := []byte(`{"model":"` + model + `","reasoning":{"effort":"high"},"input":[],"client_metadata":{"guardian_credits_requested":"true"}}`)
				req := httptest.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", strings.NewReader(string(body)))
				req.Header.Set(openAIWSTurnMetadataHeader, string(inboundJSON))
				applyCodexOAuthMimicHeadersForAccount(req, testCodexProfileAccount(72, 3), 1, "task", "", "", false, true, model)
				actual, err := syncCodexOAuthMimicRequestBody(req, body, false)
				require.NoError(t, err)
				metadata := req.Header.Get(openAIWSTurnMetadataHeader)
				require.Equal(t, metadata, gjson.GetBytes(actual, "client_metadata.x-codex-turn-metadata").String())
				require.Equal(t, gjson.False, gjson.Get(metadata, "analytics_enabled").Type)
				require.Equal(t, "true", gjson.GetBytes(actual, "client_metadata.guardian_credits_requested").String())
				require.Equal(t, "remote_compaction_v2", req.Header.Get("x-codex-beta-features"))
				require.Equal(t, "true", req.Header.Get(responsesLiteHeader))
				require.Equal(t, "model="+model, req.Header.Get("x-codex-routing-hint"))
				if kind == "compaction" {
					require.False(t, gjson.Get(metadata, "model").Exists())
					require.False(t, gjson.Get(metadata, "reasoning_effort").Exists())
				} else {
					require.Equal(t, model, gjson.Get(metadata, "model").String())
					require.Equal(t, "high", gjson.Get(metadata, "reasoning_effort").String())
				}
			})
		}
	}
}

func TestCodexSeptember23WSMetadataAndServerOptOut(t *testing.T) {
	for _, prewarm := range []bool{false, true} {
		headers := make(http.Header)
		applyCodexOAuthWSMimicHeadersForAccount(headers, testCodexProfileAccount(73, 4), 1, "task", "", "", `{"reasoning_effort":"low"}`, "gpt-6-sol")
		require.True(t, gjson.Get(headers.Get(openAIWSTurnMetadataHeader), "analytics_enabled").Bool())
		cfg := &config.Config{}
		cfg.Gateway.CodexTelemetry.Mode = "off"
		applyCodexAnalyticsPolicy(headers, cfg)
		payload := map[string]any{"type": "response.create", "model": "gpt-6-luna", "reasoning": map[string]any{"effort": "high"}, "input": []any{}}
		if prewarm {
			payload["generate"] = false
		}
		raw, _ := json.Marshal(payload)
		actual, err := syncCodexWSFrameMetadata(raw, headers, headers.Get(openAIWSTurnMetadataHeader))
		require.NoError(t, err)
		metadata := gjson.GetBytes(actual, "client_metadata.x-codex-turn-metadata").String()
		require.Equal(t, "gpt-6-luna", gjson.Get(metadata, "model").String())
		require.Equal(t, "high", gjson.Get(metadata, "reasoning_effort").String())
		require.Equal(t, gjson.False, gjson.Get(metadata, "analytics_enabled").Type)
		require.Equal(t, prewarm, gjson.Get(metadata, "request_kind").String() == "prewarm")
		require.Equal(t, "true", gjson.GetBytes(actual, "client_metadata."+responsesLiteWSMetadataKey).String())
		require.Empty(t, headers.Get(responsesLiteHeader))
		require.False(t, gjson.GetBytes(actual, "client_metadata.guardian_credits_requested").Exists(), "do not request guardian credits on behalf of clients that did not ask")
	}
}

func TestCodexSeptember23LegacyUpgradePreservesAccountEnvironment(t *testing.T) {
	for i := range 16 {
		account := testCodexProfileAccount(int64(i+1), i)
		old := codexClientProfileForAccount(account)
		old.TemplateID = strings.Replace(old.TemplateID, "windows-v4-", "windows-v1-", 1)
		old.CodexVersion, old.AppVersion = "0.155.0-alpha.2.6", "26.911.61220"
		old.BrowserVersion = "153.0.0.0"
		account.Extra[CodexClientProfileExtraKey] = old.extraValue()
		want := old
		want.CodexVersion, want.AppVersion = codexDesktopVersion, codexDesktopAppVersion
		want.BrowserVersion = "156.0.0.0"
		want.TemplateID = strings.Replace(want.TemplateID, "windows-v1-", "windows-v4-", 1)
		require.Equal(t, want, codexClientProfileForAccount(account), "cached snapshot")
		stored, err := parseCodexClientProfile(account.Extra[CodexClientProfileExtraKey])
		require.NoError(t, err)
		require.Equal(t, old, stored, "reads do not mutate shared account data")
		require.NoError(t, EnsureCodexClientProfile(account))
		require.NoError(t, EnsureCodexClientProfile(account))
		stored, err = parseCodexClientProfile(account.Extra[CodexClientProfileExtraKey])
		require.NoError(t, err)
		require.Equal(t, want, stored, "persisted update is idempotent")
		for _, field := range []string{"template", "app", "cli", "browser", "otel"} {
			custom := old
			switch field {
			case "template":
				custom.TemplateID = "custom"
			case "app":
				custom.AppVersion = "99.0.0"
			case "cli":
				custom.CodexVersion = "99.0.0"
			case "browser":
				custom.BrowserVersion = "999.0.0"
			case "otel":
				custom.OTelSDKVersion = "99.0.0"
			}
			require.Equal(t, custom, upgradeCapturedCodexClientProfile(custom), field)
		}
	}
}

func TestCodexSeptember23ContinuationMetrics(t *testing.T) {
	e := newTestCodexTelemetry()
	for _, account := range []*Account{testCodexProfileAccount(1, 2), testCodexProfileAccount(2, 3)} {
		route := codexTelemetryRouteForAccount(account, "http://proxy.invalid:8080", nil)
		ws := &codexTelemetryWS{exporter: e, route: route}
		frames := []struct{ body, mode, phase, reason string }{
			{`{"type":"response.create","model":"gpt-6-sol","generate":false}`, "full", "warmup", "no_previous_request"},
			{`{"type":"response.create","model":"gpt-6-sol"}`, "full", "generation", "other"},
			{`{"type":"response.create","model":"gpt-6-luna","previous_response_id":"private-response-id"}`, "incremental", "generation", "incremental"},
		}
		for _, frame := range frames {
			r := ws.prepareWrite([]byte(frame.body))
			ws.written(r, time.Millisecond, nil)
			found := false
			for k, a := range e.series {
				if k.route.accountID == account.ID && k.name == "codex.websocket.continuation" && k.mode == frame.mode && k.phase == frame.phase && k.reason == frame.reason {
					found = true
					require.Equal(t, r.model, k.model)
					require.Equal(t, uint64(1), a.count)
					require.Nil(t, a.buckets)
					require.Equal(t, route, a.route)
				}
			}
			require.True(t, found, frame.body)
		}
		ws.written(ws.prepareWrite([]byte(`{"type":"response.create","model":"gpt-6-luna"}`)), time.Millisecond, errors.New("write failed"))
	}
	count := 0
	for k := range e.series {
		if k.name == "codex.websocket.continuation" {
			count++
		}
	}
	require.Equal(t, 6, count, "failed writes must not generate continuation samples")
	require.NotContains(t, string(codexTelemetryPayload(e.series, time.Now())), "private-response-id")
}

func TestCodexSeptember23NewModelCatalogCapabilities(t *testing.T) {
	for _, model := range []string{"gpt-6-sol", "gpt-6-luna"} {
		descriptor := newConfiguredCodexModelDescriptor(model)
		require.Equal(t, "medium", *descriptor.DefaultReasoningLevel)
		require.EqualValues(t, 272000, descriptor.ContextWindow)
		require.EqualValues(t, 872000, descriptor.MaxContextWindow)
		levels := effortsFromConfiguredCodexLevels(descriptor.SupportedReasoningLevels)
		want := []string{"low", "medium", "high", "xhigh", "max"}
		if model == "gpt-6-sol" {
			want = append(want, "ultra")
		}
		require.Equal(t, want, levels)
		require.True(t, isOpenAICodexImageInputModel(model))
		require.Len(t, descriptor.ServiceTiers, 1)
		require.Equal(t, "priority", descriptor.ServiceTiers[0].ID)
		account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
		caps := accountCodexToolCapabilities(account, model)
		require.JSONEq(t, "true", string(caps["use_responses_lite"]))
		require.JSONEq(t, `"v2"`, string(caps["multi_agent_version"]))
		account.Type = AccountTypeAPIKey
		caps = accountCodexToolCapabilities(account, model)
		require.JSONEq(t, "false", string(caps["use_responses_lite"]))
		require.Equal(t, model, normalizeCodexModel("openai/"+model+"-max"))
		require.Equal(t, "max", normalizeOpenAIReasoningEffortForModel("max", model))
	}
	for _, unknown := range []string{"gpt-6-sol-unknown", "gpt-6-luna-ultra", "gpt-6-unknown"} {
		require.Empty(t, normalizeKnownOpenAICodexModel(unknown))
	}
}

func TestCodexSeptember23TelemetryHonorsAnalyticsOptOut(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", nil)
	account := testCodexProfileAccount(12, 3)
	require.True(t, codexTelemetryHTTPModelRequest(req, account))
	req.Header.Set(openAIWSTurnMetadataHeader, `{"analytics_enabled":false}`)
	require.False(t, codexTelemetryHTTPModelRequest(req, account))
	for _, handshakeOptOut := range []bool{false, true} {
		e := newTestCodexTelemetry()
		ws := &codexTelemetryWS{exporter: e, disabled: handshakeOptOut}
		payload := map[string]any{"type": "response.create", "model": "gpt-6-sol"}
		if !handshakeOptOut {
			payload["client_metadata"] = map[string]any{openAIWSTurnMetadataHeader: `{"analytics_enabled":false}`}
		}
		ws.written(ws.prepareWrite(payload), time.Millisecond, nil)
		ws.received([]byte(`{"type":"response.created","response":{"id":"resp_test"}}`), time.Millisecond, nil)
		ws.received([]byte(`{"type":"response.completed","response":{"id":"resp_test"}}`), time.Millisecond, nil)
		require.Empty(t, e.series)
	}
}
