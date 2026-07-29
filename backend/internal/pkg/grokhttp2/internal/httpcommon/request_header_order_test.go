package httpcommon

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/grokhttp2/http2/hpack"
)

var capturedGrokInferenceOrdinaryHeaderOrder = []string{
	"content-type",
	"user-agent",
	"x-compactions-remaining",
	"x-compaction-at",
	"x-grok-client-version",
	"x-grok-user-id",
	"x-grok-client-identifier",
	"authorization",
	"traceparent",
	"x-grok-conv-id",
	"x-grok-req-id",
	"x-grok-model-override",
	"x-grok-session-id",
	"x-grok-agent-id",
	"x-grok-turn-idx",
	"accept",
	"accept-encoding",
	"content-length",
}

func TestEncodeHeaders_WithoutGrokOrderingMatchesUpstreamV0560Control(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://cli-chat-proxy.grok.com/v1/responses", nil)
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}

	fields := encodeObservedFields(t, req, true, "upstream-control/0.56.0", nil)
	if got, want := headerNames(fields), []string{
		":authority",
		":method",
		":path",
		":scheme",
		"content-length",
		"accept-encoding",
		"user-agent",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unconfigured header order = %v, want upstream v0.56.0 control %v", got, want)
	}
}

func TestEncodeHeaders_GrokPseudoHeadersEncodeAsMSAP(t *testing.T) {
	req := newCapturedGrokInferenceRequest(t)

	fields := encodeObservedFields(t, req, false, "", capturedGrokInferenceHeaderOrder())
	if got, want := headerNames(fields[:4]), []string{
		":method",
		":scheme",
		":authority",
		":path",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("pseudo header order = %v, want %v", got, want)
	}
}

func TestEncodeHeaders_DuplicateConfiguredPseudoHeadersEmitOnceAndKeepUnconfigured(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://cli-chat-proxy.grok.com/v1/responses", nil)
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}

	fields := encodeObservedFields(t, req, false, "", &HeaderOrder{
		Pseudo: []string{":method", ":method"},
	})
	var got []string
	for _, field := range fields {
		if !strings.HasPrefix(field.Name, ":") {
			break
		}
		got = append(got, field.Name)
	}
	want := []string{":method", ":authority", ":path", ":scheme"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pseudo header order = %v, want duplicate configuration emitted once and unconfigured headers retained as %v", got, want)
	}
}

func TestEncodeHeaders_GrokOrdinaryHeadersEncodeInCaptured18HeaderOrder(t *testing.T) {
	req := newCapturedGrokInferenceRequest(t)

	fields := encodeObservedFields(t, req, false, "", capturedGrokInferenceHeaderOrder())
	if got := headerNames(fields[4:]); !reflect.DeepEqual(got, capturedGrokInferenceOrdinaryHeaderOrder) {
		t.Fatalf("ordinary header order = %v, want captured order %v", got, capturedGrokInferenceOrdinaryHeaderOrder)
	}
}

func TestEncodeHeaders_GrokUnlistedHeadersUseDeterministicTailPolicyAndPreserveDuplicates(t *testing.T) {
	req := newCapturedGrokInferenceRequest(t)
	req.Header["X-Zeta"] = []string{"z1"}
	req.Header["X-Alpha"] = []string{"a1"}
	req.Header["X-Beta"] = []string{"b1", "b2"}

	fields := encodeObservedFields(t, req, false, "", capturedGrokInferenceHeaderOrder())
	want := append([]string{}, headerNameValuesFromCapturedOrder()...)
	want = append(want,
		"x-alpha=a1",
		"x-beta=b1",
		"x-beta=b2",
		"x-zeta=z1",
	)
	if got := headerNameValues(fields[4:]); !reflect.DeepEqual(got, want) {
		t.Fatalf("ordinary header order with unlisted tail = %v, want %v", got, want)
	}
}

func TestEncodeHeaders_GrokNormalConnectKeepsOnlyMethodAndAuthorityPseudoHeaders(t *testing.T) {
	req := &http.Request{
		Method: http.MethodConnect,
		URL:    mustURL(t, "https://example.com:443"),
		Host:   "example.com:443",
		Header: http.Header{},
	}

	fields := encodeObservedFields(t, req, false, "", capturedGrokInferenceHeaderOrder())
	if got, want := headerNames(fields[:2]), []string{
		":method",
		":authority",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("CONNECT pseudo header order = %v, want %v", got, want)
	}
	if got := headerNames(fields); containsAny(got, ":scheme", ":path", ":protocol") {
		t.Fatalf("normal CONNECT must not emit :scheme, :path, or :protocol; got %v", got)
	}
}

func TestEncodeHeaders_GrokExtendedConnectKeepsProtocolPseudoHeaderAndCustomPseudoOrder(t *testing.T) {
	req := &http.Request{
		Method: http.MethodConnect,
		URL:    mustURL(t, "https://example.com/chat"),
		Host:   "example.com",
		Header: http.Header{":protocol": {"websocket"}},
	}

	fields := encodeObservedFields(t, req, false, "", capturedGrokInferenceHeaderOrder())
	if got, want := headerNames(fields[:5]), []string{
		":method",
		":scheme",
		":authority",
		":path",
		":protocol",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("extended CONNECT pseudo header order = %v, want %v", got, want)
	}
}

func TestEncodeHeaders_GrokOrderingStillFiltersConnectionSpecificAndReservedHeaders(t *testing.T) {
	req := &http.Request{
		Method: http.MethodConnect,
		URL:    mustURL(t, "https://example.com/chat"),
		Host:   "example.com",
		Header: http.Header{":protocol": {"websocket"}},
	}
	req.Header["Connection"] = []string{"keep-alive"}
	req.Header["Proxy-Connection"] = []string{"keep-alive"}
	req.Header["Transfer-Encoding"] = []string{"chunked"}
	req.Header["Upgrade"] = []string{"chunked"}
	req.Header["Keep-Alive"] = []string{"timeout=5"}

	fields := encodeObservedFields(t, req, false, "", capturedGrokInferenceHeaderOrder())
	got := headerNames(fields)
	if containsAny(got, "connection", "proxy-connection", "transfer-encoding", "upgrade", "keep-alive") {
		t.Fatalf("connection-specific headers leaked into encoded request: %v", got)
	}
	if countName(got, ":protocol") != 1 {
		t.Fatalf(":protocol should appear exactly once as pseudo-header, got %v", got)
	}
}

func TestEncodeHeaders_GrokConfiguredHeadersPreserveRepeatedValueOrder(t *testing.T) {
	req := newCapturedGrokInferenceRequest(t)
	req.Header["X-Grok-Req-Id"] = []string{"req-1", "req-2"}

	fields := encodeObservedFields(t, req, false, "", capturedGrokInferenceHeaderOrder())
	got := headerNameValues(fields[4:])
	idx := indexOf(got, "x-grok-req-id=req-1")
	if idx < 0 || idx+1 >= len(got) || got[idx+1] != "x-grok-req-id=req-2" {
		t.Fatalf("configured repeated values must stay adjacent and ordered, got %v", got)
	}
}

func TestEncodeHeaders_GrokOrderingStillSplitsCookies(t *testing.T) {
	req := newCapturedGrokInferenceRequest(t)
	req.Header["Cookie"] = []string{"a=1; b=2", "c=3"}
	order := capturedGrokInferenceHeaderOrder()
	order.Regular = append(order.Regular, "cookie")

	fields := encodeObservedFields(t, req, false, "", order)
	got := headerNameValues(fields[4:])
	if !containsSubsequence(got, []string{"cookie=a=1", "cookie=b=2", "cookie=c=3"}) {
		t.Fatalf("cookie splitting/order lost under configured ordering, got %v", got)
	}
}

func TestEncodeHeaders_PseudoOnlyOrderingKeepsUpstreamRegularOrder(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://cli-chat-proxy.grok.com/v1/responses", nil)
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}

	upstream := encodeObservedFields(t, req, true, "upstream-control/0.56.0", nil)
	fields := encodeObservedFields(t, req, true, "upstream-control/0.56.0", &HeaderOrder{
		Pseudo: []string{":method", ":scheme", ":authority", ":path"},
	})
	if got, want := headerNames(fields[4:]), headerNames(upstream[4:]); !reflect.DeepEqual(got, want) {
		t.Fatalf("pseudo-only configuration must preserve upstream regular order: got %v want %v", got, want)
	}
}

func TestEncodeHeaders_RegularOnlyOrderingKeepsUpstreamPseudoOrder(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://cli-chat-proxy.grok.com/v1/responses", nil)
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}

	upstream := encodeObservedFields(t, req, true, "upstream-control/0.56.0", nil)
	fields := encodeObservedFields(t, req, true, "upstream-control/0.56.0", &HeaderOrder{
		Regular: []string{"user-agent"},
	})
	if got, want := headerNames(fields[:4]), headerNames(upstream[:4]); !reflect.DeepEqual(got, want) {
		t.Fatalf("regular-only configuration must preserve upstream pseudo order: got %v want %v", got, want)
	}
}

func newCapturedGrokInferenceRequest(t *testing.T) *http.Request {
	t.Helper()

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
	return req
}

func capturedGrokInferenceHeaderOrder() *HeaderOrder {
	return &HeaderOrder{
		Pseudo: []string{
			":method",
			":scheme",
			":authority",
			":path",
		},
		Regular: append([]string{}, capturedGrokInferenceOrdinaryHeaderOrder...),
	}
}

func encodeObservedFields(t *testing.T, req *http.Request, addGzipHeader bool, defaultUserAgent string, headerOrder *HeaderOrder) []hpack.HeaderField {
	t.Helper()

	param := EncodeHeadersParam{
		Request: Request{
			Header:              req.Header.Clone(),
			Trailer:             req.Trailer.Clone(),
			URL:                 req.URL,
			Host:                req.Host,
			Method:              req.Method,
			ActualContentLength: actualContentLengthForTest(req),
		},
		AddGzipHeader:         addGzipHeader,
		DefaultUserAgent:      defaultUserAgent,
		PeerMaxHeaderListSize: 1 << 20,
		HeaderOrder:           headerOrder,
	}

	var block bytes.Buffer
	enc := hpack.NewEncoder(&block)
	if _, err := EncodeHeaders(context.Background(), param, func(name, value string) {
		if writeErr := enc.WriteField(hpack.HeaderField{Name: name, Value: value}); writeErr != nil {
			t.Fatalf("WriteField(%q): %v", name, writeErr)
		}
	}); err != nil {
		t.Fatalf("EncodeHeaders: %v", err)
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

func actualContentLengthForTest(req *http.Request) int64 {
	if req.Body == nil || req.Body == http.NoBody {
		return 0
	}
	if req.ContentLength != 0 {
		return req.ContentLength
	}
	return -1
}

func headerNames(fields []hpack.HeaderField) []string {
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		out = append(out, field.Name)
	}
	return out
}

func headerNameValues(fields []hpack.HeaderField) []string {
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		out = append(out, field.Name+"="+field.Value)
	}
	return out
}

func headerNameValuesFromCapturedOrder() []string {
	return []string{
		"content-type=application/json",
		"user-agent=grok-shell/0.2.112",
		"x-compactions-remaining=1",
		"x-compaction-at=400000",
		"x-grok-client-version=0.2.112",
		"x-grok-user-id=user-123",
		"x-grok-client-identifier=windows-x64",
		"authorization=Bearer token",
		"traceparent=00-0123456789abcdef0123456789abcdef-0123456789abcdef-01",
		"x-grok-conv-id=conv-123",
		"x-grok-req-id=req-123",
		"x-grok-model-override=grok-4.5",
		"x-grok-session-id=conv-123",
		"x-grok-agent-id=agent-123",
		"x-grok-turn-idx=1",
		"accept=text/event-stream",
		"accept-encoding=gzip, br, deflate",
		"content-length=20",
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", raw, err)
	}
	return u
}

func containsAny(items []string, wants ...string) bool {
	for _, item := range items {
		for _, want := range wants {
			if item == want {
				return true
			}
		}
	}
	return false
}

func countName(items []string, want string) int {
	n := 0
	for _, item := range items {
		if item == want {
			n++
		}
	}
	return n
}

func indexOf(items []string, want string) int {
	for i, item := range items {
		if item == want {
			return i
		}
	}
	return -1
}

func containsSubsequence(items, want []string) bool {
	for i := 0; i+len(want) <= len(items); i++ {
		if reflect.DeepEqual(items[i:i+len(want)], want) {
			return true
		}
	}
	return false
}
