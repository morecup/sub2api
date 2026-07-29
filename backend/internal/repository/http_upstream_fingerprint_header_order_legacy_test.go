//go:build !(go1.27 && !http2legacy)

package repository

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"

	"github.com/Wei-Shaw/sub2api/internal/config"
	grokhttp2 "github.com/Wei-Shaw/sub2api/internal/pkg/grokhttp2/http2"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// TestFingerprintHTTP2TransportEmitsGrokCLIPreamble drives the real
// http2.Transport built for the Grok profile against a listener that records
// the bytes it receives, so the assertion is on what actually goes on the wire
// rather than on the knobs we set.
//
// It is the regression guard for the whole HTTP/2 half of the fingerprint: if a
// future Go or x/net/http2 release changes how the client preamble is produced,
// this fails instead of silently reverting to a Go-shaped fingerprint.
func TestFingerprintHTTP2TransportEmitsGrokCLIPreamble(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	type preamble struct {
		settings     []tlsfingerprint.HTTP2Setting
		windowUpdate uint32
		err          error
	}
	preambleCh := make(chan preamble, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			preambleCh <- preamble{err: acceptErr}
			return
		}
		defer func() { _ = conn.Close() }()
		settings, windowUpdate, readErr := readClientPreamble(conn)
		preambleCh <- preamble{settings: settings, windowUpdate: windowUpdate, err: readErr}
	}()

	profile := tlsfingerprint.GrokCLIProfile()
	transport, err := newFingerprintHTTP2Transport(poolSettings{}, profile, func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	})
	require.NoError(t, err)
	// The preamble is protocol-level and identical over TLS; h2c keeps the test
	// free of certificate plumbing.
	enableHTTP2TransportAllowHTTP(t, transport)
	t.Cleanup(func() { closeIdleConnections(transport) })

	req, err := http.NewRequest(http.MethodPost, "http://"+listener.Addr().String()+"/v1/responses", strings.NewReader(`{}`))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// The server never answers, so the round trip fails; only the preamble matters.
	if resp, roundTripErr := transport.RoundTrip(req.WithContext(ctx)); roundTripErr == nil {
		_ = resp.Body.Close()
	}

	var got preamble
	select {
	case got = <-preambleCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the HTTP/2 client preamble")
	}
	require.NoError(t, got.err)

	require.Equal(t, profile.HTTP2.Settings, got.settings,
		"client SETTINGS must match hyper/h2 in both order and values")
	require.Equal(t, profile.HTTP2.ConnectionWindowUpdate, got.windowUpdate,
		"initial connection WINDOW_UPDATE must match hyper's 5MiB target window")
}

func TestBuildUpstreamTransportWithTLSFingerprintUsesALPNPairForHTTP2Profiles(t *testing.T) {
	roundTripper, err := buildUpstreamTransportWithTLSFingerprint(poolSettings{}, nil, tlsfingerprint.GrokCLIProfile())
	require.NoError(t, err)
	pair, ok := roundTripper.(*alpnRoundTripper)
	require.True(t, ok, "an h2-capable profile must produce an ALPN-aware round tripper")
	t.Cleanup(pair.CloseIdleConnections)

	h1, ok := pair.h1.(*http.Transport)
	require.True(t, ok)
	require.NotNil(t, h1.DialTLSContext)
	require.False(t, h1.ForceAttemptHTTP2, "HTTP/2 must be driven by the paired http2.Transport")
}

func TestNewFingerprintHTTP2Transport_GrokProfileUsesFork(t *testing.T) {
	transport, err := newFingerprintHTTP2Transport(poolSettings{}, tlsfingerprint.GrokCLIProfile(), func(ctx context.Context, network, addr string) (net.Conn, error) {
		return nil, fmt.Errorf("dial should not run in transport selection test")
	})
	require.NoError(t, err)
	require.Equal(t,
		"github.com/Wei-Shaw/sub2api/internal/pkg/grokhttp2/http2",
		reflect.TypeOf(transport).Elem().PkgPath(),
		"the real Grok profile must switch to the repository-local grokhttp2 fork",
	)
}

func TestNewFingerprintHTTP2Transport_GrokProfilePassesHeaderOrderIntoForkEncodingSeam(t *testing.T) {
	profile := tlsfingerprint.GrokCLIProfile()
	transport, err := newFingerprintHTTP2Transport(poolSettings{}, profile, func(ctx context.Context, network, addr string) (net.Conn, error) {
		return nil, fmt.Errorf("dial should not run in transport selection test")
	})
	require.NoError(t, err)

	fork, ok := transport.(*grokhttp2.Transport)
	require.True(t, ok, "the real Grok profile must build the repository-local fork transport")
	require.NotNil(t, fork.HeaderOrder, "the real Grok profile header ordering must reach the fork request encoder seam")
	require.Equal(t, profile.HTTP2.PseudoHeaderOrder, fork.HeaderOrder.Pseudo)
	require.Equal(t, profile.HTTP2.RegularHeaderOrder, fork.HeaderOrder.Regular)
}

func TestBuildUpstreamTransportWithTLSFingerprint_GrokProfileCreatesForkOnlyOnGrokCapability(t *testing.T) {
	tests := []struct {
		name       string
		profile    *tlsfingerprint.Profile
		wantPkg    string
		wantReason string
	}{
		{
			name:       "real grok profile",
			profile:    tlsfingerprint.GrokCLIProfile(),
			wantPkg:    "github.com/Wei-Shaw/sub2api/internal/pkg/grokhttp2/http2",
			wantReason: "the Grok profile should be the only built-in profile on the fork path",
		},
		{
			name:       "synthetic non grok profile",
			profile:    syntheticHTTP2Profile("Synthetic h2 impostor"),
			wantPkg:    "golang.org/x/net/http2",
			wantReason: "advertising h2 alone must not be enough to enter the Grok-specific fork path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			roundTripper, err := buildUpstreamTransportWithTLSFingerprint(poolSettings{}, nil, tt.profile)
			require.NoError(t, err)
			pair, ok := roundTripper.(*alpnRoundTripper)
			require.True(t, ok)
			t.Cleanup(pair.CloseIdleConnections)

			require.Equal(t, tt.wantPkg, reflect.TypeOf(pair.h2).Elem().PkgPath(), tt.wantReason)
		})
	}
}

// Under "proxy" isolation the account is absent from the cache key, so the
// profile identity is the only thing keeping an Anthropic (HTTP/1.1) client and
// a Grok (h2) client from sharing one transport.
func TestFingerprintClientCacheSeparatesProfilesUnderProxyIsolation(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.ConnectionPoolIsolation = config.ConnectionPoolIsolationProxy
	svc := NewHTTPUpstream(cfg).(*httpUpstreamService)

	grok, err := svc.getClientEntryWithTLS("", 1, 1, tlsfingerprint.GrokCLIProfile(), service.HTTPUpstreamProfileDefault, false, false)
	require.NoError(t, err)
	claude, err := svc.getClientEntryWithTLS("", 2, 1, tlsfingerprint.BuiltInDefaultProfile(), service.HTTPUpstreamProfileDefault, false, false)
	require.NoError(t, err)

	require.NotSame(t, grok.client, claude.client, "different profiles must not share a cached client")
	require.IsType(t, &alpnRoundTripper{}, grok.client.Transport)
	require.IsType(t, &http.Transport{}, claude.client.Transport)

	again, err := svc.getClientEntryWithTLS("", 1, 1, tlsfingerprint.GrokCLIProfile(), service.HTTPUpstreamProfileDefault, false, false)
	require.NoError(t, err)
	require.Same(t, grok.client, again.client, "the same profile and account must reuse its cached client")

	// A resumption-capable profile carries a TLS ticket store, and a ticket ties
	// two connections together for the peer. Proxy isolation keeps the account out
	// of the cache key, so resumption has to put it back: otherwise two Grok
	// accounts behind one proxy would present each other's tickets.
	otherAccount, err := svc.getClientEntryWithTLS("", 3, 1, tlsfingerprint.GrokCLIProfile(), service.HTTPUpstreamProfileDefault, false, false)
	require.NoError(t, err)
	require.NotSame(t, grok.client, otherAccount.client, "resumption must not share a ticket store across accounts")

	// Profiles that never resume keep sharing one client under proxy isolation.
	claudeAgain, err := svc.getClientEntryWithTLS("", 4, 1, tlsfingerprint.BuiltInDefaultProfile(), service.HTTPUpstreamProfileDefault, false, false)
	require.NoError(t, err)
	require.Same(t, claude.client, claudeAgain.client, "a profile without resumption must still pool across accounts")
}

func TestFingerprintClientCacheAccountIsolationRebuildsGrokTransportOnProxyChange(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.ConnectionPoolIsolation = config.ConnectionPoolIsolationAccount
	svc := NewHTTPUpstream(cfg).(*httpUpstreamService)

	first, err := svc.getClientEntryWithTLS("http://proxy-a.local:8080", 1, 1, tlsfingerprint.GrokCLIProfile(), service.HTTPUpstreamProfileDefault, false, false)
	require.NoError(t, err)
	second, err := svc.getClientEntryWithTLS("http://proxy-b.local:8080", 1, 1, tlsfingerprint.GrokCLIProfile(), service.HTTPUpstreamProfileDefault, false, false)
	require.NoError(t, err)

	require.NotSame(t, first.client, second.client, "account isolation must rebuild a Grok transport when the proxy changes")
	require.Equal(t, "http://proxy-b.local:8080", second.proxyKey)
	require.Len(t, svc.clients, 1, "account isolation should replace the old TLS transport instead of keeping both proxies cached")
}

func TestFingerprintClientCacheAccountProxyIsolationSeparatesGrokTransportsByProxy(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.ConnectionPoolIsolation = config.ConnectionPoolIsolationAccountProxy
	svc := NewHTTPUpstream(cfg).(*httpUpstreamService)

	first, err := svc.getClientEntryWithTLS("http://proxy-a.local:8080", 1, 1, tlsfingerprint.GrokCLIProfile(), service.HTTPUpstreamProfileDefault, false, false)
	require.NoError(t, err)
	firstAgain, err := svc.getClientEntryWithTLS("http://proxy-a.local:8080", 1, 1, tlsfingerprint.GrokCLIProfile(), service.HTTPUpstreamProfileDefault, false, false)
	require.NoError(t, err)
	secondProxy, err := svc.getClientEntryWithTLS("http://proxy-b.local:8080", 1, 1, tlsfingerprint.GrokCLIProfile(), service.HTTPUpstreamProfileDefault, false, false)
	require.NoError(t, err)

	require.Same(t, first.client, firstAgain.client, "the same account+proxy tuple must reuse the cached Grok transport")
	require.NotSame(t, first.client, secondProxy.client, "account+proxy isolation must not share one Grok transport across proxies")
	require.Len(t, svc.clients, 2, "different proxies should produce distinct TLS cache keys under account+proxy isolation")
}

func enableHTTP2TransportAllowHTTP(t *testing.T, transport http.RoundTripper) {
	t.Helper()

	switch typed := transport.(type) {
	case *http2.Transport:
		typed.AllowHTTP = true
	case *grokhttp2.Transport:
		typed.AllowHTTP = true
	default:
		t.Fatalf("unexpected HTTP/2 transport type %T", transport)
	}
}
