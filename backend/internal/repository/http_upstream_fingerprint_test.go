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
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
)

func TestNewFingerprintHTTP2Transport_GrokOrderedHeadersFailExplicitlyWhenUnsupported(t *testing.T) {
	prev := grokHTTP2HeaderOrderSupported
	grokHTTP2HeaderOrderSupported = func() bool { return false }
	t.Cleanup(func() { grokHTTP2HeaderOrderSupported = prev })

	transport, err := newFingerprintHTTP2Transport(poolSettings{}, tlsfingerprint.GrokCLIProfile(), func(ctx context.Context, network, addr string) (net.Conn, error) {
		return nil, fmt.Errorf("dial should not run in transport selection test")
	})
	require.Nil(t, transport)
	require.Error(t, err)
	require.Contains(t, err.Error(), "http2legacy")
	require.Contains(t, err.Error(), "HeaderOrder")
}

func TestBuildUpstreamTransportWithTLSFingerprint_NonGrokAdvertisesHTTP2SyntheticProfileStaysOnStdTransport(t *testing.T) {
	profile := syntheticHTTP2Profile("Synthetic HTTP/2 profile")

	roundTripper, err := buildUpstreamTransportWithTLSFingerprint(poolSettings{}, nil, profile)
	require.NoError(t, err)
	pair, ok := roundTripper.(*alpnRoundTripper)
	require.True(t, ok, "any h2-advertising profile still needs the ALPN-aware pair")
	t.Cleanup(pair.CloseIdleConnections)

	require.Equal(t,
		"golang.org/x/net/http2",
		reflect.TypeOf(pair.h2).Elem().PkgPath(),
		"a non-Grok profile that only advertises h2 must stay on the standard x/net transport",
	)
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

func TestALPNRoundTripperFallbackRebuildsConsumedBodyViaGetBody(t *testing.T) {
	var getBodyCalls atomic.Int64
	var h1Body string

	pair := &alpnRoundTripper{
		h2: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("dial: %w", tlsfingerprint.ErrALPNMismatch)
		}),
		h1: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			h1Body = string(body)
			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: req}, nil
		}),
	}

	const payload = `{"model":"grok-4.5","input":"replay me"}`
	req, err := http.NewRequest(http.MethodPost, "https://cli-chat-proxy.grok.com/v1/responses", io.NopCloser(strings.NewReader(payload)))
	require.NoError(t, err)
	req.GetBody = func() (io.ReadCloser, error) {
		getBodyCalls.Add(1)
		return io.NopCloser(strings.NewReader(payload)), nil
	}

	_, err = io.ReadAll(req.Body)
	require.NoError(t, err, "precondition: the original body must already be consumed before fallback cloning")

	resp, err := pair.RoundTrip(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, payload, h1Body, "fallback must read from a fresh GetBody clone, not the exhausted original body")
	require.Equal(t, int64(1), getBodyCalls.Load(), "fallback must rebuild the request body via GetBody exactly once")
}

func TestALPNRoundTripperALPNMismatchWithoutGetBodySkipsHTTP1Fallback(t *testing.T) {
	var h1Calls atomic.Int64

	pair := &alpnRoundTripper{
		h2: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("dial: %w", tlsfingerprint.ErrALPNMismatch)
		}),
		h1: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			h1Calls.Add(1)
			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
		}),
	}

	req, err := http.NewRequest(http.MethodPost, "https://cli-chat-proxy.grok.com/v1/responses", io.NopCloser(strings.NewReader(`{"model":"grok-4.5"}`)))
	require.NoError(t, err)
	req.GetBody = nil

	_, err = pair.RoundTrip(req)
	require.ErrorIs(t, err, tlsfingerprint.ErrALPNMismatch)
	require.Zero(t, h1Calls.Load(), "without GetBody the fallback must not retry with an unreplayable request body")
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

func syntheticHTTP2Profile(name string) *tlsfingerprint.Profile {
	return &tlsfingerprint.Profile{
		Name:          name,
		ALPNProtocols: []string{tlsfingerprint.ALPNProtocolHTTP2, tlsfingerprint.ALPNProtocolHTTP1},
		HTTP2:         tlsfingerprint.GrokCLIHTTP2Profile(),
	}
}

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
