package grokhttp2

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/grokhttp2/http2/hpack"
)

const (
	sourceAlignedFixturePath   = "testdata/h2-0.4.15-source-aligned.json"
	sourceAlignedHarnessLock   = "testdata/rust-h2-reference/Cargo.lock"
	sourceAlignedHarnessRunner = "cargo run --locked --offline -- <output-path>"
)

type sourceAlignedFixture struct {
	SchemaVersion        int                   `json:"schema_version"`
	Status               string                `json:"status"`
	OfficialWireVerified bool                  `json:"official_wire_verified"`
	Source               sourceAlignedSource   `json:"source"`
	SyntheticHeaders     []fixtureHeaderSchema `json:"synthetic_headers"`
	StatefulCase         sourceAlignedCase     `json:"stateful_case"`
	ContinuationCase     sourceAlignedCase     `json:"continuation_case"`
}

type sourceAlignedSource struct {
	OfficialBinary sourceAlignedBinary         `json:"official_binary"`
	PublicSnapshot sourceAlignedPublicSnapshot `json:"public_snapshot"`
	ReferenceCrate sourceAlignedReferenceCrate `json:"reference_crate"`
	Harness        sourceAlignedHarness        `json:"harness"`
}

type sourceAlignedBinary struct {
	Product          string `json:"product"`
	Version          string `json:"version"`
	InternalRevision string `json:"internal_revision"`
	SHA256           string `json:"sha256"`
	ReqwestVersion   string `json:"reqwest_version"`
	HyperVersion     string `json:"hyper_version"`
	H2Version        string `json:"h2_version"`
	HTTPVersion      string `json:"http_version"`
}

type sourceAlignedPublicSnapshot struct {
	SyncCommit    string `json:"sync_commit"`
	CargoLockHash string `json:"cargo_lock_sha256"`
	Relationship  string `json:"relationship_to_binary_revision"`
}

type sourceAlignedReferenceCrate struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Checksum string `json:"checksum_sha256"`
}

type sourceAlignedHarness struct {
	Generator     string `json:"generator"`
	Command       string `json:"command"`
	CargoLockHash string `json:"cargo_lock_sha256"`
}

type fixtureHeaderSchema struct {
	Name      string `json:"name"`
	Value     string `json:"value"`
	Sensitive bool   `json:"sensitive"`
	Behavior  string `json:"behavior"`
}

type sourceAlignedCase struct {
	Name               string                 `json:"name"`
	ConnectionCount    int                    `json:"connection_count"`
	ClientPrefaceCount int                    `json:"client_preface_count"`
	RequestCount       int                    `json:"request_count"`
	PeerSettings       []sourceAlignedSetting `json:"peer_settings"`
	Streams            []sourceAlignedStream  `json:"streams"`
}

type sourceAlignedSetting struct {
	ID    uint16 `json:"id"`
	Name  string `json:"name"`
	Value uint32 `json:"value"`
}

type sourceAlignedStream struct {
	StreamID           uint32               `json:"stream_id"`
	Frames             []sourceAlignedFrame `json:"frames"`
	HeaderBlockHex     string               `json:"header_block_hex"`
	HeaderBlockSHA256  string               `json:"header_block_sha256"`
	DecodedHeaders     []fixtureHeaderField `json:"decoded_headers,omitempty"`
	DecodedHeaderCount int                  `json:"decoded_header_count"`
}

type sourceAlignedFrame struct {
	Type           string `json:"type"`
	Flags          uint8  `json:"flags"`
	StreamID       uint32 `json:"stream_id"`
	FrameHeaderHex string `json:"frame_header_hex"`
	PayloadHex     string `json:"payload_hex"`
	FragmentHex    string `json:"fragment_hex"`
}

type fixtureHeaderField struct {
	Name      string `json:"name"`
	Value     string `json:"value"`
	Sensitive bool   `json:"sensitive"`
}

type wireRepresentation struct {
	Kind           string
	Index          uint64
	HasLiteralName bool
	NameHuffman    bool
	ValueHuffman   bool
	EncodedStart   int
	EncodedEnd     int
}

func TestSourceAlignedRustH2FixtureContract(t *testing.T) {
	fixture, raw := loadSourceAlignedFixture(t)

	if fixture.SchemaVersion != 2 {
		t.Fatalf("schema_version = %d, want 2", fixture.SchemaVersion)
	}
	if fixture.Status != "SOURCE-ALIGNED / WIRE-UNVERIFIED" {
		t.Fatalf("status = %q, want SOURCE-ALIGNED / WIRE-UNVERIFIED", fixture.Status)
	}
	if fixture.OfficialWireVerified {
		t.Fatal("source-aligned fixture must never claim official wire verification")
	}
	if got, want := len(raw), 166131; got != want {
		t.Fatalf("source-aligned fixture length = %d, want %d", got, want)
	}
	if got, want := fmt.Sprintf("%x", sha256.Sum256(raw)), "1650c6ef13cf0c01d8bd737d4809de42ccb95879f4cf18af6ab4072450a89dcf"; got != want {
		t.Fatalf("source-aligned fixture SHA-256 = %s, want %s", got, want)
	}

	wantSource := sourceAlignedSource{
		OfficialBinary: sourceAlignedBinary{
			Product:          "grok",
			Version:          "0.2.112",
			InternalRevision: "9bbd559437",
			SHA256:           "2469bd182af212c7fcb84f2981999e4e8a6a7a2e4172bad3ae7f787a1f11407c",
			ReqwestVersion:   "0.12.24",
			HyperVersion:     "1.8.1",
			H2Version:        "0.4.15",
			HTTPVersion:      "1.4.0",
		},
		PublicSnapshot: sourceAlignedPublicSnapshot{
			SyncCommit:    "47348d13ec4508dcfe440e34c6d511bb02998fb2",
			CargoLockHash: "852e088a2b4ac3586142592a6c6bbd3f78b8446a8fa8a24b5131baa44b31fd38",
			Relationship:  "independent_public_snapshot_not_binary_revision_equivalence",
		},
		ReferenceCrate: sourceAlignedReferenceCrate{
			Name:     "h2",
			Version:  "0.4.15",
			Checksum: "6cb093c84e8bd9b188d4c4a8cb6579fc016968d14c99882163cd3ff402a4f155",
		},
		Harness: sourceAlignedHarness{
			Generator:     "rust-h2-reference",
			Command:       sourceAlignedHarnessRunner,
			CargoLockHash: fixture.Source.Harness.CargoLockHash,
		},
	}
	if !reflect.DeepEqual(fixture.Source, wantSource) {
		t.Fatalf("source metadata = %#v, want %#v", fixture.Source, wantSource)
	}
	assertSHA256Hex(t, "official binary hash", fixture.Source.OfficialBinary.SHA256)
	assertSHA256Hex(t, "public Cargo.lock hash", fixture.Source.PublicSnapshot.CargoLockHash)
	assertSHA256Hex(t, "reference crate hash", fixture.Source.ReferenceCrate.Checksum)
	assertSHA256Hex(t, "harness Cargo.lock hash", fixture.Source.Harness.CargoLockHash)
	lockRaw, err := os.ReadFile(filepath.FromSlash(sourceAlignedHarnessLock))
	if err != nil {
		t.Fatalf("read source-aligned harness Cargo.lock: %v", err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(lockRaw)); got != fixture.Source.Harness.CargoLockHash {
		t.Fatalf("harness Cargo.lock hash = %s, fixture records %s", got, fixture.Source.Harness.CargoLockHash)
	}
	lockText := string(lockRaw)
	for _, want := range []string{
		"name = \"h2\"\nversion = \"0.4.15\"",
		"checksum = \"6cb093c84e8bd9b188d4c4a8cb6579fc016968d14c99882163cd3ff402a4f155\"",
		"name = \"http\"\nversion = \"1.4.0\"",
	} {
		if !strings.Contains(lockText, want) {
			t.Fatalf("harness Cargo.lock missing pinned provenance %q", want)
		}
	}

	wantHeaders := []fixtureHeaderSchema{
		{Name: "x-fixture-stable", Value: "alpha-alpha-alpha", Behavior: "dynamic_insert_then_indexed"},
		{Name: "x-fixture-repeat", Value: "first-repeat-value", Behavior: "multi_value_first_inserted_or_indexed"},
		{Name: "x-fixture-repeat", Value: "second-repeat-value", Behavior: "multi_value_nameless_without_index"},
		{Name: "content-type", Value: "fixture-content-type-one", Behavior: "dynamic_name_seed"},
		{Name: "content-type", Value: "fixture-content-type-two", Sensitive: true, Behavior: "sensitive_reuses_dynamic_name_index"},
		{Name: "x-fixture-sensitive", Value: "synthetic-sensitive-value", Sensitive: true, Behavior: "never_indexed"},
		{Name: ":path", Value: "/source-aligned", Behavior: "skip_value_index"},
		{Name: "age", Value: "7", Behavior: "skip_value_index"},
		{Name: "authorization", Value: "Synthetic fixture credential", Behavior: "skip_value_index"},
		{Name: "content-length", Value: "0", Behavior: "skip_value_index"},
		{Name: "etag", Value: "\"fixture-etag\"", Behavior: "skip_value_index"},
		{Name: "if-modified-since", Value: "Thu, 01 Jan 1970 00:00:00 GMT", Behavior: "skip_value_index"},
		{Name: "if-none-match", Value: "\"fixture-etag\"", Behavior: "skip_value_index"},
		{Name: "location", Value: "/fixture-location", Behavior: "skip_value_index"},
		{Name: "cookie", Value: "fixture=a", Behavior: "skip_value_index"},
		{Name: "set-cookie", Value: "fixture=a; Path=/", Behavior: "skip_value_index"},
		{Name: "x-fixture-empty", Value: "", Behavior: "empty_literal"},
		{Name: "x-fixture-oversized", Value: strings.Repeat("q", 3022), Behavior: "three_quarter_no_index"},
	}
	if !reflect.DeepEqual(fixture.SyntheticHeaders, wantHeaders) {
		t.Fatalf("synthetic_headers = %#v, want %#v", fixture.SyntheticHeaders, wantHeaders)
	}

	lowerRaw := strings.ToLower(string(raw))
	for _, forbidden := range []string{"bearer ", "api-key", "api_key", "user content", "cli-chat-proxy.grok.com", "x-api-key"} {
		if strings.Contains(lowerRaw, forbidden) {
			t.Fatalf("fixture contains forbidden non-synthetic material %q", forbidden)
		}
	}
}

func TestSourceAlignedRustH2FixtureCoversHPACKBranches(t *testing.T) {
	fixture, _ := loadSourceAlignedFixture(t)
	if got, want := streamIDs(fixture.StatefulCase.Streams), []uint32{1, 3}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stateful stream IDs = %v, want %v", got, want)
	}
	assertCaseTopology(t, fixture.StatefulCase, 2)

	decoder := hpack.NewDecoder(4096, func(hpack.HeaderField) {})
	decoded := make([][]hpack.HeaderField, 0, len(fixture.StatefulCase.Streams))
	representations := make([][]wireRepresentation, 0, len(fixture.StatefulCase.Streams))
	for _, stream := range fixture.StatefulCase.Streams {
		block := validateAndDecodeBlock(t, stream)
		fields, err := decoder.DecodeFull(block)
		if err != nil {
			t.Fatalf("decode stream %d with shared state: %v", stream.StreamID, err)
		}
		if got, want := toFixtureFields(fields), stream.DecodedHeaders; !reflect.DeepEqual(got, want) {
			t.Fatalf("decoded stream %d headers = %#v, want fixture %#v", stream.StreamID, got, want)
		}
		if len(fields) != stream.DecodedHeaderCount {
			t.Fatalf("decoded stream %d header count = %d, want %d", stream.StreamID, len(fields), stream.DecodedHeaderCount)
		}

		reps, err := parseWireRepresentations(block)
		if err != nil {
			t.Fatalf("parse stream %d representations: %v", stream.StreamID, err)
		}
		if len(reps) != len(fields) {
			t.Fatalf("stream %d representation count = %d, decoded header count = %d", stream.StreamID, len(reps), len(fields))
		}
		assertAllRustLiteralsUseHuffman(t, stream.StreamID, reps, fields)
		decoded = append(decoded, fields)
		representations = append(representations, reps)
	}

	assertRepresentation(t, representations[0], decoded[0], "x-fixture-stable", "literal_incremental", false)
	stableSecond := assertRepresentation(t, representations[1], decoded[1], "x-fixture-stable", "indexed", false)
	if stableSecond.Index <= 61 {
		t.Fatalf("second stable header index = %d, want dynamic table index > 61", stableSecond.Index)
	}
	for i := range decoded {
		repeated := representationsForHeader(t, representations[i], decoded[i], "x-fixture-repeat")
		if len(repeated) != 2 {
			t.Fatalf("stream %d repeated header representations = %d, want 2", fixture.StatefulCase.Streams[i].StreamID, len(repeated))
		}
		wantFirstKind := "literal_incremental"
		if i == 1 {
			wantFirstKind = "indexed"
		}
		if repeated[0].Kind != wantFirstKind {
			t.Fatalf("stream %d first repeated value kind = %q, want %q", fixture.StatefulCase.Streams[i].StreamID, repeated[0].Kind, wantFirstKind)
		}
		if repeated[1].Kind != "literal_without" || repeated[1].HasLiteralName || repeated[1].Index <= 61 {
			t.Fatalf("stream %d second repeated value = %#v, want name-less literal using a dynamic index", fixture.StatefulCase.Streams[i].StreamID, repeated[1])
		}
		if i == 1 && repeated[1].Index != repeated[0].Index {
			t.Fatalf("stream %d repeated value index = %d, want prior index %d", fixture.StatefulCase.Streams[i].StreamID, repeated[1].Index, repeated[0].Index)
		}
	}
	assertRepresentation(t, representations[0], decoded[0], "content-type", "literal_incremental", false)
	sensitiveContentType := assertRepresentation(t, representations[1], decoded[1], "content-type", "literal_never", true)
	if sensitiveContentType.Index <= 61 {
		t.Fatalf("sensitive content-type name index = %d, want dynamic index > 61", sensitiveContentType.Index)
	}
	for i := range decoded {
		assertRepresentation(t, representations[i], decoded[i], "x-fixture-sensitive", "literal_never", true)
		for _, name := range []string{
			":path",
			"age",
			"authorization",
			"content-length",
			"etag",
			"if-modified-since",
			"if-none-match",
			"location",
			"cookie",
			"set-cookie",
			"x-fixture-oversized",
		} {
			assertRepresentation(t, representations[i], decoded[i], name, "literal_without", false)
		}
	}
	emptyFirst := assertRepresentation(t, representations[0], decoded[0], "x-fixture-empty", "literal_incremental", false)
	if emptyFirst.ValueHuffman {
		t.Fatal("empty literal unexpectedly carries the Huffman flag")
	}
	assertRepresentation(t, representations[1], decoded[1], "x-fixture-empty", "indexed", false)
}

func TestSourceAlignedRustH2FixtureDocumentsDefaultGoHPACKDifferences(t *testing.T) {
	fixture, _ := loadSourceAlignedFixture(t)
	rustBlocks, rustDecoded := decodeFixtureStatefully(t, fixture.StatefulCase.Streams)

	var goWire bytes.Buffer
	goEncoder := hpack.NewEncoder(&goWire)
	goBlocks := make([][]byte, 0, len(rustDecoded))
	for requestIndex, fields := range rustDecoded {
		goWire.Reset()
		for _, field := range fields {
			if err := goEncoder.WriteField(field); err != nil {
				t.Fatalf("encode Go request %d field %q: %v", requestIndex+1, field.Name, err)
			}
		}
		goBlocks = append(goBlocks, bytes.Clone(goWire.Bytes()))
	}

	goDecoder := hpack.NewDecoder(4096, func(hpack.HeaderField) {})
	for i := range goBlocks {
		if bytes.Equal(goBlocks[i], rustBlocks[i]) {
			t.Fatalf("request %d unexpectedly has byte-identical Rust and Go HPACK blocks", i+1)
		}
		fields, err := goDecoder.DecodeFull(goBlocks[i])
		if err != nil {
			t.Fatalf("decode Go request %d: %v", i+1, err)
		}
		if !reflect.DeepEqual(fields, rustDecoded[i]) {
			t.Fatalf("Go request %d semantic headers differ from Rust: got %#v want %#v", i+1, fields, rustDecoded[i])
		}
	}

	rustRepsFirst := mustParseRepresentations(t, rustBlocks[0])
	rustRepsSecond := mustParseRepresentations(t, rustBlocks[1])
	goRepsFirst := mustParseRepresentations(t, goBlocks[0])
	goRepsSecond := mustParseRepresentations(t, goBlocks[1])

	rustLengthFirst := representationForHeader(t, rustRepsFirst, rustDecoded[0], "content-length")
	goLengthFirst := representationForHeader(t, goRepsFirst, rustDecoded[0], "content-length")
	if rustLengthFirst.Kind != "literal_without" || !rustLengthFirst.ValueHuffman {
		t.Fatalf("Rust content-length request 1 = %#v, want skip/no-index plus unconditional Huffman", rustLengthFirst)
	}
	if goLengthFirst.Kind != "literal_incremental" || goLengthFirst.ValueHuffman {
		t.Fatalf("Go content-length request 1 = %#v, want incremental indexing and raw one-byte value", goLengthFirst)
	}

	rustLengthSecond := representationForHeader(t, rustRepsSecond, rustDecoded[1], "content-length")
	goLengthSecond := representationForHeader(t, goRepsSecond, rustDecoded[1], "content-length")
	if rustLengthSecond.Kind != "literal_without" {
		t.Fatalf("Rust content-length request 2 = %#v, want skip/no-index", rustLengthSecond)
	}
	if goLengthSecond.Kind != "indexed" || goLengthSecond.Index <= 61 {
		t.Fatalf("Go content-length request 2 = %#v, want dynamic indexed reuse", goLengthSecond)
	}
}

func TestSourceAlignedRustH2FixtureContinuationSequence(t *testing.T) {
	fixture, _ := loadSourceAlignedFixture(t)
	assertCaseTopology(t, fixture.ContinuationCase, 1)
	if got, want := streamIDs(fixture.ContinuationCase.Streams), []uint32{1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("continuation stream IDs = %v, want %v", got, want)
	}

	stream := fixture.ContinuationCase.Streams[0]
	if len(stream.Frames) < 2 {
		t.Fatalf("continuation case frame count = %d, want HEADERS plus at least one CONTINUATION", len(stream.Frames))
	}
	block := validateAndDecodeBlock(t, stream)
	if len(block) <= 16_384 {
		t.Fatalf("continuation header block length = %d, want > 16384", len(block))
	}

	count := 0
	decoder := hpack.NewDecoder(4096, func(hpack.HeaderField) { count++ })
	if _, err := decoder.Write(block); err != nil {
		t.Fatalf("decode continuation block: %v", err)
	}
	if err := decoder.Close(); err != nil {
		t.Fatalf("close continuation decoder: %v", err)
	}
	if count != stream.DecodedHeaderCount {
		t.Fatalf("continuation decoded header count = %d, want %d", count, stream.DecodedHeaderCount)
	}
}

func loadSourceAlignedFixture(t *testing.T) (sourceAlignedFixture, []byte) {
	t.Helper()
	path := filepath.FromSlash(sourceAlignedFixturePath)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read source-aligned fixture %s: %v", path, err)
	}
	var fixture sourceAlignedFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("decode source-aligned fixture %s: %v", path, err)
	}
	return fixture, raw
}

func assertCaseTopology(t *testing.T, c sourceAlignedCase, requestCount int) {
	t.Helper()
	if c.ConnectionCount != 1 || c.ClientPrefaceCount != 1 || c.RequestCount != requestCount {
		t.Fatalf("case %q topology = connections:%d prefaces:%d requests:%d, want 1/1/%d", c.Name, c.ConnectionCount, c.ClientPrefaceCount, c.RequestCount, requestCount)
	}
	if len(c.Streams) != requestCount {
		t.Fatalf("case %q streams = %d, want %d", c.Name, len(c.Streams), requestCount)
	}
	wantSettings := []sourceAlignedSetting{
		{ID: 1, Name: "HEADER_TABLE_SIZE", Value: 4096},
		{ID: 5, Name: "MAX_FRAME_SIZE", Value: 16_384},
	}
	if !reflect.DeepEqual(c.PeerSettings, wantSettings) {
		t.Fatalf("case %q peer settings = %#v, want %#v", c.Name, c.PeerSettings, wantSettings)
	}
}

func validateAndDecodeBlock(t *testing.T, stream sourceAlignedStream) []byte {
	t.Helper()
	if len(stream.Frames) == 0 {
		t.Fatalf("stream %d has no captured frames", stream.StreamID)
	}
	var assembled []byte
	for i, frame := range stream.Frames {
		wantType := "CONTINUATION"
		if i == 0 {
			wantType = "HEADERS"
		}
		if frame.Type != wantType {
			t.Fatalf("stream %d frame %d type = %q, want %q", stream.StreamID, i, frame.Type, wantType)
		}
		if frame.StreamID != stream.StreamID {
			t.Fatalf("stream %d frame %d carries stream ID %d", stream.StreamID, i, frame.StreamID)
		}
		isLast := i == len(stream.Frames)-1
		if gotEndHeaders := frame.Flags&0x4 != 0; gotEndHeaders != isLast {
			t.Fatalf("stream %d frame %d END_HEADERS = %v, want %v", stream.StreamID, i, gotEndHeaders, isLast)
		}
		frameHeader, err := hex.DecodeString(frame.FrameHeaderHex)
		if err != nil {
			t.Fatalf("stream %d frame %d frame header hex: %v", stream.StreamID, i, err)
		}
		if len(frameHeader) != 9 {
			t.Fatalf("stream %d frame %d frame header length = %d, want 9", stream.StreamID, i, len(frameHeader))
		}
		payload, err := hex.DecodeString(frame.PayloadHex)
		if err != nil {
			t.Fatalf("stream %d frame %d payload hex: %v", stream.StreamID, i, err)
		}
		payloadLength := int(frameHeader[0])<<16 | int(frameHeader[1])<<8 | int(frameHeader[2])
		if payloadLength != len(payload) {
			t.Fatalf("stream %d frame %d payload length = %d, frame header records %d", stream.StreamID, i, len(payload), payloadLength)
		}
		wantTypeByte := byte(0x9)
		if i == 0 {
			wantTypeByte = 0x1
		}
		if frameHeader[3] != wantTypeByte || frameHeader[4] != frame.Flags {
			t.Fatalf("stream %d frame %d wire type/flags = 0x%02x/0x%02x, want 0x%02x/0x%02x", stream.StreamID, i, frameHeader[3], frameHeader[4], wantTypeByte, frame.Flags)
		}
		wireStreamID := binary.BigEndian.Uint32(frameHeader[5:9])
		if wireStreamID&0x80000000 != 0 || wireStreamID != frame.StreamID {
			t.Fatalf("stream %d frame %d wire stream ID = 0x%08x", stream.StreamID, i, wireStreamID)
		}
		fragment, err := hex.DecodeString(frame.FragmentHex)
		if err != nil {
			t.Fatalf("stream %d frame %d fragment hex: %v", stream.StreamID, i, err)
		}
		wireFragment, err := extractHeaderBlockFragment(frame.Type, frame.Flags, payload)
		if err != nil {
			t.Fatalf("stream %d frame %d extract raw fragment: %v", stream.StreamID, i, err)
		}
		if !bytes.Equal(wireFragment, fragment) {
			t.Fatalf("stream %d frame %d raw payload fragment does not match fragment_hex", stream.StreamID, i)
		}
		assembled = append(assembled, fragment...)
	}

	block, err := hex.DecodeString(stream.HeaderBlockHex)
	if err != nil {
		t.Fatalf("stream %d header block hex: %v", stream.StreamID, err)
	}
	if !bytes.Equal(assembled, block) {
		t.Fatalf("stream %d frame fragments do not assemble to header_block_hex", stream.StreamID)
	}
	gotHash := fmt.Sprintf("%x", sha256.Sum256(block))
	if gotHash != stream.HeaderBlockSHA256 {
		t.Fatalf("stream %d header block hash = %s, want %s", stream.StreamID, gotHash, stream.HeaderBlockSHA256)
	}
	return block
}

func extractHeaderBlockFragment(frameType string, flags uint8, payload []byte) ([]byte, error) {
	if frameType == "CONTINUATION" {
		return payload, nil
	}
	if frameType != "HEADERS" {
		return nil, fmt.Errorf("unsupported header block frame type %q", frameType)
	}
	offset := 0
	padding := 0
	if flags&0x8 != 0 {
		if len(payload) == 0 {
			return nil, fmt.Errorf("PADDED HEADERS has no pad length")
		}
		padding = int(payload[0])
		offset++
	}
	if flags&0x20 != 0 {
		offset += 5
	}
	if offset > len(payload) || padding > len(payload)-offset {
		return nil, fmt.Errorf("invalid HEADERS payload offset=%d padding=%d length=%d", offset, padding, len(payload))
	}
	return payload[offset : len(payload)-padding], nil
}

func decodeFixtureStatefully(t *testing.T, streams []sourceAlignedStream) ([][]byte, [][]hpack.HeaderField) {
	t.Helper()
	decoder := hpack.NewDecoder(4096, func(hpack.HeaderField) {})
	blocks := make([][]byte, 0, len(streams))
	decoded := make([][]hpack.HeaderField, 0, len(streams))
	for _, stream := range streams {
		block := validateAndDecodeBlock(t, stream)
		fields, err := decoder.DecodeFull(block)
		if err != nil {
			t.Fatalf("decode fixture stream %d: %v", stream.StreamID, err)
		}
		blocks = append(blocks, block)
		decoded = append(decoded, fields)
	}
	return blocks, decoded
}

func parseWireRepresentations(block []byte) ([]wireRepresentation, error) {
	var out []wireRepresentation
	for offset := 0; offset < len(block); {
		start := offset
		first := block[offset]
		if first&0x80 != 0 {
			index, next, err := readPrefixedInt(block, offset, 7)
			if err != nil {
				return nil, err
			}
			out = append(out, wireRepresentation{Kind: "indexed", Index: index, EncodedStart: start, EncodedEnd: next})
			offset = next
			continue
		}
		if first&0xe0 == 0x20 {
			_, next, err := readPrefixedInt(block, offset, 5)
			if err != nil {
				return nil, err
			}
			offset = next
			continue
		}

		var kind string
		var prefix uint8
		switch {
		case first&0xc0 == 0x40:
			kind, prefix = "literal_incremental", 6
		case first&0xf0 == 0x00:
			kind, prefix = "literal_without", 4
		case first&0xf0 == 0x10:
			kind, prefix = "literal_never", 4
		default:
			return nil, fmt.Errorf("invalid representation byte 0x%02x at offset %d", first, offset)
		}

		nameIndex, next, err := readPrefixedInt(block, offset, prefix)
		if err != nil {
			return nil, err
		}
		representation := wireRepresentation{Kind: kind, Index: nameIndex, EncodedStart: start}
		offset = next
		if nameIndex == 0 {
			representation.HasLiteralName = true
			representation.NameHuffman, offset, err = readStringLiteral(block, offset)
			if err != nil {
				return nil, err
			}
		}
		representation.ValueHuffman, offset, err = readStringLiteral(block, offset)
		if err != nil {
			return nil, err
		}
		representation.EncodedEnd = offset
		out = append(out, representation)
	}
	return out, nil
}

func readStringLiteral(block []byte, offset int) (bool, int, error) {
	if offset >= len(block) {
		return false, 0, fmt.Errorf("missing string literal at offset %d", offset)
	}
	huffmanEncoded := block[offset]&0x80 != 0
	length, next, err := readPrefixedInt(block, offset, 7)
	if err != nil {
		return false, 0, err
	}
	if length > uint64(len(block)-next) {
		return false, 0, fmt.Errorf("string length %d at offset %d exceeds remaining %d", length, offset, len(block)-next)
	}
	return huffmanEncoded, next + int(length), nil
}

func readPrefixedInt(block []byte, offset int, prefixBits uint8) (uint64, int, error) {
	if offset >= len(block) {
		return 0, 0, fmt.Errorf("missing integer at offset %d", offset)
	}
	mask := byte((1 << prefixBits) - 1)
	value := uint64(block[offset] & mask)
	offset++
	if value < uint64(mask) {
		return value, offset, nil
	}
	for shift := uint(0); ; shift += 7 {
		if offset >= len(block) {
			return 0, 0, fmt.Errorf("truncated integer")
		}
		if shift >= 63 {
			return 0, 0, fmt.Errorf("integer overflow")
		}
		b := block[offset]
		offset++
		value += uint64(b&0x7f) << shift
		if b&0x80 == 0 {
			return value, offset, nil
		}
	}
}

func assertAllRustLiteralsUseHuffman(t *testing.T, streamID uint32, reps []wireRepresentation, fields []hpack.HeaderField) {
	t.Helper()
	for i, representation := range reps {
		field := fields[i]
		if representation.HasLiteralName && field.Name != "" && !representation.NameHuffman {
			t.Fatalf("stream %d field %q literal name is not Huffman encoded", streamID, field.Name)
		}
		if representation.Kind != "indexed" && field.Value != "" && !representation.ValueHuffman {
			t.Fatalf("stream %d field %q literal value is not Huffman encoded", streamID, field.Name)
		}
	}
}

func assertRepresentation(t *testing.T, reps []wireRepresentation, fields []hpack.HeaderField, name, kind string, sensitive bool) wireRepresentation {
	t.Helper()
	representation := representationForHeader(t, reps, fields, name)
	if representation.Kind != kind {
		t.Fatalf("header %q representation = %q, want %q", name, representation.Kind, kind)
	}
	field := fieldByName(t, fields, name)
	if field.Sensitive != sensitive {
		t.Fatalf("header %q sensitive = %v, want %v", name, field.Sensitive, sensitive)
	}
	return representation
}

func representationForHeader(t *testing.T, reps []wireRepresentation, fields []hpack.HeaderField, name string) wireRepresentation {
	t.Helper()
	if len(reps) != len(fields) {
		t.Fatalf("representation/header length mismatch: %d != %d", len(reps), len(fields))
	}
	for i, field := range fields {
		if field.Name == name {
			return reps[i]
		}
	}
	t.Fatalf("header %q not found", name)
	return wireRepresentation{}
}

func representationsForHeader(t *testing.T, reps []wireRepresentation, fields []hpack.HeaderField, name string) []wireRepresentation {
	t.Helper()
	if len(reps) != len(fields) {
		t.Fatalf("representation/header length mismatch: %d != %d", len(reps), len(fields))
	}
	var matches []wireRepresentation
	for i, field := range fields {
		if field.Name == name {
			matches = append(matches, reps[i])
		}
	}
	return matches
}

func fieldByName(t *testing.T, fields []hpack.HeaderField, name string) hpack.HeaderField {
	t.Helper()
	for _, field := range fields {
		if field.Name == name {
			return field
		}
	}
	t.Fatalf("header %q not found", name)
	return hpack.HeaderField{}
}

func mustParseRepresentations(t *testing.T, block []byte) []wireRepresentation {
	t.Helper()
	representations, err := parseWireRepresentations(block)
	if err != nil {
		t.Fatalf("parse HPACK representations: %v", err)
	}
	return representations
}

func toFixtureFields(fields []hpack.HeaderField) []fixtureHeaderField {
	out := make([]fixtureHeaderField, 0, len(fields))
	for _, field := range fields {
		out = append(out, fixtureHeaderField{Name: field.Name, Value: field.Value, Sensitive: field.Sensitive})
	}
	return out
}

func streamIDs(streams []sourceAlignedStream) []uint32 {
	out := make([]uint32, 0, len(streams))
	for _, stream := range streams {
		out = append(out, stream.StreamID)
	}
	return out
}

func assertSHA256Hex(t *testing.T, name, value string) {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size {
		t.Fatalf("%s = %q, want 32-byte lowercase hex SHA-256", name, value)
	}
	if value != strings.ToLower(value) {
		t.Fatalf("%s = %q, want lowercase hex", name, value)
	}
}
