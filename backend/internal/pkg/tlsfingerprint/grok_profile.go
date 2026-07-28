package tlsfingerprint

import (
	"crypto/rand"
	"encoding/binary"
	"sort"
	"time"
)

// GrokCLIProfileName identifies the TLS/HTTP2 profile of xAI's official Grok
// Build CLI.
//
// Provenance: github.com/xai-org/grok-build. The CLI is Rust; every xAI request
// goes through `reqwest` (crates/codegen/xai-grok-http, and
// crates/codegen/xai-grok-sampler/src/shared_http.rs for chat traffic) built
// with `default-features = false` and features
// ["rustls-tls", "stream", "json", "multipart", "http2", "blocking", "socks"].
// Pinned dependency versions from its Cargo.lock: rustls 0.23.37 (the
// `rustls-tls` feature selects the *ring* provider and webpki-roots),
// hyper 1.8.1, h2 0.4.15.
const GrokCLIProfileName = "Grok Build CLI (rustls 0.23 / reqwest 0.12)"

// GrokCLIHTTP2ProfileName identifies the HTTP/2 half of the same client.
const GrokCLIHTTP2ProfileName = "Grok Build CLI (hyper 1.8 / h2 0.4)"

// grokCLICipherSuites is rustls' *ring* provider DEFAULT_CIPHER_SUITES
// (crypto/ring/mod.rs ALL_CIPHER_SUITES) in order, plus the empty
// renegotiation-info SCSV that rustls appends whenever TLS 1.2 is offered
// (client/hs.rs). rustls does not send the renegotiation_info extension, so the
// SCSV is the only place secure renegotiation shows up.
var grokCLICipherSuites = []uint16{
	// TLS 1.3 suites
	0x1302, // TLS13_AES_256_GCM_SHA384
	0x1301, // TLS13_AES_128_GCM_SHA256
	0x1303, // TLS13_CHACHA20_POLY1305_SHA256

	// TLS 1.2 suites
	0xc02c, // TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384
	0xc02b, // TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256
	0xcca9, // TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256
	0xc030, // TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384
	0xc02f, // TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256
	0xcca8, // TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256

	0x00ff, // TLS_EMPTY_RENEGOTIATION_INFO_SCSV
}

// grokCLICurves is rustls' *ring* provider ALL_KX_GROUPS (crypto/ring/mod.rs).
// The ring provider has no post-quantum group, so there is no X25519MLKEM768
// hybrid share here.
var grokCLICurves = []uint16{
	0x001d, // x25519
	0x0017, // secp256r1
	0x0018, // secp384r1
}

// grokCLISignatureAlgorithms is the order returned by
// WebPkiServerVerifier::supported_verify_schemes(), i.e. the `mapping` table of
// the *ring* provider's WebPkiSupportedAlgorithms (crypto/ring/mod.rs). The
// ECDSA-before-Ed25519-before-RSA shape is characteristic of rustls and differs
// from both OpenSSL and Go.
var grokCLISignatureAlgorithms = []uint16{
	0x0503, // ecdsa_secp384r1_sha384
	0x0403, // ecdsa_secp256r1_sha256
	0x0807, // ed25519
	0x0806, // rsa_pss_rsae_sha512
	0x0805, // rsa_pss_rsae_sha384
	0x0804, // rsa_pss_rsae_sha256
	0x0601, // rsa_pkcs1_sha512
	0x0501, // rsa_pkcs1_sha384
	0x0401, // rsa_pkcs1_sha256
}

// grokCLIExtensions is the extension set rustls 0.23.37 emits for a fresh
// (non-resumed, non-ECH) connection made by reqwest, listed in the declaration
// order of rustls' ClientExtensions struct — which is the order
// `collect_used()` returns and therefore the base order the per-connection
// shuffle permutes.
//
// Notably absent versus a Node.js or browser hello: renegotiation_info (sent as
// an SCSV instead), signed_certificate_timestamp, padding, and
// compress_certificate (rustls is built here without its brotli/zlib features,
// so it offers no certificate compression).
var grokCLIExtensions = []uint16{
	0,  // server_name
	5,  // status_request
	10, // supported_groups
	11, // ec_point_formats
	13, // signature_algorithms
	16, // application_layer_protocol_negotiation
	23, // extended_master_secret
	35, // session_ticket
	43, // supported_versions
	45, // psk_key_exchange_modes
	51, // key_share
}

// Grok Build CLI HTTP/2 preamble, derived from hyper 1.8.1's client defaults
// (src/proto/h2/client.rs) which reqwest leaves untouched apart from the
// keepalive knobs set in xai-grok-sampler's shared_http.rs.
const (
	// DEFAULT_STREAM_WINDOW = 1024 * 1024 * 2
	grokCLIH2InitialWindowSize uint32 = 2 << 20
	// DEFAULT_MAX_FRAME_SIZE = 1024 * 16
	grokCLIH2MaxFrameSize uint32 = 16 << 10
	// DEFAULT_MAX_HEADER_LIST_SIZE = 1024 * 16
	grokCLIH2MaxHeaderListSize uint32 = 16 << 10
	// DEFAULT_CONN_WINDOW = 1024 * 1024 * 5; h2 turns the target connection
	// window into a WINDOW_UPDATE increment over the protocol's initial 64KiB.
	grokCLIH2ConnectionWindowUpdate uint32 = 5<<20 - http2InitialConnWindow
	// http2_keep_alive_interval / http2_keep_alive_timeout from
	// xai-grok-sampler/src/shared_http.rs (keepalive while idle).
	grokCLIH2PingInterval = 15 * time.Second
	grokCLIH2PingTimeout  = 5 * time.Second
)

// GrokCLIProfile returns the TLS and HTTP/2 fingerprint of the official Grok
// Build CLI.
func GrokCLIProfile() *Profile {
	return &Profile{
		Name:                GrokCLIProfileName,
		CipherSuites:        grokCLICipherSuites,
		Curves:              grokCLICurves,
		PointFormats:        []uint16{0}, // uncompressed
		SignatureAlgorithms: grokCLISignatureAlgorithms,
		// reqwest offers both protocols unless the caller forces one
		// (HttpVersionPref::All).
		ALPNProtocols: []string{ALPNProtocolHTTP2, ALPNProtocolHTTP1},
		// rustls offers TLS 1.3 then TLS 1.2.
		SupportedVersions: []uint16{0x0304, 0x0303},
		// rustls sends a single key share for its preferred group.
		KeyShareGroups: []uint16{0x001d},
		PSKModes:       []uint16{1}, // psk_dhe_ke
		Extensions:     grokCLIExtensions,
		ExtensionOrder: ExtensionOrderRustls,
		EnableGREASE:   false,
		HTTP2:          GrokCLIHTTP2Profile(),
		Pool:           GrokCLIPoolProfile(),
		// rustls keeps session resumption on by default and reqwest does not turn
		// it off, so the official client presents a ticket on every connection
		// after its first. The ticket store is attached per account by the caller.
		ResumeSessions: true,
	}
}

// GrokCLIHTTP2Profile returns the HTTP/2 connection preamble of the official
// Grok Build CLI.
func GrokCLIHTTP2Profile() *HTTP2Profile {
	return &HTTP2Profile{
		Name: GrokCLIHTTP2ProfileName,
		// h2 encodes SETTINGS in the fixed field order of its Settings struct
		// (frame/settings.rs for_each): header_table_size, enable_push,
		// max_concurrent_streams, initial_window_size, max_frame_size,
		// max_header_list_size. hyper leaves the table size and stream limit
		// unset, so only these four go on the wire.
		Settings: []HTTP2Setting{
			{ID: HTTP2SettingEnablePush, Value: 0},
			{ID: HTTP2SettingInitialWindowSize, Value: grokCLIH2InitialWindowSize},
			{ID: HTTP2SettingMaxFrameSize, Value: grokCLIH2MaxFrameSize},
			{ID: HTTP2SettingMaxHeaderListSize, Value: grokCLIH2MaxHeaderListSize},
		},
		ConnectionWindowUpdate: grokCLIH2ConnectionWindowUpdate,
		PingInterval:           grokCLIH2PingInterval,
		PingTimeout:            grokCLIH2PingTimeout,
	}
}

// GrokCLIPoolProfile returns the connection reuse behavior configured in
// xai-grok-sampler/src/shared_http.rs: pool_max_idle_per_host(2),
// pool_idle_timeout(90s) and connect_timeout(10s).
//
// The idle cap is the one value that costs something to match. Relaying more than
// two concurrent requests per account means connections beyond the cap are closed
// instead of pooled, so a busy account pays extra handshakes. That is accepted
// here because connection lifetime is directly observable by the peer, while the
// extra handshakes only cost latency: each one still presents the same verified
// ClientHello (with a fresh rustls extension order, as a real rustls client does).
func GrokCLIPoolProfile() *PoolProfile {
	return &PoolProfile{
		MaxIdleConnsPerHost: 2,
		IdleConnTimeout:     90 * time.Second,
		ConnectTimeout:      10 * time.Second,
	}
}

// rustlsExtensionOrder reproduces rustls' per-connection ClientHello extension
// shuffle: rustls sorts the order-insensitive extensions by a hash of
// (order_seed, extension_type), so each connection presents a different
// extension order and therefore a different JA3.
//
// Port of ClientExtensions::order_insensitive_extensions_in_random_order in
// rustls/src/msgs/client_hello.rs. sort.SliceStable matches Rust's
// sort_by_cached_key, which is also stable.
//
// Reproducing the algorithm rather than shuffling uniformly matters: a uniform
// shuffle of 11 extensions spans ~40 million permutations, while rustls can
// only ever produce the at most 65536 permutations reachable from a u16 seed. A
// JA3 outside that set is evidence of an impostor.
func rustlsExtensionOrder(base []uint16, seed uint16) []uint16 {
	out := append([]uint16(nil), base...)
	key := func(extensionType uint16) uint32 {
		return rustlsLowQualityIntegerHash(uint32(seed)<<16 | uint32(extensionType))
	}
	sort.SliceStable(out, func(i, j int) bool {
		return key(out[i]) < key(out[j])
	})
	return out
}

// rustlsLowQualityIntegerHash is rustls' low_quality_integer_hash
// (rustls/src/msgs/client_hello.rs), Thomas Wang's 32-bit integer hash. Go's
// unsigned arithmetic wraps, matching Rust's wrapping_add.
func rustlsLowQualityIntegerHash(x uint32) uint32 {
	x = x + 0x7ed55d16 + (x << 12)
	x = (x ^ 0xc761c23c) ^ (x >> 19)
	x = x + 0x165667b1 + (x << 5)
	x = (x + 0xd3a2646c) ^ (x << 9)
	x = x + 0xfd7046c5 + (x << 3)
	x = (x ^ 0xb55a4f09) ^ (x >> 16)
	return x
}

// randomExtensionOrderSeed mirrors rustls, which draws a fresh u16 seed from
// the crypto provider for every handshake.
func randomExtensionOrderSeed() uint16 {
	var buf [2]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return 0
	}
	return binary.BigEndian.Uint16(buf[:])
}
