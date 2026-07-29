//go:build !(go1.27 && !http2legacy)

package http2

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/grokhttp2/http2/hpack"
)

type capturedRequestBlock struct {
	streamID uint32
	block    []byte
	frames   []capturedRequestFrame
}

type capturedRequestFrame struct {
	header  []byte
	payload []byte
}

type rawPeerResult struct {
	requests []capturedRequestBlock
	err      error
}

func TestClientConnRuntimeUsesSourceAlignedGrokHPACK(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	peerResult := make(chan rawPeerResult, 1)
	go func() {
		peerSide, err := listener.Accept()
		if err != nil {
			peerResult <- rawPeerResult{err: fmt.Errorf("accept: %w", err)}
			return
		}
		requests, err := captureTransportRequests(peerSide, 2)
		peerResult <- rawPeerResult{requests: requests, err: err}
	}()
	clientSide, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	transport := &Transport{
		HeaderOrder: &HeaderOrder{
			Pseudo:  []string{":method", ":scheme", ":authority", ":path"},
			Regular: []string{"x-fixture-stable", "x-fixture-repeat", "authorization", "user-agent", "content-length"},
		},
	}
	cc, err := transport.newClientConn(clientSide, false, nil)
	if err != nil {
		t.Fatalf("newClientConn: %v", err)
	}
	defer cc.Close()

	for i := 0; i < 2; i++ {
		req, err := http.NewRequest(http.MethodPost, "https://fixture.invalid/source-aligned", nil)
		if err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
		req.Header.Set("X-Fixture-Stable", "alpha-alpha-alpha")
		req.Header.Add("X-Fixture-Repeat", "first-repeat-value")
		req.Header.Add("X-Fixture-Repeat", "second-repeat-value")
		req.Header.Set("Authorization", "Synthetic fixture credential")
		req.Header.Set("User-Agent", "fixture-agent")
		res, err := cc.RoundTrip(req)
		if err != nil {
			t.Fatalf("RoundTrip %d: %v", i+1, err)
		}
		if _, err := io.Copy(io.Discard, res.Body); err != nil {
			t.Fatalf("read response %d: %v", i+1, err)
		}
		if err := res.Body.Close(); err != nil {
			t.Fatalf("close response %d: %v", i+1, err)
		}
	}

	result := <-peerResult
	if result.err != nil {
		t.Fatalf("raw peer: %v", result.err)
	}
	if got, want := requestStreamIDs(result.requests), []uint32{1, 3}; !reflect.DeepEqual(got, want) {
		t.Fatalf("request stream IDs = %v, want %v", got, want)
	}

	decoder := hpack.NewDecoder(4096, func(hpack.HeaderField) {})
	decoded := make([][]hpack.HeaderField, 0, len(result.requests))
	for _, request := range result.requests {
		fields, err := decoder.DecodeFull(request.block)
		if err != nil {
			t.Fatalf("decode stream %d: %v", request.streamID, err)
		}
		decoded = append(decoded, fields)
	}

	var sourceWire bytes.Buffer
	sourceEncoder := hpack.NewGrokClientEncoder(&sourceWire)
	var defaultWire bytes.Buffer
	defaultEncoder := hpack.NewEncoder(&defaultWire)
	defaultDiffers := false
	for i, fields := range decoded {
		sourceBlock := encodeRuntimeFields(t, sourceEncoder, &sourceWire, fields)
		if !bytes.Equal(sourceBlock, result.requests[i].block) {
			t.Fatalf("stream %d runtime HPACK differs from source-aligned encoder", result.requests[i].streamID)
		}
		defaultBlock := encodeRuntimeFields(t, defaultEncoder, &defaultWire, fields)
		defaultDiffers = defaultDiffers || !bytes.Equal(defaultBlock, result.requests[i].block)
	}
	if !defaultDiffers {
		t.Fatal("runtime unexpectedly matches the default Go encoder on both requests")
	}
}

func TestClientConnRuntimeContinuationMatchesRustH2SourceFixture(t *testing.T) {
	fixture := loadContinuationRuntimeFixture(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	peerResult := make(chan rawPeerResult, 1)
	go func() {
		peerSide, err := listener.Accept()
		if err != nil {
			peerResult <- rawPeerResult{err: fmt.Errorf("accept: %w", err)}
			return
		}
		requests, err := captureTransportRequests(peerSide, 1)
		peerResult <- rawPeerResult{requests: requests, err: err}
	}()
	clientSide, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	transport := &Transport{
		DisableCompression: true,
		HeaderOrder: &HeaderOrder{
			Pseudo:  []string{":method", ":scheme", ":authority", ":path"},
			Regular: []string{"x-fixture-continuation"},
		},
	}
	cc, err := transport.newClientConn(clientSide, false, nil)
	if err != nil {
		t.Fatalf("newClientConn: %v", err)
	}
	defer cc.Close()
	req, err := http.NewRequest(http.MethodGet, "https://fixture.invalid/continuation", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("X-Fixture-Continuation", strings.Repeat("~", 10_200))
	req.Header["User-Agent"] = []string{""}
	res, err := cc.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if err := res.Body.Close(); err != nil {
		t.Fatalf("close response: %v", err)
	}

	result := <-peerResult
	if result.err != nil {
		t.Fatalf("raw peer: %v", result.err)
	}
	if len(result.requests) != 1 || result.requests[0].streamID != 1 {
		t.Fatalf("captured requests = %#v, want one request on stream 1", result.requests)
	}
	captured := result.requests[0]
	wantBlock, err := hex.DecodeString(fixture.HeaderBlockHex)
	if err != nil {
		t.Fatalf("decode fixture block: %v", err)
	}
	if !bytes.Equal(captured.block, wantBlock) {
		t.Fatalf("runtime continuation block length = %d, want source fixture length %d", len(captured.block), len(wantBlock))
	}
	if len(captured.frames) != len(fixture.Frames) {
		t.Fatalf("runtime frame count = %d, want %d", len(captured.frames), len(fixture.Frames))
	}
	for i, frame := range captured.frames {
		wantHeader, err := hex.DecodeString(fixture.Frames[i].FrameHeaderHex)
		if err != nil {
			t.Fatalf("decode fixture frame %d header: %v", i, err)
		}
		wantPayload, err := hex.DecodeString(fixture.Frames[i].PayloadHex)
		if err != nil {
			t.Fatalf("decode fixture frame %d payload: %v", i, err)
		}
		if !bytes.Equal(frame.header, wantHeader) || !bytes.Equal(frame.payload, wantPayload) {
			t.Fatalf("runtime frame %d differs from Rust source fixture", i)
		}
	}
}

func captureTransportRequests(conn net.Conn, expectedRequests int) ([]capturedRequestBlock, error) {
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return nil, fmt.Errorf("set raw peer deadline: %w", err)
	}
	preface := make([]byte, len(clientPreface))
	if _, err := io.ReadFull(conn, preface); err != nil {
		return nil, fmt.Errorf("read client preface: %w", err)
	}
	if !bytes.Equal(preface, clientPreface) {
		return nil, fmt.Errorf("client preface mismatch")
	}

	framer := NewFramer(conn, conn)
	frame, err := framer.ReadFrame()
	if err != nil {
		return nil, fmt.Errorf("read initial settings: %w", err)
	}
	settings, ok := frame.(*SettingsFrame)
	if !ok || settings.IsAck() {
		return nil, fmt.Errorf("first frame is %T, want non-ACK SETTINGS", frame)
	}
	frame, err = framer.ReadFrame()
	if err != nil {
		return nil, fmt.Errorf("read initial window update: %w", err)
	}
	if window, ok := frame.(*WindowUpdateFrame); !ok || window.StreamID != 0 {
		return nil, fmt.Errorf("second frame is %T on stream %d, want connection WINDOW_UPDATE", frame, frame.Header().StreamID)
	}
	if err := framer.WriteSettings(
		Setting{ID: SettingHeaderTableSize, Val: 4096},
		Setting{ID: SettingMaxFrameSize, Val: 16_384},
	); err != nil {
		return nil, fmt.Errorf("write peer settings: %w", err)
	}

	requests := make([]capturedRequestBlock, 0, expectedRequests)
	ackedClientSettings := false
	for len(requests) < expectedRequests {
		frame, err := framer.ReadFrame()
		if err != nil {
			return nil, fmt.Errorf("read request frame: %w", err)
		}
		if settings, ok := frame.(*SettingsFrame); ok {
			if settings.IsAck() && !ackedClientSettings {
				if err := framer.WriteSettingsAck(); err != nil {
					return nil, fmt.Errorf("write settings ACK: %w", err)
				}
				ackedClientSettings = true
			}
			continue
		}
		headers, ok := frame.(*HeadersFrame)
		if !ok {
			continue
		}
		fragment := bytes.Clone(headers.HeaderBlockFragment())
		block := bytes.Clone(fragment)
		streamID := headers.StreamID
		capturedFrame, err := captureRequestFrame(headers.FrameHeader, fragment)
		if err != nil {
			return nil, err
		}
		frames := []capturedRequestFrame{capturedFrame}
		for !headers.HeadersEnded() {
			frame, err = framer.ReadFrame()
			if err != nil {
				return nil, fmt.Errorf("read continuation: %w", err)
			}
			continuation, ok := frame.(*ContinuationFrame)
			if !ok || continuation.StreamID != streamID {
				return nil, fmt.Errorf("invalid continuation frame %T on stream %d", frame, frame.Header().StreamID)
			}
			fragment = bytes.Clone(continuation.HeaderBlockFragment())
			block = append(block, fragment...)
			capturedFrame, err := captureRequestFrame(continuation.FrameHeader, fragment)
			if err != nil {
				return nil, err
			}
			frames = append(frames, capturedFrame)
			if continuation.HeadersEnded() {
				break
			}
		}
		requests = append(requests, capturedRequestBlock{streamID: streamID, block: block, frames: frames})
		if err := framer.WriteHeaders(HeadersFrameParam{
			StreamID:      streamID,
			BlockFragment: []byte{0x88},
			EndStream:     true,
			EndHeaders:    true,
		}); err != nil {
			return nil, fmt.Errorf("write response: %w", err)
		}
	}
	return requests, nil
}

func captureRequestFrame(header FrameHeader, fragment []byte) (capturedRequestFrame, error) {
	if header.Type == FrameHeaders && (header.Flags.Has(FlagHeadersPadded) || header.Flags.Has(FlagHeadersPriority)) {
		return capturedRequestFrame{}, fmt.Errorf("runtime HEADERS unexpectedly uses padding or priority")
	}
	if int(header.Length) != len(fragment) {
		return capturedRequestFrame{}, fmt.Errorf("runtime frame payload length = %d, fragment length = %d", header.Length, len(fragment))
	}
	rawHeader := make([]byte, 9)
	rawHeader[0] = byte(header.Length >> 16)
	rawHeader[1] = byte(header.Length >> 8)
	rawHeader[2] = byte(header.Length)
	rawHeader[3] = byte(header.Type)
	rawHeader[4] = byte(header.Flags)
	binary.BigEndian.PutUint32(rawHeader[5:9], header.StreamID)
	return capturedRequestFrame{header: rawHeader, payload: bytes.Clone(fragment)}, nil
}

type continuationRuntimeFixture struct {
	ContinuationCase struct {
		Streams []struct {
			HeaderBlockHex string `json:"header_block_hex"`
			Frames         []struct {
				FrameHeaderHex string `json:"frame_header_hex"`
				PayloadHex     string `json:"payload_hex"`
			} `json:"frames"`
		} `json:"streams"`
	} `json:"continuation_case"`
}

type continuationRuntimeStream struct {
	HeaderBlockHex string
	Frames         []struct {
		FrameHeaderHex string `json:"frame_header_hex"`
		PayloadHex     string `json:"payload_hex"`
	}
}

func loadContinuationRuntimeFixture(t *testing.T) continuationRuntimeStream {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "h2-0.4.15-source-aligned.json"))
	if err != nil {
		t.Fatalf("read source fixture: %v", err)
	}
	var fixture continuationRuntimeFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("decode source fixture: %v", err)
	}
	if len(fixture.ContinuationCase.Streams) != 1 {
		t.Fatalf("continuation fixture streams = %d, want 1", len(fixture.ContinuationCase.Streams))
	}
	stream := fixture.ContinuationCase.Streams[0]
	return continuationRuntimeStream{HeaderBlockHex: stream.HeaderBlockHex, Frames: stream.Frames}
}

func requestStreamIDs(requests []capturedRequestBlock) []uint32 {
	ids := make([]uint32, 0, len(requests))
	for _, request := range requests {
		ids = append(ids, request.streamID)
	}
	return ids
}

func encodeRuntimeFields(t *testing.T, encoder *hpack.Encoder, wire *bytes.Buffer, fields []hpack.HeaderField) []byte {
	t.Helper()
	wire.Reset()
	encoder.BeginHeaderBlock()
	for _, field := range fields {
		if err := encoder.WriteField(field); err != nil {
			t.Fatalf("encode %q: %v", field.Name, err)
		}
	}
	return bytes.Clone(wire.Bytes())
}
