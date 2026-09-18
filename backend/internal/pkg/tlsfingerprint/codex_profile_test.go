package tlsfingerprint

import (
	"context"
	cryptotls "crypto/tls"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	utls "github.com/refraction-networking/utls"
)

func TestCodexDesktopProfileClientHelloMatchesAWSLCRustlsShape(t *testing.T) {
	profile := CodexDesktopProfile()
	hello := captureClientHello(t, profile, "chatgpt.com:443")

	assertEqual(t, "legacy_version", hello.legacyVersion, uint16(0x0303))
	assertEqual(t, "session_id_len", hello.sessionIDLen, 32)
	assertSliceEqual(t, "cipher_suites", hello.cipherSuites, codexDesktopCipherSuites)
	// Independent wire values from the 0.155 loopback binary capture.
	assertSliceEqual(t, "supported_groups", hello.supportedGroups, []uint16{0x11ec, 0x001d, 0x0017, 0x0018})
	assertSliceEqual(t, "point_formats", hello.pointFormats, []uint16{0})
	assertSliceEqual(t, "signature_algorithms", hello.signatureAlgorithms, codexDesktopSignatureAlgorithms)
	assertSliceEqual(t, "alpn", hello.alpn, []string{"h2", "http/1.1"})
	assertSliceEqual(t, "supported_versions", hello.supportedVersions, []uint16{0x0304, 0x0303})
	assertSliceEqual(t, "key_share_groups", hello.keyShareGroups, []uint16{0x11ec, 0x001d})
	assertSliceEqual(t, "psk_modes", hello.pskModes, []uint16{1})
	assertSliceEqual(t, "extension_set", sortedCopy(hello.extensions), sortedCopy(codexDesktopExtensions))

	if _, ok := rustlsReachableOrders()[extensionOrderKey(hello.extensions)]; !ok {
		t.Fatalf("extension order %v is not reachable from rustls' u16 order seed", hello.extensions)
	}
	for _, absent := range []uint16{18, 21, 65281} {
		for _, present := range hello.extensions {
			if present == absent {
				t.Fatalf("extension %d must not appear in the Codex rustls hello: %v", absent, hello.extensions)
			}
		}
	}
}

func TestCodexDesktopProfileTransportAndHTTP2Shape(t *testing.T) {
	profile := CodexDesktopProfile()
	if !profile.AdvertisesHTTP2() {
		t.Fatal("Codex Desktop must offer h2")
	}
	if !profile.RequiresOrderedHTTP2Transport() {
		t.Fatal("Codex Desktop must use the header-order-capable HTTP/2 transport")
	}
	if !profile.ResumeSessions {
		t.Fatal("Codex rustls must keep TLS session resumption enabled")
	}

	h2 := profile.HTTP2
	wantSettings := []HTTP2Setting{
		{ID: HTTP2SettingEnablePush, Value: 0},
		{ID: HTTP2SettingInitialWindowSize, Value: 2 << 20},
		{ID: HTTP2SettingMaxFrameSize, Value: 16 << 10},
		{ID: HTTP2SettingMaxHeaderListSize, Value: 16 << 10},
	}
	if !reflect.DeepEqual(h2.Settings, wantSettings) {
		t.Fatalf("Codex HTTP/2 settings = %v, want %v", h2.Settings, wantSettings)
	}
	if h2.ConnectionWindowUpdate != 5<<20-http2InitialConnWindow {
		t.Fatalf("Codex connection WINDOW_UPDATE = %d", h2.ConnectionWindowUpdate)
	}
	if h2.PingInterval != 0 || h2.PingTimeout != 0 {
		t.Fatalf("Codex must keep reqwest defaults without synthetic pings: interval=%s timeout=%s", h2.PingInterval, h2.PingTimeout)
	}
	assertSliceEqual(t, "pseudo_header_order", h2.PseudoHeaderOrder, codexDesktopHTTP2PseudoHeaderOrder)
	assertSliceEqual(t, "regular_header_order", h2.RegularHeaderOrder, codexDesktopHTTP2RegularHeaderOrder)

	if !profile.DisableAutomaticCompression {
		t.Fatal("Codex HTTP must not acquire Go's synthetic Accept-Encoding header")
	}

	websocket := profile.WithoutALPN()
	assertSliceEqual(t, "websocket_alpn", websocket.EffectiveALPNProtocols(), []string(nil))
	if websocket.AdvertisesHTTP2() {
		t.Fatal("the WebSocket companion must not advertise h2")
	}
	wsHello := captureClientHello(t, websocket, "chatgpt.com:443")
	if len(wsHello.alpn) != 0 {
		t.Fatalf("Codex WebSocket ALPN = %v, want no ALPN extension", wsHello.alpn)
	}
	for _, extension := range wsHello.extensions {
		if extension == 16 {
			t.Fatalf("Codex WebSocket must omit ALPN extension: %v", wsHello.extensions)
		}
	}
}

func TestCodexDesktopProfileCompletesX25519MLKEM768Handshake(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.TLS = &cryptotls.Config{
		MinVersion:       cryptotls.VersionTLS13,
		MaxVersion:       cryptotls.VersionTLS13,
		CurvePreferences: []cryptotls.CurveID{cryptotls.X25519MLKEM768},
	}
	server.StartTLS()
	defer server.Close()

	addr := strings.TrimPrefix(server.URL, "https://")
	rawConn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial local TLS server: %v", err)
	}
	defer rawConn.Close()

	profile := CodexDesktopProfile().WithALPNProtocols(ALPNProtocolHTTP1)
	client := utls.UClient(rawConn, &utls.Config{
		ServerName:         "localhost",
		InsecureSkipVerify: true, // local ephemeral httptest certificate
	}, utls.HelloCustom)
	if err := client.ApplyPreset(buildClientHelloSpecFromProfile(profile)); err != nil {
		t.Fatalf("apply Codex TLS preset: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.HandshakeContext(ctx); err != nil {
		t.Fatalf("negotiate X25519MLKEM768: %v", err)
	}
}
