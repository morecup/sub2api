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

const officialHPACKBranchesReportSHA256 = "dca49d2d1f67bed9e8ad15d22bad1dadc95bafb0280124b1f18186df31e0e3b6"

type officialHPACKBranchesReport struct {
	Status         string `json:"status"`
	OfficialBinary struct {
		Version string `json:"version"`
		SHA256  string `json:"sha256"`
	} `json:"official_binary"`
	Scope struct {
		SyntheticAPIKeyAuthAdvertised    bool     `json:"synthetic_api_key_auth_advertised"`
		TemporaryGrokHome                bool     `json:"temporary_grok_home"`
		LocalEndpointOnly                bool     `json:"local_endpoint_only"`
		TargetRequestCount               int      `json:"target_request_count"`
		TargetRequestStreams             []uint32 `json:"target_request_streams"`
		AllSyntheticAuthValuesMatchedRAM bool     `json:"all_synthetic_auth_values_matched_in_memory"`
	} `json:"scope"`
	Transport struct {
		ALPN                     string `json:"alpn"`
		ClientPrefaceExact       bool   `json:"client_preface_exact"`
		SettingsAck              bool   `json:"settings_ack"`
		ConnectionBlocksCompared int    `json:"connection_header_blocks_compared"`
		AllConnectionBlocksEqual bool   `json:"all_connection_header_blocks_equal"`
		ContinuationAligned      bool   `json:"continuation_aligned"`
		Targets                  []struct {
			StreamID                  uint32   `json:"stream_id"`
			PathLength                int      `json:"path_length"`
			HeaderCount               int      `json:"header_count"`
			HeaderBlockLength         int      `json:"header_block_length"`
			HeaderBlockSHA256         string   `json:"header_block_sha256"`
			LocalHeaderBlockLength    int      `json:"local_header_block_length"`
			LocalHeaderBlockSHA256    string   `json:"local_header_block_sha256"`
			ByteEqual                 bool     `json:"byte_equal"`
			FrameTypes                []string `json:"frame_types"`
			FrameFlags                []uint8  `json:"frame_flags"`
			FrameFragmentLengths      []int    `json:"frame_fragment_lengths"`
			LocalFrameFragmentLengths []int    `json:"local_frame_fragment_lengths"`
			FrameSplitEqual           bool     `json:"frame_split_equal"`
			ContinuationCount         int      `json:"continuation_count"`
			AuthRepresentation        string   `json:"auth_header_representation"`
			AuthDecoderSensitive      bool     `json:"auth_header_decoder_sensitive"`
			AuthValueHuffman          bool     `json:"auth_value_huffman"`
			NeverIndexCount           int      `json:"never_index_count"`
		} `json:"targets"`
	} `json:"transport"`
	SensitiveBranch struct {
		APIKeyLiveNeverIndexCount int    `json:"api_key_live_never_index_count"`
		Classification            string `json:"classification"`
		EvidenceComplete          bool   `json:"not_applicable_evidence_complete"`
		OfficialBuildersSensitive bool   `json:"official_auth_builders_mark_sensitive"`
		OAuthBaseline             struct {
			ReportSHA256   string `json:"report_sha256"`
			RequestCount   int    `json:"request_count"`
			NeverIndex     int    `json:"never_index_count"`
			AllBlocksEqual bool   `json:"all_blocks_byte_equal"`
		} `json:"oauth_baseline"`
		PublicSourceAudit struct {
			Commit                   string `json:"commit"`
			RustFilesScanned         int    `json:"rust_files_scanned"`
			SetSensitiveCalls        int    `json:"set_sensitive_call_count"`
			HeaderSensitiveCalls     int    `json:"header_sensitive_call_count"`
			SensitiveMarkingObserved bool   `json:"sensitive_marking_api_observed"`
		} `json:"public_source_audit"`
	} `json:"sensitive_branch"`
	NetworkIsolation struct {
		AcceptedLocal      int  `json:"accepted_local_connect_count"`
		BlockedNonlocal    int  `json:"blocked_nonlocal_connect_count"`
		NonlocalWasAllowed bool `json:"nonlocal_connect_allowed"`
	} `json:"network_isolation"`
	Safety struct {
		RawHeadersPersisted          bool `json:"raw_headers_persisted"`
		DecodedHeadersPersisted      bool `json:"decoded_headers_persisted"`
		AuthorizationPersisted       bool `json:"authorization_persisted"`
		BodyPersisted                bool `json:"request_or_response_body_persisted"`
		EndpointPersisted            bool `json:"production_endpoint_persisted"`
		SyntheticKeyPersisted        bool `json:"synthetic_key_persisted_outside_temporary_home"`
		UserAuthFileRead             bool `json:"user_auth_file_read"`
		UserAuthFileModified         bool `json:"user_auth_file_modified"`
		UserConfigUnchanged          bool `json:"user_config_unchanged"`
		ParentProxyEnvironmentStable bool `json:"parent_proxy_environment_unchanged"`
		WindowsRootStoreModified     bool `json:"windows_root_store_modified"`
	} `json:"safety"`
	Limitations struct {
		PrivateBinarySourceUnavailable  bool `json:"private_binary_source_is_not_available"`
		PublicAndPrivateRevisionsDiffer bool `json:"public_source_commit_differs_from_installed_private_revision"`
		LocalNeverIndexSupportRetained  bool `json:"never_index_support_remains_in_local_encoder_for_future_sensitive_inputs"`
		ConclusionScopedToAuthBuilders  bool `json:"conclusion_is_scoped_to_current_official_oauth_and_api_key_request_builders"`
	} `json:"limitations"`
}

func TestOfficialHPACKBranchesReportIsPinnedSafeAndAligned(t *testing.T) {
	raw, err := os.ReadFile("testdata/official-wire-capture/official-hpack-branches-report.json")
	if err != nil {
		t.Fatalf("read official HPACK branches report: %v", err)
	}
	digest := sha256.Sum256(raw)
	if got := hex.EncodeToString(digest[:]); got != officialHPACKBranchesReportSHA256 {
		t.Fatalf("official HPACK branches report SHA-256 = %s, want %s", got, officialHPACKBranchesReportSHA256)
	}

	lower := strings.ToLower(string(raw))
	for _, forbidden := range []string{
		"bearer ", "access_token", "refresh_token", "id_token", "api.x.ai",
		"cli-chat-proxy", "xai-synthetic-hpack-", strings.Repeat("~", 20),
	} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("official HPACK branches report contains forbidden material %q", forbidden)
		}
	}

	var report officialHPACKBranchesReport
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatalf("decode official HPACK branches report: %v", err)
	}
	if report.Status != "OFFICIAL-CONTINUATION-ALIGNED_SENSITIVE-NEVER-INDEX-NOT-APPLICABLE" {
		t.Fatalf("unexpected report status %q", report.Status)
	}
	if report.OfficialBinary.Version != "grok 0.2.112 (9bbd559437)" ||
		report.OfficialBinary.SHA256 != "2469bd182af212c7fcb84f2981999e4e8a6a7a2e4172bad3ae7f787a1f11407c" {
		t.Fatalf("unexpected official binary provenance: %+v", report.OfficialBinary)
	}

	scope := report.Scope
	if !scope.SyntheticAPIKeyAuthAdvertised || !scope.TemporaryGrokHome || !scope.LocalEndpointOnly ||
		scope.TargetRequestCount != 2 || !reflect.DeepEqual(scope.TargetRequestStreams, []uint32{1, 3}) ||
		!scope.AllSyntheticAuthValuesMatchedRAM {
		t.Fatalf("invalid isolated capture scope: %+v", scope)
	}

	transport := report.Transport
	if transport.ALPN != "h2" || !transport.ClientPrefaceExact || !transport.SettingsAck ||
		transport.ConnectionBlocksCompared != 2 || !transport.AllConnectionBlocksEqual ||
		!transport.ContinuationAligned || len(transport.Targets) != 2 {
		t.Fatalf("invalid official branch transport summary: %+v", transport)
	}
	for index, target := range transport.Targets {
		wantStreamID := uint32(1 + index*2)
		if target.StreamID != wantStreamID || target.PathLength != 14014 || target.HeaderCount == 0 ||
			target.HeaderBlockLength <= 16384 || target.HeaderBlockLength != target.LocalHeaderBlockLength ||
			target.HeaderBlockSHA256 != target.LocalHeaderBlockSHA256 || !isSHA256Hex(target.HeaderBlockSHA256) ||
			!target.ByteEqual || !target.FrameSplitEqual || target.ContinuationCount != 1 ||
			target.AuthRepresentation != "literal_without_indexing" || target.AuthDecoderSensitive ||
			!target.AuthValueHuffman || target.NeverIndexCount != 0 ||
			!reflect.DeepEqual(target.FrameTypes, []string{"HEADERS", "CONTINUATION"}) ||
			!reflect.DeepEqual(target.FrameFlags, []uint8{0, 4}) ||
			!reflect.DeepEqual(target.FrameFragmentLengths, target.LocalFrameFragmentLengths) ||
			len(target.FrameFragmentLengths) != 2 || target.FrameFragmentLengths[0] != 16384 ||
			target.FrameFragmentLengths[0]+target.FrameFragmentLengths[1] != target.HeaderBlockLength {
			t.Fatalf("invalid target %d parity summary: %+v", index, target)
		}
	}
	if transport.Targets[1].HeaderBlockLength >= transport.Targets[0].HeaderBlockLength {
		t.Fatal("second official block did not reflect same-connection HPACK state reuse")
	}

	sensitive := report.SensitiveBranch
	if sensitive.APIKeyLiveNeverIndexCount != 0 || sensitive.OfficialBuildersSensitive ||
		!sensitive.EvidenceComplete ||
		sensitive.Classification != "NOT_APPLICABLE_FOR_OBSERVED_OFFICIAL_GROK_AUTH_BUILDERS" ||
		sensitive.OAuthBaseline.ReportSHA256 != officialWireReportSHA256 ||
		sensitive.OAuthBaseline.RequestCount != 2 || sensitive.OAuthBaseline.NeverIndex != 0 ||
		!sensitive.OAuthBaseline.AllBlocksEqual ||
		sensitive.PublicSourceAudit.Commit != "47348d13ec4508dcfe440e34c6d511bb02998fb2" ||
		sensitive.PublicSourceAudit.RustFilesScanned == 0 ||
		sensitive.PublicSourceAudit.SetSensitiveCalls != 0 ||
		sensitive.PublicSourceAudit.HeaderSensitiveCalls != 0 ||
		sensitive.PublicSourceAudit.SensitiveMarkingObserved {
		t.Fatalf("invalid observed sensitive-branch classification: %+v", sensitive)
	}

	isolation := report.NetworkIsolation
	if isolation.AcceptedLocal == 0 || isolation.BlockedNonlocal == 0 || isolation.NonlocalWasAllowed {
		t.Fatalf("invalid network isolation summary: %+v", isolation)
	}
	safety := report.Safety
	if safety.RawHeadersPersisted || safety.DecodedHeadersPersisted || safety.AuthorizationPersisted ||
		safety.BodyPersisted || safety.EndpointPersisted || safety.SyntheticKeyPersisted ||
		safety.UserAuthFileRead || safety.UserAuthFileModified || !safety.UserConfigUnchanged ||
		!safety.ParentProxyEnvironmentStable || safety.WindowsRootStoreModified {
		t.Fatalf("invalid branch evidence safety boundary: %+v", safety)
	}
	limitations := report.Limitations
	if !limitations.PrivateBinarySourceUnavailable || !limitations.PublicAndPrivateRevisionsDiffer ||
		!limitations.LocalNeverIndexSupportRetained || !limitations.ConclusionScopedToAuthBuilders {
		t.Fatalf("missing branch evidence limitation: %+v", limitations)
	}
}
