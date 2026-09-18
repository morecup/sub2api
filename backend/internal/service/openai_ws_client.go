package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	openaiwsv2 "github.com/Wei-Shaw/sub2api/internal/service/openai_ws_v2"
	coderws "github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

const openAIWSMessageReadLimitBytes int64 = 16 * 1024 * 1024
const (
	openAIWSProxyTransportMaxIdleConns        = 128
	openAIWSProxyTransportMaxIdleConnsPerHost = 64
	openAIWSProxyTransportIdleConnTimeout     = 90 * time.Second
	openAIWSProxyClientCacheMaxEntries        = 256
	openAIWSProxyClientCacheIdleTTL           = 15 * time.Minute
	openAIWSHandshakeHeaderLimitBytes         = 128 * 1024
	codexDesktopWSCompressionOffer            = "permessage-deflate; client_max_window_bits"
)

// codexDesktopWSHeaderOrder is the HTTP/1.1 order emitted by Tungstenite in
// the captured 0.151 Desktop handshake. The wire casing is intentional.
var codexDesktopWSHeaderOrder = []string{
	"Host",
	"Connection",
	"Upgrade",
	"Sec-WebSocket-Version",
	"Sec-WebSocket-Key",
	"chatgpt-account-id",
	"authorization",
	"user-agent",
	"originator",
	"openai-beta",
	"version",
	"x-codex-beta-features",
	"x-client-request-id",
	"session-id",
	"thread-id",
	"x-codex-window-id",
	"x-codex-turn-metadata",
	"x-codex-routing-hint",
	"x-oai-attestation",
	"sec-websocket-extensions",
}

type OpenAIWSTransportMetricsSnapshot struct {
	ProxyClientCacheHits   int64   `json:"proxy_client_cache_hits"`
	ProxyClientCacheMisses int64   `json:"proxy_client_cache_misses"`
	TransportReuseRatio    float64 `json:"transport_reuse_ratio"`
}

// openAIWSClientConn 抽象 WS 客户端连接，便于替换底层实现。
type openAIWSClientConn interface {
	WriteJSON(ctx context.Context, value any) error
	ReadMessage(ctx context.Context) ([]byte, error)
	Ping(ctx context.Context) error
	Close() error
}

// openAIWSIdlePingCapable is intentionally separate from openAIWSClientConn.
// A pool probe happens while no goroutine is reading an idle connection, which
// is not safe for every WebSocket implementation.
type openAIWSIdlePingCapable interface {
	SupportsIdlePingWithoutReader() bool
}

// openAIWSReaderLoopCapable 声明该实现的控制帧只在阻塞读期间被消费，
// 连接池需为其常驻一个读循环，否则空闲连接无法应答上游 ping。
type openAIWSReaderLoopCapable interface {
	RequiresReaderLoop() bool
}

// A resident pool reader handles control frames while idle. Defer model-event
// telemetry until a foreground read consumes the result, matching the original
// observation boundary instead of measuring the whole idle connection lifetime.
type openAIWSPoolReadResult struct {
	payload []byte
	err     error
	observe func(time.Duration)
}

type openAIWSPoolTelemetryReader interface {
	ReadMessageForPool(context.Context) openAIWSPoolReadResult
	ObservePoolReadError(error, time.Duration)
}

// openAIWSUpstreamPingCounter 报告连接收到过多少个上游 ping 帧，用于核对读循环是否在应答保活。
type openAIWSUpstreamPingCounter interface {
	UpstreamPingCount() int64
}

// openAIWSForceCloser 不做关闭握手直接切断连接，用于对端已不响应的场景。
type openAIWSForceCloser interface {
	CloseNow() error
}

// openAIWSClientDialer 抽象 WS 建连器。
type openAIWSTLSProfile = tlsfingerprint.Profile

type openAIWSClientDialer interface {
	Dial(ctx context.Context, wsURL string, headers http.Header, proxyURL string, profile *openAIWSTLSProfile, transportScope string) (openAIWSClientConn, int, http.Header, error)
}

type openAIWSTransportMetricsDialer interface {
	SnapshotTransportMetrics() OpenAIWSTransportMetricsSnapshot
}

func newDefaultOpenAIWSClientDialer() openAIWSClientDialer {
	return &coderOpenAIWSClientDialer{
		proxyClients: make(map[string]*openAIWSProxyClientEntry),
	}
}

type coderOpenAIWSClientDialer struct {
	telemetry    *codexTelemetryExporter
	proxyMu      sync.Mutex
	proxyClients map[string]*openAIWSProxyClientEntry
	proxyHits    atomic.Int64
	proxyMisses  atomic.Int64
}

// openAIWSHandshakeError keeps a bounded, non-logged HTTP error body so the
// Agent Identity recovery path can distinguish an invalid task from other
// 401 handshake failures.
type openAIWSHandshakeError struct {
	Body []byte
	Err  error
}

func (e *openAIWSHandshakeError) Error() string {
	if e == nil || e.Err == nil {
		return "openai ws handshake failed"
	}
	return e.Err.Error()
}

func (e *openAIWSHandshakeError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type openAIWSProxyClientEntry struct {
	client           *http.Client
	lastUsedUnixNano int64
}

func (d *coderOpenAIWSClientDialer) Dial(
	ctx context.Context,
	wsURL string,
	headers http.Header,
	proxyURL string,
	profile *tlsfingerprint.Profile,
	transportScope string,
) (openAIWSClientConn, int, http.Header, error) {
	targetURL := strings.TrimSpace(wsURL)
	if targetURL == "" {
		return nil, 0, nil, errors.New("ws url is empty")
	}

	wrapped := &coderOpenAIWSClientConn{}
	opts := &coderws.DialOptions{
		HTTPHeader:      cloneHeader(headers),
		CompressionMode: coderws.CompressionContextTakeover,
		OnPingReceived: func(context.Context, []byte) bool {
			wrapped.upstreamPings.Add(1)
			return true
		},
	}
	if profile != nil {
		profiledClient, err := d.fingerprintedHTTPClient(proxyURL, profile, transportScope)
		if err != nil {
			return nil, 0, nil, err
		}
		opts.HTTPClient = profiledClient
	} else if proxy := strings.TrimSpace(proxyURL); proxy != "" {
		proxyClient, err := d.proxyHTTPClient(proxy)
		if err != nil {
			return nil, 0, nil, err
		}
		opts.HTTPClient = proxyClient
	}

	conn, resp, err := coderws.Dial(ctx, targetURL, opts)
	if err != nil {
		status := 0
		respHeaders := http.Header(nil)
		if resp != nil {
			status = resp.StatusCode
			respHeaders = cloneHeader(resp.Header)
		}
		var body []byte
		if resp != nil && resp.Body != nil {
			body, _ = io.ReadAll(io.LimitReader(resp.Body, 8<<10))
			_ = resp.Body.Close()
		}
		return nil, status, respHeaders, &openAIWSHandshakeError{Body: body, Err: err}
	}
	// coder/websocket 默认单消息读取上限为 32KB，Codex WS 事件（如 rate_limits/大 delta）
	// 可能超过该阈值，需显式提高上限，避免本地 read_fail(message too big)。
	conn.SetReadLimit(openAIWSMessageReadLimitBytes)
	respHeaders := http.Header(nil)
	if resp != nil {
		respHeaders = cloneHeader(resp.Header)
	}
	wrapped.conn = conn
	clientConn := wrapped
	if target, err := url.Parse(targetURL); err == nil && d.telemetry != nil &&
		target.Hostname() == "chatgpt.com" && target.Path == "/backend-api/codex/responses" &&
		strings.HasPrefix(transportScope, "openai-account:") {
		id, _ := strconv.ParseInt(strings.TrimPrefix(transportScope, "openai-account:"), 10, 64)
		if id > 0 {
			clientConn.telemetry = &codexTelemetryWS{exporter: d.telemetry, route: codexTelemetryRoute{accountID: id, proxyURL: proxyURL, profile: profile, client: codexClientProfileFromContext(ctx, id)}}
			clientConn.telemetry.model.Store(codexTelemetryModelFromRoutingHint(headers.Get("x-codex-routing-hint")))
		}
	}
	return clientConn, 0, respHeaders, nil
}

func (d *coderOpenAIWSClientDialer) fingerprintedHTTPClient(proxy string, profile *tlsfingerprint.Profile, transportScope string) (*http.Client, error) {
	if d == nil {
		return nil, errors.New("openai ws dialer is nil")
	}
	if profile == nil {
		return nil, errors.New("TLS fingerprint profile is nil")
	}
	normalizedProxy := strings.TrimSpace(proxy)
	if normalizedProxy != "" {
		if _, err := url.Parse(normalizedProxy); err != nil {
			return nil, fmt.Errorf("invalid proxy url: %w", err)
		}
	}
	normalizedScope := strings.TrimSpace(transportScope)
	cacheKey := "fingerprint\x00" + normalizedScope + "\x00" + profile.CacheKey() + "\x00" + normalizedProxy
	now := time.Now().UnixNano()

	d.proxyMu.Lock()
	defer d.proxyMu.Unlock()
	if entry, ok := d.proxyClients[cacheKey]; ok && entry != nil && entry.client != nil {
		entry.lastUsedUnixNano = now
		d.proxyHits.Add(1)
		return entry.client, nil
	}
	d.cleanupProxyClientsLocked(now)
	client, err := newFingerprintedOpenAIWSHTTPClient(normalizedProxy, profile, normalizedScope)
	if err != nil {
		return nil, err
	}
	d.proxyClients[cacheKey] = &openAIWSProxyClientEntry{client: client, lastUsedUnixNano: now}
	d.ensureProxyClientCapacityLocked()
	d.proxyMisses.Add(1)
	return client, nil
}

func newFingerprintedOpenAIWSHTTPClient(proxy string, profile *tlsfingerprint.Profile, transportScope string) (*http.Client, error) {
	// Codex builds an explicit rustls ClientConfig for WebSockets and leaves
	// alpn_protocols empty, so its ClientHello omits ALPN rather than offering
	// http/1.1. HTTP Upgrade still speaks HTTP/1.1 without ALPN.
	websocketProfile := profile.WithoutALPN()
	if websocketProfile == nil {
		return nil, errors.New("TLS fingerprint profile is nil")
	}
	if websocketProfile.ResumeSessions {
		if transportScope == "" {
			// A missing account scope must fail closed for linkability: preserve the
			// ClientHello shape but do not offer tickets across anonymous callers.
			clone := *websocketProfile
			clone.ResumeSessions = false
			clone.SessionCache = nil
			websocketProfile = &clone
		} else {
			websocketProfile = websocketProfile.WithSessionCache(tlsfingerprint.NewVersionedLRUClientSessionCache(256))
		}
	}

	transport := &http.Transport{
		MaxIdleConns:        openAIWSProxyTransportMaxIdleConns,
		MaxIdleConnsPerHost: openAIWSProxyTransportMaxIdleConnsPerHost,
		IdleConnTimeout:     openAIWSProxyTransportIdleConnTimeout,
		TLSHandshakeTimeout: 10 * time.Second,
		ForceAttemptHTTP2:   false,
		DisableCompression:  true,
	}

	var parsedProxy *url.URL
	if proxy != "" {
		var err error
		parsedProxy, err = url.Parse(proxy)
		if err != nil {
			return nil, fmt.Errorf("invalid proxy url: %w", err)
		}
	}

	switch {
	case parsedProxy == nil:
		transport.DialTLSContext = orderCodexWebSocketHandshake(tlsfingerprint.NewDialer(websocketProfile, nil).DialTLSContext)
	case strings.EqualFold(parsedProxy.Scheme, "http"):
		transport.DialTLSContext = orderCodexWebSocketHandshake(tlsfingerprint.NewHTTPProxyDialer(websocketProfile, parsedProxy).DialTLSContext)
	case strings.EqualFold(parsedProxy.Scheme, "socks5"), strings.EqualFold(parsedProxy.Scheme, "socks5h"):
		transport.DialTLSContext = orderCodexWebSocketHandshake(tlsfingerprint.NewSOCKS5ProxyDialer(websocketProfile, parsedProxy).DialTLSContext)
	default:
		// HTTPS and unknown proxy schemes keep routing through the configured
		// proxy. The current uTLS CONNECT dialer cannot safely fingerprint the
		// outer HTTPS hop, so retaining egress privacy takes precedence.
		transport.Proxy = http.ProxyURL(parsedProxy)
	}
	return &http.Client{Transport: transport}, nil
}

func orderCodexWebSocketHandshake(dial tlsfingerprint.DialTLSFunc) tlsfingerprint.DialTLSFunc {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := dial(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return &codexWebSocketHandshakeConn{Conn: conn}, nil
	}
}

// codexWebSocketHandshakeConn rewrites only the first HTTP/1.1 request header
// on a freshly dialed WS connection. After the Upgrade request it becomes a
// transparent net.Conn, so WebSocket frames are untouched.
type codexWebSocketHandshakeConn struct {
	net.Conn
	mu          sync.Mutex
	pending     []byte
	passthrough bool
}

func (c *codexWebSocketHandshakeConn) Write(payload []byte) (int, error) {
	if c == nil || c.Conn == nil {
		return 0, net.ErrClosed
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.passthrough {
		return c.Conn.Write(payload)
	}

	c.pending = append(c.pending, payload...)
	headerEnd := bytes.Index(c.pending, []byte("\r\n\r\n"))
	if headerEnd < 0 {
		if len(c.pending) <= openAIWSHandshakeHeaderLimitBytes {
			return len(payload), nil
		}
		buffered := c.pending
		c.pending = nil
		c.passthrough = true
		if err := writeOpenAIWSAll(c.Conn, buffered); err != nil {
			return 0, err
		}
		return len(payload), nil
	}

	headerEnd += len("\r\n\r\n")
	header, ok := reorderCodexWebSocketHandshake(c.pending[:headerEnd])
	if !ok {
		header = append([]byte(nil), c.pending[:headerEnd]...)
	}
	out := make([]byte, 0, len(header)+len(c.pending)-headerEnd)
	out = append(out, header...)
	out = append(out, c.pending[headerEnd:]...)
	c.pending = nil
	c.passthrough = true
	if err := writeOpenAIWSAll(c.Conn, out); err != nil {
		return 0, err
	}
	return len(payload), nil
}

type codexWebSocketHeaderLine struct {
	name  string
	value string
}

func reorderCodexWebSocketHandshake(block []byte) ([]byte, bool) {
	if !bytes.HasSuffix(block, []byte("\r\n\r\n")) {
		return nil, false
	}
	lines := strings.Split(string(block[:len(block)-len("\r\n\r\n")]), "\r\n")
	if len(lines) < 2 || !strings.HasPrefix(lines[0], "GET ") || !strings.HasSuffix(lines[0], " HTTP/1.1") {
		return nil, false
	}

	grouped := make(map[string][]codexWebSocketHeaderLine, len(lines)-1)
	for _, line := range lines[1:] {
		separator := strings.IndexByte(line, ':')
		if separator <= 0 {
			return nil, false
		}
		name := strings.TrimSpace(line[:separator])
		lowerName := strings.ToLower(name)
		if lowerName == "" {
			return nil, false
		}
		grouped[lowerName] = append(grouped[lowerName], codexWebSocketHeaderLine{
			name:  name,
			value: strings.TrimSpace(line[separator+1:]),
		})
	}

	var ordered strings.Builder
	ordered.Grow(len(block) + len("; client_max_window_bits"))
	ordered.WriteString(lines[0])
	ordered.WriteString("\r\n")
	emit := func(lowerName, wireName string) {
		fields := grouped[lowerName]
		for _, field := range fields {
			value := field.value
			if lowerName == "sec-websocket-extensions" && strings.EqualFold(value, "permessage-deflate") {
				value = codexDesktopWSCompressionOffer
			}
			ordered.WriteString(wireName)
			ordered.WriteString(": ")
			ordered.WriteString(value)
			ordered.WriteString("\r\n")
		}
		delete(grouped, lowerName)
	}

	const extensionHeader = "sec-websocket-extensions"
	for _, wireName := range codexDesktopWSHeaderOrder {
		lowerName := strings.ToLower(wireName)
		if lowerName != extensionHeader {
			emit(lowerName, wireName)
		}
	}
	// Preserve forward compatibility: unrecognized headers remain on the wire
	// in deterministic order, immediately before Tungstenite's extension offer.
	tail := make([]string, 0, len(grouped))
	for lowerName := range grouped {
		if lowerName != extensionHeader {
			tail = append(tail, lowerName)
		}
	}
	sort.Strings(tail)
	for _, lowerName := range tail {
		emit(lowerName, grouped[lowerName][0].name)
	}
	emit(extensionHeader, "sec-websocket-extensions")
	ordered.WriteString("\r\n")
	return []byte(ordered.String()), true
}

func writeOpenAIWSAll(conn net.Conn, payload []byte) error {
	for len(payload) > 0 {
		written, err := conn.Write(payload)
		if err != nil {
			return err
		}
		if written <= 0 {
			return io.ErrNoProgress
		}
		payload = payload[written:]
	}
	return nil
}

func (d *coderOpenAIWSClientDialer) proxyHTTPClient(proxy string) (*http.Client, error) {
	if d == nil {
		return nil, errors.New("openai ws dialer is nil")
	}
	normalizedProxy := strings.TrimSpace(proxy)
	if normalizedProxy == "" {
		return nil, errors.New("proxy url is empty")
	}
	parsedProxyURL, err := url.Parse(normalizedProxy)
	if err != nil {
		return nil, fmt.Errorf("invalid proxy url: %w", err)
	}
	now := time.Now().UnixNano()

	d.proxyMu.Lock()
	defer d.proxyMu.Unlock()
	if entry, ok := d.proxyClients[normalizedProxy]; ok && entry != nil && entry.client != nil {
		entry.lastUsedUnixNano = now
		d.proxyHits.Add(1)
		return entry.client, nil
	}
	d.cleanupProxyClientsLocked(now)
	transport := &http.Transport{
		Proxy:               http.ProxyURL(parsedProxyURL),
		MaxIdleConns:        openAIWSProxyTransportMaxIdleConns,
		MaxIdleConnsPerHost: openAIWSProxyTransportMaxIdleConnsPerHost,
		IdleConnTimeout:     openAIWSProxyTransportIdleConnTimeout,
		TLSHandshakeTimeout: 10 * time.Second,
		ForceAttemptHTTP2:   true,
	}
	client := &http.Client{Transport: transport}
	d.proxyClients[normalizedProxy] = &openAIWSProxyClientEntry{
		client:           client,
		lastUsedUnixNano: now,
	}
	d.ensureProxyClientCapacityLocked()
	d.proxyMisses.Add(1)
	return client, nil
}

func (d *coderOpenAIWSClientDialer) cleanupProxyClientsLocked(nowUnixNano int64) {
	if d == nil || len(d.proxyClients) == 0 {
		return
	}
	idleTTL := openAIWSProxyClientCacheIdleTTL
	if idleTTL <= 0 {
		return
	}
	now := time.Unix(0, nowUnixNano)
	for key, entry := range d.proxyClients {
		if entry == nil || entry.client == nil {
			delete(d.proxyClients, key)
			continue
		}
		lastUsed := time.Unix(0, entry.lastUsedUnixNano)
		if now.Sub(lastUsed) > idleTTL {
			closeOpenAIWSProxyClient(entry.client)
			delete(d.proxyClients, key)
		}
	}
}

func (d *coderOpenAIWSClientDialer) ensureProxyClientCapacityLocked() {
	if d == nil {
		return
	}
	maxEntries := openAIWSProxyClientCacheMaxEntries
	if maxEntries <= 0 {
		return
	}
	for len(d.proxyClients) > maxEntries {
		var oldestKey string
		var oldestLastUsed int64
		hasOldest := false
		for key, entry := range d.proxyClients {
			lastUsed := int64(0)
			if entry != nil {
				lastUsed = entry.lastUsedUnixNano
			}
			if !hasOldest || lastUsed < oldestLastUsed {
				hasOldest = true
				oldestKey = key
				oldestLastUsed = lastUsed
			}
		}
		if !hasOldest {
			return
		}
		if entry := d.proxyClients[oldestKey]; entry != nil {
			closeOpenAIWSProxyClient(entry.client)
		}
		delete(d.proxyClients, oldestKey)
	}
}

func closeOpenAIWSProxyClient(client *http.Client) {
	if client == nil || client.Transport == nil {
		return
	}
	if transport, ok := client.Transport.(*http.Transport); ok && transport != nil {
		transport.CloseIdleConnections()
	}
}

func (d *coderOpenAIWSClientDialer) SnapshotTransportMetrics() OpenAIWSTransportMetricsSnapshot {
	if d == nil {
		return OpenAIWSTransportMetricsSnapshot{}
	}
	hits := d.proxyHits.Load()
	misses := d.proxyMisses.Load()
	total := hits + misses
	reuseRatio := 0.0
	if total > 0 {
		reuseRatio = float64(hits) / float64(total)
	}
	return OpenAIWSTransportMetricsSnapshot{
		ProxyClientCacheHits:   hits,
		ProxyClientCacheMisses: misses,
		TransportReuseRatio:    reuseRatio,
	}
}

type coderOpenAIWSClientConn struct {
	telemetry     *codexTelemetryWS
	conn          *coderws.Conn
	upstreamPings atomic.Int64
}

func (c *coderOpenAIWSClientConn) UpstreamPingCount() int64 {
	if c == nil {
		return 0
	}
	return c.upstreamPings.Load()
}

var _ openaiwsv2.FrameConn = (*coderOpenAIWSClientConn)(nil)

func (c *coderOpenAIWSClientConn) WriteJSON(ctx context.Context, value any) error {
	if c == nil || c.conn == nil {
		return errOpenAIWSConnClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	start := time.Now()
	observation := c.telemetry.prepareWrite(value)
	err := wsjson.Write(ctx, c.conn, value)
	c.telemetry.written(observation, time.Since(start), err)
	return err
}

func (c *coderOpenAIWSClientConn) ReadMessage(ctx context.Context) ([]byte, error) {
	started := time.Now()
	result := c.ReadMessageForPool(ctx)
	if result.observe != nil {
		result.observe(time.Since(started))
	}
	return result.payload, result.err
}

func (c *coderOpenAIWSClientConn) ReadMessageForPool(ctx context.Context) openAIWSPoolReadResult {
	if c == nil || c.conn == nil {
		return openAIWSPoolReadResult{err: errOpenAIWSConnClosed}
	}
	if ctx == nil {
		ctx = context.Background()
	}

	msgType, payload, err := c.conn.Read(ctx)
	result := openAIWSPoolReadResult{payload: payload, err: err}
	if msgType == coderws.MessageText || err != nil {
		result.observe = func(duration time.Duration) { c.telemetry.received(payload, duration, err) }
	}
	if err != nil {
		result.payload = nil
		return result
	}
	switch msgType {
	case coderws.MessageText, coderws.MessageBinary:
		return result
	default:
		result.payload, result.err = nil, errOpenAIWSConnClosed
		return result
	}
}

func (c *coderOpenAIWSClientConn) ObservePoolReadError(err error, duration time.Duration) {
	if c != nil {
		c.telemetry.received(nil, duration, err)
	}
}

func (c *coderOpenAIWSClientConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	if c == nil || c.conn == nil {
		return coderws.MessageText, nil, errOpenAIWSConnClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	start := time.Now()
	msgType, payload, err := c.conn.Read(ctx)
	if msgType == coderws.MessageText || err != nil {
		c.telemetry.received(payload, time.Since(start), err)
	}
	if err != nil {
		return coderws.MessageText, nil, err
	}
	return msgType, payload, nil
}

func (c *coderOpenAIWSClientConn) WriteFrame(ctx context.Context, msgType coderws.MessageType, payload []byte) error {
	if c == nil || c.conn == nil {
		return errOpenAIWSConnClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	start := time.Now()
	var observation *codexTelemetryWSRequest
	if msgType == coderws.MessageText {
		observation = c.telemetry.prepareWrite(payload)
	}
	err := c.conn.Write(ctx, msgType, payload)
	if msgType == coderws.MessageText {
		c.telemetry.written(observation, time.Since(start), err)
	}
	return err
}

func (c *coderOpenAIWSClientConn) Ping(ctx context.Context) error {
	if c == nil || c.conn == nil {
		return errOpenAIWSConnClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return c.conn.Ping(ctx)
}

// SupportsIdlePingWithoutReader reports the actual coder/websocket contract.
// Conn.Ping waits for a pong, while control frames are only consumed by Read.
// Without a reader, using Ping as a health probe would deterministically time
// out a healthy socket; the pool compensates with a resident reader loop.
func (*coderOpenAIWSClientConn) SupportsIdlePingWithoutReader() bool {
	return false
}

// RequiresReaderLoop 让池为 coder/websocket 连接常驻读循环，空闲期也能应答 ping。
func (*coderOpenAIWSClientConn) RequiresReaderLoop() bool {
	return true
}

func (c *coderOpenAIWSClientConn) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	// Close 为幂等，忽略重复关闭错误。
	_ = c.conn.Close(coderws.StatusNormalClosure, "")
	_ = c.conn.CloseNow()
	return nil
}

func (c *coderOpenAIWSClientConn) CloseNow() error {
	if c == nil || c.conn == nil {
		return nil
	}
	_ = c.conn.CloseNow()
	return nil
}
