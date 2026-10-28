package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type codexEgressProbeStub struct {
	mu    sync.Mutex
	calls []string
	exits map[string]*ProxyExitInfo
	err   error
}

func (p *codexEgressProbeStub) ProbeProxy(_ context.Context, route string) (*ProxyExitInfo, int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, route)
	return p.exits[route], 1, p.err
}

type codexEgressCacheStub struct {
	info *ProxyLatencyInfo
}

func (c *codexEgressCacheStub) GetProxyLatencies(_ context.Context, ids []int64) (map[int64]*ProxyLatencyInfo, error) {
	return map[int64]*ProxyLatencyInfo{ids[0]: c.info}, nil
}
func (c *codexEgressCacheStub) SetProxyLatency(context.Context, int64, *ProxyLatencyInfo) error {
	return nil
}

func TestCodexEgressLocaleAndExactTimezone(t *testing.T) {
	for _, tc := range []struct{ country, locale, zone string }{
		{"US", "en-US", "America/Los_Angeles"},
		{"US", "en-US", "America/New_York"},
		{"JP", "ja-JP", "Asia/Tokyo"},
		{"GB", "en-GB", "Europe/London"},
		{"DE", "de-DE", "Europe/Berlin"},
		{"FR", "fr-FR", "Europe/Paris"},
		{"KR", "ko-KR", "Asia/Seoul"},
		{"TW", "zh-TW", "Asia/Taipei"},
		{"HK", "zh-HK", "Asia/Hong_Kong"},
		{"SG", "en-SG", "Asia/Singapore"},
		{"CN", "zh-CN", "Asia/Shanghai"},
	} {
		t.Run(tc.country+"/"+tc.zone, func(t *testing.T) {
			profile, ok := codexEgressProfileFromExit("route", &ProxyExitInfo{IP: "203.0.113.7", CountryCode: tc.country, Timezone: tc.zone})
			require.True(t, ok)
			require.Equal(t, tc.locale, profile.locale)
			require.Equal(t, tc.zone, profile.timezone)
		})
	}
	for _, exit := range []*ProxyExitInfo{
		nil,
		{IP: "203.0.113.7", CountryCode: "US"}, // Never guess a timezone from the country.
		{IP: "203.0.113.7", CountryCode: "US", Timezone: "Local"},
		{IP: "203.0.113.7", CountryCode: "US", Timezone: "Mars/Olympus"},
		{IP: "203.0.113.7", CountryCode: "ZZ", Timezone: "UTC"},
		{IP: "203.0.113.7", CountryCode: "US\r\nInjected: yes", Timezone: "UTC"},
		{IP: "bad", CountryCode: "US", Timezone: "UTC"},
	} {
		_, ok := codexEgressProfileFromExit("route", exit)
		require.False(t, ok)
	}
}

func TestCodexEgressSwitchesWithBindingAndPreservesIdentity(t *testing.T) {
	ctx := context.Background()
	account := testCodexProfileAccount(904, 0)
	proxyID := int64(19)
	account.ProxyID = &proxyID
	account.Proxy = &Proxy{ID: proxyID, Protocol: "http", Host: "proxy.invalid", Port: 8080, Username: "us-exit", Password: "secret"}
	usURL := account.Proxy.URL()
	jpProxy := *account.Proxy
	jpProxy.Username = "jp-exit" // Same proxy ID/host, different residential exit.
	jpURL := jpProxy.URL()
	probe := &codexEgressProbeStub{exits: map[string]*ProxyExitInfo{
		usURL: {IP: "203.0.113.7", CountryCode: "US", Timezone: "America/Los_Angeles"},
		jpURL: {IP: "203.0.113.8", CountryCode: "JP", Timezone: "Asia/Tokyo"},
	}}
	s := NewCodexEgressProfileService(probe, nil)
	original := codexClientProfileForAccount(account)
	before, err := json.Marshal(account)
	require.NoError(t, err)
	us := s.forBoundAccount(ctx, account)
	require.NotSame(t, account, us)
	usProfile := codexClientProfileForAccount(us)
	require.Equal(t, "en-US", usProfile.Locale)
	require.Equal(t, "America/Los_Angeles", usProfile.Timezone)
	after, err := json.Marshal(us)
	require.NoError(t, err)
	require.JSONEq(t, string(before), string(after), "the regional overlay must not enter persistence/scheduler JSON")
	require.Equal(t, original, codexClientProfileForAccount(account), "shared source snapshot is immutable")
	boundToJapan := *account
	boundToJapan.Proxy = &jpProxy
	jp := s.forBoundAccount(ctx, &boundToJapan)
	jpProfile := codexClientProfileForAccount(jp)
	require.Equal(t, "ja-JP", jpProfile.Locale)
	require.Equal(t, "Asia/Tokyo", jpProfile.Timezone)
	for _, p := range []CodexClientProfile{usProfile, jpProfile} {
		require.Equal(t, original.InstallationID, p.InstallationID)
		require.Equal(t, original.DeviceID, p.DeviceID)
		require.Equal(t, original.CodexVersion, p.CodexVersion)
	}
	require.Same(t, jp, s.forBoundAccount(ctx, jp), "a request keeps a consistent snapshot")
	_ = s.forBoundAccount(ctx, account)
	require.Equal(t, []string{usURL, jpURL}, probe.calls)

	// A failed new exit cannot borrow the previous exit's region.
	probe.err = errors.New("unavailable")
	failed := s.forAccount(ctx, us, "http://different.invalid:8080")
	require.Equal(t, original, codexClientProfileForAccount(failed))
}

func TestCodexEgressRefreshFailureAndDirect(t *testing.T) {
	ctx := context.Background()
	account := testCodexProfileAccount(905, 0)
	probe := &codexEgressProbeStub{exits: map[string]*ProxyExitInfo{
		"": {IP: "203.0.113.9", CountryCode: "US", Timezone: "America/New_York"},
	}}
	s := NewCodexEgressProfileService(probe, nil)
	now := time.Now()
	s.now = func() time.Time { return now }
	first := s.forBoundAccount(ctx, account)
	require.Equal(t, "America/New_York", codexClientProfileForAccount(first).Timezone)
	probe.exits[""].Timezone = "America/Chicago"
	now = now.Add(codexEgressProfileTTL)
	require.Equal(t, "America/Chicago", codexClientProfileForAccount(s.forBoundAccount(ctx, account)).Timezone)
	probe.err = errors.New("lookup failed")
	now = now.Add(codexEgressProfileTTL)
	fallback := s.forBoundAccount(ctx, account)
	require.Equal(t, codexClientProfileForAccount(account), codexClientProfileForAccount(fallback))
	_ = s.forBoundAccount(ctx, account)
	require.Len(t, probe.calls, 3, "negative cache prevents repeated probes")
	now = now.Add(codexEgressProfileRetry)
	probe.err = nil
	require.Equal(t, "America/Chicago", codexClientProfileForAccount(s.forBoundAccount(ctx, account)).Timezone)
	require.Equal(t, []string{"", "", "", ""}, probe.calls, "direct accounts use the server egress")

	proxyID := int64(9)
	account.ProxyID = &proxyID
	require.Same(t, account, s.forBoundAccount(ctx, account))
	require.Len(t, probe.calls, 4, "an unresolved proxy must not probe directly")
	account.Type = AccountTypeAPIKey
	require.Same(t, account, s.forAccount(ctx, account, "http://ignored"))
	require.Len(t, probe.calls, 4)
}

func TestCodexEgressUsesOnlyFreshMatchingProxySnapshots(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	for _, tc := range []struct {
		name, key string
		age       time.Duration
		wantProbe bool
	}{
		{"fresh", proxyEgressKey("http://proxy"), time.Minute, false},
		{"old_credentials", proxyEgressKey("http://previous"), time.Minute, true},
		{"legacy_no_key", "", time.Minute, true},
		{"expired", proxyEgressKey("http://proxy"), codexEgressProfileTTL, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache := &codexEgressCacheStub{info: &ProxyLatencyInfo{Success: true, IPAddress: "203.0.113.10", CountryCode: "US", Timezone: "America/Chicago", EgressKey: tc.key, UpdatedAt: now.Add(-tc.age)}}
			probe := &codexEgressProbeStub{exits: map[string]*ProxyExitInfo{"http://proxy": {IP: "203.0.113.11", CountryCode: "JP", Timezone: "Asia/Tokyo"}}}
			s := NewCodexEgressProfileService(probe, cache)
			s.now = func() time.Time { return now }
			profile := s.resolve(ctx, 9, "http://proxy")
			if tc.wantProbe {
				require.Equal(t, "Asia/Tokyo", profile.timezone)
				require.Len(t, probe.calls, 1)
			} else {
				require.Equal(t, "America/Chicago", profile.timezone)
				require.Empty(t, probe.calls)
			}
		})
	}
}

func TestCodexEgressHTTPWSWebviewProfileConsistency(t *testing.T) {
	account := testCodexProfileAccount(906, 0)
	probe := &codexEgressProbeStub{exits: map[string]*ProxyExitInfo{"": {IP: "203.0.113.12", CountryCode: "JP", Timezone: "Asia/Tokyo"}}}
	s := &OpenAIGatewayService{codexEgressProfiles: NewCodexEgressProfileService(probe, nil)}
	ctx := context.Background()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req, err := s.buildUpstreamRequest(ctx, c, account, []byte(`{"model":"gpt-6-astra","input":"hello"}`), "fixture", true, "task", true)
	require.NoError(t, err)
	ws, _, err := s.buildOpenAIWSHeaders(ctx, nil, account, "fixture", OpenAIWSProtocolDecision{}, true, "", "", "task", "gpt-6-astra")
	require.NoError(t, err)
	for _, header := range []http.Header{req.Header, ws} {
		token := gjson.Get(header.Get("x-oai-attestation"), "t").String()
		payload, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, "v1."))
		require.NoError(t, err)
		require.Contains(t, string(payload), "ja-JP")
		require.Contains(t, string(payload), "Asia/Tokyo")
		require.NotContains(t, string(payload), "Asia/Shanghai")
	}
	regional := s.prepareCodexEgressAccount(ctx, account)
	headers := codexDesktopAPIWebviewHeaders(regional, "fixture", "fixture-account")
	require.Equal(t, "ja-JP", headers["oai-language"])
	require.Equal(t, "ja-JP,ja;q=0.9", headers["accept-language"])
	require.Equal(t, "Asia/Tokyo", codexClientProfileFromContext(req.Context(), account.ID).Timezone)
	require.Equal(t, "Asia/Shanghai", codexClientProfileForAccount(account).Timezone)
	require.Len(t, probe.calls, 1)
}

func TestCodexEgressTimezoneMetadataDSTAndContentIsolation(t *testing.T) {
	account := testCodexProfileAccount(907, 0)
	account.codexEgressProfile = &codexEgressProfile{locale: "en-US", timezone: "America/New_York"}
	body := []byte(`{"timezone":"Asia/Shanghai","timezone_offset_min":-480,"messages":[{"text":"<timezone>Asia/Shanghai</timezone>","timezone":"Asia/Tokyo"}]}`)
	for _, tc := range []struct {
		month  time.Month
		offset int64
	}{{time.January, 300}, {time.July, 240}} {
		result, err := rewriteCodexEgressTimezone(body, account, time.Date(2026, tc.month, 15, 12, 0, 0, 0, time.UTC))
		require.NoError(t, err)
		require.Equal(t, "America/New_York", gjson.GetBytes(result, "timezone").String())
		require.Equal(t, tc.offset, gjson.GetBytes(result, "timezone_offset_min").Int())
		require.Equal(t, gjson.GetBytes(body, "messages").Raw, gjson.GetBytes(result, "messages").Raw)
	}
	for _, body := range []string{`{"input":"hello"}`, `{"tool":{"timezone":"UTC"}}`, `not json`, `[]`} {
		result, err := rewriteCodexEgressTimezone([]byte(body), account, time.Now())
		require.NoError(t, err)
		require.Equal(t, body, string(result))
	}
}

type codexEgressProbeFunc func(context.Context, string) (*ProxyExitInfo, int64, error)

func (f codexEgressProbeFunc) ProbeProxy(ctx context.Context, proxy string) (*ProxyExitInfo, int64, error) {
	return f(ctx, proxy)
}

func TestCodexEgressConcurrentLookupSurvivesCallerCancellation(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	probe := codexEgressProbeFunc(func(ctx context.Context, route string) (*ProxyExitInfo, int64, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
			return &ProxyExitInfo{IP: "203.0.113.1", CountryCode: "JP", Timezone: "Asia/Tokyo"}, 1, ctx.Err()
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		}
	})
	s := NewCodexEgressProfileService(probe, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := make(chan codexEgressProfile, 1)
	go func() { first <- s.resolve(ctx, 1, "http://proxy") }()
	<-started
	cancel()
	require.Empty(t, (<-first).locale)
	results := make(chan codexEgressProfile, 32)
	for range 32 {
		go func() { results <- s.resolve(context.Background(), 1, "http://proxy") }()
	}
	close(release)
	for range 32 {
		require.Equal(t, "Asia/Tokyo", (<-results).timezone)
	}
	require.Equal(t, int32(1), calls.Load())
}

func TestCodexEgressDesktopAndQuotaUseActualRoute(t *testing.T) {
	account := testCodexProfileAccount(908, 0)
	account.Credentials = map[string]any{"access_token": "fixture", "chatgpt_account_id": "fixture-account"}
	probe := &codexEgressProbeStub{exits: map[string]*ProxyExitInfo{
		"":                      {IP: "203.0.113.1", CountryCode: "JP", Timezone: "Asia/Tokyo"},
		"http://selected-proxy": {IP: "203.0.113.2", CountryCode: "DE", Timezone: "Europe/Berlin"},
	}}
	profiles := NewCodexEgressProfileService(probe, nil)
	seen := make(chan http.Header, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Clone()
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"available_count":0,"credits":[]}`))
	}))
	defer server.Close()
	desktop := ProvideCodexDesktopAPIService(newQuotaRedirectingFactory(server), nil, profiles)
	_, err := desktop.GetRateLimitResetCredits(context.Background(), account)
	require.NoError(t, err)
	header := <-seen
	require.Equal(t, "ja-JP", header.Get("oai-language"))
	require.Equal(t, "ja-JP,ja;q=0.9", header.Get("accept-language"))

	quota := &OpenAIQuotaService{accountRepo: &stubQuotaAccountRepo{accounts: map[int64]*Account{account.ID: account}}, codexEgressProfiles: profiles}
	// A quota client may have selected its route before reloading credentials.
	headers, _, err := quota.buildCodexQuotaHeaders(context.Background(), account.ID, "fixture", "fixture-account", false, "http://selected-proxy")
	require.NoError(t, err)
	require.Equal(t, "de-DE", headers["oai-language"])
	require.Equal(t, "de-DE,de;q=0.9", headers["accept-language"])
	require.Equal(t, []string{"", "http://selected-proxy"}, probe.calls)
}
