package repository

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/config"
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
	transport.AllowHTTP = true
	t.Cleanup(transport.CloseIdleConnections)

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

func TestBuildUpstreamTransportWithTLSFingerprintKeepsSingleTransportForHTTP1Profiles(t *testing.T) {
	roundTripper, err := buildUpstreamTransportWithTLSFingerprint(poolSettings{}, nil, tlsfingerprint.BuiltInDefaultProfile())
	require.NoError(t, err)
	transport, ok := roundTripper.(*http.Transport)
	require.True(t, ok, "an http/1.1-only profile must keep the single transport path")
	require.NotNil(t, transport.DialTLSContext)
}

func TestALPNRoundTripperDemotesHostAfterALPNMismatch(t *testing.T) {
	var h2Calls, h1Calls atomic.Int64
	var h1Bodies []string

	pair := &alpnRoundTripper{
		h2: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			h2Calls.Add(1)
			return nil, fmt.Errorf("dial: %w", tlsfingerprint.ErrALPNMismatch)
		}),
		h1: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			h1Calls.Add(1)
			body, err := io.ReadAll(req.Body)
			if err != nil {
				return nil, err
			}
			h1Bodies = append(h1Bodies, string(body))
			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: req}, nil
		}),
	}

	for i := 0; i < 2; i++ {
		req, err := http.NewRequest(http.MethodPost, "https://cli-chat-proxy.grok.com/v1/responses", bytes.NewReader([]byte(`{"model":"grok-4.5"}`)))
		require.NoError(t, err)
		resp, err := pair.RoundTrip(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
	}

	require.Equal(t, int64(1), h2Calls.Load(), "the h2 attempt must be made once and then cached away")
	require.Equal(t, int64(2), h1Calls.Load())
	require.Equal(t, []string{`{"model":"grok-4.5"}`, `{"model":"grok-4.5"}`}, h1Bodies,
		"the request body must be replayed on the fallback")
}

func TestALPNRoundTripperPropagatesNonALPNErrors(t *testing.T) {
	upstreamErr := fmt.Errorf("connection refused")
	pair := &alpnRoundTripper{
		h2: roundTripperFunc(func(*http.Request) (*http.Response, error) { return nil, upstreamErr }),
		h1: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("HTTP/1.1 must not be used for non-ALPN failures")
			return nil, nil
		}),
	}
	req, err := http.NewRequest(http.MethodGet, "https://api.x.ai/v1/models", nil)
	require.NoError(t, err)
	_, err = pair.RoundTrip(req)
	require.ErrorIs(t, err, upstreamErr)
}

func TestFingerprintDialerRejectsUnsupportedProxySchemes(t *testing.T) {
	profile := tlsfingerprint.GrokCLIProfile()
	for _, raw := range []string{"https://proxy.example:8443", "socks4://proxy.example:1080"} {
		proxyURL, err := url.Parse(raw)
		require.NoError(t, err)
		_, supported := fingerprintDialer(profile, proxyURL)
		require.False(t, supported, "proxy scheme %s must fall back to the plain transport", raw)
	}
	for _, raw := range []string{"http://proxy.example:8080", "socks5://proxy.example:1080", "socks5h://proxy.example:1080"} {
		proxyURL, err := url.Parse(raw)
		require.NoError(t, err)
		dial, supported := fingerprintDialer(profile, proxyURL)
		require.True(t, supported, "proxy scheme %s must be fingerprinted", raw)
		require.NotNil(t, dial)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// readClientPreamble reads the HTTP/2 client preface plus the SETTINGS and
// connection WINDOW_UPDATE frames that follow it.
func readClientPreamble(conn net.Conn) ([]tlsfingerprint.HTTP2Setting, uint32, error) {
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return nil, 0, err
	}
	preface := make([]byte, len("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"))
	if _, err := io.ReadFull(conn, preface); err != nil {
		return nil, 0, err
	}
	if string(preface) != "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n" {
		return nil, 0, fmt.Errorf("unexpected client preface %q", preface)
	}

	var settings []tlsfingerprint.HTTP2Setting
	windowUpdate := uint32(0)
	for settings == nil || windowUpdate == 0 {
		header := make([]byte, 9)
		if _, err := io.ReadFull(conn, header); err != nil {
			return settings, windowUpdate, err
		}
		frameLen := int(header[0])<<16 | int(header[1])<<8 | int(header[2])
		payload := make([]byte, frameLen)
		if _, err := io.ReadFull(conn, payload); err != nil {
			return settings, windowUpdate, err
		}
		switch header[3] {
		case 0x4: // SETTINGS
			if header[4] != 0 { // skip the ACK we may see later
				continue
			}
			for offset := 0; offset+6 <= len(payload); offset += 6 {
				settings = append(settings, tlsfingerprint.HTTP2Setting{
					ID:    binary.BigEndian.Uint16(payload[offset:]),
					Value: binary.BigEndian.Uint32(payload[offset+2:]),
				})
			}
		case 0x8: // WINDOW_UPDATE
			if !bytes.Equal(header[5:9], []byte{0, 0, 0, 0}) {
				continue // stream-level update
			}
			windowUpdate = binary.BigEndian.Uint32(payload) & 0x7fffffff
		}
	}
	return settings, windowUpdate, nil
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

// TestGrokCLIProfilePinsPoolAndResumption pins the reqwest client configuration
// captured from xai-grok-sampler/src/shared_http.rs.
func TestGrokCLIProfilePinsPoolAndResumption(t *testing.T) {
	profile := tlsfingerprint.GrokCLIProfile()

	require.NotNil(t, profile.Pool)
	require.Equal(t, 2, profile.Pool.MaxIdleConnsPerHost)
	require.Equal(t, 90*time.Second, profile.Pool.IdleConnTimeout)
	require.Equal(t, 10*time.Second, profile.Pool.ConnectTimeout)
	require.Equal(t, 10*time.Second, profile.ConnectTimeout())
	require.True(t, profile.ResumeSessions)
	// A profile only resumes once a ticket store is attached, which is what keeps
	// resumption scoped to one account.
	require.False(t, profile.ResumesSessions())

	// The pool profile narrows the operator's settings but never widens them.
	settings := poolSettings{maxIdleConnsPerHost: 32, idleConnTimeout: 30 * time.Second}
	narrowed := applyProfilePoolProfile(settings, profile)
	require.Equal(t, 2, narrowed.maxIdleConnsPerHost)
	require.Equal(t, 90*time.Second, narrowed.idleConnTimeout)

	tight := poolSettings{maxIdleConnsPerHost: 1, idleConnTimeout: 30 * time.Second}
	require.Equal(t, 1, applyProfilePoolProfile(tight, profile).maxIdleConnsPerHost)

	// A profile without pool hints leaves the settings untouched.
	require.Equal(t, settings, applyProfilePoolProfile(settings, tlsfingerprint.BuiltInDefaultProfile()))
}

func TestProfileCacheKeyDistinguishesObservableConfiguration(t *testing.T) {
	grok := tlsfingerprint.GrokCLIProfile()
	require.NotEqual(t, grok.CacheKey(), tlsfingerprint.BuiltInDefaultProfile().CacheKey())
	require.Equal(t, grok.CacheKey(), tlsfingerprint.GrokCLIProfile().CacheKey())
	require.NotEqual(t, grok.CacheKey(), grok.WithALPNProtocols(tlsfingerprint.ALPNProtocolHTTP1).CacheKey())

	var absent *tlsfingerprint.Profile
	require.Equal(t, "none", absent.CacheKey())
}
