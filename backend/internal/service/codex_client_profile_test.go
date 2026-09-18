package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func testCodexProfileAccount(id int64, template int) *Account {
	p := codexClientEnvironmentPool()[template]
	p.InstallationID, p.DeviceID = uuid.NewString(), uuid.NewString()
	return &Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Extra: map[string]any{CodexClientProfileExtraKey: p.extraValue()}}
}

func TestCodexClientEnvironmentPool(t *testing.T) {
	pool := codexClientEnvironmentPool()
	require.Len(t, pool, 16)
	seen := make(map[string]bool)
	for _, p := range pool {
		require.False(t, seen[p.TemplateID])
		seen[p.TemplateID] = true
		p.InstallationID, p.DeviceID = uuid.NewString(), uuid.NewString()
		parsed, err := parseCodexClientProfile(p.extraValue())
		require.NoError(t, err)
		require.Equal(t, p, parsed)
		require.Contains(t, p.UserAgent(), p.OSVersion)
		require.Contains(t, p.UserAgent(), p.CodexVersion)
		require.Contains(t, p.WebviewUserAgent(), p.BrowserVersion)
		require.Contains(t, p.AcceptLanguage(), p.Locale)
		require.Equal(t, codexDesktopVersion, p.CodexVersion)
		require.Equal(t, codexDesktopAppVersion, p.AppVersion)
	}
}

func TestCodexClientProfilePersistenceAndImport(t *testing.T) {
	identities := map[string]bool{}
	for i := range 64 {
		account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{"keep": true}}
		require.NoError(t, InitializeCodexClientProfile(account))
		original := codexClientProfileForAccount(account)
		for _, id := range []string{original.InstallationID, original.DeviceID} {
			require.False(t, identities[id])
			identities[id] = true
		}
		// Simulate INSERT, database JSON serialization, process restart and an
		// OAuth refresh. None of these may choose another environment or ID.
		account.ID = int64(i + 1)
		raw, err := json.Marshal(account)
		require.NoError(t, err)
		var restored Account
		require.NoError(t, json.Unmarshal(raw, &restored))
		restored.Credentials = map[string]any{"access_token": "changed-token"}
		require.NoError(t, EnsureCodexClientProfile(&restored))
		require.Equal(t, original, codexClientProfileForAccount(&restored))
		require.Equal(t, true, restored.Extra["keep"])
		require.Equal(t, original.DeviceID, restored.GetOpenAIDeviceID())
		// Importing that JSON as a new record must not clone its identity.
		restored.ID = 0
		require.NoError(t, InitializeCodexClientProfile(&restored))
		imported := codexClientProfileForAccount(&restored)
		require.NotEqual(t, original.InstallationID, imported.InstallationID)
		require.NotEqual(t, original.DeviceID, imported.DeviceID)
		require.Equal(t, original.TemplateID, imported.TemplateID)
	}
}

func TestCodexClientProfileLegacyBackfillAndCopy(t *testing.T) {
	account := &Account{ID: 17, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	legacyID := codexInstallationIDForAccount(account.ID, "")
	require.NoError(t, EnsureCodexClientProfile(account))
	p := codexClientProfileForAccount(account)
	require.Equal(t, legacyID, p.InstallationID)
	copyExtra, err := duplicateAccountExtra(account.Extra)
	require.NoError(t, err)
	require.NotContains(t, copyExtra, CodexClientProfileExtraKey)
	copyAccount := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: copyExtra}
	require.NoError(t, InitializeCodexClientProfile(copyAccount))
	require.NotEqual(t, p.InstallationID, codexClientProfileForAccount(copyAccount).InstallationID)
	require.NotEqual(t, p.DeviceID, codexClientProfileForAccount(copyAccount).DeviceID)
	for _, platform := range []string{PlatformOpenAI, PlatformAnthropic, PlatformGrok} {
		other := &Account{Platform: platform, Type: AccountTypeAPIKey}
		require.NoError(t, EnsureCodexClientProfile(other))
		require.Empty(t, other.Extra)
	}
}

func TestCodexClientProfileUpdateIsolationAndValidation(t *testing.T) {
	a, b := testCodexProfileAccount(41, 1), testCodexProfileAccount(42, 1)
	extra := map[string]any{"keep": true}
	require.NoError(t, PreserveCodexClientProfileUpdate(a, extra))
	require.Equal(t, a.Extra[CodexClientProfileExtraKey], extra[CodexClientProfileExtraKey])
	require.Error(t, PreserveCodexClientProfileUpdate(a, b.Extra))
	for _, field := range []string{"codex_version", "os_version", "browser_version", "installation_id", "template_id"} {
		t.Run(field, func(t *testing.T) {
			p := codexClientProfileForAccount(a).extraValue()
			p[field] = "bad\r\nAuthorization: secret"
			_, err := parseCodexClientProfile(p)
			require.Error(t, err)
		})
	}
	p := codexClientProfileForAccount(a).extraValue()
	p["cookie"] = "must-not-be-an-environment-field"
	_, err := parseCodexClientProfile(p)
	require.Error(t, err)
	_, err = parseCodexClientProfile(nil)
	require.Error(t, err)
}

func TestCodexClientProfileHTTPWSWebviewConsistency(t *testing.T) {
	a, b := testCodexProfileAccount(51, 0), testCodexProfileAccount(52, 15)
	for _, account := range []*Account{a, b} {
		p := codexClientProfileForAccount(account)
		req, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", nil)
		require.NoError(t, err)
		applyCodexOAuthMimicHeadersForAccount(req, account, 7, "task", "", codexDesktopOriginator, false, true, "gpt-6-astra")
		ws := make(http.Header)
		applyCodexOAuthWSMimicHeadersForAccount(ws, account, 7, "task", "", codexDesktopOriginator, "", "gpt-6-astra")
		for _, headers := range []http.Header{req.Header, ws} {
			require.Equal(t, p.UserAgent(), headers.Get("user-agent"))
			require.Equal(t, p.CodexVersion, headers.Get("version"))
			require.Equal(t, p.InstallationID, gjson.Get(headers.Get("x-codex-turn-metadata"), "installation_id").String())
			token := gjson.Get(headers.Get("x-oai-attestation"), "t").String()
			payload, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, "v1."))
			require.NoError(t, err)
			require.Contains(t, string(payload), p.Locale)
			require.Contains(t, string(payload), p.Timezone)
		}
		body, err := syncCodexOAuthMimicRequestBody(req, []byte(`{"model":"gpt-6-astra","input":"hello"}`), false)
		require.NoError(t, err)
		metadata := gjson.GetBytes(body, "client_metadata.x-codex-turn-metadata").String()
		require.Equal(t, p.InstallationID, gjson.Get(metadata, "installation_id").String())
		webview := codexDesktopAPIWebviewHeaders(account, "token", "upstream-account")
		require.Equal(t, p.WebviewUserAgent(), webview["user-agent"])
		require.Equal(t, p.Locale, webview["oai-language"])
		require.Equal(t, p.AcceptLanguage(), webview["accept-language"])
		require.Equal(t, p, codexClientProfileFromContext(req.Context(), account.ID))
		require.Equal(t, defaultCodexClientProfile(), codexClientProfileFromContext(req.Context(), -1))
	}
	require.NotEqual(t, codexAccountDeviceProfile(a).Attestation, codexAccountDeviceProfile(b).Attestation)
}

func TestCodexClientTelemetrySameProxyIsolationAndProfileSnapshot(t *testing.T) {
	a, b := testCodexProfileAccount(61, 0), testCodexProfileAccount(62, 15)
	e := newTestCodexTelemetry()
	e.config = config.CodexTelemetryConfig{Mode: "remote", Endpoint: "https://collector.invalid/metrics", StatsigAPIKey: "public-client-key"}
	profile := tlsfingerprint.CodexDesktopProfile()
	for _, account := range []*Account{a, a, b} {
		e.recordAPIRequest(codexTelemetryRouteForAccount(account, "http://same-proxy", profile), "gpt-6-astra", 200, true, 3*time.Millisecond)
	}
	// Same account, new environment: keep the previous batch's resource intact.
	changed := codexClientProfileForAccount(a)
	changed.OSVersion = "10.0.26200"
	a.Extra[CodexClientProfileExtraKey] = changed.extraValue()
	e.recordAPIRequest(codexTelemetryRouteForAccount(a, "http://same-proxy", profile), "gpt-6-astra", 200, true, time.Millisecond)
	seen := make(map[string]int64)
	e.send = func(req *http.Request, route codexTelemetryRoute) (*http.Response, error) {
		require.Equal(t, "http://same-proxy", route.proxyURL)
		require.Empty(t, req.Header.Get("authorization"))
		require.Empty(t, req.Header.Get("cookie"))
		raw, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		for _, id := range []string{route.client.InstallationID, route.client.DeviceID} {
			require.NotContains(t, string(raw), id, "do not add uncaptured identity attributes to official OTLP")
		}
		attrs := map[string]string{}
		for _, attr := range gjson.GetBytes(raw, "resourceMetrics.0.resource.attributes").Array() {
			attrs[attr.Get("key").String()] = attr.Get("value.stringValue").String()
		}
		require.Equal(t, route.client.OSVersion, attrs["os_version"])
		require.Equal(t, route.client.CodexVersion, attrs["service.version"])
		count := gjson.GetBytes(raw, `resourceMetrics.0.scopeMetrics.0.metrics.#(name=="codex.api_request").sum.dataPoints.0.asInt`).Int()
		key := route.client.OSVersion + ":" + route.client.InstallationID
		seen[key] = count
		return &http.Response{StatusCode: 202, Body: http.NoBody}, nil
	}
	e.flush(context.Background())
	require.Len(t, seen, 3)
	require.Equal(t, int64(2), seen["10.0.26100:"+changed.InstallationID])
	require.Equal(t, int64(1), seen["10.0.26200:"+changed.InstallationID])
	require.Equal(t, int64(1), seen["10.0.26200:"+codexClientProfileForAccount(b).InstallationID])
}

func TestCodexClientProfileModelsRequest(t *testing.T) {
	account := testCodexProfileAccount(71, 15)
	account.Credentials = map[string]any{"access_token": "local-test-token", "chatgpt_account_id": "test-account"}
	p := codexClientProfileForAccount(account)
	var got http.Header
	var clientVersion string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		clientVersion = r.URL.Query().Get("client_version")
		w.Header().Set("content-type", "application/json")
		_, _ = io.WriteString(w, `{"models":[{"slug":"gpt-6-astra"}]}`)
	}))
	defer server.Close()
	original := chatgptCodexModelsURL
	chatgptCodexModelsURL = server.URL
	defer func() { chatgptCodexModelsURL = original }()
	s := &OpenAIGatewayService{}
	_, err := s.FetchCodexModelsManifest(context.Background(), account, "incoming-untrusted-version", "")
	require.NoError(t, err)
	require.Equal(t, p.UserAgent(), got.Get("user-agent"))
	require.Equal(t, p.CodexVersion, got.Get("version"))
	require.Equal(t, "0.155.0", clientVersion)
}

type codexProfileProbeDialer struct {
	openAIWSFakeDialer
	seen      CodexClientProfile
	accountID int64
}

func (d *codexProfileProbeDialer) Dial(ctx context.Context, url string, headers http.Header, proxy string, profile *openAIWSTLSProfile, scope string) (openAIWSClientConn, int, http.Header, error) {
	d.seen = codexClientProfileFromContext(ctx, d.accountID)
	return d.openAIWSFakeDialer.Dial(ctx, url, headers, proxy, profile, scope)
}

func TestCodexClientProfileWSPoolCarriesAccountSnapshot(t *testing.T) {
	pool := newOpenAIWSConnPool(&config.Config{})
	defer pool.Close()
	a, b := testCodexProfileAccount(81, 0), testCodexProfileAccount(82, 15)
	dialer := &codexProfileProbeDialer{}
	pool.setClientDialerForTest(dialer)
	for _, account := range []*Account{a, b} {
		dialer.accountID = account.ID
		conn, err := pool.dialConn(context.Background(), openAIWSAcquireRequest{Account: account, WSURL: "wss://local-test.invalid", TransportScope: openAIWSTransportScope(account)})
		require.NoError(t, err)
		require.Equal(t, codexClientProfileForAccount(account), dialer.seen)
		conn.close()
	}
}
