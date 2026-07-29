package repository

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"

	grokhttp2 "github.com/Wei-Shaw/sub2api/internal/pkg/grokhttp2/http2"
	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
)

var grokHTTP2HeaderOrderSupported = grokhttp2.SupportsHeaderOrder

// fingerprintDialer resolves the uTLS dialer to use for the configured proxy.
//
// supported=false means the proxy scheme cannot carry a fingerprinted
// handshake, and the caller must keep the plain transport so proxy routing is
// not silently bypassed.
func fingerprintDialer(profile *tlsfingerprint.Profile, proxyURL *url.URL) (dial tlsfingerprint.DialTLSFunc, supported bool) {
	if proxyURL == nil {
		slog.Debug("tls_fingerprint_transport_direct")
		return tlsfingerprint.NewDialer(profile, nil).DialTLSContext, true
	}
	switch strings.ToLower(proxyURL.Scheme) {
	case "socks5", "socks5h":
		slog.Debug("tls_fingerprint_transport_socks5", "proxy", proxyURL.Host)
		return tlsfingerprint.NewSOCKS5ProxyDialer(profile, proxyURL).DialTLSContext, true
	case "http":
		slog.Debug("tls_fingerprint_transport_http_connect", "proxy", proxyURL.Host)
		return tlsfingerprint.NewHTTPProxyDialer(profile, proxyURL).DialTLSContext, true
	case "https":
		// The fingerprint dialer emits a plaintext CONNECT preface and cannot
		// establish TLS to an HTTPS proxy.
		return nil, false
	default:
		slog.Debug("tls_fingerprint_transport_unknown_scheme_fallback", "scheme", proxyURL.Scheme)
		return nil, false
	}
}

// newFingerprintHTTP1Transport builds the HTTP/1.1 half of a fingerprinted
// transport pair.
func newFingerprintHTTP1Transport(settings poolSettings, dial tlsfingerprint.DialTLSFunc) *http.Transport {
	return &http.Transport{
		MaxIdleConns:          settings.maxIdleConns,
		MaxIdleConnsPerHost:   settings.maxIdleConnsPerHost,
		MaxConnsPerHost:       settings.maxConnsPerHost,
		IdleConnTimeout:       settings.idleConnTimeout,
		ResponseHeaderTimeout: settings.responseHeaderTimeout,
		// net/http cannot upgrade a uTLS connection to HTTP/2 (it only
		// recognizes *tls.Conn), so HTTP/2 is driven by the paired
		// http2.Transport instead.
		ForceAttemptHTTP2: false,
		DialTLSContext:    dial,
	}
}

// newFingerprintHTTP2Transport builds the HTTP/2 half of a fingerprinted
// transport pair.
//
// The per-stream and per-connection receive windows that decide the client
// SETTINGS and the initial WINDOW_UPDATE are only reachable through
// net/http.Transport.HTTP2, which http2.ConfigureTransports links to the
// returned HTTP/2 transport. The carrier transport exists solely to hold that
// configuration: ConfigureTransports registers an alternate "https" round
// tripper on it, so it must never serve requests itself.
func newFingerprintHTTP2Transport(settings poolSettings, profile *tlsfingerprint.Profile, dial tlsfingerprint.DialTLSFunc) (http.RoundTripper, error) {
	if profile.HTTP2 == nil {
		// A profile that offers h2 without an HTTP/2 preamble still has to speak
		// HTTP/2 once the server selects it, but the connection preface will be
		// net/http2's own and therefore reads as Go.
		slog.Warn("tls_fingerprint_http2_profile_missing", "profile", profile.Name)
	}
	carrier := &http.Transport{
		IdleConnTimeout:       settings.idleConnTimeout,
		ResponseHeaderTimeout: settings.responseHeaderTimeout,
	}
	profile.HTTP2.ConfigureTransports(carrier, nil)

	if profile.RequiresGrokHTTP2Transport() {
		if profileRequestsOrderedHeaders(profile.HTTP2) && !grokHTTP2HeaderOrderSupported() {
			return nil, errors.New("grok ordered HTTP/2 fingerprint unsupported on go1.27 wrapper path; build with http2legacy to retain HeaderOrder support")
		}
		transport, err := grokhttp2.ConfigureTransports(carrier)
		if err != nil {
			return nil, err
		}
		transport.ConnPool = nil
		transport.IdleConnTimeout = settings.idleConnTimeout
		applyHTTP2ProfileToGrokTransport(profile.HTTP2, transport)

		requireH2 := tlsfingerprint.RequireALPN(tlsfingerprint.ALPNProtocolHTTP2, profile, dial)
		transport.DialTLSContext = func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			return requireH2(ctx, network, addr)
		}
		return transport, nil
	}

	transport, err := http2.ConfigureTransports(carrier)
	if err != nil {
		return nil, err
	}
	// ConfigureTransports hands back a transport wired to a no-dial pool,
	// because it expects net/http to own dialing. Clearing the pool lets it
	// dial through DialTLSContext on its own.
	transport.ConnPool = nil
	transport.IdleConnTimeout = settings.idleConnTimeout
	profile.HTTP2.ConfigureTransports(nil, transport)

	requireH2 := tlsfingerprint.RequireALPN(tlsfingerprint.ALPNProtocolHTTP2, profile, dial)
	transport.DialTLSContext = func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
		return requireH2(ctx, network, addr)
	}
	return transport, nil
}

func profileRequestsOrderedHeaders(profile *tlsfingerprint.HTTP2Profile) bool {
	return profile != nil && (len(profile.PseudoHeaderOrder) > 0 || len(profile.RegularHeaderOrder) > 0)
}

func applyHTTP2ProfileToGrokTransport(profile *tlsfingerprint.HTTP2Profile, transport *grokhttp2.Transport) {
	if profile == nil || transport == nil {
		return
	}
	if len(profile.PseudoHeaderOrder) > 0 || len(profile.RegularHeaderOrder) > 0 {
		transport.HeaderOrder = &grokhttp2.HeaderOrder{
			Pseudo:  append([]string{}, profile.PseudoHeaderOrder...),
			Regular: append([]string{}, profile.RegularHeaderOrder...),
		}
	}
	if value, ok := profile.Setting(tlsfingerprint.HTTP2SettingMaxFrameSize); ok {
		transport.MaxReadFrameSize = value
	}
	if value, ok := profile.Setting(tlsfingerprint.HTTP2SettingMaxHeaderListSize); ok {
		transport.MaxHeaderListSize = value
	}
	if profile.PingInterval > 0 {
		transport.ReadIdleTimeout = profile.PingInterval
	}
	if profile.PingTimeout > 0 {
		transport.PingTimeout = profile.PingTimeout
	}
}

// buildUpstreamTransportWithTLSFingerprint builds the upstream round tripper
// that presents the profile's TLS fingerprint.
//
// When the profile offers h2 during ALPN, requests go over a real HTTP/2
// transport whose connection preamble is rewritten to the profile's shape;
// hosts that decline h2 fall back to an HTTP/1.1-only fingerprint, mirroring
// what the emulated client does when its h2 attempt fails.
// applyProfilePoolProfile narrows the pool to the emulated client's reuse
// behavior. Only the values the emulated client actually pins are touched, so
// operator configuration keeps deciding everything else.
func applyProfilePoolProfile(settings poolSettings, profile *tlsfingerprint.Profile) poolSettings {
	if profile == nil || profile.Pool == nil {
		return settings
	}
	if profile.Pool.MaxIdleConnsPerHost > 0 && profile.Pool.MaxIdleConnsPerHost < settings.maxIdleConnsPerHost {
		settings.maxIdleConnsPerHost = profile.Pool.MaxIdleConnsPerHost
	}
	if profile.Pool.IdleConnTimeout > 0 {
		settings.idleConnTimeout = profile.Pool.IdleConnTimeout
	}
	return settings
}

// newFingerprintSessionCache returns the ticket store for one transport, or nil
// when the profile does not resume.
//
// One store per transport is what keeps resumption from linking accounts: the
// caller pins a resumption-capable transport to a single account, so the tickets
// in this store only ever describe that account's connections.
func newFingerprintSessionCache(profile *tlsfingerprint.Profile) utls.ClientSessionCache {
	if profile == nil || !profile.ResumeSessions {
		return nil
	}
	// rustls' default client session store keeps 256 entries; sub2api talks to
	// one host per transport, so the capacity only bounds ticket churn.
	return tlsfingerprint.NewVersionedLRUClientSessionCache(256)
}

func buildUpstreamTransportWithTLSFingerprint(settings poolSettings, proxyURL *url.URL, profile *tlsfingerprint.Profile) (http.RoundTripper, error) {
	settings = applyProfilePoolProfile(settings, profile)
	if cache := newFingerprintSessionCache(profile); cache != nil {
		profile = profile.WithSessionCache(cache)
	}
	if !profile.AdvertisesHTTP2() {
		dial, supported := fingerprintDialer(profile, proxyURL)
		if !supported {
			return buildUnfingerprintedFallbackTransport(settings, proxyURL)
		}
		return newFingerprintHTTP1Transport(settings, dial), nil
	}

	h2Dial, supported := fingerprintDialer(profile, proxyURL)
	if !supported {
		return buildUnfingerprintedFallbackTransport(settings, proxyURL)
	}
	// A client that gives up on h2 stops offering it, so the HTTP/1.1 companion
	// advertises http/1.1 alone.
	h1Profile := profile.WithALPNProtocols(tlsfingerprint.ALPNProtocolHTTP1)
	h1Dial, _ := fingerprintDialer(h1Profile, proxyURL)
	h1 := newFingerprintHTTP1Transport(settings, h1Dial)

	h2, err := newFingerprintHTTP2Transport(settings, profile, h2Dial)
	if err != nil {
		return nil, err
	}
	slog.Debug("tls_fingerprint_transport_alpn_h2_enabled", "profile", profile.Name)
	return &alpnRoundTripper{h1: h1, h2: h2}, nil
}

// buildUnfingerprintedFallbackTransport keeps proxy routing working for proxy
// schemes the fingerprint dialer cannot tunnel through. Handing back the plain
// transport is deliberate: bypassing the proxy to keep the fingerprint would
// leak the real egress IP, which is the worse trade.
func buildUnfingerprintedFallbackTransport(settings poolSettings, proxyURL *url.URL) (http.RoundTripper, error) {
	return buildUpstreamTransport(settings, proxyURL, upstreamProtocolModeDefault)
}

// alpnRoundTripper sends requests over the fingerprinted HTTP/2 transport and
// demotes a host to the HTTP/1.1 transport once that host is observed to
// decline h2. The decision is cached per host so the extra handshake is paid at
// most once.
type alpnRoundTripper struct {
	h1     http.RoundTripper
	h2     http.RoundTripper
	http1  sync.Map // host -> struct{}
	warned sync.Map // host -> struct{}
}

func (rt *alpnRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	host := requestHostKey(req)
	if _, demoted := rt.http1.Load(host); demoted {
		return rt.h1.RoundTrip(req)
	}

	resp, err := rt.h2.RoundTrip(req)
	if err == nil || !errors.Is(err, tlsfingerprint.ErrALPNMismatch) {
		return resp, err
	}

	rt.http1.Store(host, struct{}{})
	if _, seen := rt.warned.LoadOrStore(host, struct{}{}); !seen {
		slog.Info("tls_fingerprint_http2_unavailable_using_http1", "host", host, "error", err)
	}

	fallbackReq, ok := cloneRequestForProtocolFallback(req)
	if !ok {
		return nil, err
	}
	return rt.h1.RoundTrip(fallbackReq)
}

// CloseIdleConnections keeps the upstream client cache's eviction path working
// for both halves of the pair.
func (rt *alpnRoundTripper) CloseIdleConnections() {
	closeIdleConnections(rt.h1)
	closeIdleConnections(rt.h2)
}

func closeIdleConnections(rt http.RoundTripper) {
	if closer, ok := rt.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

// cloneRequestForProtocolFallback rebuilds a request for a retry on the other
// protocol. The ALPN mismatch happens during the handshake, before any body
// byte is written, but the body reader is still rewound through GetBody so the
// retry cannot depend on that ordering.
func cloneRequestForProtocolFallback(req *http.Request) (*http.Request, bool) {
	if req.Body == nil || req.Body == http.NoBody {
		return req, true
	}
	if req.GetBody == nil {
		return nil, false
	}
	body, err := req.GetBody()
	if err != nil {
		return nil, false
	}
	clone := req.Clone(req.Context())
	clone.Body = body
	return clone, true
}

func requestHostKey(req *http.Request) string {
	if req == nil || req.URL == nil {
		return ""
	}
	return strings.ToLower(req.URL.Host)
}
