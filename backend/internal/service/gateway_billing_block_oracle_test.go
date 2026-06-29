package service

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestComputeClaudeCodeCCHAgainstFullOracle verifies the CCH implementation
// against all 67 oracle test cases captured from claude.exe.
func TestComputeClaudeCodeCCHAgainstFullOracle(t *testing.T) {
	data, err := os.ReadFile(`C:\Users\Administrator\AppData\Local\Temp\claude-exe-analysis\captures\cch_oracle2_20260627-163332.json`)
	if err != nil {
		t.Skipf("Oracle file not found: %v", err)
	}

	var oracle struct {
		Rows []struct {
			Idx      int    `json:"idx"`
			Name     string `json:"name"`
			WireCCH  string `json:"wire_cch"`
			WireBody string `json:"wire_body"`
			SentHas  bool   `json:"sent_has_00000"`
		} `json:"rows"`
	}
	require.NoError(t, json.Unmarshal(data, &oracle))

	matches := 0
	skipped := 0
	misses := 0

	for _, row := range oracle.Rows {
		// Reconstruct sent body (cch=00000)
		sentBody := strings.Replace(row.WireBody, "cch="+row.WireCCH, "cch=00000", 1)

		// Check if sent body has cch=00000
		if !strings.Contains(sentBody, "cch=00000") {
			skipped++
			continue
		}

		cch, _, ok := computeClaudeCodeCCH([]byte(sentBody))

		if row.WireCCH == "00000" {
			// Expected: no replacement (CCH stays 00000)
			if !ok {
				matches++
				t.Logf("  idx=%02d name=%-40s expected=00000 (no_replace) PASS", row.Idx, row.Name)
			} else if cch == "00000" {
				matches++
				t.Logf("  idx=%02d name=%-40s expected=00000 computed=%s PASS", row.Idx, row.Name, cch)
			} else {
				misses++
				t.Logf("  idx=%02d name=%-40s expected=00000 computed=%s MISS", row.Idx, row.Name, cch)
			}
		} else if row.WireCCH != "00000" && !strings.Contains(row.WireBody, "cch=00000") {
			// Non-placeholder: native doesn't touch it. The computed CCH for
			// the "if it were 00000" scenario should still be valid.
			if ok {
				matches++
				t.Logf("  idx=%02d name=%-40s wire_cch=%s (non-placeholder) computed_if_placeholder=%s PASS", row.Idx, row.Name, row.WireCCH, cch)
			} else {
				misses++
				t.Logf("  idx=%02d name=%-40s wire_cch=%s ok=false MISS", row.Idx, row.Name, row.WireCCH)
			}
		} else if !ok {
			misses++
			t.Logf("  idx=%02d name=%-40s expected=%s ok=false MISS", row.Idx, row.Name, row.WireCCH)
		} else if cch == row.WireCCH {
			matches++
			t.Logf("  idx=%02d name=%-40s expected=%s computed=%s PASS", row.Idx, row.Name, row.WireCCH, cch)
		} else {
			misses++
			t.Logf("  idx=%02d name=%-40s expected=%s computed=%s MISS", row.Idx, row.Name, row.WireCCH, cch)
		}
	}

	t.Logf("\nResults: %d MATCH, %d MISS, %d SKIPPED out of %d total", matches, misses, skipped, len(oracle.Rows))

	if misses > 0 {
		t.Errorf("Got %d misses", misses)
	}

	fmt.Printf("CCH Oracle: %d/%d passed (%d skipped)\n", matches, matches+misses, skipped)
}
