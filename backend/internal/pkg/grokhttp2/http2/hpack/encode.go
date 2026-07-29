// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package hpack

import (
	"io"
)

const (
	uint32Max              = ^uint32(0)
	initialHeaderTableSize = 4096
)

type encoderMode uint8

const (
	encoderModeDefault encoderMode = iota
	encoderModeGrokClient
)

type grokTableSizeUpdate struct {
	count    uint8
	min, max uint32
}

type grokHeaderBlockState struct {
	hasLast   bool
	name      string
	nameIndex uint64
}

type Encoder struct {
	dynTab dynamicTable
	// minSize is the minimum table size set by
	// SetMaxDynamicTableSize after the previous Header Table Size
	// Update.
	minSize uint32
	// maxSizeLimit is the maximum table size this encoder
	// supports. This will protect the encoder from too large
	// size.
	maxSizeLimit uint32
	// tableSizeUpdate indicates whether "Header Table Size
	// Update" is required.
	tableSizeUpdate bool
	w               io.Writer
	buf             []byte
	mode            encoderMode
	grokSizeUpdate  grokTableSizeUpdate
	grokHeaderBlock grokHeaderBlockState
}

// NewEncoder returns a new Encoder which performs HPACK encoding. An
// encoded data is written to w.
func NewEncoder(w io.Writer) *Encoder {
	return newEncoder(w, encoderModeDefault)
}

// NewGrokClientEncoder returns an Encoder whose wire decisions match the
// crates.io h2 0.4.15 encoder used by the Grok client reference path.
func NewGrokClientEncoder(w io.Writer) *Encoder {
	return newEncoder(w, encoderModeGrokClient)
}

func newEncoder(w io.Writer, mode encoderMode) *Encoder {
	maxSizeLimit := uint32(initialHeaderTableSize)
	if mode == encoderModeGrokClient {
		maxSizeLimit = uint32Max
	}
	e := &Encoder{
		minSize:         uint32Max,
		maxSizeLimit:    maxSizeLimit,
		tableSizeUpdate: false,
		w:               w,
		mode:            mode,
	}
	e.dynTab.table.init()
	e.dynTab.setMaxSize(initialHeaderTableSize)
	return e
}

// BeginHeaderBlock resets per-block state used by the Grok client encoder.
// It is a no-op for the default encoder.
func (e *Encoder) BeginHeaderBlock() {
	if e.mode == encoderModeGrokClient {
		e.grokHeaderBlock = grokHeaderBlockState{}
	}
}

// WriteField encodes f into a single Write to e's underlying Writer.
// This function may also produce bytes for "Header Table Size Update"
// if necessary. If produced, it is done before encoding f.
func (e *Encoder) WriteField(f HeaderField) error {
	e.buf = e.buf[:0]

	if e.mode == encoderModeGrokClient {
		e.appendGrokTableSizeUpdates()
	} else if e.tableSizeUpdate {
		e.tableSizeUpdate = false
		if e.minSize < e.dynTab.maxSize {
			e.buf = appendTableSize(e.buf, e.minSize)
		}
		e.minSize = uint32Max
		e.buf = appendTableSize(e.buf, e.dynTab.maxSize)
	}
	if e.mode == encoderModeGrokClient && e.grokHeaderBlock.hasLast && e.grokHeaderBlock.name == f.Name {
		if e.grokHeaderBlock.nameIndex == 0 {
			e.buf = appendNewName(e.buf, f, false, true)
		} else {
			e.buf = appendIndexedName(e.buf, f, e.grokHeaderBlock.nameIndex, false, true)
		}
		return e.writeBuf()
	}

	idx, nameValueMatch := e.searchTable(f)
	grokNameIndex := idx
	if nameValueMatch {
		e.buf = appendIndexed(e.buf, idx)
	} else {
		indexing := e.shouldIndex(f)
		if indexing {
			e.dynTab.add(f)
			if e.mode == encoderModeGrokClient {
				grokNameIndex = uint64(staticTable.len()) + 1
			}
		}

		if idx == 0 {
			e.buf = appendNewName(e.buf, f, indexing, e.mode == encoderModeGrokClient)
		} else {
			e.buf = appendIndexedName(e.buf, f, idx, indexing, e.mode == encoderModeGrokClient)
		}
	}
	if e.mode == encoderModeGrokClient {
		e.grokHeaderBlock = grokHeaderBlockState{
			hasLast:   true,
			name:      f.Name,
			nameIndex: grokNameIndex,
		}
	}
	return e.writeBuf()
}

func (e *Encoder) writeBuf() error {
	n, err := e.w.Write(e.buf)
	if err == nil && n != len(e.buf) {
		err = io.ErrShortWrite
	}
	return err
}

// searchTable searches f in both stable and dynamic header tables.
// The static header table is searched first. Only when there is no
// exact match for both name and value, the dynamic header table is
// then searched. If there is no match, i is 0. If both name and value
// match, i is the matched index and nameValueMatch becomes true. If
// only name matches, i points to that index and nameValueMatch
// becomes false.
func (e *Encoder) searchTable(f HeaderField) (i uint64, nameValueMatch bool) {
	if e.mode == encoderModeGrokClient {
		i, nameValueMatch = grokSearchStaticTable(f)
	} else {
		i, nameValueMatch = staticTable.search(f)
	}
	if nameValueMatch {
		return i, true
	}
	if e.mode == encoderModeGrokClient && (grokSkipValueIndex(f.Name) || e.grokFieldTooLarge(f)) {
		return i, false
	}

	dynamicField := f
	if e.mode == encoderModeGrokClient {
		dynamicField.Sensitive = false
	}
	j, nameValueMatch := e.dynTab.table.search(dynamicField)
	if e.mode == encoderModeGrokClient && f.Sensitive && j != 0 {
		return j + uint64(staticTable.len()), nameValueMatch
	}
	if nameValueMatch || (i == 0 && j != 0) {
		return j + uint64(staticTable.len()), nameValueMatch
	}

	return i, false
}

func grokSearchStaticTable(f HeaderField) (uint64, bool) {
	switch f.Name {
	case ":authority":
		return 1, false
	case ":method":
		switch f.Value {
		case "GET":
			return 2, true
		case "POST":
			return 3, true
		default:
			return 2, false
		}
	case ":scheme":
		switch f.Value {
		case "http":
			return 6, true
		case "https":
			return 7, true
		default:
			return 6, false
		}
	case ":path":
		switch f.Value {
		case "/":
			return 4, true
		case "/index.html":
			return 5, true
		default:
			return 4, false
		}
	case ":status":
		switch f.Value {
		case "200":
			return 8, true
		case "204":
			return 9, true
		case "206":
			return 10, true
		case "304":
			return 11, true
		case "400":
			return 12, true
		case "404":
			return 13, true
		case "500":
			return 14, true
		default:
			return 8, false
		}
	case "accept-encoding":
		if f.Value == "gzip, deflate" {
			return 16, true
		}
	default:
	}
	i, _ := staticTable.search(f)
	return i, false
}

// SetMaxDynamicTableSize changes the dynamic header table size to v.
// The actual size is bounded by the value passed to
// SetMaxDynamicTableSizeLimit.
func (e *Encoder) SetMaxDynamicTableSize(v uint32) {
	if v > e.maxSizeLimit {
		v = e.maxSizeLimit
	}
	if e.mode == encoderModeGrokClient {
		e.queueGrokTableSizeUpdate(v)
		return
	}
	if v < e.minSize {
		e.minSize = v
	}
	e.tableSizeUpdate = true
	e.dynTab.setMaxSize(v)
}

func (e *Encoder) queueGrokTableSizeUpdate(v uint32) {
	update := &e.grokSizeUpdate
	switch update.count {
	case 0:
		if v != e.dynTab.maxSize {
			update.count = 1
			update.max = v
		}
	case 1:
		old := update.max
		if v > old {
			if old > e.dynTab.maxSize {
				update.max = v
			} else {
				update.count = 2
				update.min = old
				update.max = v
			}
		} else {
			update.max = v
		}
	case 2:
		if v < update.min {
			update.count = 1
			update.max = v
		} else {
			update.max = v
		}
	}
}

func (e *Encoder) appendGrokTableSizeUpdates() {
	update := e.grokSizeUpdate
	e.grokSizeUpdate = grokTableSizeUpdate{}
	switch update.count {
	case 1:
		e.dynTab.setMaxSize(update.max)
		e.buf = appendTableSize(e.buf, update.max)
	case 2:
		e.dynTab.setMaxSize(update.min)
		e.buf = appendTableSize(e.buf, update.min)
		e.dynTab.setMaxSize(update.max)
		e.buf = appendTableSize(e.buf, update.max)
	}
}

// MaxDynamicTableSize returns the current dynamic header table size.
func (e *Encoder) MaxDynamicTableSize() (v uint32) {
	return e.dynTab.maxSize
}

// SetMaxDynamicTableSizeLimit changes the maximum value that can be
// specified in SetMaxDynamicTableSize to v. By default, it is set to
// 4096, which is the same size of the default dynamic header table
// size described in HPACK specification. If the current maximum
// dynamic header table size is strictly greater than v, "Header Table
// Size Update" will be done in the next WriteField call and the
// maximum dynamic header table size is truncated to v.
func (e *Encoder) SetMaxDynamicTableSizeLimit(v uint32) {
	if e.mode == encoderModeGrokClient {
		e.maxSizeLimit = v
		target := e.dynTab.maxSize
		if e.grokSizeUpdate.count != 0 {
			target = e.grokSizeUpdate.max
		}
		if target > v {
			e.SetMaxDynamicTableSize(v)
		}
		return
	}
	e.maxSizeLimit = v
	if e.dynTab.maxSize > v {
		e.tableSizeUpdate = true
		e.dynTab.setMaxSize(v)
	}
}

// shouldIndex reports whether f should be indexed.
func (e *Encoder) shouldIndex(f HeaderField) bool {
	if e.mode == encoderModeGrokClient {
		return !f.Sensitive && !grokSkipValueIndex(f.Name) && !e.grokFieldTooLarge(f)
	}
	return !f.Sensitive && f.Size() <= e.dynTab.maxSize
}

func (e *Encoder) grokFieldTooLarge(f HeaderField) bool {
	size := uint64(len(f.Name)) + uint64(len(f.Value)) + 32
	return size*4 > uint64(e.dynTab.maxSize)*3
}

func grokSkipValueIndex(name string) bool {
	switch name {
	case ":path",
		"age",
		"authorization",
		"content-length",
		"etag",
		"if-modified-since",
		"if-none-match",
		"location",
		"cookie",
		"set-cookie":
		return true
	default:
		return false
	}
}

// appendIndexed appends index i, as encoded in "Indexed Header Field"
// representation, to dst and returns the extended buffer.
func appendIndexed(dst []byte, i uint64) []byte {
	first := len(dst)
	dst = appendVarInt(dst, 7, i)
	dst[first] |= 0x80
	return dst
}

// appendNewName appends f, as encoded in one of "Literal Header field
// - New Name" representation variants, to dst and returns the
// extended buffer.
//
// If f.Sensitive is true, "Never Indexed" representation is used. If
// f.Sensitive is false and indexing is true, "Incremental Indexing"
// representation is used.
func appendNewName(dst []byte, f HeaderField, indexing, alwaysHuffman bool) []byte {
	dst = append(dst, encodeTypeByte(indexing, f.Sensitive))
	dst = appendHpackString(dst, f.Name, alwaysHuffman)
	return appendHpackString(dst, f.Value, alwaysHuffman)
}

// appendIndexedName appends f and index i referring indexed name
// entry, as encoded in one of "Literal Header field - Indexed Name"
// representation variants, to dst and returns the extended buffer.
//
// If f.Sensitive is true, "Never Indexed" representation is used. If
// f.Sensitive is false and indexing is true, "Incremental Indexing"
// representation is used.
func appendIndexedName(dst []byte, f HeaderField, i uint64, indexing, alwaysHuffman bool) []byte {
	first := len(dst)
	var n byte
	if indexing {
		n = 6
	} else {
		n = 4
	}
	dst = appendVarInt(dst, n, i)
	dst[first] |= encodeTypeByte(indexing, f.Sensitive)
	return appendHpackString(dst, f.Value, alwaysHuffman)
}

// appendTableSize appends v, as encoded in "Header Table Size Update"
// representation, to dst and returns the extended buffer.
func appendTableSize(dst []byte, v uint32) []byte {
	first := len(dst)
	dst = appendVarInt(dst, 5, uint64(v))
	dst[first] |= 0x20
	return dst
}

// appendVarInt appends i, as encoded in variable integer form using n
// bit prefix, to dst and returns the extended buffer.
//
// See
// https://httpwg.org/specs/rfc7541.html#integer.representation
func appendVarInt(dst []byte, n byte, i uint64) []byte {
	k := uint64((1 << n) - 1)
	if i < k {
		return append(dst, byte(i))
	}
	dst = append(dst, byte(k))
	i -= k
	for ; i >= 128; i >>= 7 {
		dst = append(dst, byte(0x80|(i&0x7f)))
	}
	return append(dst, byte(i))
}

// appendHpackString appends s, as encoded in "String Literal"
// representation, to dst and returns the extended buffer.
//
// s will be encoded in Huffman codes only when it produces strictly
// shorter byte string.
func appendHpackString(dst []byte, s string, alwaysHuffman bool) []byte {
	huffmanLength := HuffmanEncodeLength(s)
	if s != "" && (alwaysHuffman || huffmanLength < uint64(len(s))) {
		first := len(dst)
		dst = appendVarInt(dst, 7, huffmanLength)
		dst = AppendHuffmanString(dst, s)
		dst[first] |= 0x80
	} else {
		dst = appendVarInt(dst, 7, uint64(len(s)))
		dst = append(dst, s...)
	}
	return dst
}

// encodeTypeByte returns type byte. If sensitive is true, type byte
// for "Never Indexed" representation is returned. If sensitive is
// false and indexing is true, type byte for "Incremental Indexing"
// representation is returned. Otherwise, type byte for "Without
// Indexing" is returned.
func encodeTypeByte(indexing, sensitive bool) byte {
	if sensitive {
		return 0x10
	}
	if indexing {
		return 0x40
	}
	return 0
}
