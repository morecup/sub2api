package tlsfingerprint

import (
	"encoding/json"
	"os"
	"testing"
)

func TestCodexOctober28ClientHelloAgainstIndependentCapture(t *testing.T) {
	raw, err := os.ReadFile("../../../../docs/codex-desktop-capture/2026-10-28_0.165.0-alpha.1.2/transport-evidence.json")
	if err != nil {
		t.Fatal(err)
	}
	type connection struct {
		Hello struct {
			CipherSuites []uint16                   `json:"cipher_suites"`
			Extensions   []uint16                   `json:"extension_order"`
			Values       map[string]json.RawMessage `json:"extension_values"`
		} `json:"client_hello"`
	}
	var capture struct{ HTTP, WS []connection }
	if err := json.Unmarshal(raw, &capture); err != nil {
		t.Fatal(err)
	}
	for name, connections := range map[string][]connection{"HTTP": capture.HTTP, "WS": capture.WS} {
		t.Run(name, func(t *testing.T) {
			if len(connections) == 0 {
				t.Fatal("missing captured connection")
			}
			profile := CodexDesktopProfile()
			if name == "WS" {
				profile = profile.WithoutALPN()
			}
			actual := captureClientHello(t, profile, "chatgpt.com:443")
			want := connections[0].Hello
			var signatures, groups []uint16
			if err := json.Unmarshal(want.Values["13"], &signatures); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(want.Values["10"], &groups); err != nil {
				t.Fatal(err)
			}
			assertSliceEqual(t, "signature_algorithms", actual.signatureAlgorithms, signatures)
			assertSliceEqual(t, "cipher_suites", actual.cipherSuites, want.CipherSuites)
			assertSliceEqual(t, "supported_groups", actual.supportedGroups, groups)
			assertSliceEqual(t, "extension_set", sortedCopy(actual.extensions), sortedCopy(want.Extensions))
		})
	}
}
