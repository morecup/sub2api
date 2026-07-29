package tlsfingerprint

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
)

// capturedGrokCLIHellos are real ClientHello messages from the official Grok
// Build CLI 0.2.112 (grok 0.2.112, 9bbd559437) on Windows, captured by pointing
// the CLI's configured base_url at a local listener that records the first TLS
// record. A ClientHello is sent before certificate validation, so no trusted
// certificate is involved.
//
// Records 1 and 2 come from the pooled HTTP/2 sampling client; record 3 is the
// pool-less HTTP/1.1 fallback client (xai-grok-sampler shared_http.rs
// build_http_client_http1), which is why it offers only http/1.1.
var capturedGrokCLIHellos = []struct {
	name           string
	legacyVersion  uint16
	sessionIDLen   int
	compression    []uint8
	cipherSuites   []uint16
	extensionOrder []uint16
	groups         []uint16
	pointFormats   []uint16
	sigAlgs        []uint16
	alpn           []string
	versions       []uint16
	keyShares      []uint16
	pskModes       []uint16
	ja3            string
	ja3Hash        string
}{
	{
		name:           "h2 client connection 1",
		legacyVersion:  0x0303,
		sessionIDLen:   32,
		compression:    []uint8{0},
		cipherSuites:   []uint16{0x1302, 0x1301, 0x1303, 0xc02c, 0xc02b, 0xcca9, 0xc030, 0xc02f, 0xcca8, 0x00ff},
		extensionOrder: []uint16{23, 5, 35, 45, 13, 51, 11, 43, 0, 10, 16},
		groups:         []uint16{0x001d, 0x0017, 0x0018},
		pointFormats:   []uint16{0},
		sigAlgs:        []uint16{0x0503, 0x0403, 0x0807, 0x0806, 0x0805, 0x0804, 0x0601, 0x0501, 0x0401},
		alpn:           []string{"h2", "http/1.1"},
		versions:       []uint16{0x0304, 0x0303},
		keyShares:      []uint16{0x001d},
		pskModes:       []uint16{1},
		ja3:            "771,4866-4865-4867-49196-49195-52393-49200-49199-52392-255,23-5-35-45-13-51-11-43-0-10-16,29-23-24,0",
		ja3Hash:        "9c9c61f5dcb89d0910b2824438a2116d",
	},
	{
		name:           "h2 client connection 2",
		legacyVersion:  0x0303,
		sessionIDLen:   32,
		compression:    []uint8{0},
		cipherSuites:   []uint16{0x1302, 0x1301, 0x1303, 0xc02c, 0xc02b, 0xcca9, 0xc030, 0xc02f, 0xcca8, 0x00ff},
		extensionOrder: []uint16{11, 43, 51, 13, 16, 45, 35, 0, 5, 10, 23},
		groups:         []uint16{0x001d, 0x0017, 0x0018},
		pointFormats:   []uint16{0},
		sigAlgs:        []uint16{0x0503, 0x0403, 0x0807, 0x0806, 0x0805, 0x0804, 0x0601, 0x0501, 0x0401},
		alpn:           []string{"h2", "http/1.1"},
		versions:       []uint16{0x0304, 0x0303},
		keyShares:      []uint16{0x001d},
		pskModes:       []uint16{1},
		ja3:            "771,4866-4865-4867-49196-49195-52393-49200-49199-52392-255,11-43-51-13-16-45-35-0-5-10-23,29-23-24,0",
		ja3Hash:        "b1356dd1ed56d68d67781ba72440bc0a",
	},
	{
		name:           "http/1.1 fallback client",
		legacyVersion:  0x0303,
		sessionIDLen:   32,
		compression:    []uint8{0},
		cipherSuites:   []uint16{0x1302, 0x1301, 0x1303, 0xc02c, 0xc02b, 0xcca9, 0xc030, 0xc02f, 0xcca8, 0x00ff},
		extensionOrder: []uint16{13, 51, 16, 23, 0, 35, 43, 5, 45, 10, 11},
		groups:         []uint16{0x001d, 0x0017, 0x0018},
		pointFormats:   []uint16{0},
		sigAlgs:        []uint16{0x0503, 0x0403, 0x0807, 0x0806, 0x0805, 0x0804, 0x0601, 0x0501, 0x0401},
		alpn:           []string{"http/1.1"},
		versions:       []uint16{0x0304, 0x0303},
		keyShares:      []uint16{0x001d},
		pskModes:       []uint16{1},
		ja3:            "771,4866-4865-4867-49196-49195-52393-49200-49199-52392-255,13-51-16-23-0-35-43-5-45-10-11,29-23-24,0",
		ja3Hash:        "cf89188c591b33cc50aa9e816d7ba2f6",
	},
}

// TestGrokCLIProfileMatchesCapturedClientHello pins the profile against real
// captured traffic rather than against the source reading it was derived from.
func TestGrokCLIProfileMatchesCapturedClientHello(t *testing.T) {
	profile := GrokCLIProfile()
	http1Profile := profile.WithALPNProtocols(ALPNProtocolHTTP1)

	for _, want := range capturedGrokCLIHellos {
		t.Run(want.name, func(t *testing.T) {
			assertSliceEqual(t, "cipher_suites", profile.CipherSuites, want.cipherSuites)
			assertSliceEqual(t, "supported_groups", profile.Curves, want.groups)
			assertSliceEqual(t, "ec_point_formats", profile.PointFormats, want.pointFormats)
			assertSliceEqual(t, "signature_algorithms", profile.SignatureAlgorithms, want.sigAlgs)
			assertSliceEqual(t, "supported_versions", profile.SupportedVersions, want.versions)
			assertSliceEqual(t, "key_share_groups", profile.KeyShareGroups, want.keyShares)
			assertSliceEqual(t, "psk_modes", profile.PSKModes, want.pskModes)

			// The captured order is one draw of the per-connection shuffle, so
			// the profile can only be pinned to the same extension set.
			assertSliceEqual(t, "extension_set", sortedCopy(profile.Extensions), sortedCopy(want.extensionOrder))

			// Every captured order must be reproducible from some order_seed. An
			// order outside that set would mean the shuffle is not rustls'.
			if _, ok := rustlsReachableOrders()[extensionOrderKey(want.extensionOrder)]; !ok {
				t.Fatalf("captured order %v is not reachable from any order_seed", want.extensionOrder)
			}

			// The h2 client offers both protocols; the fallback client drops h2,
			// which is what the HTTP/1.1 companion profile reproduces.
			effective := profile
			if len(want.alpn) == 1 && want.alpn[0] == ALPNProtocolHTTP1 {
				effective = http1Profile
			}
			assertSliceEqual(t, "alpn", effective.EffectiveALPNProtocols(), want.alpn)
		})
	}
}

// TestGrokCLIProfileReproducesCapturedJA3 rebuilds each captured JA3 from the
// profile plus the captured extension order, so a change to any JA3 input
// (version, cipher list, groups, point formats) fails here.
func TestGrokCLIProfileReproducesCapturedJA3(t *testing.T) {
	profile := GrokCLIProfile()
	for _, want := range capturedGrokCLIHellos {
		t.Run(want.name, func(t *testing.T) {
			ja3 := strings.Join([]string{
				"771",
				joinUint16s(profile.CipherSuites),
				joinUint16s(want.extensionOrder),
				joinUint16s(profile.Curves),
				joinUint16s(profile.PointFormats),
			}, ",")
			if ja3 != want.ja3 {
				t.Fatalf("JA3 mismatch:\n got: %s\nwant: %s", ja3, want.ja3)
			}
			if got := md5Hex(ja3); got != want.ja3Hash {
				t.Fatalf("JA3 hash = %s, want %s", got, want.ja3Hash)
			}
		})
	}
}

// TestGrokCLIProfileEmitsCapturedWireShape drives the dialer and compares the
// generated ClientHello against the captured records field by field, including
// the per-extension body lengths.
func TestGrokCLIProfileEmitsCapturedWireShape(t *testing.T) {
	// The capture used https://localhost:9443, so SNI carries "localhost".
	hello := captureClientHello(t, GrokCLIProfile(), "localhost:9443")
	want := capturedGrokCLIHellos[0]

	assertEqual(t, "legacy_version", hello.legacyVersion, want.legacyVersion)
	assertEqual(t, "session_id_len", hello.sessionIDLen, want.sessionIDLen)
	assertSliceEqual(t, "compression_methods", hello.compressionMethods, want.compression)
	assertSliceEqual(t, "cipher_suites", hello.cipherSuites, want.cipherSuites)
	assertSliceEqual(t, "supported_groups", hello.supportedGroups, want.groups)
	assertSliceEqual(t, "point_formats", hello.pointFormats, want.pointFormats)
	assertSliceEqual(t, "signature_algorithms", hello.signatureAlgorithms, want.sigAlgs)
	assertSliceEqual(t, "alpn", hello.alpn, want.alpn)
	assertSliceEqual(t, "supported_versions", hello.supportedVersions, want.versions)
	assertSliceEqual(t, "key_share_groups", hello.keyShareGroups, want.keyShares)
	assertSliceEqual(t, "psk_modes", hello.pskModes, want.pskModes)
	assertSliceEqual(t, "extension_set", sortedCopy(hello.extensions), sortedCopy(want.extensionOrder))

	// Captured per-extension body sizes for the h2 client against
	// https://localhost:9443 (SNI "localhost", 9 bytes).
	wantSizes := map[uint16]int{
		0:  14, // server_name: 2 + 1 + 2 + len("localhost")
		5:  5,  // status_request: OCSP, empty responder/extension lists
		10: 8,  // supported_groups: 2 + 3*2
		11: 2,  // ec_point_formats: 1 + 1
		13: 20, // signature_algorithms: 2 + 9*2
		16: 14, // alpn: 2 + (1+2) + (1+8)
		23: 0,  // extended_master_secret
		35: 0,  // session_ticket request
		43: 5,  // supported_versions: 1 + 2*2
		45: 2,  // psk_key_exchange_modes: 1 + 1
		51: 38, // key_share: 2 + 2 + 2 + 32
	}
	if !reflect.DeepEqual(hello.extensionSizes, wantSizes) {
		t.Fatalf("extension body sizes = %v, want %v", hello.extensionSizes, wantSizes)
	}
}

// Captured HTTP/2 client preamble of the official Grok Build CLI 0.2.112, read
// off a TLS endpoint holding a real Let's Encrypt certificate (the CLI has no
// way to skip certificate validation, so a trusted cert is the only way to see
// past the handshake).
//
// Raw observation, connection from grok-shell/0.2.112 (windows; x86_64):
//
//	SETTINGS  ENABLE_PUSH          (0x2) = 0
//	SETTINGS  INITIAL_WINDOW_SIZE  (0x4) = 2097152
//	SETTINGS  MAX_FRAME_SIZE       (0x5) = 16384
//	SETTINGS  MAX_HEADER_LIST_SIZE (0x6) = 16384
//	WINDOW_UPDATE stream=0 increment=5177345
//	frames: SETTINGS -> WINDOW_UPDATE -> HEADERS -> HEADERS -> DATA...
//	pseudo-header order: :method, :scheme, :authority, :path
//
// Measured twice, once with the CLI in API-key mode against a custom base URL
// and once in OAuth mode with the CLI chat proxy endpoint redirected
// ([endpoints] cli_chat_proxy_base_url). Both produced this exact preamble, so
// the HTTP/2 fingerprint does not depend on the credential type.
const (
	capturedGrokCLIAkamaiFingerprint = "2:0;4:2097152;5:16384;6:16384|5177345|0|m,s,a,p"
)

// TestGrokCLIHTTP2ProfileMatchesCapturedPreamble pins the SETTINGS order and
// values plus the connection WINDOW_UPDATE against the captured preamble.
//
// Header ordering now belongs to the Grok fork tests: this package only owns
// the profile data itself, so the observable request-head block order is locked
// closer to the encoder path.
func TestGrokCLIHTTP2ProfileMatchesCapturedPreamble(t *testing.T) {
	profile := GrokCLIHTTP2Profile()

	settings := make([]string, 0, len(profile.Settings))
	for _, setting := range profile.Settings {
		settings = append(settings, fmt.Sprintf("%d:%d", setting.ID, setting.Value))
	}
	got := fmt.Sprintf("%s|%d", strings.Join(settings, ";"), profile.ConnectionWindowUpdate)
	if got != "2:0;4:2097152;5:16384;6:16384|5177345" {
		t.Fatalf("SETTINGS/WINDOW_UPDATE = %s, want 2:0;4:2097152;5:16384;6:16384|5177345", got)
	}

	capturedPrefix, _, _ := strings.Cut(capturedGrokCLIAkamaiFingerprint, "|0|")
	if got != capturedPrefix {
		t.Fatalf("SETTINGS/WINDOW_UPDATE diverged from the capture:\n got: %s\nwant: %s", got, capturedPrefix)
	}
}

// TestCapturedGrokCLIAcceptEncoding documents the Accept-Encoding the official
// client sends, which the transport layer reproduces for the CLI proxy host.
// The value follows from the CLI building reqwest with only the gzip, brotli and
// deflate features; the capture confirms it.
func TestCapturedGrokCLIAcceptEncoding(t *testing.T) {
	const captured = "gzip, br, deflate"
	if xai.CLIAcceptEncoding != captured {
		t.Fatalf("xai.CLIAcceptEncoding = %q, want the captured %q", xai.CLIAcceptEncoding, captured)
	}
}
