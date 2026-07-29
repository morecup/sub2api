package grokhttp2

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

type HPACKBranch string

const (
	HPACKBranchLiteralHuffman      HPACKBranch = "literal_huffman"
	HPACKBranchDynamicTableInsert  HPACKBranch = "dynamic_table_insert"
	HPACKBranchSensitiveNeverIndex HPACKBranch = "sensitive_never_index"
	HPACKBranchSkipBranch          HPACKBranch = "skip_branch"
)

const rawEvidenceMarshalError = "raw evidence must not be serialized; sanitize it first"

type Evidence struct {
	ProcessID         string
	ConnectionID      string
	SessionID         string
	ALPN              string
	CertificateSHA256 string
	ClientPreface     []byte
	Settings          []SettingRecord
	SettingsAck       bool
	GoawayObserved    bool
	CloseObserved     bool
	Requests          []RequestEvidence
}

type SettingRecord struct {
	ID    uint16
	Value uint32
}

type RequestEvidence struct {
	RequestID       string
	ProcessID       string
	ConnectionID    string
	SessionID       string
	StreamID        uint32
	Completed       bool
	DecodedHeaders  []DecodedHeader
	RawHeaderBlocks []RawHeaderBlock
	UserContent     string
}

type RawHeaderBlock struct {
	FrameType  string
	StreamID   uint32
	Fragment   []byte
	EndHeaders bool
}

type DecodedHeader struct {
	Name  string
	Value string
}

type SanitizedFixture struct {
	data repositoryFixtureData
}

type repositoryFixtureData struct {
	valid             bool
	processHash       string
	connectionHash    string
	sessionHash       string
	certificateHash   string
	alpnHash          string
	settingsHash      string
	settingsCount     int
	requestCount      int
	sanitizedRequests []sanitizedRequest
}

type sanitizedRequest struct {
	requestHash        string
	streamHash         string
	decodedHeaderCount int
	rawBlockCount      int
	rawBlockLengths    []int
}

func (Evidence) MarshalJSON() ([]byte, error) { return nil, errors.New(rawEvidenceMarshalError) }
func (SettingRecord) MarshalJSON() ([]byte, error) {
	return nil, errors.New(rawEvidenceMarshalError)
}
func (RequestEvidence) MarshalJSON() ([]byte, error) { return nil, errors.New(rawEvidenceMarshalError) }
func (RawHeaderBlock) MarshalJSON() ([]byte, error)  { return nil, errors.New(rawEvidenceMarshalError) }
func (DecodedHeader) MarshalJSON() ([]byte, error)   { return nil, errors.New(rawEvidenceMarshalError) }

func ValidateEvidence(e Evidence) error {
	var issues []string
	if e.ProcessID == "" || e.ConnectionID == "" || e.SessionID == "" || len(e.Requests) < 2 {
		issues = append(issues, "two requests from the same process and connection")
	}
	if e.ALPN != "h2" {
		issues = append(issues, "ALPN h2")
	}
	if !isSHA256Hex(e.CertificateSHA256) {
		issues = append(issues, "certificate hash must be 64 hexadecimal SHA-256 characters")
	}
	if string(e.ClientPreface) != "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n" {
		issues = append(issues, "client preface")
	}
	issues = append(issues, validateSettings(e.Settings)...)
	if !e.SettingsAck {
		issues = append(issues, "SETTINGS ACK")
	}
	if !e.GoawayObserved && !e.CloseObserved {
		issues = append(issues, "GOAWAY or close")
	}

	streamIDs := make(map[uint32]struct{}, len(e.Requests))
	requestIDs := make(map[string]struct{}, len(e.Requests))
	for i, req := range e.Requests {
		issues = append(issues, validateRequestBinding(e, req)...)
		var previousStreamID uint32
		if i > 0 {
			previousStreamID = e.Requests[i-1].StreamID
		}
		issues = append(issues, validateRequestStream(req, streamIDs, previousStreamID, i > 0)...)
		issues = append(issues, validateRawHeaderBlocks(req)...)
		if req.RequestID == "" {
			issues = append(issues, "request IDs")
		} else if _, ok := requestIDs[req.RequestID]; ok {
			issues = append(issues, "distinct request IDs")
		} else {
			requestIDs[req.RequestID] = struct{}{}
		}
	}
	if len(streamIDs) < 2 {
		issues = append(issues, "distinct stream IDs")
	}
	if len(issues) > 0 {
		return errors.New("invalid HPACK evidence: " + strings.Join(uniqueStrings(issues), ", "))
	}
	return nil
}

func ValidateConclusions(e Evidence, conclusions []HPACKBranch) error {
	if err := ValidateEvidence(e); err != nil {
		return err
	}
	if len(conclusions) == 0 {
		return nil
	}
	return fmt.Errorf("indeterminate HPACK conclusions: validator-owned analysis covered no branches, requested %s", joinBranches(conclusions))
}

func SanitizeEvidenceFixture(e Evidence) (SanitizedFixture, error) {
	if err := ValidateEvidence(e); err != nil {
		return SanitizedFixture{}, err
	}
	fixture := SanitizedFixture{data: repositoryFixtureData{
		valid:           true,
		processHash:     shortHash(e.ProcessID),
		connectionHash:  shortHash(e.ConnectionID),
		sessionHash:     shortHash(e.SessionID),
		certificateHash: shortHash(e.CertificateSHA256),
		alpnHash:        shortHash(e.ALPN),
		settingsHash:    shortHash(settingsFingerprint(e.Settings)),
		settingsCount:   len(e.Settings),
		requestCount:    len(e.Requests),
	}}
	for _, req := range e.Requests {
		sanitized := sanitizedRequest{
			requestHash:        shortHash(req.RequestID),
			streamHash:         shortHash(strconv.FormatUint(uint64(req.StreamID), 10)),
			decodedHeaderCount: len(req.DecodedHeaders),
			rawBlockCount:      len(req.RawHeaderBlocks),
		}
		for _, block := range req.RawHeaderBlocks {
			sanitized.rawBlockLengths = append(sanitized.rawBlockLengths, len(block.Fragment))
		}
		fixture.data.sanitizedRequests = append(fixture.data.sanitizedRequests, sanitized)
	}
	return fixture, nil
}

func (f SanitizedFixture) RepositoryText() string {
	if !f.data.valid {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "process_hash:%s\nconnection_hash:%s\nsession_hash:%s\ncertificate_hash:%s\nalpn_hash:%s\n",
		f.data.processHash, f.data.connectionHash, f.data.sessionHash, f.data.certificateHash, f.data.alpnHash)
	fmt.Fprintf(&b, "settings_hash:%s\nsettings_count:%d\nrequest_count:%d\n",
		f.data.settingsHash, f.data.settingsCount, f.data.requestCount)
	for _, req := range f.data.sanitizedRequests {
		fmt.Fprintf(&b, "request_hash:%s\nstream_hash:%s\ndecoded_header_count:%d\nraw_block_count:%d\nraw_block_lengths:%v\n",
			req.requestHash, req.streamHash, req.decodedHeaderCount, req.rawBlockCount, req.rawBlockLengths)
	}
	return b.String()
}

func validateSettings(settings []SettingRecord) []string {
	if len(settings) == 0 {
		return []string{"SETTINGS"}
	}
	var issues []string
	seen := make(map[uint16]struct{}, len(settings))
	for _, setting := range settings {
		if _, ok := seen[setting.ID]; ok {
			issues = append(issues, "SETTINGS duplicate IDs")
			continue
		}
		seen[setting.ID] = struct{}{}
	}
	return issues
}

func validateRequestBinding(e Evidence, req RequestEvidence) []string {
	var issues []string
	if req.ProcessID != e.ProcessID {
		issues = append(issues, "request lifecycle binding same process")
	}
	if req.ConnectionID != e.ConnectionID {
		issues = append(issues, "request lifecycle binding same connection")
	}
	if req.SessionID != e.SessionID {
		issues = append(issues, "request lifecycle binding same session")
	}
	if !req.Completed {
		issues = append(issues, "completed requests")
	}
	return issues
}

func validateRequestStream(req RequestEvidence, streamIDs map[uint32]struct{}, previousStreamID uint32, hasPrevious bool) []string {
	var issues []string
	if req.StreamID == 0 {
		return []string{"distinct stream IDs"}
	}
	const reservedBit = uint32(1 << 31)
	if req.StreamID&reservedBit != 0 {
		issues = append(issues, "client stream IDs must be 31-bit stream IDs")
	}
	if req.StreamID%2 == 0 {
		issues = append(issues, "client-initiated odd stream IDs")
	}
	if hasPrevious && req.StreamID&reservedBit == 0 && previousStreamID&reservedBit == 0 && req.StreamID <= previousStreamID {
		issues = append(issues, "strictly increasing stream IDs in capture order")
	}
	if _, ok := streamIDs[req.StreamID]; ok {
		issues = append(issues, "distinct stream IDs")
	} else {
		streamIDs[req.StreamID] = struct{}{}
	}
	return issues
}

func validateRawHeaderBlocks(req RequestEvidence) []string {
	if len(req.RawHeaderBlocks) == 0 {
		return []string{"raw HEADERS or CONTINUATION bytes"}
	}
	var issues []string
	var sawEndHeaders bool
	for i, block := range req.RawHeaderBlocks {
		if len(block.Fragment) == 0 {
			issues = append(issues, "raw HEADERS or CONTINUATION bytes")
		}
		if block.StreamID != req.StreamID {
			issues = append(issues, "raw HEADERS/CONTINUATION same stream")
		}
		switch {
		case i == 0 && block.FrameType != "HEADERS":
			issues = append(issues, "raw HEADERS must start with HEADERS")
		case i > 0 && block.FrameType != "CONTINUATION":
			issues = append(issues, "raw HEADERS continuation must use CONTINUATION")
		}
		if block.EndHeaders {
			sawEndHeaders = true
			if i != len(req.RawHeaderBlocks)-1 {
				issues = append(issues, "raw HEADERS end_headers must terminate the sequence")
			}
		}
	}
	if !sawEndHeaders {
		issues = append(issues, "raw HEADERS sequence must end with END_HEADERS")
	}
	return issues
}

func settingsFingerprint(settings []SettingRecord) string {
	parts := make([]string, 0, len(settings))
	for _, setting := range settings {
		parts = append(parts, fmt.Sprintf("%d:%d", setting.ID, setting.Value))
	}
	return strings.Join(parts, "|")
}

func isSHA256Hex(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:12])
}

func joinBranches(branches []HPACKBranch) string {
	parts := make([]string, 0, len(branches))
	for _, branch := range branches {
		parts = append(parts, string(branch))
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func uniqueStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, item := range in {
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}

var (
	_ json.Marshaler = Evidence{}
	_ json.Marshaler = SettingRecord{}
	_ json.Marshaler = RequestEvidence{}
	_ json.Marshaler = RawHeaderBlock{}
	_ json.Marshaler = DecodedHeader{}
)
