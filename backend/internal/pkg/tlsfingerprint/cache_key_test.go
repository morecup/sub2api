package tlsfingerprint

import "testing"

func TestProfileCacheKeyIncludesGrokHTTP2ObservableConfiguration(t *testing.T) {
	baseline := cacheKeyHTTP2Profile()

	tests := []struct {
		name   string
		mutate func(*Profile)
	}{
		{
			name: "transport capability",
			mutate: func(profile *Profile) {
				profile.UseOrderedHTTP2Transport = false
			},
		},
		{
			name: "pseudo-header order",
			mutate: func(profile *Profile) {
				profile.HTTP2.PseudoHeaderOrder = []string{":method", ":authority", ":scheme", ":path"}
			},
		},
		{
			name: "regular header order",
			mutate: func(profile *Profile) {
				profile.HTTP2.RegularHeaderOrder = []string{"accept", "authorization", "content-type"}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := cloneCacheKeyProfile(baseline)
			test.mutate(changed)
			if baseline.CacheKey() == changed.CacheKey() {
				t.Fatalf("CacheKey collided after changing %s", test.name)
			}
		})
	}

	identical := cloneCacheKeyProfile(baseline)
	if baseline.CacheKey() != identical.CacheKey() {
		t.Fatalf("identical observable configuration produced unstable CacheKey values: %q != %q", baseline.CacheKey(), identical.CacheKey())
	}
}

func cacheKeyHTTP2Profile() *Profile {
	return &Profile{
		Name:                     "cache-key-http2-profile",
		ALPNProtocols:            []string{ALPNProtocolHTTP2, ALPNProtocolHTTP1},
		UseOrderedHTTP2Transport: true,
		HTTP2: &HTTP2Profile{
			Name:               "cache-key-http2-preamble",
			PseudoHeaderOrder:  []string{":method", ":scheme", ":authority", ":path"},
			RegularHeaderOrder: []string{"authorization", "accept", "content-type"},
		},
	}
}

func TestProfileCacheKeySeparatesALPNAndAutomaticCompressionModes(t *testing.T) {
	base := CodexDesktopProfile()
	withoutALPN := base.WithoutALPN()
	if base.CacheKey() == withoutALPN.CacheKey() {
		t.Fatal("omitting ALPN must produce a distinct transport cache key")
	}

	withAutomaticCompression := *base
	withAutomaticCompression.DisableAutomaticCompression = false
	if base.CacheKey() == withAutomaticCompression.CacheKey() {
		t.Fatal("automatic compression mode must produce a distinct transport cache key")
	}
}

func cloneCacheKeyProfile(profile *Profile) *Profile {
	clone := *profile
	clone.ALPNProtocols = append([]string(nil), profile.ALPNProtocols...)
	if profile.HTTP2 != nil {
		http2Clone := *profile.HTTP2
		http2Clone.PseudoHeaderOrder = append([]string(nil), profile.HTTP2.PseudoHeaderOrder...)
		http2Clone.RegularHeaderOrder = append([]string(nil), profile.HTTP2.RegularHeaderOrder...)
		clone.HTTP2 = &http2Clone
	}
	return &clone
}
