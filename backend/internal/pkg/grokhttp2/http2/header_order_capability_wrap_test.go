//go:build go1.27 && !http2legacy

package http2

import "testing"

func TestSupportsHeaderOrder_Go127WrapperReportsUnsupported(t *testing.T) {
	if SupportsHeaderOrder() {
		t.Fatal("go1.27 wrapper path must report header-order support as unavailable")
	}
}
