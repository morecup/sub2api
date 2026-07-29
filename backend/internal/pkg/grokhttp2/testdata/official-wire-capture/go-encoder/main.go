package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"

	"github.com/Wei-Shaw/sub2api/internal/pkg/grokhttp2/http2/hpack"
)

type request struct {
	Op     string  `json:"op"`
	Size   uint32  `json:"size,omitempty"`
	Fields []field `json:"fields,omitempty"`
}

type field struct {
	Name      string `json:"name_b64"`
	Value     string `json:"value_b64"`
	Sensitive bool   `json:"sensitive"`
}

type response struct {
	OK       bool   `json:"ok"`
	Length   int    `json:"length,omitempty"`
	SHA256   string `json:"sha256,omitempty"`
	BlockB64 string `json:"block_b64,omitempty"`
	Error    string `json:"error,omitempty"`
}

func main() {
	var wire bytes.Buffer
	encoder := hpack.NewGrokClientEncoder(&wire)
	input := bufio.NewScanner(os.Stdin)
	input.Buffer(make([]byte, 64*1024), 32*1024*1024)
	output := json.NewEncoder(os.Stdout)

	for input.Scan() {
		var req request
		if err := json.Unmarshal(input.Bytes(), &req); err != nil {
			writeResponse(output, response{Error: "invalid request"})
			continue
		}

		switch req.Op {
		case "set_table_size":
			encoder.SetMaxDynamicTableSize(req.Size)
			writeResponse(output, response{OK: true})
		case "encode":
			wire.Reset()
			encoder.BeginHeaderBlock()
			if err := encodeFields(encoder, req.Fields); err != nil {
				writeResponse(output, response{Error: err.Error()})
				continue
			}
			block := wire.Bytes()
			digest := sha256.Sum256(block)
			writeResponse(output, response{
				OK:       true,
				Length:   len(block),
				SHA256:   hex.EncodeToString(digest[:]),
				BlockB64: base64.StdEncoding.EncodeToString(block),
			})
		case "close":
			writeResponse(output, response{OK: true})
			return
		default:
			writeResponse(output, response{Error: "unknown operation"})
		}
	}
	if err := input.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "read request:", err)
		os.Exit(1)
	}
}

func encodeFields(encoder *hpack.Encoder, fields []field) error {
	for _, encoded := range fields {
		name, err := base64.StdEncoding.DecodeString(encoded.Name)
		if err != nil {
			return fmt.Errorf("invalid header name")
		}
		value, err := base64.StdEncoding.DecodeString(encoded.Value)
		if err != nil {
			return fmt.Errorf("invalid header value")
		}
		if err := encoder.WriteField(hpack.HeaderField{
			Name:      string(name),
			Value:     string(value),
			Sensitive: encoded.Sensitive,
		}); err != nil {
			return fmt.Errorf("encode header: %w", err)
		}
	}
	return nil
}

func writeResponse(output *json.Encoder, resp response) {
	resp.OK = resp.Error == ""
	if err := output.Encode(resp); err != nil {
		fmt.Fprintln(os.Stderr, "write response:", err)
		os.Exit(1)
	}
}
