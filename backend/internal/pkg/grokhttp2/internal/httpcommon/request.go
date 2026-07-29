// Copyright 2025 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package httpcommon

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptrace"
	"net/textproto"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/grokhttp2/http/httpguts"
	"github.com/Wei-Shaw/sub2api/internal/pkg/grokhttp2/http2/hpack"
)

var (
	ErrRequestHeaderListSize = errors.New("request header list larger than peer's advertised limit")
)

// Request is a subset of http.Request.
// It'd be simpler to pass an *http.Request, of course, but we can't depend on net/http
// without creating a dependency cycle.
type Request struct {
	URL                 *url.URL
	Method              string
	Host                string
	Header              map[string][]string
	Trailer             map[string][]string
	ActualContentLength int64 // 0 means 0, -1 means unknown
}

// EncodeHeadersParam is parameters to EncodeHeaders.
type EncodeHeadersParam struct {
	Request Request

	// AddGzipHeader indicates that an "accept-encoding: gzip" header should be
	// added to the request.
	AddGzipHeader bool

	// PeerMaxHeaderListSize, when non-zero, is the peer's MAX_HEADER_LIST_SIZE setting.
	PeerMaxHeaderListSize uint64

	// DefaultUserAgent is the User-Agent header to send when the request
	// neither contains a User-Agent nor disables it.
	DefaultUserAgent string

	// HeaderOrder optionally overrides request pseudo-header and ordinary
	// header emission order. When nil, request encoding remains upstream-
	// equivalent to golang.org/x/net v0.56.0.
	HeaderOrder *HeaderOrder
}

// HeaderOrder describes the optional request header ordering override used by
// the Grok HTTP/2 fork. It is intentionally limited to pseudo-header order and
// ordinary header order only.
type HeaderOrder struct {
	Pseudo  []string
	Regular []string
}

// EncodeHeadersResult is the result of EncodeHeaders.
type EncodeHeadersResult struct {
	HasBody     bool
	HasTrailers bool
}

// EncodeHeaders constructs request headers common to HTTP/2 and HTTP/3.
// It validates a request and calls headerf with each pseudo-header and header
// for the request.
// The headerf function is called with the validated, canonicalized header name.
func EncodeHeaders(ctx context.Context, param EncodeHeadersParam, headerf func(name, value string)) (res EncodeHeadersResult, _ error) {
	req := param.Request

	// Check for invalid connection-level headers.
	if err := checkConnHeaders(req.Header); err != nil {
		return res, err
	}

	if req.URL == nil {
		return res, errors.New("Request.URL is nil")
	}

	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	host, err := httpguts.PunycodeHostPort(host)
	if err != nil {
		return res, err
	}
	if !httpguts.ValidHostHeader(host) {
		return res, errors.New("invalid Host header")
	}

	// isNormalConnect is true if this is a non-extended CONNECT request.
	isNormalConnect := false
	var protocol string
	if vv := req.Header[":protocol"]; len(vv) > 0 {
		protocol = vv[0]
	}
	if req.Method == "CONNECT" && protocol == "" {
		isNormalConnect = true
	} else if protocol != "" && req.Method != "CONNECT" {
		return res, errors.New("invalid :protocol header in non-CONNECT request")
	}

	// Validate the path, except for non-extended CONNECT requests which have no path.
	var path string
	if !isNormalConnect {
		path = req.URL.RequestURI()
		if !validPseudoPath(path) {
			orig := path
			path = strings.TrimPrefix(path, req.URL.Scheme+"://"+host)
			if !validPseudoPath(path) {
				if req.URL.Opaque != "" {
					return res, fmt.Errorf("invalid request :path %q from URL.Opaque = %q", orig, req.URL.Opaque)
				} else {
					return res, fmt.Errorf("invalid request :path %q", orig)
				}
			}
		}
	}

	// Check for any invalid headers+trailers and return an error before we
	// potentially pollute our hpack state. (We want to be able to
	// continue to reuse the hpack encoder for future requests)
	if err := validateHeaders(req.Header); err != "" {
		return res, fmt.Errorf("invalid HTTP header %s", err)
	}
	if err := validateHeaders(req.Trailer); err != "" {
		return res, fmt.Errorf("invalid HTTP trailer %s", err)
	}

	trailers, err := commaSeparatedTrailers(req.Trailer)
	if err != nil {
		return res, err
	}

	enumerateHeaders := func(f func(name, value string)) {
		emitPseudoHeadersInUpstreamOrder(req, path, host, isNormalConnect, protocol, f)
		emitRegularHeadersInUpstreamOrder(req, trailers, param, f)
	}
	if hasCustomHeaderOrder(param.HeaderOrder) {
		enumerateHeaders = func(f func(name, value string)) {
			enumerateHeadersWithCustomOrder(req, path, host, isNormalConnect, protocol, trailers, param, f)
		}
	}

	// Do a first pass over the headers counting bytes to ensure
	// we don't exceed cc.peerMaxHeaderListSize. This is done as a
	// separate pass before encoding the headers to prevent
	// modifying the hpack state.
	if param.PeerMaxHeaderListSize > 0 {
		hlSize := uint64(0)
		enumerateHeaders(func(name, value string) {
			hf := hpack.HeaderField{Name: name, Value: value}
			hlSize += uint64(hf.Size())
		})

		if hlSize > param.PeerMaxHeaderListSize {
			return res, ErrRequestHeaderListSize
		}
	}

	trace := httptrace.ContextClientTrace(ctx)

	// Header list size is ok. Write the headers.
	enumerateHeaders(func(name, value string) {
		name, ascii := LowerHeader(name)
		if !ascii {
			// Skip writing invalid headers. Per RFC 7540, Section 8.1.2, header
			// field names have to be ASCII characters (just as in HTTP/1.x).
			return
		}

		headerf(name, value)

		if trace != nil && trace.WroteHeaderField != nil {
			trace.WroteHeaderField(name, []string{value})
		}
	})

	res.HasBody = req.ActualContentLength != 0
	res.HasTrailers = trailers != ""
	return res, nil
}

type headerFieldEntry struct {
	name  string
	value string
}

func hasCustomHeaderOrder(order *HeaderOrder) bool {
	return order != nil && (len(order.Pseudo) > 0 || len(order.Regular) > 0)
}

func emitPseudoHeadersInUpstreamOrder(req Request, path, host string, isNormalConnect bool, protocol string, f func(name, value string)) {
	// 8.1.2.3 Request Pseudo-Header Fields
	// The :path pseudo-header field includes the path and query parts of the
	// target URI (the path-absolute production and optionally a '?' character
	// followed by the query production, see Sections 3.3 and 3.4 of
	// [RFC3986]).
	f(":authority", host)
	method := req.Method
	if method == "" {
		method = "GET"
	}
	f(":method", method)
	if !isNormalConnect {
		f(":path", path)
		f(":scheme", req.URL.Scheme)
	}
	if protocol != "" {
		f(":protocol", protocol)
	}
}

func emitRegularHeadersInUpstreamOrder(req Request, trailers string, param EncodeHeadersParam, f func(name, value string)) {
	if trailers != "" {
		f("trailer", trailers)
	}

	var didUA bool
	for k, vv := range req.Header {
		if asciiEqualFold(k, "host") || asciiEqualFold(k, "content-length") {
			// Host is :authority, already sent.
			// Content-Length is automatic, set below.
			continue
		} else if asciiEqualFold(k, "connection") ||
			asciiEqualFold(k, "proxy-connection") ||
			asciiEqualFold(k, "transfer-encoding") ||
			asciiEqualFold(k, "upgrade") ||
			asciiEqualFold(k, "keep-alive") {
			// Per 8.1.2.2 Connection-Specific Header
			// Fields, don't send connection-specific
			// fields. We have already checked if any
			// are error-worthy so just ignore the rest.
			continue
		} else if asciiEqualFold(k, "user-agent") {
			// Match Go's http1 behavior: at most one
			// User-Agent. If set to nil or empty string,
			// then omit it. Otherwise if not mentioned,
			// include the default (below).
			didUA = true
			if len(vv) < 1 {
				continue
			}
			vv = vv[:1]
			if vv[0] == "" {
				continue
			}
		} else if asciiEqualFold(k, "cookie") {
			// Per 8.1.2.5 To allow for better compression efficiency, the
			// Cookie header field MAY be split into separate header fields,
			// each with one or more cookie-pairs.
			for _, v := range vv {
				for {
					p := strings.IndexByte(v, ';')
					if p < 0 {
						break
					}
					f("cookie", v[:p])
					p++
					// strip space after semicolon if any.
					for p+1 <= len(v) && v[p] == ' ' {
						p++
					}
					v = v[p:]
				}
				if len(v) > 0 {
					f("cookie", v)
				}
			}
			continue
		} else if k == ":protocol" {
			// :protocol pseudo-header was already sent above.
			continue
		}

		for _, v := range vv {
			f(k, v)
		}
	}
	if shouldSendReqContentLength(req.Method, req.ActualContentLength) {
		f("content-length", strconv.FormatInt(req.ActualContentLength, 10))
	}
	if param.AddGzipHeader {
		f("accept-encoding", "gzip")
	}
	if !didUA {
		f("user-agent", param.DefaultUserAgent)
	}
}

func enumerateHeadersWithCustomOrder(req Request, path, host string, isNormalConnect bool, protocol, trailers string, param EncodeHeadersParam, f func(name, value string)) {
	pseudoHeaders := configuredPseudoHeaders(req, path, host, isNormalConnect, protocol)
	if len(param.HeaderOrder.Pseudo) > 0 {
		for _, field := range orderPseudoHeaders(pseudoHeaders, param.HeaderOrder) {
			f(field.name, field.value)
		}
	} else {
		emitPseudoHeadersInUpstreamOrder(req, path, host, isNormalConnect, protocol, f)
	}

	if len(param.HeaderOrder.Regular) > 0 {
		for _, field := range orderRegularHeaders(collectRegularHeaders(req, trailers, param), param.HeaderOrder) {
			f(field.name, field.value)
		}
	} else {
		emitRegularHeadersInUpstreamOrder(req, trailers, param, f)
	}
}

func configuredPseudoHeaders(req Request, path, host string, isNormalConnect bool, protocol string) []headerFieldEntry {
	pseudoHeaders := []headerFieldEntry{
		{name: ":authority", value: host},
	}

	method := req.Method
	if method == "" {
		method = "GET"
	}
	pseudoHeaders = append(pseudoHeaders, headerFieldEntry{name: ":method", value: method})

	if !isNormalConnect {
		pseudoHeaders = append(pseudoHeaders,
			headerFieldEntry{name: ":path", value: path},
			headerFieldEntry{name: ":scheme", value: req.URL.Scheme},
		)
	}
	if protocol != "" {
		pseudoHeaders = append(pseudoHeaders, headerFieldEntry{name: ":protocol", value: protocol})
	}
	return pseudoHeaders
}

func orderPseudoHeaders(headers []headerFieldEntry, order *HeaderOrder) []headerFieldEntry {
	if len(headers) == 0 {
		return nil
	}
	available := make(map[string]headerFieldEntry, len(headers))
	for _, field := range headers {
		available[field.name] = field
	}

	var ordered []headerFieldEntry
	seen := make(map[string]struct{}, len(headers))
	for _, name := range order.Pseudo {
		if _, ok := seen[name]; ok {
			continue
		}
		if field, ok := available[name]; ok {
			ordered = append(ordered, field)
			seen[name] = struct{}{}
		}
	}
	for _, name := range []string{":authority", ":method", ":path", ":scheme", ":protocol"} {
		if _, ok := seen[name]; ok {
			continue
		}
		if field, ok := available[name]; ok {
			ordered = append(ordered, field)
		}
	}
	return ordered
}

func collectRegularHeaders(req Request, trailers string, param EncodeHeadersParam) []headerFieldEntry {
	var didUA bool
	grouped := make(map[string][]string, len(req.Header)+4)

	appendValue := func(name, value string) {
		grouped[name] = append(grouped[name], value)
	}

	if trailers != "" {
		appendValue("trailer", trailers)
	}

	for k, vv := range req.Header {
		switch {
		case asciiEqualFold(k, "host") || asciiEqualFold(k, "content-length"):
			continue
		case asciiEqualFold(k, "connection") ||
			asciiEqualFold(k, "proxy-connection") ||
			asciiEqualFold(k, "transfer-encoding") ||
			asciiEqualFold(k, "upgrade") ||
			asciiEqualFold(k, "keep-alive"):
			continue
		case asciiEqualFold(k, "user-agent"):
			didUA = true
			if len(vv) < 1 {
				continue
			}
			if vv[0] == "" {
				continue
			}
			appendValue("user-agent", vv[0])
			continue
		case asciiEqualFold(k, "cookie"):
			for _, v := range vv {
				appendSplitCookieValues(grouped, v)
			}
			continue
		case k == ":protocol":
			continue
		}

		name, ascii := LowerHeader(k)
		if !ascii {
			continue
		}
		for _, v := range vv {
			appendValue(name, v)
		}
	}

	if shouldSendReqContentLength(req.Method, req.ActualContentLength) {
		appendValue("content-length", strconv.FormatInt(req.ActualContentLength, 10))
	}
	if param.AddGzipHeader {
		appendValue("accept-encoding", "gzip")
	}
	if !didUA {
		appendValue("user-agent", param.DefaultUserAgent)
	}

	entries := make([]headerFieldEntry, 0, len(grouped))
	for name, values := range grouped {
		for _, value := range values {
			entries = append(entries, headerFieldEntry{name: name, value: value})
		}
	}
	return entries
}

func appendSplitCookieValues(grouped map[string][]string, value string) {
	for {
		p := strings.IndexByte(value, ';')
		if p < 0 {
			break
		}
		grouped["cookie"] = append(grouped["cookie"], value[:p])
		p++
		for p+1 <= len(value) && value[p] == ' ' {
			p++
		}
		value = value[p:]
	}
	if len(value) > 0 {
		grouped["cookie"] = append(grouped["cookie"], value)
	}
}

func orderRegularHeaders(headers []headerFieldEntry, order *HeaderOrder) []headerFieldEntry {
	if len(headers) == 0 {
		return nil
	}

	grouped := make(map[string][]string, len(headers))
	for _, field := range headers {
		grouped[field.name] = append(grouped[field.name], field.value)
	}

	var ordered []headerFieldEntry
	emit := func(name string) {
		values, ok := grouped[name]
		if !ok {
			return
		}
		for _, value := range values {
			ordered = append(ordered, headerFieldEntry{name: name, value: value})
		}
		delete(grouped, name)
	}

	for _, rawName := range order.Regular {
		name, ascii := LowerHeader(rawName)
		if !ascii {
			continue
		}
		emit(name)
	}

	var tailNames []string
	for name := range grouped {
		tailNames = append(tailNames, name)
	}
	sort.Strings(tailNames)
	for _, name := range tailNames {
		emit(name)
	}
	return ordered
}

// IsRequestGzip reports whether we should add an Accept-Encoding: gzip header
// for a request.
func IsRequestGzip(method string, header map[string][]string, disableCompression bool) bool {
	// TODO(bradfitz): this is a copy of the logic in net/http. Unify somewhere?
	if !disableCompression &&
		len(header["Accept-Encoding"]) == 0 &&
		len(header["Range"]) == 0 &&
		method != "HEAD" {
		// Request gzip only, not deflate. Deflate is ambiguous and
		// not as universally supported anyway.
		// See: https://zlib.net/zlib_faq.html#faq39
		//
		// Note that we don't request this for HEAD requests,
		// due to a bug in nginx:
		//   http://trac.nginx.org/nginx/ticket/358
		//   https://golang.org/issue/5522
		//
		// We don't request gzip if the request is for a range, since
		// auto-decoding a portion of a gzipped document will just fail
		// anyway. See https://golang.org/issue/8923
		return true
	}
	return false
}

// checkConnHeaders checks whether req has any invalid connection-level headers.
//
// https://www.rfc-editor.org/rfc/rfc9114.html#section-4.2-3
// https://www.rfc-editor.org/rfc/rfc9113.html#section-8.2.2-1
//
// Certain headers are special-cased as okay but not transmitted later.
// For example, we allow "Transfer-Encoding: chunked", but drop the header when encoding.
func checkConnHeaders(h map[string][]string) error {
	if vv := h["Upgrade"]; len(vv) > 0 && (vv[0] != "" && vv[0] != "chunked") {
		return fmt.Errorf("invalid Upgrade request header: %q", vv)
	}
	if vv := h["Transfer-Encoding"]; len(vv) > 0 && (len(vv) > 1 || vv[0] != "" && vv[0] != "chunked") {
		return fmt.Errorf("invalid Transfer-Encoding request header: %q", vv)
	}
	if vv := h["Connection"]; len(vv) > 0 && (len(vv) > 1 || vv[0] != "" && !asciiEqualFold(vv[0], "close") && !asciiEqualFold(vv[0], "keep-alive")) {
		return fmt.Errorf("invalid Connection request header: %q", vv)
	}
	return nil
}

func commaSeparatedTrailers(trailer map[string][]string) (string, error) {
	keys := make([]string, 0, len(trailer))
	for k := range trailer {
		k = CanonicalHeader(k)
		switch k {
		case "Transfer-Encoding", "Trailer", "Content-Length":
			return "", fmt.Errorf("invalid Trailer key %q", k)
		}
		keys = append(keys, k)
	}
	if len(keys) > 0 {
		sort.Strings(keys)
		return strings.Join(keys, ","), nil
	}
	return "", nil
}

// validPseudoPath reports whether v is a valid :path pseudo-header
// value. It must be either:
//
//   - a non-empty string starting with '/'
//   - the string '*', for OPTIONS requests.
//
// For now this is only used a quick check for deciding when to clean
// up Opaque URLs before sending requests from the Transport.
// See golang.org/issue/16847
//
// We used to enforce that the path also didn't start with "//", but
// Google's GFE accepts such paths and Chrome sends them, so ignore
// that part of the spec. See golang.org/issue/19103.
func validPseudoPath(v string) bool {
	return (len(v) > 0 && v[0] == '/') || v == "*"
}

func validateHeaders(hdrs map[string][]string) string {
	for k, vv := range hdrs {
		if !httpguts.ValidHeaderFieldName(k) && k != ":protocol" {
			return fmt.Sprintf("name %q", k)
		}
		for _, v := range vv {
			if !httpguts.ValidHeaderFieldValue(v) {
				// Don't include the value in the error,
				// because it may be sensitive.
				return fmt.Sprintf("value for header %q", k)
			}
		}
	}
	return ""
}

// shouldSendReqContentLength reports whether we should send
// a "content-length" request header. This logic is basically a copy of the net/http
// transferWriter.shouldSendContentLength.
// The contentLength is the corrected contentLength (so 0 means actually 0, not unknown).
// -1 means unknown.
func shouldSendReqContentLength(method string, contentLength int64) bool {
	if contentLength > 0 {
		return true
	}
	if contentLength < 0 {
		return false
	}
	// For zero bodies, whether we send a content-length depends on the method.
	// It also kinda doesn't matter for http2 either way, with END_STREAM.
	switch method {
	case "POST", "PUT", "PATCH":
		return true
	default:
		return false
	}
}

// ServerRequestParam is parameters to NewServerRequest.
type ServerRequestParam struct {
	Method                  string
	Scheme, Authority, Path string
	Protocol                string
	Header                  map[string][]string
}

// ServerRequestResult is the result of NewServerRequest.
type ServerRequestResult struct {
	// Various http.Request fields.
	URL        *url.URL
	RequestURI string
	Trailer    map[string][]string

	NeedsContinue bool // client provided an "Expect: 100-continue" header

	// If the request should be rejected, this is a short string suitable for passing
	// to the http2 package's CountError function.
	// It might be a bit odd to return errors this way rather than returning an error,
	// but this ensures we don't forget to include a CountError reason.
	InvalidReason string
}

func NewServerRequest(rp ServerRequestParam) ServerRequestResult {
	needsContinue := httpguts.HeaderValuesContainsToken(rp.Header["Expect"], "100-continue")
	if needsContinue {
		delete(rp.Header, "Expect")
	}
	// Merge Cookie headers into one "; "-delimited value.
	if cookies := rp.Header["Cookie"]; len(cookies) > 1 {
		rp.Header["Cookie"] = []string{strings.Join(cookies, "; ")}
	}

	// Setup Trailers
	var trailer map[string][]string
	for _, v := range rp.Header["Trailer"] {
		for _, key := range strings.Split(v, ",") {
			key = textproto.CanonicalMIMEHeaderKey(textproto.TrimString(key))
			switch key {
			case "Transfer-Encoding", "Trailer", "Content-Length":
				// Bogus. (copy of http1 rules)
				// Ignore.
			default:
				if trailer == nil {
					trailer = make(map[string][]string)
				}
				trailer[key] = nil
			}
		}
	}
	delete(rp.Header, "Trailer")

	// "':authority' MUST NOT include the deprecated userinfo subcomponent
	// for "http" or "https" schemed URIs."
	// https://www.rfc-editor.org/rfc/rfc9113.html#section-8.3.1-2.3.8
	if strings.IndexByte(rp.Authority, '@') != -1 && (rp.Scheme == "http" || rp.Scheme == "https") {
		return ServerRequestResult{
			InvalidReason: "userinfo_in_authority",
		}
	}

	var url_ *url.URL
	var requestURI string
	if rp.Method == "CONNECT" && rp.Protocol == "" {
		url_ = &url.URL{Host: rp.Authority}
		requestURI = rp.Authority // mimic HTTP/1 server behavior
	} else {
		// "[The :path] pseudo-header field MUST NOT be empty [...]"
		// https://www.rfc-editor.org/rfc/rfc9113.html#section-8.3.1-2.4.2
		if rp.Path == "" || (rp.Path[0] != '/' && rp.Path != "*") {
			return ServerRequestResult{
				InvalidReason: "bad_path",
			}
		}

		var err error
		url_, err = url.ParseRequestURI(rp.Path)
		if err != nil {
			return ServerRequestResult{
				InvalidReason: "bad_path",
			}
		}
		requestURI = rp.Path
	}

	return ServerRequestResult{
		URL:           url_,
		NeedsContinue: needsContinue,
		RequestURI:    requestURI,
		Trailer:       trailer,
	}
}
