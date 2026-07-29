//go:build go1.27 && !http2legacy

package repository

import (
	"context"
	"fmt"
	"net"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
)

func TestNewFingerprintHTTP2Transport_GrokOrderedHeadersFailOnGo127WrapperBranch(t *testing.T) {
	require.False(t, grokHTTP2HeaderOrderSupported(), "go1.27 wrapper branch must report header-order support as unavailable")

	transport, err := newFingerprintHTTP2Transport(poolSettings{}, tlsfingerprint.GrokCLIProfile(), func(ctx context.Context, network, addr string) (net.Conn, error) {
		return nil, fmt.Errorf("dial should not run in transport selection test")
	})
	require.Nil(t, transport)
	require.Error(t, err)
	require.Contains(t, err.Error(), "go1.27 wrapper")
	require.Contains(t, err.Error(), "http2legacy")
}
