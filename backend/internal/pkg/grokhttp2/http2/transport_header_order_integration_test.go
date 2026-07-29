//go:build !(go1.27 && !http2legacy)

package http2

import (
	"bytes"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/grokhttp2/http2/hpack"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
)

func TestEncodeRequestHeaders_RealGrokProfileOrderReachesTransportEncodingPath(t *testing.T) {
	profile := tlsfingerprint.GrokCLIProfile()
	if profile == nil || profile.HTTP2 == nil {
		t.Fatal("GrokCLIProfile missing HTTP2 profile")
	}

	req, err := http.NewRequest(http.MethodPost, "https://cli-chat-proxy.grok.com/v1/responses", strings.NewReader(`{"model":"grok-4.5"}`))
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}
	req.Header = http.Header{
		"Content-Type":             {"application/json"},
		"User-Agent":               {"grok-shell/0.2.112"},
		"X-Compactions-Remaining":  {"1"},
		"X-Compaction-At":          {"400000"},
		"X-Grok-Client-Version":    {"0.2.112"},
		"X-Grok-User-Id":           {"user-123"},
		"X-Grok-Client-Identifier": {"windows-x64"},
		"Authorization":            {"Bearer token"},
		"Traceparent":              {"00-0123456789abcdef0123456789abcdef-0123456789abcdef-01"},
		"X-Grok-Conv-Id":           {"conv-123"},
		"X-Grok-Req-Id":            {"req-123"},
		"X-Grok-Model-Override":    {"grok-4.5"},
		"X-Grok-Session-Id":        {"conv-123"},
		"X-Grok-Agent-Id":          {"agent-123"},
		"X-Grok-Turn-Idx":          {"1"},
		"Accept":                   {"text/event-stream"},
		"Accept-Encoding":          {"gzip, br, deflate"},
	}

	transport := &Transport{
		HeaderOrder: &HeaderOrder{
			Pseudo:  append([]string{}, profile.HTTP2.PseudoHeaderOrder...),
			Regular: append([]string{}, profile.HTTP2.RegularHeaderOrder...),
		},
	}

	fields := encodeObservedTransportHeaderFields(t, transport, req, false)
	if got, want := headerNames(fields[:4]), []string{":method", ":scheme", ":authority", ":path"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("pseudo header order = %v, want %v", got, want)
	}

	wantRegular := append([]string{}, profile.HTTP2.RegularHeaderOrder...)
	if got := headerNames(fields[4:]); !reflect.DeepEqual(got, wantRegular) {
		t.Fatalf("regular header order = %v, want %v", got, wantRegular)
	}
}

func encodeObservedTransportHeaderFields(t *testing.T, transport *Transport, req *http.Request, addGzipHeader bool) []hpack.HeaderField {
	t.Helper()

	var block bytes.Buffer
	enc := hpack.NewEncoder(&block)
	if _, err := encodeRequestHeaders(req, addGzipHeader, 1<<20, transport.HeaderOrder, func(name, value string) {
		if writeErr := enc.WriteField(hpack.HeaderField{Name: name, Value: value}); writeErr != nil {
			t.Fatalf("WriteField(%q): %v", name, writeErr)
		}
	}); err != nil {
		t.Fatalf("encodeRequestHeaders: %v", err)
	}

	var fields []hpack.HeaderField
	dec := hpack.NewDecoder(4096, func(field hpack.HeaderField) {
		fields = append(fields, field)
	})
	if _, err := dec.Write(block.Bytes()); err != nil {
		t.Fatalf("Decoder.Write: %v", err)
	}
	return fields
}

func headerNames(fields []hpack.HeaderField) []string {
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		out = append(out, field.Name)
	}
	return out
}
