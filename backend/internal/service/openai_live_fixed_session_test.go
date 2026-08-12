package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplyLiveUpstreamIdentityHeadersFixedSessionID(t *testing.T) {
	const fixed = "019ff4d1-0567-7630-ba3d-e564a4a519ac"
	headers := make(http.Header)
	headers.Set("session-id", "client-session")
	headers.Set("thread-id", "client-thread")

	applyLiveUpstreamIdentityHeaders(headers, fixed)

	require.Equal(t, fixed, headers.Get("session-id"))
	require.Equal(t, fixed, headers.Get("thread-id"))
}
