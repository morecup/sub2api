package tlsfingerprint

// CodexDesktopProfileName identifies the TLS profile used by Codex Desktop
// 26.911.7940.0 / codex-rs 0.155.0-alpha.2.6.
//
// Provenance: direct loopback TLS/H2 capture of the isolated bundled binary on
// 2026-09-18. The provider used dummy auth and an offline fixture. Desktop-only
// HTTP header order is from the 2026-09-17 application capture.
const CodexDesktopProfileName = "Codex Desktop 26.911.7940.0 (rustls / reqwest)"

// CodexDesktopHTTP2ProfileName identifies the HTTP/2 half of that profile.
const CodexDesktopHTTP2ProfileName = "Codex Desktop 26.911.7940.0 (hyper / h2)"

// codexDesktopCipherSuites is rustls' aws-lc-rs DEFAULT_CIPHER_SUITES in
// provider order, followed by the secure-renegotiation SCSV rustls appends
// while TLS 1.2 is offered.
var codexDesktopCipherSuites = []uint16{
	0x1302, // TLS13_AES_256_GCM_SHA384
	0x1301, // TLS13_AES_128_GCM_SHA256
	0x1303, // TLS13_CHACHA20_POLY1305_SHA256
	0xc02c, // TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384
	0xc02b, // TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256
	0xcca9, // TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256
	0xc030, // TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384
	0xc02f, // TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256
	0xcca8, // TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256
	0x00ff, // TLS_EMPTY_RENEGOTIATION_INFO_SCSV
}

// The 0.155 binary advertises X25519MLKEM768 first, followed by the classical
// groups. It sends both the hybrid and X25519 shares on the initial hello.
var codexDesktopCurves = []uint16{
	0x11ec, // X25519MLKEM768
	0x001d, // x25519
	0x0017, // secp256r1
	0x0018, // secp384r1
}

var codexDesktopKeyShareGroups = []uint16{
	0x11ec, // X25519MLKEM768
	0x001d, // x25519
}

// aws-lc-rs additionally supports P-521 certificates; that entry is the only
// signature-scheme difference from the ring-backed Grok rustls profile.
var codexDesktopSignatureAlgorithms = []uint16{
	0x0503, // ecdsa_secp384r1_sha384
	0x0403, // ecdsa_secp256r1_sha256
	0x0603, // ecdsa_secp521r1_sha512
	0x0807, // ed25519
	0x0806, // rsa_pss_rsae_sha512
	0x0805, // rsa_pss_rsae_sha384
	0x0804, // rsa_pss_rsae_sha256
	0x0601, // rsa_pkcs1_sha512
	0x0501, // rsa_pkcs1_sha384
	0x0401, // rsa_pkcs1_sha256
}

// Fresh rustls 0.23 connections use this extension set. rustls permutes these
// extensions per connection; pre_shared_key is appended separately when a
// cached TLS 1.3 ticket is available.
var codexDesktopExtensions = []uint16{
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

const (
	codexDesktopH2InitialWindowSize      uint32 = 2 << 20
	codexDesktopH2MaxFrameSize           uint32 = 16 << 10
	codexDesktopH2MaxHeaderListSize      uint32 = 16 << 10
	codexDesktopH2ConnectionWindowUpdate uint32 = 5<<20 - http2InitialConnWindow
)

var codexDesktopHTTP2PseudoHeaderOrder = []string{
	":method",
	":scheme",
	":authority",
	":path",
}

var codexDesktopHTTP2RegularHeaderOrder = []string{
	"version",
	"x-codex-beta-features",
	"x-codex-window-id",
	"x-codex-turn-metadata",
	"x-oai-attestation",
	"x-openai-internal-codex-responses-lite",
	"x-codex-routing-hint",
	"x-client-request-id",
	"session-id",
	"thread-id",
	"accept",
	"content-encoding",
	"content-type",
	"authorization",
	"chatgpt-account-id",
	"originator",
	"user-agent",
	"cookie",
	"content-length",
}

// CodexDesktopProfile returns the TLS and HTTP/2 fingerprint of the captured
// official Codex Desktop build.
func CodexDesktopProfile() *Profile {
	return &Profile{
		Name:                        CodexDesktopProfileName,
		CipherSuites:                codexDesktopCipherSuites,
		Curves:                      codexDesktopCurves,
		PointFormats:                []uint16{0},
		SignatureAlgorithms:         codexDesktopSignatureAlgorithms,
		ALPNProtocols:               []string{ALPNProtocolHTTP2, ALPNProtocolHTTP1},
		SupportedVersions:           []uint16{0x0304, 0x0303},
		KeyShareGroups:              codexDesktopKeyShareGroups,
		PSKModes:                    []uint16{1},
		Extensions:                  codexDesktopExtensions,
		ExtensionOrder:              ExtensionOrderRustls,
		HTTP2:                       CodexDesktopHTTP2Profile(),
		UseOrderedHTTP2Transport:    true,
		ResumeSessions:              true,
		DisableAutomaticCompression: true,
	}
}

// CodexDesktopHTTP2Profile returns the hyper/h2 connection preamble and the
// captured request-header order. Codex does not configure HTTP/2 keepalive, so
// both ping durations intentionally remain zero.
func CodexDesktopHTTP2Profile() *HTTP2Profile {
	return &HTTP2Profile{
		Name: CodexDesktopHTTP2ProfileName,
		Settings: []HTTP2Setting{
			{ID: HTTP2SettingEnablePush, Value: 0},
			{ID: HTTP2SettingInitialWindowSize, Value: codexDesktopH2InitialWindowSize},
			{ID: HTTP2SettingMaxFrameSize, Value: codexDesktopH2MaxFrameSize},
			{ID: HTTP2SettingMaxHeaderListSize, Value: codexDesktopH2MaxHeaderListSize},
		},
		ConnectionWindowUpdate: codexDesktopH2ConnectionWindowUpdate,
		PseudoHeaderOrder:      append([]string{}, codexDesktopHTTP2PseudoHeaderOrder...),
		RegularHeaderOrder:     append([]string{}, codexDesktopHTTP2RegularHeaderOrder...),
	}
}
