package service

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
)

type recordingTLSUpstream struct {
	profiles []*tlsfingerprint.Profile
	plainDo  int
}

func (u *recordingTLSUpstream) Do(*http.Request, string, int64, int) (*http.Response, error) {
	u.plainDo++
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
}

func (u *recordingTLSUpstream) DoWithTLS(_ *http.Request, _ string, _ int64, _ int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	u.profiles = append(u.profiles, profile)
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
}

// Every OpenAI-compatible upstream call goes through doUpstreamRequest, so
// Grok and Codex OAuth each receive their matching official-client profile.
func TestDoUpstreamRequestAppliesPlatformFingerprint(t *testing.T) {
	upstream := &recordingTLSUpstream{}
	svc := &OpenAIGatewayService{
		httpUpstream:        upstream,
		tlsFPProfileService: &TLSFingerprintProfileService{},
	}

	req, err := http.NewRequest(http.MethodPost, "https://cli-chat-proxy.grok.com/v1/responses", strings.NewReader("{}"))
	require.NoError(t, err)
	resp, err := svc.doUpstreamRequest(req, "", &Account{ID: 7, Platform: PlatformGrok, Type: AccountTypeOAuth, Concurrency: 3})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	codexReq, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", strings.NewReader("{}"))
	require.NoError(t, err)
	_, err = svc.doUpstreamRequest(codexReq, "", &Account{ID: 8, Platform: PlatformOpenAI, Type: AccountTypeOAuth})
	require.NoError(t, err)
	apiKeyReq, err := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/responses", strings.NewReader("{}"))
	require.NoError(t, err)
	_, err = svc.doUpstreamRequest(apiKeyReq, "", &Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeAPIKey})
	require.NoError(t, err)

	require.Len(t, upstream.profiles, 3)
	require.NotNil(t, upstream.profiles[0])
	require.Equal(t, tlsfingerprint.GrokCLIProfileName, upstream.profiles[0].Name)
	require.NotNil(t, upstream.profiles[1])
	require.Equal(t, tlsfingerprint.CodexDesktopProfileName, upstream.profiles[1].Name)
	require.Nil(t, upstream.profiles[2], "OpenAI API Key traffic must not masquerade as Codex Desktop")
	require.Zero(t, upstream.plainDo)
}

// The resolver has to stay nil-safe: tests and reduced wirings construct the
// gateway without the profile service.
func TestDoUpstreamRequestWithoutProfileServiceKeepsStockTransport(t *testing.T) {
	upstream := &recordingTLSUpstream{}
	svc := &OpenAIGatewayService{httpUpstream: upstream}

	req, err := http.NewRequest(http.MethodPost, "https://cli-chat-proxy.grok.com/v1/responses", strings.NewReader("{}"))
	require.NoError(t, err)
	_, err = svc.doUpstreamRequest(req, "", &Account{ID: 9, Platform: PlatformGrok, Type: AccountTypeOAuth})
	require.NoError(t, err)

	require.Len(t, upstream.profiles, 1)
	require.Nil(t, upstream.profiles[0])
}

type cookieIntegrationUpstream struct {
	recordingTLSUpstream
	send func(*http.Request) (*http.Response, error)
}

func (u *cookieIntegrationUpstream) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.send(req)
}

func TestDoUpstreamRequestCookieResponseLifecycle(t *testing.T) {
	account := &Account{ID: 8, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	var received []string
	u := &cookieIntegrationUpstream{}
	u.send = func(req *http.Request) (*http.Response, error) {
		received = append(received, req.Header.Get("Cookie"))
		return &http.Response{StatusCode: 200, Request: req, Header: http.Header{"Set-Cookie": {"server=issued; Path=/; Secure"}}, Body: http.NoBody}, nil
	}
	svc := &OpenAIGatewayService{httpUpstream: u}
	for range 2 {
		_, err := svc.doUpstreamRequest(codexCookieTestRequest(t, "/backend-api/me"), "", account)
		require.NoError(t, err)
	}
	require.Equal(t, []string{"", "server=issued"}, received)
	u.send = func(req *http.Request) (*http.Response, error) {
		redirectURL, _ := url.Parse("https://example.com/")
		return &http.Response{StatusCode: 200, Request: &http.Request{URL: redirectURL}, Header: http.Header{"Set-Cookie": {"server=foreign; Path=/"}}, Body: http.NoBody}, nil
	}
	_, err := svc.doUpstreamRequest(codexCookieTestRequest(t, "/backend-api/me"), "", account)
	require.NoError(t, err)
	probe := codexCookieTestRequest(t, "/backend-api/me")
	svc.codexCookies.prepare(probe, account, "")
	require.Equal(t, "server=issued", probe.Header.Get("Cookie"), "redirect response cannot seed another host's cookies")
}

func TestDoUpstreamRequestMissingConfiguredProxyNeverDialsDirect(t *testing.T) {
	id := int64(1)
	u := &recordingTLSUpstream{}
	svc := &OpenAIGatewayService{httpUpstream: u}
	_, err := svc.doUpstreamRequest(codexCookieTestRequest(t, "/backend-api/me"), "", &Account{ID: 8, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ProxyID: &id})
	require.ErrorContains(t, err, "configured OpenAI proxy")
	require.Empty(t, u.profiles)
	require.Zero(t, u.plainDo)
}
