package tlsfingerprint

import (
	"testing"

	utls "github.com/refraction-networking/utls"
)

func TestVersionedClientSessionCacheTracksNegotiatedVersion(t *testing.T) {
	cache := NewVersionedLRUClientSessionCache(2)
	tracked, ok := cache.(negotiatedVersionSessionCache)
	if !ok {
		t.Fatal("versioned cache does not expose negotiated version tracking")
	}

	cache.Put("example.test", new(utls.ClientSessionState))
	tracked.recordNegotiatedVersion("example.test", utls.VersionTLS13)
	if !cachedSessionUsesTLS13(cache, "example.test") {
		t.Fatal("TLS 1.3 cache entry was not recognized")
	}

	cache.Put("example.test", nil)
	if cachedSessionUsesTLS13(cache, "example.test") {
		t.Fatal("deleted cache entry retained its negotiated version")
	}
}

func TestOmitLegacySessionTicketExtensionKeepsPSKLast(t *testing.T) {
	extensions := []utls.TLSExtension{
		&utls.SNIExtension{},
		&utls.SessionTicketExtension{},
		&utls.UtlsPreSharedKeyExtension{},
	}
	filtered := omitLegacySessionTicketExtension(extensions)
	if len(filtered) != 2 {
		t.Fatalf("filtered extension count = %d, want 2", len(filtered))
	}
	if _, ok := filtered[0].(*utls.SNIExtension); !ok {
		t.Fatalf("first extension type = %T, want SNI", filtered[0])
	}
	if _, ok := filtered[1].(*utls.UtlsPreSharedKeyExtension); !ok {
		t.Fatalf("last extension type = %T, want PSK", filtered[1])
	}
}
