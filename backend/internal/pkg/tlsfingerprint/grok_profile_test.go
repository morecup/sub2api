package tlsfingerprint

import (
	"context"
	"net"
	"reflect"
	"sort"
	"testing"
	"time"
)

// Reference values produced by an independent model of rustls'
// low_quality_integer_hash (rustls/src/msgs/client_hello.rs) with explicit
// 32-bit masking, so a mistake in the Go port's shift/wrap handling is caught
// rather than confirmed.
func TestRustlsLowQualityIntegerHashMatchesReference(t *testing.T) {
	cases := []struct {
		in   uint32
		want uint32
	}{
		{in: 0, want: 0x6b4ed927},
		{in: 1, want: 0xb48681b6},
		{in: 0xffffffff, want: 0xfe64c182},
		{in: 0x0001002b, want: 0x971c492f},
	}
	for _, tc := range cases {
		if got := rustlsLowQualityIntegerHash(tc.in); got != tc.want {
			t.Fatalf("rustlsLowQualityIntegerHash(%#08x) = %#08x, want %#08x", tc.in, got, tc.want)
		}
	}
}

func TestRustlsExtensionOrderMatchesReference(t *testing.T) {
	cases := []struct {
		seed uint16
		want []uint16
	}{
		{seed: 0x0000, want: []uint16{5, 16, 43, 11, 10, 0, 13, 35, 23, 51, 45}},
		{seed: 0x0001, want: []uint16{16, 10, 45, 13, 0, 43, 23, 5, 35, 11, 51}},
		{seed: 0x1234, want: []uint16{35, 51, 10, 23, 0, 16, 43, 13, 5, 45, 11}},
		{seed: 0xffff, want: []uint16{13, 5, 11, 10, 45, 23, 43, 16, 51, 0, 35}},
	}
	for _, tc := range cases {
		got := rustlsExtensionOrder(grokCLIExtensions, tc.seed)
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("rustlsExtensionOrder(seed=%#04x) = %v, want %v", tc.seed, got, tc.want)
		}
	}
}

func TestRustlsExtensionOrderPreservesTheExtensionSetAndDoesNotMutateInput(t *testing.T) {
	base := append([]uint16(nil), grokCLIExtensions...)
	for seed := 0; seed < 0x10000; seed += 977 {
		got := rustlsExtensionOrder(grokCLIExtensions, uint16(seed))
		if !reflect.DeepEqual(sortedCopy(got), sortedCopy(base)) {
			t.Fatalf("seed %#04x changed the extension set: %v", seed, got)
		}
	}
	if !reflect.DeepEqual(grokCLIExtensions, base) {
		t.Fatalf("rustlsExtensionOrder mutated its input: %v", grokCLIExtensions)
	}
}

// The randomization only reaches the permutations a u16 seed can produce, which
// is far fewer than a uniform shuffle would. Emitting an order outside that set
// would make the client distinguishable from rustls, so the reachable set is
// pinned here and reused to validate real ClientHellos below.
func TestRustlsExtensionOrderReachableSetIsBounded(t *testing.T) {
	reachable := rustlsReachableOrders()
	if len(reachable) != 64845 {
		t.Fatalf("reachable rustls permutations = %d, want 64845", len(reachable))
	}
}

func TestGrokCLIProfileClientHelloMatchesRustlsShape(t *testing.T) {
	hello := captureClientHello(t, GrokCLIProfile(), "cli-chat-proxy.grok.com:443")

	assertEqual(t, "legacy_version", hello.legacyVersion, uint16(0x0303))
	assertEqual(t, "session_id_len", hello.sessionIDLen, 32)
	assertSliceEqual(t, "cipher_suites", hello.cipherSuites, grokCLICipherSuites)
	assertSliceEqual(t, "compression_methods", hello.compressionMethods, []uint8{0})
	assertSliceEqual(t, "supported_groups", hello.supportedGroups, grokCLICurves)
	assertSliceEqual(t, "point_formats", hello.pointFormats, []uint16{0})
	assertSliceEqual(t, "signature_algorithms", hello.signatureAlgorithms, grokCLISignatureAlgorithms)
	assertSliceEqual(t, "alpn", hello.alpn, []string{"h2", "http/1.1"})
	assertSliceEqual(t, "supported_versions", hello.supportedVersions, []uint16{0x0304, 0x0303})
	assertSliceEqual(t, "key_share_groups", hello.keyShareGroups, []uint16{0x001d})
	assertSliceEqual(t, "psk_modes", hello.pskModes, []uint16{1})

	// rustls sends no renegotiation_info extension (it uses the SCSV in the
	// cipher list), no SCT request, and no padding.
	for _, absent := range []uint16{18, 21, 65281} {
		for _, present := range hello.extensions {
			if present == absent {
				t.Fatalf("extension %d must not be offered by a rustls client: %v", absent, hello.extensions)
			}
		}
	}
	assertSliceEqual(t, "extension_set", sortedCopy(hello.extensions), sortedCopy(grokCLIExtensions))

	if _, ok := rustlsReachableOrders()[extensionOrderKey(hello.extensions)]; !ok {
		t.Fatalf("extension order %v is not reachable from any rustls order_seed", hello.extensions)
	}
}

// Consecutive connections must present different extension orders, the way a
// real rustls client does; a single stable JA3 would itself be a signal.
func TestGrokCLIProfileRandomizesExtensionOrderPerConnection(t *testing.T) {
	profile := GrokCLIProfile()
	seen := make(map[string]struct{})
	for i := 0; i < 12; i++ {
		hello := captureClientHello(t, profile, "cli-chat-proxy.grok.com:443")
		seen[extensionOrderKey(hello.extensions)] = struct{}{}
	}
	if len(seen) < 2 {
		t.Fatalf("extension order did not vary across connections: %v", seen)
	}
}

func TestGrokCLIProfileAdvertisesHTTP2(t *testing.T) {
	profile := GrokCLIProfile()
	if !profile.AdvertisesHTTP2() {
		t.Fatal("Grok CLI profile must offer h2 during ALPN")
	}
	if !profile.RequiresGrokHTTP2Transport() {
		t.Fatal("Grok CLI profile must opt into the Grok-specific HTTP/2 transport fork")
	}
	http1 := profile.WithALPNProtocols(ALPNProtocolHTTP1)
	if http1.AdvertisesHTTP2() {
		t.Fatal("the HTTP/1.1 companion must not offer h2")
	}
	if !http1.RequiresGrokHTTP2Transport() {
		t.Fatal("changing ALPN must not clear the explicit Grok fork capability")
	}
	synthetic := &Profile{ALPNProtocols: []string{ALPNProtocolHTTP2, ALPNProtocolHTTP1}}
	if synthetic.RequiresGrokHTTP2Transport() {
		t.Fatal("advertising h2 alone must not opt a profile into the Grok fork")
	}
	assertSliceEqual(t, "http1_alpn", http1.EffectiveALPNProtocols(), []string{"http/1.1"})
	// The companion must keep the same handshake shape, only the ALPN differs.
	assertSliceEqual(t, "http1_ciphers", http1.CipherSuites, profile.CipherSuites)
	if !profile.AdvertisesHTTP2() {
		t.Fatal("WithALPNProtocols must not mutate the receiver")
	}
}

// captureClientHello runs the dialer against a pipe that records the first TLS
// record, so the assertions run on the bytes that would hit the network.
func captureClientHello(t *testing.T, profile *Profile, addr string) *parsedClientHello {
	t.Helper()

	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() { _ = clientConn.Close() })
	t.Cleanup(func() { _ = serverConn.Close() })

	rawCh := make(chan []byte, 1)
	errCh := make(chan error, 1)
	go func() {
		defer func() { _ = serverConn.Close() }()
		raw, err := readFirstTLSRecord(serverConn)
		if err != nil {
			errCh <- err
			return
		}
		rawCh <- raw
	}()

	dialer := NewDialer(profile, func(context.Context, string, string) (net.Conn, error) {
		return clientConn, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if conn, err := dialer.DialTLSContext(ctx, "tcp", addr); err == nil {
		_ = conn.Close()
		t.Fatal("expected the handshake to fail after the local capture")
	}

	var raw []byte
	select {
	case raw = <-rawCh:
	case err := <-errCh:
		t.Fatalf("read ClientHello: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the captured ClientHello")
	}

	hello, err := parseClientHelloRecord(raw)
	if err != nil {
		t.Fatalf("parse ClientHello: %v", err)
	}
	return hello
}

func rustlsReachableOrders() map[string]struct{} {
	orders := make(map[string]struct{})
	for seed := 0; seed <= 0xffff; seed++ {
		orders[extensionOrderKey(rustlsExtensionOrder(grokCLIExtensions, uint16(seed)))] = struct{}{}
	}
	return orders
}

func extensionOrderKey(extensions []uint16) string {
	return joinUint16s(extensions)
}

func sortedCopy(values []uint16) []uint16 {
	out := append([]uint16(nil), values...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
