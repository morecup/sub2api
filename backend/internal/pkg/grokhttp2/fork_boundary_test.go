package grokhttp2

import "testing"

func TestUpstreamVersionPinnedToXNetV0560(t *testing.T) {
	if UpstreamVersion != "golang.org/x/net v0.56.0" {
		t.Fatalf("UpstreamVersion = %q, want %q", UpstreamVersion, "golang.org/x/net v0.56.0")
	}
}
