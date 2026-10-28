//go:build !(go1.27 && !http2legacy)

package repository

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

func TestCodexOctober28ProductionHTTP2HeaderWireOrder(t *testing.T) {
	raw, err := os.ReadFile("../../../docs/codex-desktop-capture/2026-10-28_0.165.0-alpha.1.2/request-contract.json")
	require.NoError(t, err)
	var capture struct {
		HTTP struct {
			Names []string `json:"names"`
		}
	}
	require.NoError(t, json.Unmarshal(raw, &capture))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	type result struct {
		names []string
		err   error
	}
	results := make(chan result, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			results <- result{err: err}
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		preface := make([]byte, len(http2.ClientPreface))
		if _, err = io.ReadFull(conn, preface); err != nil {
			results <- result{err: err}
			return
		}
		framer := http2.NewFramer(conn, conn)
		framer.ReadMetaHeaders = hpack.NewDecoder(4096, nil)
		if err = framer.WriteSettings(); err != nil {
			results <- result{err: err}
			return
		}
		for {
			frame, err := framer.ReadFrame()
			if err != nil {
				results <- result{err: err}
				return
			}
			if headers, ok := frame.(*http2.MetaHeadersFrame); ok {
				var names []string
				for _, field := range headers.Fields {
					names = append(names, field.Name)
				}
				results <- result{names: names}
				return
			}
		}
	}()
	transport, err := newFingerprintHTTP2Transport(poolSettings{}, tlsfingerprint.CodexDesktopProfile(), func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	})
	require.NoError(t, err)
	enableHTTP2TransportAllowHTTP(t, transport)
	t.Cleanup(func() { closeIdleConnections(transport) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+listener.Addr().String()+"/backend-api/codex/responses", strings.NewReader("{}"))
	require.NoError(t, err)
	for _, name := range capture.HTTP.Names {
		if name != "content-length" {
			req.Header.Set(name, "fixture")
		}
	}
	if resp, err := transport.RoundTrip(req); err == nil {
		_ = resp.Body.Close()
	}
	select {
	case actual := <-results:
		require.NoError(t, actual.err)
		want := append([]string{":method", ":scheme", ":authority", ":path"}, capture.HTTP.Names...)
		require.Equal(t, want, actual.names)
	case <-ctx.Done():
		t.Fatal("no HTTP/2 headers observed")
	}
}
