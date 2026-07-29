package hpack

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type rustSourceFixture struct {
	StatefulCase struct {
		Streams []rustSourceStream `json:"streams"`
	} `json:"stateful_case"`
	ContinuationCase struct {
		Streams []rustSourceStream `json:"streams"`
	} `json:"continuation_case"`
}

type rustSourceStream struct {
	HeaderBlockHex string        `json:"header_block_hex"`
	DecodedHeaders []HeaderField `json:"decoded_headers"`
}

func TestGrokClientEncoderMatchesRustH2SourceFixture(t *testing.T) {
	fixture := loadRustSourceFixture(t)
	var wire bytes.Buffer
	encoder := NewGrokClientEncoder(&wire)
	for i, stream := range fixture.StatefulCase.Streams {
		got := encodeGrokFields(t, encoder, &wire, stream.DecodedHeaders)
		want, err := hex.DecodeString(stream.HeaderBlockHex)
		if err != nil {
			t.Fatalf("decode stateful stream %d fixture: %v", i, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("stateful stream %d HPACK mismatch: got len=%d sha256=%x, want len=%d sha256=%x", i, len(got), sha256.Sum256(got), len(want), sha256.Sum256(want))
		}
	}

	wire.Reset()
	encoder = NewGrokClientEncoder(&wire)
	stream := fixture.ContinuationCase.Streams[0]
	got := encodeGrokFields(t, encoder, &wire, stream.DecodedHeaders)
	want, err := hex.DecodeString(stream.HeaderBlockHex)
	if err != nil {
		t.Fatalf("decode continuation fixture: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("continuation HPACK mismatch: got len=%d sha256=%x, want len=%d sha256=%x", len(got), sha256.Sum256(got), len(want), sha256.Sum256(want))
	}
}

func TestGrokClientEncoderUsesRustHuffmanAndIndexingRules(t *testing.T) {
	t.Run("non-empty literal always uses Huffman", func(t *testing.T) {
		var wire bytes.Buffer
		encoder := NewGrokClientEncoder(&wire)
		if err := encoder.WriteField(HeaderField{Name: "content-length", Value: "0"}); err != nil {
			t.Fatalf("WriteField: %v", err)
		}
		if got, want := hex.EncodeToString(wire.Bytes()), "0f0d8107"; got != want {
			t.Fatalf("encoded content-length = %s, want %s", got, want)
		}
	})

	t.Run("skip value index list", func(t *testing.T) {
		fields := []HeaderField{
			{Name: ":path", Value: "/source-aligned"},
			{Name: "age", Value: "7"},
			{Name: "authorization", Value: "Synthetic fixture credential"},
			{Name: "content-length", Value: "0"},
			{Name: "etag", Value: "\"fixture-etag\""},
			{Name: "if-modified-since", Value: "Thu, 01 Jan 1970 00:00:00 GMT"},
			{Name: "if-none-match", Value: "\"fixture-etag\""},
			{Name: "location", Value: "/fixture-location"},
			{Name: "cookie", Value: "fixture=a"},
			{Name: "set-cookie", Value: "fixture=a; Path=/"},
		}
		for _, field := range fields {
			t.Run(strings.TrimPrefix(field.Name, ":"), func(t *testing.T) {
				var wire bytes.Buffer
				encoder := NewGrokClientEncoder(&wire)
				first := encodeGrokFields(t, encoder, &wire, []HeaderField{field})
				second := encodeGrokFields(t, encoder, &wire, []HeaderField{field})
				if got := representationType(first); got != "without" {
					t.Fatalf("first representation = %s, want without", got)
				}
				if got := representationType(second); got != "without" {
					t.Fatalf("second representation = %s, want without", got)
				}
			})
		}
	})

	t.Run("sensitive uses never index", func(t *testing.T) {
		var wire bytes.Buffer
		encoder := NewGrokClientEncoder(&wire)
		field := HeaderField{Name: "x-fixture-sensitive", Value: "synthetic-sensitive-value", Sensitive: true}
		for i := 0; i < 2; i++ {
			block := encodeGrokFields(t, encoder, &wire, []HeaderField{field})
			if got := representationType(block); got != "never" {
				t.Fatalf("write %d representation = %s, want never", i+1, got)
			}
		}
	})

	t.Run("empty literal remains raw", func(t *testing.T) {
		var wire bytes.Buffer
		encoder := NewGrokClientEncoder(&wire)
		block := encodeGrokFields(t, encoder, &wire, []HeaderField{{Name: "x-fixture-empty", Value: ""}})
		if block[len(block)-1] != 0 {
			t.Fatalf("empty literal terminator = 0x%02x, want 0", block[len(block)-1])
		}
	})
}

func TestGrokClientEncoderThreeQuarterIndexThreshold(t *testing.T) {
	const name = "x-boundary"
	thresholdValueLength := 3*initialHeaderTableSize/4 - 32 - len(name)
	tests := []struct {
		name       string
		valueDelta int
		firstKind  string
		secondKind string
	}{
		{name: "below", valueDelta: -1, firstKind: "incremental", secondKind: "indexed"},
		{name: "equal", valueDelta: 0, firstKind: "incremental", secondKind: "indexed"},
		{name: "above", valueDelta: 1, firstKind: "without", secondKind: "without"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var wire bytes.Buffer
			encoder := NewGrokClientEncoder(&wire)
			field := HeaderField{Name: name, Value: strings.Repeat("v", thresholdValueLength+tt.valueDelta)}
			first := encodeGrokFields(t, encoder, &wire, []HeaderField{field})
			second := encodeGrokFields(t, encoder, &wire, []HeaderField{field})
			if got := representationType(first); got != tt.firstKind {
				t.Fatalf("first representation = %s, want %s", got, tt.firstKind)
			}
			if got := representationType(second); got != tt.secondKind {
				t.Fatalf("second representation = %s, want %s", got, tt.secondKind)
			}
		})
	}
}

func TestGrokClientEncoderUsesRustPseudoHeaderNameIndices(t *testing.T) {
	tests := []struct {
		field HeaderField
		first byte
	}{
		{field: HeaderField{Name: ":method", Value: "PATCH"}, first: 0x42},
		{field: HeaderField{Name: ":scheme", Value: "fixture"}, first: 0x46},
		{field: HeaderField{Name: ":path", Value: "/source-aligned"}, first: 0x04},
		{field: HeaderField{Name: ":status", Value: "418"}, first: 0x48},
	}
	for _, tt := range tests {
		t.Run(strings.TrimPrefix(tt.field.Name, ":"), func(t *testing.T) {
			var wire bytes.Buffer
			encoder := NewGrokClientEncoder(&wire)
			block := encodeGrokFields(t, encoder, &wire, []HeaderField{tt.field})
			if block[0] != tt.first {
				t.Fatalf("first representation byte = 0x%02x, want 0x%02x", block[0], tt.first)
			}
		})
	}
}

func TestGrokClientEncoderUsesRustStaticFieldMatching(t *testing.T) {
	t.Run("skip field empty value is not a static exact match", func(t *testing.T) {
		var wire bytes.Buffer
		encoder := NewGrokClientEncoder(&wire)
		block := encodeGrokFields(t, encoder, &wire, []HeaderField{{Name: "age", Value: ""}})
		if got := representationType(block); got != "without" {
			t.Fatalf("age empty representation = %s, want without", got)
		}
	})

	t.Run("ordinary empty value is inserted dynamically", func(t *testing.T) {
		var wire bytes.Buffer
		encoder := NewGrokClientEncoder(&wire)
		field := HeaderField{Name: "content-type", Value: ""}
		first := encodeGrokFields(t, encoder, &wire, []HeaderField{field})
		second := encodeGrokFields(t, encoder, &wire, []HeaderField{field})
		if got := representationType(first); got != "incremental" {
			t.Fatalf("first content-type empty representation = %s, want incremental", got)
		}
		if got := representationType(second); got != "indexed" {
			t.Fatalf("second content-type empty representation = %s, want indexed", got)
		}
	})

	t.Run("accept encoding keeps its one static exact value", func(t *testing.T) {
		var wire bytes.Buffer
		encoder := NewGrokClientEncoder(&wire)
		block := encodeGrokFields(t, encoder, &wire, []HeaderField{{Name: "accept-encoding", Value: "gzip, deflate"}})
		if got := representationType(block); got != "indexed" {
			t.Fatalf("accept-encoding representation = %s, want indexed", got)
		}
	})
}

func TestGrokClientEncoderIgnoresUnchangedTableSizeSetting(t *testing.T) {
	field := HeaderField{Name: ":method", Value: "POST"}
	var grokWire bytes.Buffer
	grokEncoder := NewGrokClientEncoder(&grokWire)
	grokEncoder.SetMaxDynamicTableSize(initialHeaderTableSize)
	if got, want := hex.EncodeToString(encodeGrokFields(t, grokEncoder, &grokWire, []HeaderField{field})), "83"; got != want {
		t.Fatalf("Grok encoder unchanged table setting = %s, want %s", got, want)
	}

	var defaultWire bytes.Buffer
	defaultEncoder := NewEncoder(&defaultWire)
	defaultEncoder.SetMaxDynamicTableSize(initialHeaderTableSize)
	if got, want := hex.EncodeToString(encodeGrokFields(t, defaultEncoder, &defaultWire, []HeaderField{field})), "3fe11f83"; got != want {
		t.Fatalf("default encoder unchanged table setting = %s, want %s", got, want)
	}
}

func TestGrokClientEncoderUsesRustTableSizeUpdateQueue(t *testing.T) {
	tests := []struct {
		name    string
		updates []uint32
		want    []uint32
	}{
		{name: "successive increases above current collapse", updates: []uint32{8000, 9000}, want: []uint32{9000}},
		{name: "decrease then increase keeps minimum", updates: []uint32{8000, 100, 8000, 4000}, want: []uint32{100, 4000}},
		{name: "new minimum replaces pair", updates: []uint32{8000, 100, 8000, 4000, 50}, want: []uint32{50}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var wire bytes.Buffer
			encoder := NewGrokClientEncoder(&wire)
			for _, size := range tt.updates {
				encoder.SetMaxDynamicTableSize(size)
			}
			got := encodeGrokFields(t, encoder, &wire, []HeaderField{{Name: ":method", Value: "POST"}})
			var want []byte
			for _, size := range tt.want {
				want = appendTableSize(want, size)
			}
			want = append(want, 0x83)
			if !bytes.Equal(got, want) {
				t.Fatalf("encoded size updates = %x, want %x", got, want)
			}
		})
	}
}

func TestGrokClientEncoderSensitiveFieldCanReuseExactDynamicEntry(t *testing.T) {
	var wire bytes.Buffer
	encoder := NewGrokClientEncoder(&wire)
	field := HeaderField{Name: "x-sensitive-transition", Value: "shared-value"}
	encodeGrokFields(t, encoder, &wire, []HeaderField{field})
	field.Sensitive = true
	if got, want := hex.EncodeToString(encodeGrokFields(t, encoder, &wire, []HeaderField{field})), "be"; got != want {
		t.Fatalf("sensitive exact dynamic match = %s, want indexed representation %s", got, want)
	}
}

func TestGrokClientEncoderSensitiveFieldUsesDynamicNameBeforeStaticName(t *testing.T) {
	var wire bytes.Buffer
	encoder := NewGrokClientEncoder(&wire)
	encodeGrokFields(t, encoder, &wire, []HeaderField{{Name: "content-type", Value: "fixture-content-type-one"}})
	block := encodeGrokFields(t, encoder, &wire, []HeaderField{{Name: "content-type", Value: "fixture-content-type-two", Sensitive: true}})
	if len(block) < 2 || block[0] != 0x1f || block[1] != 0x2f {
		t.Fatalf("sensitive dynamic name prefix = %x, want dynamic index 62 prefix 1f2f", block)
	}
}

func TestDefaultEncoderRetainsUpstreamBehavior(t *testing.T) {
	var wire bytes.Buffer
	encoder := NewEncoder(&wire)
	field := HeaderField{Name: "content-length", Value: "0"}
	first := encodeGrokFields(t, encoder, &wire, []HeaderField{field})
	second := encodeGrokFields(t, encoder, &wire, []HeaderField{field})
	if got, want := hex.EncodeToString(first), "5c0130"; got != want {
		t.Fatalf("default first content-length = %s, want %s", got, want)
	}
	if got := representationType(second); got != "indexed" {
		t.Fatalf("default second representation = %s, want indexed", got)
	}
}

func loadRustSourceFixture(t *testing.T) rustSourceFixture {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "h2-0.4.15-source-aligned.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Rust source fixture: %v", err)
	}
	var fixture rustSourceFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("decode Rust source fixture: %v", err)
	}
	return fixture
}

func encodeGrokFields(t *testing.T, encoder *Encoder, wire *bytes.Buffer, fields []HeaderField) []byte {
	t.Helper()
	wire.Reset()
	encoder.BeginHeaderBlock()
	for _, field := range fields {
		if err := encoder.WriteField(field); err != nil {
			t.Fatalf("WriteField(%q): %v", field.Name, err)
		}
	}
	return bytes.Clone(wire.Bytes())
}

func representationType(block []byte) string {
	if len(block) == 0 {
		return "empty"
	}
	switch first := block[0]; {
	case first&0x80 != 0:
		return "indexed"
	case first&0xc0 == 0x40:
		return "incremental"
	case first&0xf0 == 0x10:
		return "never"
	default:
		return "without"
	}
}
