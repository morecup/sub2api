package tlsfingerprint

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"reflect"
	"testing"
	"time"
)

type parsedClientHello struct {
	recordVersion       uint16
	recordLen           int
	handshakeLen        int
	legacyVersion       uint16
	sessionIDLen        int
	cipherSuites        []uint16
	compressionMethods  []uint8
	extensions          []uint16
	supportedGroups     []uint16
	pointFormats        []uint16
	signatureAlgorithms []uint16
	alpn                []string
	supportedVersions   []uint16
	keyShareGroups      []uint16
	pskModes            []uint16
}

func TestDefaultClientHelloRawCaptureMatchesLocalClaudeCode(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer func() { _ = clientConn.Close() }()
	defer func() { _ = serverConn.Close() }()

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

	dialer := NewDialer(BuiltInDefaultProfile(), func(context.Context, string, string) (net.Conn, error) {
		return clientConn, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := dialer.DialTLSContext(ctx, "tcp", "api.anthropic.com:443")
	if err == nil {
		_ = conn.Close()
		t.Fatal("expected handshake to fail after local raw capture")
	}

	var raw []byte
	select {
	case raw = <-rawCh:
	case err := <-errCh:
		t.Fatalf("read ClientHello: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for captured ClientHello")
	}

	hello, err := parseClientHelloRecord(raw)
	if err != nil {
		t.Fatalf("parse ClientHello: %v", err)
	}

	assertEqual(t, "record_version", hello.recordVersion, uint16(0x0301))
	assertEqual(t, "record_len", hello.recordLen, 512)
	assertEqual(t, "handshake_len", hello.handshakeLen, 508)
	assertEqual(t, "legacy_version", hello.legacyVersion, uint16(0x0303))
	assertEqual(t, "session_id_len", hello.sessionIDLen, 32)
	assertSliceEqual(t, "cipher_suites", hello.cipherSuites, defaultCipherSuites)
	assertSliceEqual(t, "compression_methods", hello.compressionMethods, []uint8{0})
	assertSliceEqual(t, "extensions", hello.extensions, defaultExtensionOrder)
	assertSliceEqual(t, "supported_groups", hello.supportedGroups, []uint16{29, 23, 24})
	assertSliceEqual(t, "point_formats", hello.pointFormats, []uint16{0})
	assertSliceEqual(t, "signature_algorithms", hello.signatureAlgorithms, []uint16{1027, 2052, 1025, 1283, 2053, 1281, 2054, 1537, 513})
	assertSliceEqual(t, "alpn", hello.alpn, []string{"http/1.1"})
	assertSliceEqual(t, "supported_versions", hello.supportedVersions, []uint16{772, 771})
	assertSliceEqual(t, "key_share_groups", hello.keyShareGroups, []uint16{29})
	assertSliceEqual(t, "psk_modes", hello.pskModes, []uint16{1})

	ja3Raw := hello.ja3Raw()
	if ja3Raw != BuiltInDefaultJA3Raw {
		t.Fatalf("JA3 raw mismatch:\n got: %s\nwant: %s", ja3Raw, BuiltInDefaultJA3Raw)
	}
	if got := md5Hex(ja3Raw); got != BuiltInDefaultJA3Hash {
		t.Fatalf("JA3 hash = %s, want %s", got, BuiltInDefaultJA3Hash)
	}
}

func readFirstTLSRecord(conn net.Conn) ([]byte, error) {
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return nil, err
	}
	header := make([]byte, 5)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, err
	}
	if header[0] != 22 {
		return nil, fmt.Errorf("record type = %d, want handshake(22)", header[0])
	}
	recordLen := int(header[3])<<8 | int(header[4])
	body := make([]byte, recordLen)
	if _, err := io.ReadFull(conn, body); err != nil {
		return nil, err
	}
	return append(header, body...), nil
}

func parseClientHelloRecord(raw []byte) (*parsedClientHello, error) {
	if len(raw) < 5 {
		return nil, fmt.Errorf("record too short: %d", len(raw))
	}
	hello := &parsedClientHello{
		recordVersion: uint16(raw[1])<<8 | uint16(raw[2]),
		recordLen:     int(raw[3])<<8 | int(raw[4]),
	}
	if len(raw) != 5+hello.recordLen {
		return nil, fmt.Errorf("record length mismatch: got %d bytes, header says %d", len(raw), hello.recordLen)
	}
	body := raw[5:]
	if len(body) < 4 || body[0] != 1 {
		return nil, fmt.Errorf("not a ClientHello handshake")
	}
	hello.handshakeLen = int(body[1])<<16 | int(body[2])<<8 | int(body[3])
	if hello.handshakeLen != len(body)-4 {
		return nil, fmt.Errorf("handshake length mismatch: got %d, header says %d", len(body)-4, hello.handshakeLen)
	}

	offset := 4
	hello.legacyVersion = readUint16(body, &offset)
	offset += 32 // random
	hello.sessionIDLen = readUint8(body, &offset)
	offset += hello.sessionIDLen
	hello.cipherSuites = readUint16Vector(body, &offset, 2)
	hello.compressionMethods = readUint8Vector(body, &offset, 1)

	extensionsLen := readUint16AsInt(body, &offset)
	extensionsEnd := offset + extensionsLen
	if extensionsEnd > len(body) {
		return nil, fmt.Errorf("extensions length exceeds handshake")
	}
	for offset < extensionsEnd {
		extID := readUint16(body, &offset)
		extLen := readUint16AsInt(body, &offset)
		extEnd := offset + extLen
		if extEnd > extensionsEnd {
			return nil, fmt.Errorf("extension %d length exceeds extensions block", extID)
		}
		extData := body[offset:extEnd]
		offset = extEnd
		hello.extensions = append(hello.extensions, extID)
		parseClientHelloExtension(hello, extID, extData)
	}
	return hello, nil
}

func parseClientHelloExtension(hello *parsedClientHello, extID uint16, data []byte) {
	offset := 0
	switch extID {
	case 10:
		hello.supportedGroups = readUint16Vector(data, &offset, 2)
	case 11:
		points := readUint8Vector(data, &offset, 1)
		hello.pointFormats = make([]uint16, len(points))
		for i, p := range points {
			hello.pointFormats[i] = uint16(p)
		}
	case 13:
		hello.signatureAlgorithms = readUint16Vector(data, &offset, 2)
	case 16:
		hello.alpn = readALPN(data)
	case 43:
		versions := readUint8Vector(data, &offset, 1)
		for i := 0; i+1 < len(versions); i += 2 {
			hello.supportedVersions = append(hello.supportedVersions, uint16(versions[i])<<8|uint16(versions[i+1]))
		}
	case 45:
		modes := readUint8Vector(data, &offset, 1)
		hello.pskModes = make([]uint16, len(modes))
		for i, m := range modes {
			hello.pskModes[i] = uint16(m)
		}
	case 51:
		keySharesLen := readUint16AsInt(data, &offset)
		keySharesEnd := offset + keySharesLen
		for offset < keySharesEnd {
			group := readUint16(data, &offset)
			keyExchangeLen := readUint16AsInt(data, &offset)
			offset += keyExchangeLen
			hello.keyShareGroups = append(hello.keyShareGroups, group)
		}
	}
}

func (h *parsedClientHello) ja3Raw() string {
	return fmt.Sprintf("%d,%s,%s,%s,%s",
		h.legacyVersion,
		joinUint16s(h.cipherSuites),
		joinUint16s(h.extensions),
		joinUint16s(h.supportedGroups),
		joinUint16s(h.pointFormats),
	)
}

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func readUint8(data []byte, offset *int) int {
	v := data[*offset]
	*offset = *offset + 1
	return int(v)
}

func readUint16(data []byte, offset *int) uint16 {
	v := uint16(data[*offset])<<8 | uint16(data[*offset+1])
	*offset += 2
	return v
}

func readUint16AsInt(data []byte, offset *int) int {
	return int(readUint16(data, offset))
}

func readUint8Vector(data []byte, offset *int, lenBytes int) []uint8 {
	var n int
	if lenBytes == 1 {
		n = readUint8(data, offset)
	} else {
		n = readUint16AsInt(data, offset)
	}
	out := append([]uint8(nil), data[*offset:*offset+n]...)
	*offset += n
	return out
}

func readUint16Vector(data []byte, offset *int, lenBytes int) []uint16 {
	raw := readUint8Vector(data, offset, lenBytes)
	out := make([]uint16, 0, len(raw)/2)
	for i := 0; i+1 < len(raw); i += 2 {
		out = append(out, uint16(raw[i])<<8|uint16(raw[i+1]))
	}
	return out
}

func readALPN(data []byte) []string {
	offset := 0
	raw := readUint8Vector(data, &offset, 2)
	out := make([]string, 0, 1)
	for i := 0; i < len(raw); {
		n := int(raw[i])
		i++
		out = append(out, string(raw[i:i+n]))
		i += n
	}
	return out
}

func assertEqual[T comparable](t *testing.T, name string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
}

func assertSliceEqual[T comparable](t *testing.T, name string, got, want []T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
}
