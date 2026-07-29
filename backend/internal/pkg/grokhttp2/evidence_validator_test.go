package grokhttp2

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestHPACKEvidenceRejectsDecodedHeadersOnly(t *testing.T) {
	evidence := validEvidenceFixture()
	evidence.Requests[0].RawHeaderBlocks = nil
	evidence.Requests[1].RawHeaderBlocks = nil

	err := ValidateEvidence(evidence)
	if err == nil || !strings.Contains(err.Error(), "raw HEADERS or CONTINUATION bytes") {
		t.Fatalf("ValidateEvidence error = %v, want raw header block rejection", err)
	}
}

func TestHPACKEvidenceRequiresSameConnectionTwoRequests(t *testing.T) {
	evidence := validEvidenceFixture()
	evidence.Requests = evidence.Requests[:1]

	err := ValidateEvidence(evidence)
	if err == nil || !strings.Contains(err.Error(), "two requests from the same process and connection") {
		t.Fatalf("ValidateEvidence error = %v, want same-connection two-request rejection", err)
	}
}

func TestHPACKEvidenceRequiresRequestLifecycleBindings(t *testing.T) {
	evidence := validEvidenceFixture()
	evidence.Requests[1].ProcessID = "other-process"
	evidence.Requests[1].ConnectionID = "other-connection"
	evidence.Requests[1].SessionID = "other-session"

	err := ValidateEvidence(evidence)
	if err == nil {
		t.Fatal("ValidateEvidence error = nil, want lifecycle binding rejection")
	}
	for _, want := range []string{"request lifecycle binding", "same process", "same connection", "same session"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ValidateEvidence error = %v, want substring %q", err, want)
		}
	}
}

func TestHPACKEvidenceRequiresLifecycleMetadata(t *testing.T) {
	evidence := validEvidenceFixture()
	evidence.ALPN = ""
	evidence.CertificateSHA256 = ""
	evidence.ClientPreface = nil
	evidence.SettingsAck = false
	evidence.CloseObserved = false
	evidence.GoawayObserved = false

	err := ValidateEvidence(evidence)
	if err == nil {
		t.Fatal("ValidateEvidence error = nil, want lifecycle metadata rejection")
	}
	for _, want := range []string{"ALPN", "certificate hash", "client preface", "SETTINGS ACK", "GOAWAY or close"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ValidateEvidence error = %v, want substring %q", err, want)
		}
	}
}

func TestHPACKEvidenceRejectsWeakSettingsAndRawBlockSemantics(t *testing.T) {
	evidence := validEvidenceFixture()
	evidence.Settings = []SettingRecord{{ID: 0x2, Value: 0}, {ID: 0x2, Value: 1}}
	evidence.Requests[0].RawHeaderBlocks = []RawHeaderBlock{
		{FrameType: "CONTINUATION", StreamID: 1, Fragment: []byte{0x01}},
		{FrameType: "HEADERS", StreamID: 5, Fragment: nil, EndHeaders: true},
	}

	err := ValidateEvidence(evidence)
	if err == nil {
		t.Fatal("ValidateEvidence error = nil, want SETTINGS/raw block rejection")
	}
	for _, want := range []string{"SETTINGS", "duplicate", "raw HEADERS", "CONTINUATION", "same stream"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ValidateEvidence error = %v, want substring %q", err, want)
		}
	}
}

func TestHPACKEvidenceRequiresCompletedDistinctSensibleStreams(t *testing.T) {
	evidence := validEvidenceFixture()
	evidence.Requests[0].Completed = false
	evidence.Requests[1].StreamID = 1

	err := ValidateEvidence(evidence)
	if err == nil {
		t.Fatal("ValidateEvidence error = nil, want completion/stream rejection")
	}
	for _, want := range []string{"completed requests", "distinct stream IDs"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ValidateEvidence error = %v, want substring %q", err, want)
		}
	}

	evidence = validEvidenceFixture()
	evidence.Requests[1].StreamID = 2
	err = ValidateEvidence(evidence)
	if err == nil || !strings.Contains(err.Error(), "client-initiated odd stream IDs") {
		t.Fatalf("ValidateEvidence error = %v, want odd stream rejection", err)
	}
}

func TestHPACKEvidenceRejectsReservedAndRegressingClientStreamIDs(t *testing.T) {
	t.Run("reserved bit", func(t *testing.T) {
		evidence := validEvidenceFixture()
		evidence.Requests[1].StreamID = 0x80000003
		evidence.Requests[1].RawHeaderBlocks[0].StreamID = 0x80000003

		err := ValidateEvidence(evidence)
		if err == nil || !strings.Contains(err.Error(), "31-bit stream IDs") {
			t.Fatalf("ValidateEvidence error = %v, want reserved-bit rejection", err)
		}
	})

	t.Run("capture order regression", func(t *testing.T) {
		evidence := validEvidenceFixture()
		evidence.Requests[0], evidence.Requests[1] = evidence.Requests[1], evidence.Requests[0]

		err := ValidateEvidence(evidence)
		if err == nil || !strings.Contains(err.Error(), "strictly increasing stream IDs in capture order") {
			t.Fatalf("ValidateEvidence error = %v, want capture-order rejection", err)
		}
	})
}

func TestHPACKEvidenceRequiresH2ALPNAndSHA256CertificateHash(t *testing.T) {
	t.Run("ALPN", func(t *testing.T) {
		evidence := validEvidenceFixture()
		evidence.ALPN = "http/1.1"

		err := ValidateEvidence(evidence)
		if err == nil || !strings.Contains(err.Error(), "ALPN h2") {
			t.Fatalf("ValidateEvidence error = %v, want h2 ALPN rejection", err)
		}
	})

	for _, certificateHash := range []string{
		"not-a-sha256",
		strings.Repeat("z", 64),
	} {
		t.Run("certificate hash "+certificateHash[:3], func(t *testing.T) {
			evidence := validEvidenceFixture()
			evidence.CertificateSHA256 = certificateHash

			err := ValidateEvidence(evidence)
			if err == nil || !strings.Contains(err.Error(), "64 hexadecimal SHA-256") {
				t.Fatalf("ValidateEvidence error = %v, want SHA-256 certificate hash rejection", err)
			}
		})
	}
}

func TestEvidenceRejectsDirectJSONSerialization(t *testing.T) {
	evidence := validEvidenceFixture()
	tests := []struct {
		name  string
		value any
	}{
		{name: "Evidence", value: evidence},
		{name: "Evidence pointer", value: &evidence},
		{name: "RequestEvidence", value: evidence.Requests[0]},
		{name: "RequestEvidence pointer", value: &evidence.Requests[0]},
		{name: "RawHeaderBlock", value: evidence.Requests[0].RawHeaderBlocks[0]},
		{name: "RawHeaderBlock pointer", value: &evidence.Requests[0].RawHeaderBlocks[0]},
		{name: "DecodedHeader", value: evidence.Requests[0].DecodedHeaders[0]},
		{name: "DecodedHeader pointer", value: &evidence.Requests[0].DecodedHeaders[0]},
		{name: "SettingRecord", value: evidence.Settings[0]},
		{name: "SettingRecord pointer", value: &evidence.Settings[0]},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := json.Marshal(test.value)
			if err == nil || !strings.Contains(err.Error(), "must not be serialized") {
				t.Fatalf("json.Marshal(%s) error = %v, want serialization rejection", test.name, err)
			}
		})
	}
}

func TestHPACKEvidenceRepositoryFixtureContainsOnlyDerivedData(t *testing.T) {
	evidence := validEvidenceFixture()
	evidence.Requests[0].DecodedHeaders = append(evidence.Requests[0].DecodedHeaders,
		DecodedHeader{Name: "authorization", Value: "Bearer super-secret"},
		DecodedHeader{Name: "x-grok-user-id", Value: "user-12345"},
	)
	evidence.Requests[0].UserContent = "tell me a secret"

	fixture, err := SanitizeEvidenceFixture(evidence)
	if err != nil {
		t.Fatalf("SanitizeEvidenceFixture error = %v", err)
	}

	serialized := fixture.RepositoryText()
	for _, forbidden := range []string{
		"Bearer super-secret",
		"tell me a secret",
		"user-12345",
		"authorization: Bearer",
		"grok.exe:4242",
		"tcp-1",
		"session-1",
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"content-type",
		":method",
		"settings_ack",
		"goaway_observed",
		"close_observed",
		"user_content",
		"completed",
		":true",
		":false",
	} {
		if strings.Contains(serialized, forbidden) {
			t.Fatalf("sanitized fixture leaked %q in %q", forbidden, serialized)
		}
	}
	for _, want := range []string{"process_hash:", "connection_hash:", "session_hash:", "request_hash:", "stream_hash:", "raw_block_lengths:"} {
		if !strings.Contains(serialized, want) {
			t.Fatalf("sanitized fixture = %q, want %q", serialized, want)
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(serialized), "\n") {
		key, _, ok := strings.Cut(line, ":")
		if !ok {
			t.Fatalf("sanitized fixture line = %q, want key:value", line)
		}
		if !strings.HasSuffix(key, "_hash") && !strings.HasSuffix(key, "_count") && key != "raw_block_lengths" {
			t.Fatalf("sanitized fixture key = %q, want only derived hash/count/length fields", key)
		}
	}

	withoutUserContent := validEvidenceFixture()
	withoutUserContent.Requests[0].DecodedHeaders = evidence.Requests[0].DecodedHeaders
	withoutUserContentFixture, err := SanitizeEvidenceFixture(withoutUserContent)
	if err != nil {
		t.Fatalf("SanitizeEvidenceFixture(without user content) error = %v", err)
	}
	if got := withoutUserContentFixture.RepositoryText(); got != serialized {
		t.Fatalf("user content changed repository fixture:\nwith content: %q\nwithout content: %q", serialized, got)
	}
}

func TestSanitizedFixtureCannotBeCallerForged(t *testing.T) {
	fixtureType := reflect.TypeOf(SanitizedFixture{})
	for i := 0; i < fixtureType.NumField(); i++ {
		field := fixtureType.Field(i)
		if field.IsExported() {
			t.Fatalf("SanitizedFixture field %q is exported; callers can forge repository text", field.Name)
		}
	}

	var forged SanitizedFixture
	if got := forged.RepositoryText(); got != "" {
		t.Fatalf("zero-value caller-constructed SanitizedFixture.RepositoryText() = %q, want empty", got)
	}
}

func TestSanitizedFixtureSettingsHashPreservesWireOrder(t *testing.T) {
	first := validEvidenceFixture()
	second := validEvidenceFixture()
	second.Settings[0], second.Settings[1] = second.Settings[1], second.Settings[0]

	firstFixture, err := SanitizeEvidenceFixture(first)
	if err != nil {
		t.Fatalf("SanitizeEvidenceFixture(first) error = %v", err)
	}
	secondFixture, err := SanitizeEvidenceFixture(second)
	if err != nil {
		t.Fatalf("SanitizeEvidenceFixture(second) error = %v", err)
	}
	firstHash := repositoryField(t, firstFixture.RepositoryText(), "settings_hash")
	secondHash := repositoryField(t, secondFixture.RepositoryText(), "settings_hash")
	if firstHash == secondHash {
		t.Fatalf("settings hashes are both %q; want wire-order-sensitive fingerprints", firstHash)
	}
}

func TestHPACKEvidenceAcceptsUniqueExtensionSettingsAndPreservesWireOrder(t *testing.T) {
	first := validEvidenceFixture()
	first.Settings = append(first.Settings,
		SettingRecord{ID: 0x8, Value: 1},
		SettingRecord{ID: 0x9, Value: 1},
	)
	second := first
	second.Settings = append([]SettingRecord(nil), first.Settings...)
	second.Settings[2], second.Settings[3] = second.Settings[3], second.Settings[2]

	if err := ValidateEvidence(first); err != nil {
		t.Fatalf("ValidateEvidence(first) error = %v, want unique extension SETTINGS accepted", err)
	}
	if err := ValidateEvidence(second); err != nil {
		t.Fatalf("ValidateEvidence(second) error = %v, want unique extension SETTINGS accepted", err)
	}

	firstFixture, err := SanitizeEvidenceFixture(first)
	if err != nil {
		t.Fatalf("SanitizeEvidenceFixture(first) error = %v", err)
	}
	secondFixture, err := SanitizeEvidenceFixture(second)
	if err != nil {
		t.Fatalf("SanitizeEvidenceFixture(second) error = %v", err)
	}
	firstHash := repositoryField(t, firstFixture.RepositoryText(), "settings_hash")
	secondHash := repositoryField(t, secondFixture.RepositoryText(), "settings_hash")
	if firstHash == secondHash {
		t.Fatalf("extension SETTINGS hashes are both %q; want wire-order-sensitive fingerprints", firstHash)
	}
}

func TestHPACKAnalysisRejectsCallerClaimsAsIndeterminate(t *testing.T) {
	evidence := validEvidenceFixture()
	err := ValidateConclusions(evidence, []HPACKBranch{
		HPACKBranchLiteralHuffman,
		HPACKBranchDynamicTableInsert,
	})
	if err == nil {
		t.Fatal("ValidateConclusions error = nil, want indeterminate rejection")
	}
	for _, want := range []string{"indeterminate", "literal_huffman", "dynamic_table_insert"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ValidateConclusions error = %v, want substring %q", err, want)
		}
	}
}

func TestHPACKEvidenceAcceptsQualifiedFixtureButOnlyEmptyConclusions(t *testing.T) {
	evidence := validEvidenceFixture()

	if err := ValidateEvidence(evidence); err != nil {
		t.Fatalf("ValidateEvidence error = %v", err)
	}
	if err := ValidateConclusions(evidence, nil); err != nil {
		t.Fatalf("ValidateConclusions error = %v", err)
	}
}

func validEvidenceFixture() Evidence {
	return Evidence{
		ProcessID:         "grok.exe:4242",
		ConnectionID:      "tcp-1",
		SessionID:         "session-1",
		ALPN:              "h2",
		CertificateSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		ClientPreface:     []byte("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"),
		Settings: []SettingRecord{
			{ID: 0x2, Value: 0},
			{ID: 0x4, Value: 2097152},
		},
		SettingsAck:    true,
		GoawayObserved: false,
		CloseObserved:  true,
		Requests: []RequestEvidence{
			{
				RequestID:    "request-1",
				ProcessID:    "grok.exe:4242",
				ConnectionID: "tcp-1",
				SessionID:    "session-1",
				StreamID:     1,
				Completed:    true,
				DecodedHeaders: []DecodedHeader{
					{Name: ":method", Value: "POST"},
					{Name: "content-type", Value: "application/json"},
				},
				RawHeaderBlocks: []RawHeaderBlock{
					{FrameType: "HEADERS", StreamID: 1, Fragment: []byte{0x82, 0x87}, EndHeaders: false},
					{FrameType: "CONTINUATION", StreamID: 1, Fragment: []byte{0x44}, EndHeaders: true},
				},
			},
			{
				RequestID:    "request-2",
				ProcessID:    "grok.exe:4242",
				ConnectionID: "tcp-1",
				SessionID:    "session-1",
				StreamID:     3,
				Completed:    true,
				DecodedHeaders: []DecodedHeader{
					{Name: ":method", Value: "POST"},
					{Name: "content-type", Value: "application/json"},
				},
				RawHeaderBlocks: []RawHeaderBlock{
					{FrameType: "HEADERS", StreamID: 3, Fragment: []byte{0x82, 0x87, 0x45}, EndHeaders: true},
				},
			},
		},
	}
}

func repositoryField(t *testing.T, repositoryText, field string) string {
	t.Helper()
	for _, line := range strings.Split(repositoryText, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok && key == field {
			return value
		}
	}
	t.Fatalf("repository fixture missing field %q in %q", field, repositoryText)
	return ""
}
