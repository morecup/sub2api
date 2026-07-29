package tlsfingerprint

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"net"
	"strconv"

	utls "github.com/refraction-networking/utls"
)

// ErrALPNMismatch reports that the server picked a different application
// protocol than the caller requires. Callers use it to fall back to another
// transport instead of speaking the wrong protocol over the connection.
var ErrALPNMismatch = errors.New("tlsfingerprint: server did not negotiate the required ALPN protocol")

// DialTLSFunc matches http.Transport.DialTLSContext.
type DialTLSFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// NegotiatedProtocol returns the ALPN protocol agreed on conn.
func NegotiatedProtocol(conn net.Conn) (string, bool) {
	switch typed := conn.(type) {
	case *utls.UConn:
		return typed.ConnectionState().NegotiatedProtocol, true
	case interface{ ConnectionState() utls.ConnectionState }:
		return typed.ConnectionState().NegotiatedProtocol, true
	case interface{ ConnectionState() tls.ConnectionState }:
		return typed.ConnectionState().NegotiatedProtocol, true
	default:
		return "", false
	}
}

// RequireALPN wraps dial so it fails with ErrALPNMismatch unless the handshake
// selected proto. When the profile carries an HTTP/2 preamble, the connection is
// also wrapped so that preamble is rewritten to match the emulated client.
//
// Connections whose protocol cannot be determined are passed through: the
// caller cannot do better than trust its own ALPN offer.
func RequireALPN(proto string, profile *Profile, dial DialTLSFunc) DialTLSFunc {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := dial(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		if negotiated, ok := NegotiatedProtocol(conn); ok && negotiated != proto {
			_ = conn.Close()
			return nil, fmt.Errorf("%w: got %q, want %q", ErrALPNMismatch, negotiated, proto)
		}
		if proto == ALPNProtocolHTTP2 && profile != nil {
			return profile.HTTP2.WrapConn(conn), nil
		}
		return conn, nil
	}
}

// CacheKey returns a short, stable identity for the profile's observable
// transport configuration. Callers that pool connections per profile use it so
// two profiles can never share a transport: the ALPN offer alone already
// changes the transport's shape (HTTP/1.1 only versus an h2 pair), and sharing
// would also leak one client's identity onto another's connections.
func (p *Profile) CacheKey() string {
	if p == nil {
		return "none"
	}
	digest := fnv.New64a()
	writeString := func(value string) {
		_, _ = digest.Write([]byte(value))
		_, _ = digest.Write([]byte{0})
	}
	writeStrings := func(name string, values []string) {
		writeString(name)
		writeString(strconv.Itoa(len(values)))
		for _, value := range values {
			writeString(value)
		}
	}
	writeUint16s := func(values []uint16) {
		var buf [2]byte
		for _, value := range values {
			binary.BigEndian.PutUint16(buf[:], value)
			_, _ = digest.Write(buf[:])
		}
		_, _ = digest.Write([]byte{0})
	}

	writeString(p.Name)
	writeString(string(p.ExtensionOrder))
	writeString("use-grok-http2-transport")
	writeString(strconv.FormatBool(p.UseGrokHTTP2Transport))
	if p.EnableGREASE {
		writeString("grease")
	}
	if p.ResumeSessions {
		// A resumption-capable profile can put pre_shared_key on the wire, so it
		// must never share a transport with one that cannot.
		writeString("resume")
	}
	if p.Pool != nil {
		writeString(strconv.Itoa(p.Pool.MaxIdleConnsPerHost))
		writeString(p.Pool.IdleConnTimeout.String())
		writeString(p.Pool.ConnectTimeout.String())
	}
	writeUint16s(p.CipherSuites)
	writeUint16s(p.Curves)
	writeUint16s(p.PointFormats)
	writeUint16s(p.SignatureAlgorithms)
	writeUint16s(p.SupportedVersions)
	writeUint16s(p.KeyShareGroups)
	writeUint16s(p.PSKModes)
	writeUint16s(p.Extensions)
	for _, proto := range p.EffectiveALPNProtocols() {
		writeString(proto)
	}
	if p.HTTP2 != nil {
		writeString(p.HTTP2.Name)
		for _, setting := range p.HTTP2.Settings {
			writeUint16s([]uint16{setting.ID})
			var buf [4]byte
			binary.BigEndian.PutUint32(buf[:], setting.Value)
			_, _ = digest.Write(buf[:])
		}
		var buf [4]byte
		binary.BigEndian.PutUint32(buf[:], p.HTTP2.ConnectionWindowUpdate)
		_, _ = digest.Write(buf[:])
		writeStrings("http2-pseudo-header-order", p.HTTP2.PseudoHeaderOrder)
		writeStrings("http2-regular-header-order", p.HTTP2.RegularHeaderOrder)
	}
	return strconv.FormatUint(digest.Sum64(), 16)
}
