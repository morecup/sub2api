package service

import (
	"net/http"
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

// Every OpenAI-compatible upstream call goes through doUpstreamRequest, so Grok
// traffic is fingerprinted while Codex traffic keeps the stock transport.
func TestDoUpstreamRequestAppliesGrokFingerprintOnly(t *testing.T) {
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

	require.Len(t, upstream.profiles, 2)
	require.NotNil(t, upstream.profiles[0])
	require.Equal(t, tlsfingerprint.GrokCLIProfileName, upstream.profiles[0].Name)
	require.Nil(t, upstream.profiles[1], "Codex has no captured profile and must keep the stock handshake")
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
