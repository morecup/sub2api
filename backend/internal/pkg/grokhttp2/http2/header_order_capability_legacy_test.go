//go:build !(go1.27 && !http2legacy)

package http2

import "testing"

func TestSupportsHeaderOrder_LegacyPathsReportSupported(t *testing.T) {
	if !SupportsHeaderOrder() {
		t.Fatal("legacy/http2legacy paths must report header-order support")
	}
}
