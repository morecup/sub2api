package tlsfingerprint

import (
	"encoding/binary"
	"log/slog"
	"math"
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/net/http2"
)

// HTTP/2 SETTINGS identifiers (RFC 9113 section 6.5.2).
const (
	HTTP2SettingHeaderTableSize      uint16 = 0x1
	HTTP2SettingEnablePush           uint16 = 0x2
	HTTP2SettingMaxConcurrentStreams uint16 = 0x3
	HTTP2SettingInitialWindowSize    uint16 = 0x4
	HTTP2SettingMaxFrameSize         uint16 = 0x5
	HTTP2SettingMaxHeaderListSize    uint16 = 0x6
)

const (
	// http2ClientPreface is the fixed connection preface every HTTP/2 client
	// sends before its first frame (RFC 9113 section 3.4).
	http2ClientPreface = "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"

	http2FrameHeaderLen        = 9
	http2FrameTypeSettings     = 0x4
	http2FrameTypeWindowUpdate = 0x8
	http2SettingEntryLen       = 6

	// http2InitialConnWindow is the per-connection flow-control window every
	// endpoint starts with, before any WINDOW_UPDATE (RFC 9113 section 6.9.2).
	http2InitialConnWindow = 65535

	// http2PrefaceRewriteBudget bounds how many bytes the preface rewriter will
	// hold back while waiting for a frame to complete. The client preamble is a
	// few dozen bytes, so hitting this budget means the stream does not look
	// like a preamble and rewriting is abandoned.
	http2PrefaceRewriteBudget = 16 << 10
)

// HTTP2Setting is one entry of an HTTP/2 SETTINGS frame.
type HTTP2Setting struct {
	ID    uint16
	Value uint32
}

// HTTP2Profile describes the HTTP/2 connection preamble of the client being
// emulated: the SETTINGS entries in wire order, the initial connection-level
// WINDOW_UPDATE increment, and the keepalive PING cadence.
//
// These three values are what HTTP/2 fingerprinting (Akamai-style
// "SETTINGS|WINDOW_UPDATE|PRIORITY|pseudo-header-order") clusters on. Go's
// net/http2 client sends a distinctive preamble of its own, so a TLS
// fingerprint alone is not enough to look like a non-Go client.
//
// Known limitation: the pseudo-header order is hardcoded in
// golang.org/x/net/http2 (:authority, :method, :path, :scheme) and cannot be
// configured, so that one component of the Akamai fingerprint still reads as
// Go. Everything else in the preamble is reproduced exactly.
type HTTP2Profile struct {
	// Name identifies the emulated client for logs.
	Name string
	// Settings are the SETTINGS entries in the exact order the emulated client
	// puts them on the wire.
	Settings []HTTP2Setting
	// ConnectionWindowUpdate is the increment of the WINDOW_UPDATE frame the
	// emulated client sends on stream 0 right after SETTINGS. Zero leaves the
	// local stack's own WINDOW_UPDATE untouched.
	ConnectionWindowUpdate uint32
	// PingInterval and PingTimeout mirror the emulated client's HTTP/2
	// keepalive. Zero leaves the local defaults in place.
	PingInterval time.Duration
	PingTimeout  time.Duration
}

// Setting returns the configured value for id.
func (p *HTTP2Profile) Setting(id uint16) (uint32, bool) {
	if p == nil {
		return 0, false
	}
	for _, setting := range p.Settings {
		if setting.ID == id {
			return setting.Value, true
		}
	}
	return 0, false
}

// ConfigureTransports maps the profile onto the knobs that decide which
// SETTINGS golang.org/x/net/http2 emits.
//
// h1 is only used as a configuration carrier: the per-stream and
// per-connection receive windows are reachable only through
// net/http.Transport.HTTP2, which http2.ConfigureTransports links to the
// HTTP/2 transport. Aligning the knobs (rather than only rewriting the wire)
// keeps the local flow-control accounting equal to what the peer is told.
func (p *HTTP2Profile) ConfigureTransports(h1 *http.Transport, h2 *http2.Transport) {
	if p == nil {
		return
	}
	if h1 != nil {
		cfg := h1.HTTP2
		if cfg == nil {
			cfg = &http.HTTP2Config{}
		}
		if value, ok := p.Setting(HTTP2SettingInitialWindowSize); ok {
			cfg.MaxReceiveBufferPerStream = int(value)
		}
		if value, ok := p.Setting(HTTP2SettingMaxFrameSize); ok {
			cfg.MaxReadFrameSize = int(value)
		}
		// net/http2 writes MaxReceiveBufferPerConnection as the WINDOW_UPDATE
		// increment, matching how h2/hyper derive theirs from the target
		// connection window.
		if p.ConnectionWindowUpdate >= http2InitialConnWindow {
			cfg.MaxReceiveBufferPerConnection = int(p.ConnectionWindowUpdate)
		}
		h1.HTTP2 = cfg
	}
	if h2 != nil {
		if value, ok := p.Setting(HTTP2SettingMaxFrameSize); ok {
			h2.MaxReadFrameSize = value
		}
		if value, ok := p.Setting(HTTP2SettingMaxHeaderListSize); ok {
			h2.MaxHeaderListSize = value
		} else {
			// net/http2 sends its own 10MiB default unless the limit is
			// "infinite"; MaxUint32 is how it is told to omit the setting.
			h2.MaxHeaderListSize = math.MaxUint32
		}
		if p.PingInterval > 0 {
			h2.ReadIdleTimeout = p.PingInterval
		}
		if p.PingTimeout > 0 {
			h2.PingTimeout = p.PingTimeout
		}
	}
}

// WrapConn returns conn with its HTTP/2 client preamble rewritten to match the
// profile. It is a no-op when the profile carries no SETTINGS.
func (p *HTTP2Profile) WrapConn(conn net.Conn) net.Conn {
	if p == nil || conn == nil || (len(p.Settings) == 0 && p.ConnectionWindowUpdate == 0) {
		return conn
	}
	return &http2PrefaceConn{Conn: conn, profile: p}
}

// http2PrefaceConn rewrites the HTTP/2 client preamble written by the local
// HTTP/2 stack so the SETTINGS order and values, plus the initial
// connection-level WINDOW_UPDATE, match the emulated client.
//
// Safety rule: a setting is only ever emitted if the local stack advertised it,
// and only with a value less than or equal to the local one. Every client
// SETTINGS entry and the connection WINDOW_UPDATE describe limits the peer must
// respect when sending to us, so narrowing them can never let the peer overrun
// the local flow-control accounting. Settings the local stack advertised but
// the profile omits are appended rather than dropped, for the same reason.
type http2PrefaceConn struct {
	net.Conn
	profile *HTTP2Profile

	mu               sync.Mutex
	pending          []byte
	prefaceSeen      bool
	settingsDone     bool
	windowUpdateDone bool
	passthrough      bool
}

func (c *http2PrefaceConn) Write(b []byte) (int, error) {
	c.mu.Lock()
	if c.passthrough && len(c.pending) == 0 {
		c.mu.Unlock()
		return c.Conn.Write(b)
	}

	c.pending = append(c.pending, b...)
	out := c.consumeLocked()
	c.mu.Unlock()

	if len(out) == 0 {
		// The preamble frame is still incomplete. Reporting the bytes as
		// written is safe: they are flushed as soon as the frame completes, and
		// a failure afterwards fails the connection either way.
		return len(b), nil
	}
	if _, err := c.Conn.Write(out); err != nil {
		return 0, err
	}
	return len(b), nil
}

// consumeLocked drains as much of the pending buffer as can be forwarded,
// rewriting the preamble frames on the way through.
func (c *http2PrefaceConn) consumeLocked() []byte {
	var out []byte

	if !c.prefaceSeen {
		if len(c.pending) < len(http2ClientPreface) {
			return c.flushOnBudgetLocked(out)
		}
		if string(c.pending[:len(http2ClientPreface)]) != http2ClientPreface {
			// Not an HTTP/2 client stream; leave it alone.
			c.passthrough = true
			out = append(out, c.pending...)
			c.pending = nil
			return out
		}
		out = append(out, c.pending[:len(http2ClientPreface)]...)
		c.pending = c.pending[len(http2ClientPreface):]
		c.prefaceSeen = true
	}

	for {
		if c.settingsDone && c.windowUpdateDone {
			c.passthrough = true
			out = append(out, c.pending...)
			c.pending = nil
			return out
		}
		if len(c.pending) < http2FrameHeaderLen {
			return c.flushOnBudgetLocked(out)
		}
		frameLen := int(c.pending[0])<<16 | int(c.pending[1])<<8 | int(c.pending[2])
		frameType := c.pending[3]
		flags := c.pending[4]
		streamID := binary.BigEndian.Uint32(c.pending[5:9]) & 0x7fffffff
		total := http2FrameHeaderLen + frameLen

		rewritable := streamID == 0 &&
			((frameType == http2FrameTypeSettings && flags == 0 && !c.settingsDone) ||
				(frameType == http2FrameTypeWindowUpdate && !c.windowUpdateDone))
		if !rewritable {
			// The preamble is over (or this frame is not one we touch): forward
			// everything buffered and stop inspecting.
			c.passthrough = true
			out = append(out, c.pending...)
			c.pending = nil
			return out
		}
		if len(c.pending) < total {
			return c.flushOnBudgetLocked(out)
		}

		payload := c.pending[http2FrameHeaderLen:total]
		switch frameType {
		case http2FrameTypeSettings:
			out = append(out, encodeHTTP2SettingsFrame(c.profile.rewriteSettings(payload))...)
			c.settingsDone = true
		case http2FrameTypeWindowUpdate:
			out = append(out, encodeHTTP2WindowUpdateFrame(c.profile.rewriteConnectionWindowUpdate(payload))...)
			c.windowUpdateDone = true
		}
		c.pending = c.pending[total:]
	}
}

// flushOnBudgetLocked gives up on rewriting once the held-back bytes stop
// looking like a client preamble, so a pathological stream can never stall.
func (c *http2PrefaceConn) flushOnBudgetLocked(out []byte) []byte {
	if len(c.pending) <= http2PrefaceRewriteBudget {
		return out
	}
	slog.Debug("tls_fingerprint_http2_preface_rewrite_abandoned", "buffered", len(c.pending))
	c.passthrough = true
	out = append(out, c.pending...)
	c.pending = nil
	return out
}

// rewriteSettings reorders the locally generated SETTINGS entries into the
// profile's order, narrowing values that the profile advertises lower.
func (p *HTTP2Profile) rewriteSettings(payload []byte) []HTTP2Setting {
	local := decodeHTTP2Settings(payload)
	out := make([]HTTP2Setting, 0, len(local)+len(p.Settings))
	emitted := make(map[uint16]bool, len(p.Settings))

	for _, want := range p.Settings {
		localValue, ok := findHTTP2Setting(local, want.ID)
		if !ok {
			// Advertising a setting the local stack did not would promise the
			// peer limits the local stack is not prepared for.
			slog.Debug("tls_fingerprint_http2_setting_unavailable", "profile", p.Name, "setting", want.ID)
			continue
		}
		value := want.Value
		if value > localValue {
			slog.Debug("tls_fingerprint_http2_setting_clamped",
				"profile", p.Name, "setting", want.ID, "requested", want.Value, "local", localValue)
			value = localValue
		}
		out = append(out, HTTP2Setting{ID: want.ID, Value: value})
		emitted[want.ID] = true
	}
	for _, setting := range local {
		if !emitted[setting.ID] {
			slog.Debug("tls_fingerprint_http2_setting_preserved", "profile", p.Name, "setting", setting.ID)
			out = append(out, setting)
		}
	}
	return out
}

// rewriteConnectionWindowUpdate narrows the locally generated connection-level
// WINDOW_UPDATE increment to the profile's value.
func (p *HTTP2Profile) rewriteConnectionWindowUpdate(payload []byte) uint32 {
	local := uint32(0)
	if len(payload) >= 4 {
		local = binary.BigEndian.Uint32(payload) & 0x7fffffff
	}
	if p.ConnectionWindowUpdate == 0 || p.ConnectionWindowUpdate > local {
		return local
	}
	return p.ConnectionWindowUpdate
}

func decodeHTTP2Settings(payload []byte) []HTTP2Setting {
	out := make([]HTTP2Setting, 0, len(payload)/http2SettingEntryLen)
	for offset := 0; offset+http2SettingEntryLen <= len(payload); offset += http2SettingEntryLen {
		out = append(out, HTTP2Setting{
			ID:    binary.BigEndian.Uint16(payload[offset:]),
			Value: binary.BigEndian.Uint32(payload[offset+2:]),
		})
	}
	return out
}

func findHTTP2Setting(settings []HTTP2Setting, id uint16) (uint32, bool) {
	for _, setting := range settings {
		if setting.ID == id {
			return setting.Value, true
		}
	}
	return 0, false
}

func encodeHTTP2SettingsFrame(settings []HTTP2Setting) []byte {
	payloadLen := len(settings) * http2SettingEntryLen
	frame := make([]byte, http2FrameHeaderLen+payloadLen)
	frame[0] = byte(payloadLen >> 16)
	frame[1] = byte(payloadLen >> 8)
	frame[2] = byte(payloadLen)
	frame[3] = http2FrameTypeSettings
	for i, setting := range settings {
		offset := http2FrameHeaderLen + i*http2SettingEntryLen
		binary.BigEndian.PutUint16(frame[offset:], setting.ID)
		binary.BigEndian.PutUint32(frame[offset+2:], setting.Value)
	}
	return frame
}

func encodeHTTP2WindowUpdateFrame(increment uint32) []byte {
	frame := make([]byte, http2FrameHeaderLen+4)
	frame[2] = 4
	frame[3] = http2FrameTypeWindowUpdate
	binary.BigEndian.PutUint32(frame[http2FrameHeaderLen:], increment&0x7fffffff)
	return frame
}
