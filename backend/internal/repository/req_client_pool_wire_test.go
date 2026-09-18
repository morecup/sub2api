package repository

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

type privacyWireSnapshot struct {
	CipherSuites     []uint16
	Curves           []uint16
	SignatureSchemes []uint16
	Versions         []uint16
	ALPN             []string
	Settings         []http2.Setting
	ConnectionWindow uint32
	PriorityFrames   []http2.PriorityParam
	HeaderPriority   http2.PriorityParam
	Headers          []hpack.HeaderField
}

// Use a loopback TLS peer to check the factory's actual browser handshake,
// HTTP/2 settings and header order. WebView callers inherit this transport even
// when they replace its generic browser headers with the captured app profile.
func TestCreatePrivacyReqClientPreservesBrowserWire(t *testing.T) {
	certificateServer := httptest.NewTLSServer(nil)
	tlsConfig := certificateServer.TLS.Clone()
	certificateServer.Close()
	tlsConfig.NextProtos = []string{"h2"}
	snapshot := privacyWireSnapshot{}
	tlsConfig.GetConfigForClient = func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		snapshot.CipherSuites = privacyWireWithoutGREASE(hello.CipherSuites)
		for _, curve := range hello.SupportedCurves {
			snapshot.Curves = append(snapshot.Curves, uint16(curve))
		}
		snapshot.Curves = privacyWireWithoutGREASE(snapshot.Curves)
		for _, scheme := range hello.SignatureSchemes {
			snapshot.SignatureSchemes = append(snapshot.SignatureSchemes, uint16(scheme))
		}
		snapshot.Versions = privacyWireWithoutGREASE(hello.SupportedVersions)
		snapshot.ALPN = append([]string(nil), hello.SupportedProtos...)
		return nil, nil
	}
	listener, err := tls.Listen("tcp", "127.0.0.1:0", tlsConfig)
	require.NoError(t, err)
	defer listener.Close()
	completed := make(chan error, 1)
	go func() { completed <- readPrivacyWire(listener, &snapshot) }()

	client, err := CreatePrivacyReqClient("")
	require.NoError(t, err)
	defer client.CloseIdleConnections()
	originalTLS := client.GetTLSClientConfig()
	defer client.SetTLSClientConfig(originalTLS)
	client.SetTLSClientConfig(&tls.Config{InsecureSkipVerify: true}) // Only this loopback test peer.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	response, requestErr := client.R().SetContext(ctx).Get("https://" + listener.Addr().String() + "/audit")
	select {
	case serverErr := <-completed:
		require.NoError(t, serverErr)
	case <-ctx.Done():
		t.Fatal("loopback HTTP/2 peer did not finish")
	}
	require.NoError(t, requestErr)
	require.Equal(t, 200, response.StatusCode)
	if output := os.Getenv("OAUTH_PRIVACY_WIRE_OUTPUT"); output != "" {
		raw, err := json.MarshalIndent(snapshot, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(output, raw, 0600))
	}

	require.Equal(t, []uint16{tls.TLS_AES_128_GCM_SHA256, tls.TLS_AES_256_GCM_SHA384, tls.TLS_CHACHA20_POLY1305_SHA256,
		tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256, tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
		tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384, tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
		tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256, tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
		tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA, tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
		tls.TLS_RSA_WITH_AES_128_GCM_SHA256, tls.TLS_RSA_WITH_AES_256_GCM_SHA384,
		tls.TLS_RSA_WITH_AES_128_CBC_SHA, tls.TLS_RSA_WITH_AES_256_CBC_SHA}, snapshot.CipherSuites)
	require.Equal(t, []uint16{29, 23, 24}, snapshot.Curves)
	require.Equal(t, []uint16{0x0403, 0x0804, 0x0401, 0x0503, 0x0805, 0x0501, 0x0806, 0x0601}, snapshot.SignatureSchemes)
	require.Equal(t, []uint16{tls.VersionTLS13, tls.VersionTLS12}, snapshot.Versions)
	require.Equal(t, []string{"h2", "http/1.1"}, snapshot.ALPN)
	require.Equal(t, []http2.Setting{{ID: http2.SettingHeaderTableSize, Val: 65536},
		{ID: http2.SettingEnablePush, Val: 0}, {ID: http2.SettingMaxConcurrentStreams, Val: 1000},
		{ID: http2.SettingInitialWindowSize, Val: 6291456}, {ID: http2.SettingMaxHeaderListSize, Val: 262144}}, snapshot.Settings)
	require.Equal(t, uint32(15663105), snapshot.ConnectionWindow)
	require.Empty(t, snapshot.PriorityFrames)
	require.Equal(t, http2.PriorityParam{StreamDep: 0, Exclusive: true, Weight: 255}, snapshot.HeaderPriority)
	var names []string
	headers := map[string]string{}
	for _, field := range snapshot.Headers {
		names = append(names, field.Name)
		headers[field.Name] = field.Value
	}
	require.GreaterOrEqual(t, len(names), 4)
	require.Equal(t, []string{":method", ":authority", ":scheme", ":path"}, names[:4])
	require.Contains(t, headers["user-agent"], "Chrome/120.0.0.0")
	require.NotContains(t, headers["user-agent"], "Firefox/")
	require.Equal(t, "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7", headers["accept"])
}

func privacyWireWithoutGREASE(values []uint16) []uint16 {
	var result []uint16
	for _, value := range values {
		if value&0x0f0f == 0x0a0a && byte(value>>8) == byte(value) {
			continue
		}
		result = append(result, value)
	}
	return result
}

func readPrivacyWire(listener net.Listener, snapshot *privacyWireSnapshot) error {
	conn, err := listener.Accept()
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	preface := make([]byte, len(http2.ClientPreface))
	if _, err := io.ReadFull(conn, preface); err != nil {
		return err
	}
	if string(preface) != http2.ClientPreface {
		return fmt.Errorf("unexpected HTTP/2 preface")
	}
	framer := http2.NewFramer(conn, conn)
	if err := framer.WriteSettings(); err != nil {
		return err
	}
	for {
		frame, err := framer.ReadFrame()
		if err != nil {
			return err
		}
		switch frame := frame.(type) {
		case *http2.SettingsFrame:
			if !frame.IsAck() {
				if err := frame.ForeachSetting(func(s http2.Setting) error { snapshot.Settings = append(snapshot.Settings, s); return nil }); err != nil {
					return err
				}
				if err := framer.WriteSettingsAck(); err != nil {
					return err
				}
			}
		case *http2.WindowUpdateFrame:
			if frame.StreamID == 0 {
				snapshot.ConnectionWindow = frame.Increment
			}
		case *http2.PriorityFrame:
			snapshot.PriorityFrames = append(snapshot.PriorityFrames, frame.PriorityParam)
		case *http2.HeadersFrame:
			snapshot.HeaderPriority = frame.Priority
			if !frame.HeadersEnded() {
				return fmt.Errorf("unexpected continuation in small fixture request")
			}
			decoder := hpack.NewDecoder(65536, func(field hpack.HeaderField) {
				if field.Name == ":authority" {
					field.Value = "<loopback>"
				}
				snapshot.Headers = append(snapshot.Headers, field)
			})
			if _, err := decoder.Write(frame.HeaderBlockFragment()); err != nil {
				return err
			}
			var block bytes.Buffer
			encoder := hpack.NewEncoder(&block)
			if err := encoder.WriteField(hpack.HeaderField{Name: ":status", Value: "200"}); err != nil {
				return err
			}
			if err := encoder.WriteField(hpack.HeaderField{Name: "content-length", Value: "0"}); err != nil {
				return err
			}
			return framer.WriteHeaders(http2.HeadersFrameParam{StreamID: frame.StreamID, BlockFragment: block.Bytes(), EndStream: true, EndHeaders: true})
		case *http2.GoAwayFrame:
			return fmt.Errorf("peer closed before sending request headers")
		}
	}
}
