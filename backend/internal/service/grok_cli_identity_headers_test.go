package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
)

// The header sets below were captured from the official Grok Build CLI 0.2.112
// on the OAuth CLI-proxy path.
//
// POST /v1/responses carried: content-type, user-agent,
// x-compactions-remaining, x-compaction-at, x-grok-client-version,
// x-grok-user-id, x-grok-client-identifier, authorization, traceparent,
// x-grok-conv-id, x-grok-req-id, x-grok-model-override, x-grok-session-id,
// x-grok-agent-id, x-grok-turn-idx, accept, accept-encoding, content-length.
// Notably absent: x-xai-token-auth and x-grok-client-mode.
//
// GET /v1/models and GET /v1/settings carried x-xai-token-auth: xai-grok-cli,
// x-grok-client-mode and the account identity headers on top of the shared
// identity.
func TestApplyGrokCLIInferenceHeadersMatchesCapturedChatRequest(t *testing.T) {
	headers := http.Header{}
	applyGrokCLIInferenceHeaders(headers)

	require.Equal(t, xai.CLIUserAgent(), headers.Get("User-Agent"))
	require.Equal(t, xai.EffectiveCLIClientVersion(), headers.Get("X-Grok-Client-Version"))
	require.Equal(t, xai.CLIClientIdentifier, headers.Get(xai.CLIClientIdentifierHeader))

	// The official client does not send these on an inference request, so neither
	// may leak in here: an extra header is as identifying as a missing one.
	require.Empty(t, headers.Get("X-Grok-Client-Mode"))
	require.Empty(t, headers.Get("X-XAI-Token-Auth"))
}

func TestApplyGrokCLIControlPlaneHeadersMatchesCapturedMetadataRequest(t *testing.T) {
	headers := http.Header{}
	applyGrokCLIControlPlaneHeaders(headers)

	require.Equal(t, xai.CLIUserAgent(), headers.Get("User-Agent"))
	require.Equal(t, xai.EffectiveCLIClientVersion(), headers.Get("X-Grok-Client-Version"))
	require.Equal(t, xai.CLIClientIdentifier, headers.Get(xai.CLIClientIdentifierHeader))
	require.Equal(t, "interactive", headers.Get("X-Grok-Client-Mode"))
}

// grokUpstreamUserAgent is asserted across the Grok forwarding tests, so pin it
// to the captured identity rather than to the workspace string it used to hold.
func TestGrokUpstreamUserAgentIsTheCapturedCLIIdentity(t *testing.T) {
	require.Equal(t, xai.CLIUserAgent(), grokUpstreamUserAgent)
	require.Contains(t, grokUpstreamUserAgent, "grok-shell/")
	require.NotContains(t, grokUpstreamUserAgent, "xai-grok-workspace")
}
