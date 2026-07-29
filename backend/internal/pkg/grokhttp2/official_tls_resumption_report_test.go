package grokhttp2

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const officialTLSResumptionReportSHA256 = "2372f64ff14f2d51b3526d20fa77adec01fdd615a30841128e4f838e79539c9b"

type tlsClientHelloSummary struct {
	CipherSuites        []uint16 `json:"cipher_suites"`
	CompressionMethods  []uint8  `json:"compression_methods"`
	ExtensionOrder      []uint16 `json:"extension_order"`
	PreSharedKeyLast    bool     `json:"pre_shared_key_last"`
	PreSharedKeyPresent bool     `json:"pre_shared_key_present"`
	SessionIDLength     int      `json:"session_id_length"`
}

type tlsServerHelloSummary struct {
	CipherSuite         uint16 `json:"cipher_suite"`
	ResumptionAccepted  bool   `json:"resumption_accepted"`
	SelectedPSKIdentity int    `json:"selected_psk_identity"`
	SelectedVersion     uint16 `json:"selected_version"`
}

type officialTLSResumptionReport struct {
	Status         string `json:"status"`
	OfficialBinary struct {
		Version string `json:"version"`
		SHA256  string `json:"sha256"`
	} `json:"official_binary"`
	Evidence struct {
		OfficialPSKOffered          bool `json:"official_psk_offered"`
		OfficialPSKSelectedByServer bool `json:"official_psk_selected_by_server"`
		LocalPSKOffered             bool `json:"local_psk_offered"`
		LocalPSKSelectedByServer    bool `json:"local_psk_selected_by_server"`
		SameOfficialProcess         bool `json:"same_official_process"`
		SameOfficialSession         bool `json:"same_official_session"`
		TCPReconnectForced          bool `json:"tcp_reconnect_forced_between_prompts"`
	} `json:"evidence"`
	Official struct {
		FreshClientHello   tlsClientHelloSummary `json:"fresh_client_hello"`
		ResumedClientHello tlsClientHelloSummary `json:"resumed_client_hello"`
		ResumedServerHello tlsServerHelloSummary `json:"resumed_server_hello"`
	} `json:"official"`
	Local struct {
		FreshClientHello   tlsClientHelloSummary `json:"fresh_client_hello"`
		ResumedClientHello tlsClientHelloSummary `json:"resumed_client_hello"`
		ResumedServerHello tlsServerHelloSummary `json:"resumed_server_hello"`
	} `json:"local_grok_profile"`
	Parity struct {
		ExactExtensionOrderRequired bool   `json:"extension_order_exact_match_required"`
		ExtensionOrderReason        string `json:"extension_order_reason"`
		RequiredChecksPassed        bool   `json:"required_checks_passed"`
		Fresh                       struct {
			AllRequiredEqual bool            `json:"all_required_equal"`
			Checks           map[string]bool `json:"checks"`
		} `json:"fresh"`
		Resumed struct {
			AllRequiredEqual bool            `json:"all_required_equal"`
			Checks           map[string]bool `json:"checks"`
		} `json:"resumed"`
		Server map[string]bool `json:"server"`
	} `json:"parity"`
	Safety struct {
		ConfigUnchanged              bool `json:"config_unchanged"`
		HostnamePersisted            bool `json:"hostname_persisted"`
		ParentProxyEnvironmentStable bool `json:"parent_proxy_environment_unchanged"`
		RequestOrResponsePersisted   bool `json:"request_or_response_persisted"`
		TicketIdentityHashRunSalted  bool `json:"ticket_identity_hash_is_run_salted"`
		TicketIdentityPersisted      bool `json:"ticket_identity_persisted"`
		TLSBytesModified             bool `json:"tls_bytes_modified"`
		TLSWasTerminated             bool `json:"tls_terminated_by_capture_proxy"`
		WindowsRootStoreModified     bool `json:"windows_root_store_modified"`
	} `json:"safety"`
	Limitations struct {
		RandomizedOrderComparedBySet bool `json:"randomized_extension_order_compared_by_set_and_invariants"`
		SingleOfficialSample         bool `json:"single_official_resumption_sample"`
		TicketAndBinderUnavailable   bool `json:"ticket_and_binder_bytes_are_intentionally_unavailable_from_report"`
	} `json:"limitations"`
}

func TestOfficialTLSResumptionReportIsPinnedSafeAndStructurallyAligned(t *testing.T) {
	raw, err := os.ReadFile("testdata/official-wire-capture/official-tls-resumption-report.json")
	if err != nil {
		t.Fatalf("read official TLS resumption report: %v", err)
	}
	digest := sha256.Sum256(raw)
	if got := hex.EncodeToString(digest[:]); got != officialTLSResumptionReportSHA256 {
		t.Fatalf("official TLS resumption report SHA-256 = %s, want %s", got, officialTLSResumptionReportSHA256)
	}
	lower := strings.ToLower(string(raw))
	for _, forbidden := range []string{
		"bearer ", "access_token", "refresh_token", "id_token", "api.x.ai", "cli-chat-proxy",
	} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("official TLS resumption report contains forbidden material %q", forbidden)
		}
	}

	var report officialTLSResumptionReport
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatalf("decode official TLS resumption report: %v", err)
	}
	if report.Status != "OFFICIAL-TLS-RESUMPTION-STRUCTURALLY-ALIGNED" {
		t.Fatalf("unexpected report status %q", report.Status)
	}
	if report.OfficialBinary.Version != "grok 0.2.112 (9bbd559437)" ||
		report.OfficialBinary.SHA256 != "2469bd182af212c7fcb84f2981999e4e8a6a7a2e4172bad3ae7f787a1f11407c" {
		t.Fatalf("unexpected official binary provenance: %+v", report.OfficialBinary)
	}
	evidence := report.Evidence
	if !evidence.OfficialPSKOffered || !evidence.OfficialPSKSelectedByServer ||
		!evidence.LocalPSKOffered || !evidence.LocalPSKSelectedByServer ||
		!evidence.SameOfficialProcess || !evidence.SameOfficialSession || !evidence.TCPReconnectForced {
		t.Fatalf("incomplete TLS resumption evidence: %+v", evidence)
	}

	officialFresh, localFresh := report.Official.FreshClientHello, report.Local.FreshClientHello
	officialResumed, localResumed := report.Official.ResumedClientHello, report.Local.ResumedClientHello
	if officialFresh.PreSharedKeyPresent || localFresh.PreSharedKeyPresent ||
		officialFresh.PreSharedKeyLast || localFresh.PreSharedKeyLast {
		t.Fatal("fresh ClientHello unexpectedly contains a pre_shared_key offer")
	}
	if !officialResumed.PreSharedKeyPresent || !localResumed.PreSharedKeyPresent ||
		!officialResumed.PreSharedKeyLast || !localResumed.PreSharedKeyLast {
		t.Fatal("resumed ClientHello is missing the final pre_shared_key extension")
	}
	if slices.Contains(officialResumed.ExtensionOrder, uint16(35)) ||
		slices.Contains(localResumed.ExtensionOrder, uint16(35)) {
		t.Fatal("resumed ClientHello retained the empty session_ticket extension")
	}
	if officialResumed.ExtensionOrder[len(officialResumed.ExtensionOrder)-1] != 41 ||
		localResumed.ExtensionOrder[len(localResumed.ExtensionOrder)-1] != 41 {
		t.Fatal("pre_shared_key is not the last resumed ClientHello extension")
	}
	if !reflect.DeepEqual(officialFresh.CipherSuites, localFresh.CipherSuites) ||
		!reflect.DeepEqual(officialResumed.CipherSuites, localResumed.CipherSuites) ||
		!reflect.DeepEqual(officialFresh.CompressionMethods, localFresh.CompressionMethods) ||
		!reflect.DeepEqual(officialResumed.CompressionMethods, localResumed.CompressionMethods) ||
		officialFresh.SessionIDLength != localFresh.SessionIDLength ||
		officialResumed.SessionIDLength != localResumed.SessionIDLength {
		t.Fatal("official/local ClientHello core fields differ")
	}
	for name, hello := range map[string]tlsServerHelloSummary{
		"official": report.Official.ResumedServerHello,
		"local":    report.Local.ResumedServerHello,
	} {
		if !hello.ResumptionAccepted || hello.SelectedPSKIdentity != 0 ||
			hello.SelectedVersion != 772 || hello.CipherSuite != 4866 {
			t.Fatalf("%s resumed ServerHello did not accept TLS 1.3 PSK: %+v", name, hello)
		}
	}

	parity := report.Parity
	if parity.ExactExtensionOrderRequired || parity.ExtensionOrderReason == "" ||
		!parity.RequiredChecksPassed || !parity.Fresh.AllRequiredEqual ||
		!parity.Resumed.AllRequiredEqual || !allChecksTrue(parity.Fresh.Checks) ||
		!allChecksTrue(parity.Resumed.Checks) || !allChecksTrue(parity.Server) {
		t.Fatalf("invalid TLS resumption parity summary: %+v", parity)
	}
	safety := report.Safety
	if !safety.ConfigUnchanged || safety.HostnamePersisted || !safety.ParentProxyEnvironmentStable ||
		safety.RequestOrResponsePersisted || !safety.TicketIdentityHashRunSalted ||
		safety.TicketIdentityPersisted || safety.TLSBytesModified || safety.TLSWasTerminated ||
		safety.WindowsRootStoreModified {
		t.Fatalf("invalid TLS resumption safety boundary: %+v", safety)
	}
	limitations := report.Limitations
	if !limitations.RandomizedOrderComparedBySet || !limitations.SingleOfficialSample ||
		!limitations.TicketAndBinderUnavailable {
		t.Fatalf("missing TLS resumption limitation: %+v", limitations)
	}
}

func allChecksTrue(checks map[string]bool) bool {
	if len(checks) == 0 {
		return false
	}
	for _, passed := range checks {
		if !passed {
			return false
		}
	}
	return true
}
