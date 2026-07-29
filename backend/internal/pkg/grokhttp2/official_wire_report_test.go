package grokhttp2

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

const officialWireReportSHA256 = "ab4332a2b2bb7d0411c73cbf92c502ff3ef96881871704079ba8fdd12fd20729"

type officialWireReport struct {
	Status         string `json:"status"`
	OfficialBinary struct {
		Version string `json:"version"`
		SHA256  string `json:"sha256"`
	} `json:"official_binary"`
	Scope struct {
		SameProcess           bool `json:"same_process"`
		SameSession           bool `json:"same_session"`
		SameConnection        bool `json:"same_connection"`
		ConsecutiveOddStreams bool `json:"consecutive_odd_streams"`
		TargetRequestCount    int  `json:"target_request_count"`
	} `json:"scope"`
	Transport struct {
		ALPN               string          `json:"alpn"`
		ClientPrefaceExact bool            `json:"client_preface_exact"`
		SettingsAck        bool            `json:"settings_ack"`
		CloseObserved      bool            `json:"close_observed"`
		StreamIDs          []uint32        `json:"stream_ids"`
		ClientSettings     []SettingRecord `json:"client_settings"`
	} `json:"transport"`
	Requests []struct {
		StreamID                   uint32 `json:"stream_id"`
		Completed                  bool   `json:"completed"`
		FrameCount                 int    `json:"frame_count"`
		OfficialHeaderBlockLength  int    `json:"official_header_block_length"`
		OfficialHeaderBlockSHA256  string `json:"official_header_block_sha256"`
		LocalHeaderBlockLength     int    `json:"local_header_block_length"`
		LocalHeaderBlockSHA256     string `json:"local_header_block_sha256"`
		ByteEqual                  bool   `json:"byte_equal"`
		DynamicReferenceCount      int    `json:"dynamic_reference_count"`
		LiteralNonemptyAllHuffman  bool   `json:"literal_nonempty_all_huffman"`
		SensitiveNeverIndexCount   int    `json:"sensitive_never_index_count"`
		SkipFieldCount             int    `json:"skip_field_count"`
		SkipWithoutNeverIndexCount int    `json:"skip_without_or_never_index_count"`
		SkipPolicyAligned          bool   `json:"skip_policy_aligned"`
	} `json:"requests"`
	Parity struct {
		ComparedBlocks           int  `json:"all_header_blocks_on_target_connection_compared"`
		AllConnectionBlocksEqual bool `json:"all_header_blocks_on_target_connection_equal"`
		TargetBlocksEqual        bool `json:"target_header_blocks_equal"`
		HuffmanAligned           bool `json:"huffman_branch_aligned"`
		DynamicReuseObserved     bool `json:"dynamic_table_reuse_observed"`
		SensitiveObserved        bool `json:"sensitive_never_index_observed"`
		SkipAligned              bool `json:"skip_policy_aligned"`
	} `json:"parity"`
	BinaryAudit struct {
		EmbeddedVersions map[string][]string `json:"embedded_component_versions"`
		RegistryPath     bool                `json:"h2_crates_io_registry_path_observed"`
		SourceAnchors    int                 `json:"h2_source_file_anchor_count"`
		UniqueFiles      int                 `json:"h2_unique_source_file_count"`
		LineAnchors      int                 `json:"h2_line_anchor_count"`
		AnchorsValid     bool                `json:"h2_anchors_valid_against_local_crates_io_source"`
		GitMarkers       int                 `json:"embedded_git_h2_source_marker_count"`
		PublicLockMatch  bool                `json:"public_cargo_lock_sha256_match"`
		RegistryLock     bool                `json:"public_lock_uses_registry_h2_0_4_15"`
		AbsenceProven    bool                `json:"private_patch_absence_proven"`
		PatchEvidence    bool                `json:"replacement_or_patch_evidence_found"`
		ScenarioPatch    bool                `json:"declared_scenario_wire_affecting_patch_observed"`
		Verdict          string              `json:"verdict"`
	} `json:"binary_audit"`
	Safety struct {
		RawHeadersPersisted      bool `json:"raw_headers_persisted"`
		DecodedHeadersPersisted  bool `json:"decoded_headers_persisted"`
		AuthorizationPersisted   bool `json:"authorization_persisted"`
		BodyPersisted            bool `json:"request_or_response_body_persisted"`
		EndpointPersisted        bool `json:"production_endpoint_persisted"`
		ConfigUnchanged          bool `json:"config_unchanged"`
		ParentProxyEnvUnchanged  bool `json:"parent_proxy_environment_unchanged"`
		WindowsRootStoreModified bool `json:"windows_root_store_modified"`
	} `json:"safety"`
}

func TestOfficialWireReportIsPinnedSafeAndScenarioAligned(t *testing.T) {
	raw, err := os.ReadFile("testdata/official-wire-capture/official-wire-report.json")
	if err != nil {
		t.Fatalf("read official wire report: %v", err)
	}
	digest := sha256.Sum256(raw)
	if got := hex.EncodeToString(digest[:]); got != officialWireReportSHA256 {
		t.Fatalf("official wire report SHA-256 = %s, want %s", got, officialWireReportSHA256)
	}

	lower := strings.ToLower(string(raw))
	for _, forbidden := range []string{"bearer ", "access_token", "refresh_token", "id_token", "api.x.ai", "cli-chat-proxy"} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("official wire report contains forbidden material %q", forbidden)
		}
	}

	var report officialWireReport
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatalf("decode official wire report: %v", err)
	}
	if report.Status != "OFFICIAL-WIRE-ALIGNED" {
		t.Fatalf("status = %q, want OFFICIAL-WIRE-ALIGNED", report.Status)
	}
	if report.OfficialBinary.Version != "grok 0.2.112 (9bbd559437)" ||
		report.OfficialBinary.SHA256 != "2469bd182af212c7fcb84f2981999e4e8a6a7a2e4172bad3ae7f787a1f11407c" {
		t.Fatalf("unexpected official binary provenance: %+v", report.OfficialBinary)
	}
	if !report.Scope.SameProcess || !report.Scope.SameSession || !report.Scope.SameConnection ||
		!report.Scope.ConsecutiveOddStreams || report.Scope.TargetRequestCount != 2 {
		t.Fatalf("invalid live capture binding: %+v", report.Scope)
	}
	if report.Transport.ALPN != "h2" || !report.Transport.ClientPrefaceExact ||
		!report.Transport.SettingsAck || !report.Transport.CloseObserved ||
		!reflect.DeepEqual(report.Transport.StreamIDs, []uint32{3, 5}) {
		t.Fatalf("invalid live H2 lifecycle: %+v", report.Transport)
	}
	wantSettings := []SettingRecord{{ID: 2, Value: 0}, {ID: 4, Value: 2097152}, {ID: 5, Value: 16384}, {ID: 6, Value: 16384}}
	if !reflect.DeepEqual(report.Transport.ClientSettings, wantSettings) {
		t.Fatalf("client SETTINGS = %+v, want %+v", report.Transport.ClientSettings, wantSettings)
	}

	if len(report.Requests) != 2 {
		t.Fatalf("request count = %d, want 2", len(report.Requests))
	}
	for _, request := range report.Requests {
		if !request.Completed || request.FrameCount != 1 || !request.ByteEqual ||
			request.OfficialHeaderBlockLength != request.LocalHeaderBlockLength ||
			request.OfficialHeaderBlockSHA256 != request.LocalHeaderBlockSHA256 ||
			!isSHA256Hex(request.OfficialHeaderBlockSHA256) ||
			request.DynamicReferenceCount == 0 || !request.LiteralNonemptyAllHuffman ||
			request.SensitiveNeverIndexCount != 0 || request.SkipFieldCount != 3 ||
			request.SkipWithoutNeverIndexCount != request.SkipFieldCount || !request.SkipPolicyAligned {
			t.Fatalf("invalid request parity summary: %+v", request)
		}
	}
	if report.Parity.ComparedBlocks != 3 || !report.Parity.AllConnectionBlocksEqual ||
		!report.Parity.TargetBlocksEqual || !report.Parity.HuffmanAligned ||
		!report.Parity.DynamicReuseObserved || report.Parity.SensitiveObserved || !report.Parity.SkipAligned {
		t.Fatalf("invalid parity summary: %+v", report.Parity)
	}

	audit := report.BinaryAudit
	if !reflect.DeepEqual(audit.EmbeddedVersions["h2"], []string{"0.4.15"}) ||
		!audit.RegistryPath || audit.SourceAnchors != 415 || audit.UniqueFiles != 31 ||
		audit.LineAnchors != 235 || !audit.AnchorsValid || audit.GitMarkers != 0 ||
		!audit.PublicLockMatch || !audit.RegistryLock || audit.AbsenceProven ||
		audit.PatchEvidence || audit.ScenarioPatch ||
		audit.Verdict != "NO_PATCH_EVIDENCE_AND_DECLARED_SCENARIO_WIRE_ALIGNED" {
		t.Fatalf("invalid binary audit boundary: %+v", audit)
	}
	safety := report.Safety
	if safety.RawHeadersPersisted || safety.DecodedHeadersPersisted || safety.AuthorizationPersisted ||
		safety.BodyPersisted || safety.EndpointPersisted || !safety.ConfigUnchanged ||
		!safety.ParentProxyEnvUnchanged || safety.WindowsRootStoreModified {
		t.Fatalf("invalid evidence safety boundary: %+v", safety)
	}
}
