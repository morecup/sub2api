package service

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCoderOpenAIWSClientDialer_ProxyHTTPClientReuse(t *testing.T) {
	dialer := newDefaultOpenAIWSClientDialer()
	impl, ok := dialer.(*coderOpenAIWSClientDialer)
	require.True(t, ok)

	c1, err := impl.proxyHTTPClient("http://127.0.0.1:8080")
	require.NoError(t, err)
	c2, err := impl.proxyHTTPClient("http://127.0.0.1:8080")
	require.NoError(t, err)
	require.Same(t, c1, c2, "同一代理地址应复用同一个 HTTP 客户端")

	c3, err := impl.proxyHTTPClient("http://127.0.0.1:8081")
	require.NoError(t, err)
	require.NotSame(t, c1, c3, "不同代理地址应分离客户端")
}

func TestCoderOpenAIWSClientDialer_ProxyHTTPClientInvalidURL(t *testing.T) {
	dialer := newDefaultOpenAIWSClientDialer()
	impl, ok := dialer.(*coderOpenAIWSClientDialer)
	require.True(t, ok)

	_, err := impl.proxyHTTPClient("://bad")
	require.Error(t, err)
}

func TestCoderOpenAIWSClientDialer_TransportMetricsSnapshot(t *testing.T) {
	dialer := newDefaultOpenAIWSClientDialer()
	impl, ok := dialer.(*coderOpenAIWSClientDialer)
	require.True(t, ok)

	_, err := impl.proxyHTTPClient("http://127.0.0.1:18080")
	require.NoError(t, err)
	_, err = impl.proxyHTTPClient("http://127.0.0.1:18080")
	require.NoError(t, err)
	_, err = impl.proxyHTTPClient("http://127.0.0.1:18081")
	require.NoError(t, err)

	snapshot := impl.SnapshotTransportMetrics()
	require.Equal(t, int64(1), snapshot.ProxyClientCacheHits)
	require.Equal(t, int64(2), snapshot.ProxyClientCacheMisses)
	require.InDelta(t, 1.0/3.0, snapshot.TransportReuseRatio, 0.0001)
}

func TestCoderOpenAIWSClientDialer_ProxyClientCacheCapacity(t *testing.T) {
	dialer := newDefaultOpenAIWSClientDialer()
	impl, ok := dialer.(*coderOpenAIWSClientDialer)
	require.True(t, ok)

	total := openAIWSProxyClientCacheMaxEntries + 32
	for i := 0; i < total; i++ {
		_, err := impl.proxyHTTPClient(fmt.Sprintf("http://127.0.0.1:%d", 20000+i))
		require.NoError(t, err)
	}

	impl.proxyMu.Lock()
	cacheSize := len(impl.proxyClients)
	impl.proxyMu.Unlock()

	require.LessOrEqual(t, cacheSize, openAIWSProxyClientCacheMaxEntries, "代理客户端缓存应受容量上限约束")
}

func TestCoderOpenAIWSClientDialer_ProxyClientCacheIdleTTL(t *testing.T) {
	dialer := newDefaultOpenAIWSClientDialer()
	impl, ok := dialer.(*coderOpenAIWSClientDialer)
	require.True(t, ok)

	oldProxy := "http://127.0.0.1:28080"
	_, err := impl.proxyHTTPClient(oldProxy)
	require.NoError(t, err)

	impl.proxyMu.Lock()
	oldEntry := impl.proxyClients[oldProxy]
	require.NotNil(t, oldEntry)
	oldEntry.lastUsedUnixNano = time.Now().Add(-openAIWSProxyClientCacheIdleTTL - time.Minute).UnixNano()
	impl.proxyMu.Unlock()

	// 触发一次新的代理获取，驱动 TTL 清理。
	_, err = impl.proxyHTTPClient("http://127.0.0.1:28081")
	require.NoError(t, err)

	impl.proxyMu.Lock()
	_, exists := impl.proxyClients[oldProxy]
	impl.proxyMu.Unlock()

	require.False(t, exists, "超过空闲 TTL 的代理客户端应被回收")
}

func TestCoderOpenAIWSClientDialer_ProxyTransportTLSHandshakeTimeout(t *testing.T) {
	dialer := newDefaultOpenAIWSClientDialer()
	impl, ok := dialer.(*coderOpenAIWSClientDialer)
	require.True(t, ok)

	client, err := impl.proxyHTTPClient("http://127.0.0.1:38080")
	require.NoError(t, err)
	require.NotNil(t, client)

	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	require.NotNil(t, transport)
	require.Equal(t, 10*time.Second, transport.TLSHandshakeTimeout)
}

func TestCoderOpenAIWSClientDialer_FingerprintedClientsAreScopedAndHTTP1Only(t *testing.T) {
	dialer := newDefaultOpenAIWSClientDialer()
	impl, ok := dialer.(*coderOpenAIWSClientDialer)
	require.True(t, ok)
	profile := builtInProfileForAccount(&Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth})
	require.NotNil(t, profile)

	first, err := impl.fingerprintedHTTPClient("", profile, "openai-account:1")
	require.NoError(t, err)
	again, err := impl.fingerprintedHTTPClient("", profile, "openai-account:1")
	require.NoError(t, err)
	otherAccount, err := impl.fingerprintedHTTPClient("", profile, "openai-account:2")
	require.NoError(t, err)
	require.Same(t, first, again)
	require.NotSame(t, first, otherAccount, "TLS ticket stores must not cross account scopes")

	transport, ok := first.Transport.(*http.Transport)
	require.True(t, ok)
	require.False(t, transport.ForceAttemptHTTP2)
	require.True(t, transport.DisableCompression)
	require.NotNil(t, transport.DialTLSContext)
	require.Nil(t, transport.Proxy)
}

func TestReorderCodexWebSocketHandshakeMatchesCapturedWireShape(t *testing.T) {
	// This input follows net/http's special-header + sorted-map shape. The
	// rewriter must produce Tungstenite's captured order and compression offer.
	raw := strings.Join([]string{
		"GET /backend-api/codex/responses HTTP/1.1",
		"Host: chatgpt.com",
		"User-Agent: Codex Desktop/test",
		"Authorization: Bearer redacted",
		"Connection: Upgrade",
		"Sec-WebSocket-Extensions: permessage-deflate",
		"Sec-WebSocket-Key: redacted-key",
		"Sec-WebSocket-Version: 13",
		"Upgrade: websocket",
		"",
		"",
	}, "\r\n")

	reordered, ok := reorderCodexWebSocketHandshake([]byte(raw))
	require.True(t, ok)
	lines := strings.Split(strings.TrimSuffix(string(reordered), "\r\n\r\n"), "\r\n")
	var names []string
	for _, line := range lines[1:] {
		name, _, found := strings.Cut(line, ":")
		require.True(t, found)
		names = append(names, name)
	}
	require.Equal(t, []string{
		"Host",
		"Connection",
		"Upgrade",
		"Sec-WebSocket-Version",
		"Sec-WebSocket-Key",
		"authorization",
		"user-agent",
		"sec-websocket-extensions",
	}, names)
	require.Contains(t, string(reordered), "sec-websocket-extensions: "+codexDesktopWSCompressionOffer+"\r\n")
}

func TestCodexWebSocketHandshakeConnReordersFragmentedWriteAndThenPassesThrough(t *testing.T) {
	rawHeader := strings.Join([]string{
		"GET /backend-api/codex/responses HTTP/1.1",
		"Host: chatgpt.com",
		"User-Agent: Codex Desktop/test",
		"Authorization: Bearer redacted",
		"Connection: Upgrade",
		"Sec-WebSocket-Extensions: permessage-deflate",
		"Sec-WebSocket-Key: redacted-key",
		"Sec-WebSocket-Version: 13",
		"Upgrade: websocket",
		"",
		"",
	}, "\r\n")
	reordered, ok := reorderCodexWebSocketHandshake([]byte(rawHeader))
	require.True(t, ok)

	firstFrame := []byte{0x81, 0x02, 'o', 'k'}
	secondFrame := []byte{0x89, 0x00}
	firstCut := len(rawHeader) / 3
	secondCut := 2 * len(rawHeader) / 3
	fragments := [][]byte{
		[]byte(rawHeader[:firstCut]),
		[]byte(rawHeader[firstCut:secondCut]),
		append([]byte(rawHeader[secondCut:]), firstFrame...),
		secondFrame,
	}

	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
	})
	require.NoError(t, serverConn.SetDeadline(time.Now().Add(5*time.Second)))
	wrapped := &codexWebSocketHandshakeConn{Conn: clientConn}

	writeErr := make(chan error, 1)
	go func() {
		for i, fragment := range fragments {
			written, err := wrapped.Write(fragment)
			if err != nil {
				writeErr <- fmt.Errorf("fragment %d: %w", i, err)
				return
			}
			if written != len(fragment) {
				writeErr <- fmt.Errorf("fragment %d: wrote %d of %d bytes", i, written, len(fragment))
				return
			}
		}
		writeErr <- nil
	}()

	want := make([]byte, 0, len(reordered)+len(firstFrame)+len(secondFrame))
	want = append(want, reordered...)
	want = append(want, firstFrame...)
	want = append(want, secondFrame...)
	got := make([]byte, len(want))
	_, err := io.ReadFull(serverConn, got)
	require.NoError(t, err)
	require.Equal(t, want, got)
	require.NoError(t, <-writeErr)
}

func TestCoderOpenAIWSClientDialer_FingerprintedProxyRouting(t *testing.T) {
	profile := builtInProfileForAccount(&Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth})
	tests := []struct {
		name           string
		proxy          string
		wantCustomTLS  bool
		wantPlainProxy bool
	}{
		{name: "http connect", proxy: "http://127.0.0.1:8080", wantCustomTLS: true},
		{name: "socks5", proxy: "socks5://127.0.0.1:1080", wantCustomTLS: true},
		{name: "https safe fallback", proxy: "https://127.0.0.1:8443", wantPlainProxy: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := newFingerprintedOpenAIWSHTTPClient(tt.proxy, profile, "openai-account:1")
			require.NoError(t, err)
			transport, ok := client.Transport.(*http.Transport)
			require.True(t, ok)
			require.Equal(t, tt.wantCustomTLS, transport.DialTLSContext != nil)
			require.Equal(t, tt.wantPlainProxy, transport.Proxy != nil)
		})
	}
}

func TestCoderOpenAIWSClientConn_DoesNotSupportIdlePingWithoutReader(t *testing.T) {
	require.False(t, (&coderOpenAIWSClientConn{}).SupportsIdlePingWithoutReader())
}
