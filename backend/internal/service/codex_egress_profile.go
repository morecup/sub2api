package service

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // Production binaries must resolve IANA zones without host zoneinfo.

	"golang.org/x/sync/singleflight"
	"golang.org/x/text/language"
)

const (
	codexEgressProfileTTL     = 10 * time.Minute
	codexEgressProfileRetry   = time.Minute
	codexEgressProfileTimeout = 3 * time.Second
	codexEgressProfileMaxSize = 2048
)

// Regional values belong to an egress, not to an installation. Keep them on a
// request-local account copy so changing proxies cannot change persisted IDs or
// mutate a scheduler snapshot used by concurrent requests.
type codexEgressProfile struct {
	key      string
	locale   string
	timezone string
}

type codexEgressProfileEntry struct {
	profile   codexEgressProfile
	expiresAt time.Time
}

// CodexEgressProfileService shares bounded probe results across accounts using
// the same egress. Cold lookups have a short deadline; failures retain the saved
// client profile and are negatively cached. Probes always use the selected route.
type CodexEgressProfileService struct {
	prober ProxyExitInfoProber
	cache  ProxyLatencyCache
	mu     sync.Mutex
	items  map[string]codexEgressProfileEntry
	flight singleflight.Group
	now    func() time.Time
}

func NewCodexEgressProfileService(prober ProxyExitInfoProber, cache ProxyLatencyCache) *CodexEgressProfileService {
	return &CodexEgressProfileService{prober: prober, cache: cache, items: make(map[string]codexEgressProfileEntry), now: time.Now}
}

// Hash the full route, including proxy credentials: residential providers often
// select the exit through the username. Never put those credentials in cache keys.
func proxyEgressKey(proxyURL string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(proxyURL)))
}

func codexLocaleForCountry(country string) string {
	country = strings.ToUpper(strings.TrimSpace(country))
	if len(country) != 2 {
		return ""
	}
	region, err := language.ParseRegion(country)
	if err != nil || !region.IsCountry() {
		return ""
	}
	// These multilingual regions use a deterministic desktop default. IP
	// geography cannot tell us a user's language preference.
	switch country {
	case "CN", "HK", "TW":
		return "zh-" + country
	case "SG":
		return "en-SG"
	}
	base, confidence := language.Make("und-" + country).Base()
	if confidence == language.No || base.String() == "und" {
		return ""
	}
	return base.String() + "-" + country
}

func codexEgressProfileFromExit(key string, exit *ProxyExitInfo) (codexEgressProfile, bool) {
	if exit == nil || net.ParseIP(exit.IP) == nil {
		return codexEgressProfile{}, false
	}
	locale := codexLocaleForCountry(exit.CountryCode)
	zone := strings.TrimSpace(exit.Timezone)
	// A country alone is insufficient for countries with multiple time zones.
	if locale == "" || zone == "" || zone == "Local" || len(zone) > 128 {
		return codexEgressProfile{}, false
	}
	if _, err := time.LoadLocation(zone); err != nil {
		return codexEgressProfile{}, false
	}
	return codexEgressProfile{key: key, locale: locale, timezone: zone}, true
}

func (s *CodexEgressProfileService) cached(key string) (codexEgressProfile, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.items[key]
	return entry.profile, ok && s.now().Before(entry.expiresAt)
}

func (s *CodexEgressProfileService) resolve(ctx context.Context, proxyID int64, proxyURL string) codexEgressProfile {
	key := proxyEgressKey(proxyURL)
	if ctx.Err() != nil {
		return codexEgressProfile{key: key}
	}
	if profile, ok := s.cached(key); ok {
		return profile
	}
	result := s.flight.DoChan(key, func() (any, error) {
		if profile, ok := s.cached(key); ok {
			return profile, nil
		}
		// One caller cancelling must not poison the shared lookup for others.
		probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), codexEgressProfileTimeout)
		defer cancel()
		profile := codexEgressProfile{key: key}
		ttl := codexEgressProfileRetry
		if s.cache != nil && proxyID > 0 {
			if infos, err := s.cache.GetProxyLatencies(probeCtx, []int64{proxyID}); err == nil {
				info := infos[proxyID]
				if info != nil && info.Success && info.EgressKey == key && !info.UpdatedAt.After(s.now()) && s.now().Sub(info.UpdatedAt) < codexEgressProfileTTL {
					if p, ok := codexEgressProfileFromExit(key, &ProxyExitInfo{IP: info.IPAddress, CountryCode: info.CountryCode, Timezone: info.Timezone}); ok {
						profile, ttl = p, codexEgressProfileTTL-s.now().Sub(info.UpdatedAt)
					}
				}
			}
		}
		if profile.locale == "" && s.prober != nil {
			exit, _, err := s.prober.ProbeProxy(probeCtx, proxyURL)
			if err == nil {
				if p, ok := codexEgressProfileFromExit(key, exit); ok {
					profile, ttl = p, codexEgressProfileTTL
				}
			}
		}
		s.mu.Lock()
		if len(s.items) >= codexEgressProfileMaxSize {
			// Evict the oldest expiry, including negative/expired entries.
			oldestKey := ""
			var oldest time.Time
			for k, entry := range s.items {
				if oldest.IsZero() || entry.expiresAt.Before(oldest) {
					oldestKey, oldest = k, entry.expiresAt
				}
			}
			delete(s.items, oldestKey)
		}
		s.items[key] = codexEgressProfileEntry{profile: profile, expiresAt: s.now().Add(ttl)}
		s.mu.Unlock()
		return profile, nil
	})
	select {
	case <-ctx.Done():
		return codexEgressProfile{key: key}
	case result := <-result:
		return result.Val.(codexEgressProfile)
	}
}

func (s *CodexEgressProfileService) forAccount(ctx context.Context, account *Account, proxyURL string) *Account {
	if s == nil || account == nil || !account.IsOpenAIOAuth() {
		return account
	}
	if account.codexEgressProfile != nil && account.codexEgressProfile.key == proxyEgressKey(proxyURL) {
		return account
	}
	proxyID := int64(0)
	if account.ProxyID != nil {
		proxyID = *account.ProxyID
	}
	profile := s.resolve(ctx, proxyID, proxyURL)
	copy := *account
	copy.codexEgressProfile = &profile
	return &copy
}

func (s *CodexEgressProfileService) forBoundAccount(ctx context.Context, account *Account) *Account {
	if s == nil || account == nil || !account.IsOpenAIOAuth() {
		return account
	}
	proxyURL := ""
	if account.ProxyID != nil {
		if account.Proxy == nil || account.Proxy.ID != *account.ProxyID {
			return account // Unresolved bindings must never trigger a direct probe.
		}
		proxyURL = account.Proxy.URL()
	}
	return s.forAccount(ctx, account, proxyURL)
}

func (s *OpenAIGatewayService) prepareCodexEgressAccount(ctx context.Context, account *Account) *Account {
	if s == nil {
		return account
	}
	return s.codexEgressProfiles.forBoundAccount(ctx, account)
}

func (s *OpenAIQuotaService) codexEgressAccount(ctx context.Context, account *Account) *Account {
	if s == nil {
		return account
	}
	return s.codexEgressProfiles.forBoundAccount(ctx, account)
}
