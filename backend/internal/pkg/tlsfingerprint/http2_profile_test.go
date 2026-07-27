package tlsfingerprint

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"net"
	"net/http"
	"reflect"
	"testing"

	"golang.org/x/net/http2"
)

// goStyleSettings is the SETTINGS list golang.org/x/net/http2 emits, in its own
// fixed order (ENABLE_PUSH, INITIAL_WINDOW_SIZE, MAX_FRAME_SIZE,
// MAX_HEADER_LIST_SIZE), with the values the Grok profile's knobs produce.
var goStyleSettings = []HTTP2Setting{
	{ID: HTTP2SettingEnablePush, Value: 0},
	{ID: HTTP2SettingInitialWindowSize, Value: grokCLIH2InitialWindowSize},
	{ID: HTTP2SettingMaxFrameSize, Value: grokCLIH2MaxFrameSize},
	{ID: HTTP2SettingMaxHeaderListSize, Value: grokCLIH2MaxHeaderListSize},
}

func TestGrokCLIHTTP2ProfileTargetsHyperDefaults(t *testing.T) {
	profile := GrokCLIHTTP2Profile()

	// hyper 1.8.1 client defaults (src/proto/h2/client.rs).
	assertSliceEqual(t, "settings", profile.Settings, goStyleSettings)
	assertEqual(t, "initial_window_size", grokCLIH2InitialWindowSize, uint32(2097152))
	assertEqual(t, "max_frame_size", grokCLIH2MaxFrameSize, uint32(16384))
	assertEqual(t, "max_header_list_size", grokCLIH2MaxHeaderListSize, uint32(16384))
	// DEFAULT_CONN_WINDOW (5MiB) minus the protocol's initial 64KiB window.
	assertEqual(t, "connection_window_update", profile.ConnectionWindowUpdate, uint32(5177345))
}

func TestHTTP2PrefaceRewriterReordersSettingsAndKeepsValues(t *testing.T) {
	// A profile that wants a different order than the local stack emits.
	profile := &HTTP2Profile{
		Name: "test",
		Settings: []HTTP2Setting{
			{ID: HTTP2SettingMaxHeaderListSize, Value: 16384},
			{ID: HTTP2SettingEnablePush, Value: 0},
			{ID: HTTP2SettingInitialWindowSize, Value: 2097152},
			{ID: HTTP2SettingMaxFrameSize, Value: 16384},
		},
		ConnectionWindowUpdate: 5177345,
	}

	written := writeThroughRewriter(t, profile, localPreamble(goStyleSettings, 5177345))

	settings, windowUpdate := parsePreamble(t, written)
	assertSliceEqual(t, "settings", settings, profile.Settings)
	assertEqual(t, "window_update", windowUpdate, uint32(5177345))
}

func TestHTTP2PrefaceRewriterNeverWidensLocalLimits(t *testing.T) {
	local := []HTTP2Setting{
		{ID: HTTP2SettingEnablePush, Value: 0},
		{ID: HTTP2SettingInitialWindowSize, Value: 1 << 20},
		{ID: HTTP2SettingMaxFrameSize, Value: 16384},
	}
	profile := &HTTP2Profile{
		Name: "test",
		Settings: []HTTP2Setting{
			{ID: HTTP2SettingInitialWindowSize, Value: 4 << 20}, // wider than local
			{ID: HTTP2SettingEnablePush, Value: 0},
			{ID: HTTP2SettingMaxFrameSize, Value: 16384},
		},
		ConnectionWindowUpdate: 1 << 30, // wider than local
	}

	written := writeThroughRewriter(t, profile, localPreamble(local, 5177345))

	settings, windowUpdate := parsePreamble(t, written)
	assertSliceEqual(t, "settings", settings, []HTTP2Setting{
		{ID: HTTP2SettingInitialWindowSize, Value: 1 << 20},
		{ID: HTTP2SettingEnablePush, Value: 0},
		{ID: HTTP2SettingMaxFrameSize, Value: 16384},
	})
	assertEqual(t, "window_update", windowUpdate, uint32(5177345))
}

func TestHTTP2PrefaceRewriterSkipsSettingsTheLocalStackDidNotSend(t *testing.T) {
	local := []HTTP2Setting{
		{ID: HTTP2SettingEnablePush, Value: 0},
		{ID: HTTP2SettingInitialWindowSize, Value: 2097152},
	}
	profile := &HTTP2Profile{
		Name: "test",
		Settings: []HTTP2Setting{
			{ID: HTTP2SettingHeaderTableSize, Value: 65536}, // not advertised locally
			{ID: HTTP2SettingEnablePush, Value: 0},
			{ID: HTTP2SettingInitialWindowSize, Value: 2097152},
		},
	}

	written := writeThroughRewriter(t, profile, localPreamble(local, 0))

	settings, _ := parsePreamble(t, written)
	assertSliceEqual(t, "settings", settings, local)
}

func TestHTTP2PrefaceRewriterPreservesUnprofiledLocalSettings(t *testing.T) {
	local := []HTTP2Setting{
		{ID: HTTP2SettingEnablePush, Value: 0},
		{ID: HTTP2SettingInitialWindowSize, Value: 2097152},
		{ID: HTTP2SettingMaxHeaderListSize, Value: 10 << 20},
	}
	profile := &HTTP2Profile{
		Name: "test",
		Settings: []HTTP2Setting{
			{ID: HTTP2SettingInitialWindowSize, Value: 2097152},
			{ID: HTTP2SettingEnablePush, Value: 0},
		},
	}

	written := writeThroughRewriter(t, profile, localPreamble(local, 0))

	settings, _ := parsePreamble(t, written)
	assertSliceEqual(t, "settings", settings, []HTTP2Setting{
		{ID: HTTP2SettingInitialWindowSize, Value: 2097152},
		{ID: HTTP2SettingEnablePush, Value: 0},
		{ID: HTTP2SettingMaxHeaderListSize, Value: 10 << 20},
	})
}

// The local stack flushes the preamble in one write, but the rewriter must also
// survive an arbitrarily chopped stream.
func TestHTTP2PrefaceRewriterHandlesSplitWrites(t *testing.T) {
	profile := GrokCLIHTTP2Profile()
	preamble := localPreamble(goStyleSettings, 5177345)
	trailing := []byte("trailing-frame-bytes")

	for _, chunk := range []int{1, 3, 9, 17, 24, 25, 33} {
		sink := &recordingConn{}
		conn := profile.WrapConn(sink)
		payload := append(append([]byte(nil), preamble...), trailing...)
		for offset := 0; offset < len(payload); offset += chunk {
			end := min(offset+chunk, len(payload))
			if _, err := conn.Write(payload[offset:end]); err != nil {
				t.Fatalf("chunk=%d write: %v", chunk, err)
			}
		}
		settings, windowUpdate := parsePreamble(t, sink.written)
		assertSliceEqual(t, "settings", settings, profile.Settings)
		assertEqual(t, "window_update", windowUpdate, uint32(5177345))
		if got := string(sink.written[len(sink.written)-len(trailing):]); got != string(trailing) {
			t.Fatalf("chunk=%d trailing bytes = %q, want %q", chunk, got, trailing)
		}
	}
}

func TestHTTP2PrefaceRewriterPassesThroughNonHTTP2Streams(t *testing.T) {
	sink := &recordingConn{}
	conn := GrokCLIHTTP2Profile().WrapConn(sink)
	payload := []byte("GET / HTTP/1.1\r\nHost: example.com\r\n\r\n")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !reflect.DeepEqual(sink.written, payload) {
		t.Fatalf("stream was modified: %q", sink.written)
	}
}

func TestHTTP2ProfileConfigureTransportsMapsKnobs(t *testing.T) {
	profile := GrokCLIHTTP2Profile()
	h1 := &http.Transport{}
	h2 := &http2.Transport{}
	profile.ConfigureTransports(h1, h2)

	if h1.HTTP2 == nil {
		t.Fatal("HTTP2 config was not attached to the carrier transport")
	}
	assertEqual(t, "MaxReceiveBufferPerStream", h1.HTTP2.MaxReceiveBufferPerStream, int(grokCLIH2InitialWindowSize))
	assertEqual(t, "MaxReceiveBufferPerConnection", h1.HTTP2.MaxReceiveBufferPerConnection, int(grokCLIH2ConnectionWindowUpdate))
	assertEqual(t, "MaxReadFrameSize", h1.HTTP2.MaxReadFrameSize, int(grokCLIH2MaxFrameSize))
	assertEqual(t, "h2.MaxReadFrameSize", h2.MaxReadFrameSize, grokCLIH2MaxFrameSize)
	assertEqual(t, "h2.MaxHeaderListSize", h2.MaxHeaderListSize, grokCLIH2MaxHeaderListSize)
	assertEqual(t, "h2.ReadIdleTimeout", h2.ReadIdleTimeout, grokCLIH2PingInterval)
	assertEqual(t, "h2.PingTimeout", h2.PingTimeout, grokCLIH2PingTimeout)
}

// A profile that omits MAX_HEADER_LIST_SIZE must make net/http2 omit it too,
// which it only does for the "infinite" sentinel.
func TestHTTP2ProfileConfigureTransportsOmitsMaxHeaderListSize(t *testing.T) {
	profile := &HTTP2Profile{Settings: []HTTP2Setting{{ID: HTTP2SettingEnablePush, Value: 0}}}
	h2 := &http2.Transport{}
	profile.ConfigureTransports(nil, h2)
	assertEqual(t, "h2.MaxHeaderListSize", h2.MaxHeaderListSize, uint32(0xffffffff))
}

func TestRequireALPNRejectsMismatchAndWrapsHTTP2Connections(t *testing.T) {
	profile := GrokCLIProfile()

	dial := RequireALPN(ALPNProtocolHTTP2, profile, func(context.Context, string, string) (net.Conn, error) {
		return &alpnConn{recordingConn: &recordingConn{}, proto: "http/1.1"}, nil
	})
	if _, err := dial(context.Background(), "tcp", "example.com:443"); !errors.Is(err, ErrALPNMismatch) {
		t.Fatalf("err = %v, want ErrALPNMismatch", err)
	}

	dial = RequireALPN(ALPNProtocolHTTP2, profile, func(context.Context, string, string) (net.Conn, error) {
		return &alpnConn{recordingConn: &recordingConn{}, proto: "h2"}, nil
	})
	conn, err := dial(context.Background(), "tcp", "example.com:443")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if _, ok := conn.(*http2PrefaceConn); !ok {
		t.Fatalf("conn type = %T, want *http2PrefaceConn", conn)
	}
}

// localPreamble builds the bytes golang.org/x/net/http2 writes when opening a
// connection: the client preface, one SETTINGS frame, then the connection-level
// WINDOW_UPDATE.
func localPreamble(settings []HTTP2Setting, windowUpdate uint32) []byte {
	out := []byte(http2ClientPreface)
	out = append(out, encodeHTTP2SettingsFrame(settings)...)
	if windowUpdate > 0 {
		out = append(out, encodeHTTP2WindowUpdateFrame(windowUpdate)...)
	}
	return out
}

func writeThroughRewriter(t *testing.T, profile *HTTP2Profile, payload []byte) []byte {
	t.Helper()
	sink := &recordingConn{}
	conn := profile.WrapConn(sink)
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	return sink.written
}

func parsePreamble(t *testing.T, raw []byte) ([]HTTP2Setting, uint32) {
	t.Helper()
	if len(raw) < len(http2ClientPreface) || string(raw[:len(http2ClientPreface)]) != http2ClientPreface {
		t.Fatalf("missing client preface: %q", raw)
	}
	rest := raw[len(http2ClientPreface):]

	var settings []HTTP2Setting
	windowUpdate := uint32(0)
	for len(rest) >= http2FrameHeaderLen {
		frameLen := int(rest[0])<<16 | int(rest[1])<<8 | int(rest[2])
		frameType := rest[3]
		if len(rest) < http2FrameHeaderLen+frameLen {
			break
		}
		payload := rest[http2FrameHeaderLen : http2FrameHeaderLen+frameLen]
		switch frameType {
		case http2FrameTypeSettings:
			settings = decodeHTTP2Settings(payload)
		case http2FrameTypeWindowUpdate:
			windowUpdate = binary.BigEndian.Uint32(payload) & 0x7fffffff
		default:
			return settings, windowUpdate
		}
		rest = rest[http2FrameHeaderLen+frameLen:]
	}
	return settings, windowUpdate
}

type recordingConn struct {
	net.Conn
	written []byte
}

func (c *recordingConn) Write(b []byte) (int, error) {
	c.written = append(c.written, b...)
	return len(b), nil
}

func (c *recordingConn) Close() error { return nil }

type alpnConn struct {
	*recordingConn
	proto string
}

func (c *alpnConn) ConnectionState() tls.ConnectionState {
	return tls.ConnectionState{NegotiatedProtocol: c.proto}
}
