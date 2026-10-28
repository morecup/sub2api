package service

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCodexSeptember30ModelCapabilities(t *testing.T) {
	const model = "gpt-6.1-sol"
	for _, alias := range []string{model, "openai/" + model, "GPT-6.1-SOL", model + "-max", model + "-ultra-openai-compact"} {
		require.Equal(t, model, normalizeKnownOpenAICodexModel(alias))
		require.True(t, isCodexResponsesLiteModel(alias))
		require.True(t, isOpenAICodexImageInputModel(alias))
		require.Equal(t, "max", normalizeOpenAIReasoningEffortForModel("max", alias))
		d := newConfiguredCodexModelDescriptor(alias)
		require.Equal(t, "low", *d.DefaultReasoningLevel)
		require.EqualValues(t, 500000, d.ContextWindow)
		require.EqualValues(t, 872000, d.MaxContextWindow)
		require.Equal(t, []string{"low", "medium", "high", "xhigh", "max", "ultra"}, effortsFromConfiguredCodexLevels(d.SupportedReasoningLevels))
		require.Len(t, d.ServiceTiers, 1)
		require.Equal(t, "priority", d.ServiceTiers[0].ID)
	}
	for _, invalid := range []string{"gpt-6.1", "gpt-6.1-astra", "gpt-6.1-sol-unknown"} {
		require.Empty(t, normalizeKnownOpenAICodexModel(invalid))
		require.False(t, isCodexResponsesLiteModel(invalid))
	}
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	caps := accountCodexToolCapabilities(account, model)
	require.JSONEq(t, "true", string(caps["use_responses_lite"]))
	require.JSONEq(t, `"v2"`, string(caps["multi_agent_version"]))
	require.JSONEq(t, `"xhigh"`, string(caps["multi_agent_reasoning_effort"]))
	account.Type = AccountTypeAPIKey
	require.JSONEq(t, "false", string(accountCodexToolCapabilities(account, model)["use_responses_lite"]))
}

func TestCodexSeptember30WorkOriginatorAndMetadata(t *testing.T) {
	account := testCodexProfileAccount(30, 2)
	for _, originator := range []string{codexWorkDesktopOriginator, codexDesktopOriginator, "untrusted-originator"} {
		want := codexDesktopOriginator
		if originator == codexWorkDesktopOriginator {
			want = codexWorkDesktopOriginator
		}
		for _, kind := range []string{"turn", "thread_title", "compaction"} {
			body := []byte(`{"model":"gpt-6.1-sol","reasoning":{"effort":"high"},"input":[],"client_metadata":{"mcp_attribution":"preserve-client-value"}}`)
			req := httptest.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", strings.NewReader(string(body)))
			if kind == "thread_title" {
				req.Header.Set(openAIWSTurnMetadataHeader, `{"thread_source":"thread_title"}`)
			}
			if kind == "compaction" {
				req.Header.Set(openAIWSTurnMetadataHeader, `{"request_kind":"compaction"}`)
			}
			applyCodexOAuthMimicHeadersForAccount(req, account, 1, "capture", "", originator, false, true, "gpt-6.1-sol")
			enforceCodexIdentityHeadersForAccount(req.Header, account)
			actual, err := syncCodexOAuthMimicRequestBody(req, body, false)
			require.NoError(t, err)
			require.Equal(t, want, req.Header.Get("originator"))
			md := req.Header.Get(openAIWSTurnMetadataHeader)
			require.Equal(t, md, gjson.GetBytes(actual, "client_metadata.x-codex-turn-metadata").String())
			require.Equal(t, "preserve-client-value", gjson.GetBytes(actual, "client_metadata.mcp_attribution").String())
			if kind == "turn" {
				require.Equal(t, "desktop_app", gjson.Get(md, "client_type").String())
				require.Equal(t, "codex", gjson.Get(md, "source").String())
			} else {
				require.False(t, gjson.Get(md, "client_type").Exists())
				require.False(t, gjson.Get(md, "source").Exists())
			}
		}
		headers := make(http.Header)
		applyCodexOAuthWSMimicHeadersForAccount(headers, account, 1, "capture", "", originator, "", "gpt-6.1-sol")
		enforceCodexIdentityHeadersForAccount(headers, account)
		require.Equal(t, want, headers.Get("originator"))
		require.Empty(t, headers.Get(responsesLiteHeader))
		require.False(t, gjson.Get(headers.Get(openAIWSTurnMetadataHeader), "client_type").Exists())
	}
	webview := buildCodexDesktopWebviewHeaders("test-token", "test-account", "zh-CN", false)
	applyCodexDesktopSessionHeaders(webview, account)
	require.Equal(t, codexDesktopOriginator, webview["originator"])
	require.Contains(t, webview["user-agent"], "Chrome/156.0.0.0")
}

func TestCodexSeptember30ProfileUpgradePreservesCustomData(t *testing.T) {
	for _, latest := range codexClientEnvironmentPool() {
		old := latest
		old.TemplateID = strings.Replace(old.TemplateID, "windows-v4-", "windows-v2-", 1)
		old.CodexVersion, old.AppVersion, old.BrowserVersion = "0.155.0-alpha.16", "26.917.51856", "153.0.0.0"
		require.Equal(t, latest, upgradeCapturedCodexClientProfile(old))
		require.Equal(t, latest, upgradeCapturedCodexClientProfile(latest))
		for _, field := range []string{"template", "cli", "app", "browser", "otel"} {
			custom := old
			switch field {
			case "template":
				custom.TemplateID = "custom"
			case "cli":
				custom.CodexVersion = "99.0.0"
			case "app":
				custom.AppVersion = "99.0.0"
			case "browser":
				custom.BrowserVersion = "999.0.0.0"
			case "otel":
				custom.OTelSDKVersion = "99.0.0"
			}
			require.Equal(t, custom, upgradeCapturedCodexClientProfile(custom), field)
		}
	}
}
