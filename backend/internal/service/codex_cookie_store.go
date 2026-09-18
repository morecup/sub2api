package service

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/publicsuffix"
)

const codexCookieMaxAccounts = 2048
const codexCookieIdleTTL = 24 * time.Hour

// Cookie values remain in memory. Account, proxy and credential changes isolate
// jars; downstream cookies and telemetry endpoints never participate.
type codexCookieStore struct {
	mu      sync.Mutex
	entries map[[32]byte]*codexCookieSession
}

type codexCookieSession struct {
	jar      *cookiejar.Jar
	lastUsed time.Time // protected by the store mutex
	mu       sync.Mutex
	keys     map[string]struct{}
}

func (s *codexCookieStore) prepare(req *http.Request, account *Account, proxyURL string) *codexCookieSession {
	if req == nil || req.URL == nil || account == nil || account.ID <= 0 || !account.IsOpenAIOAuth() ||
		req.URL.Scheme != "https" || !strings.EqualFold(req.URL.Hostname(), "chatgpt.com") {
		return nil
	}
	seed := codexDesktopOptionalCookie(account)
	key := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s\x00%s\x00%s", account.ID, proxyURL, account.GetCredential("chatgpt_account_id"), seed)))
	now := time.Now()
	s.mu.Lock()
	if s.entries == nil {
		s.entries = make(map[[32]byte]*codexCookieSession)
	}
	entry := s.entries[key]
	if entry == nil || now.Sub(entry.lastUsed) >= codexCookieIdleTTL {
		for k, v := range s.entries {
			if now.Sub(v.lastUsed) >= codexCookieIdleTTL {
				delete(s.entries, k)
			}
		}
		if len(s.entries) >= codexCookieMaxAccounts {
			var oldest [32]byte
			var oldestTime time.Time
			for k, v := range s.entries {
				if oldestTime.IsZero() || v.lastUsed.Before(oldestTime) {
					oldest, oldestTime = k, v.lastUsed
				}
			}
			delete(s.entries, oldest)
		}
		jar, _ := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
		entry = &codexCookieSession{jar: jar, keys: make(map[string]struct{})}
		seedReq := &http.Request{Header: http.Header{"Cookie": []string{seed}}}
		seedCookies := seedReq.Cookies()
		for _, c := range seedCookies {
			c.Path = "/"
			c.Secure = true
		}
		entry.receive(&url.URL{Scheme: "https", Host: "chatgpt.com", Path: "/"}, seedCookies)
		s.entries[key] = entry
	}
	entry.lastUsed = now
	s.mu.Unlock()
	// An upstream deletion must not be undone by reattaching the static seed on
	// every request. Seed only when the account/route session is first created.
	req.Header.Del("Cookie")
	for _, c := range entry.jar.Cookies(req.URL) {
		req.AddCookie(c)
	}
	return entry
}

func (s *codexCookieSession) receive(u *url.URL, cookies []*http.Cookie) {
	if u == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, c := range cookies {
		if i >= 64 {
			break
		}
		if c == nil || len(c.Name)+len(c.Value) > 4096 {
			continue
		}
		key := c.Domain + "\x00" + c.Path + "\x00" + c.Name
		_, known := s.keys[key]
		if !known && len(s.keys) >= 128 {
			continue
		}
		// Domain/path/Secure/expiry checks are delegated to the standard jar.
		s.jar.SetCookies(u, []*http.Cookie{c})
		if c.MaxAge < 0 || (!c.Expires.IsZero() && c.Expires.Before(time.Now())) {
			delete(s.keys, key)
		} else {
			s.keys[key] = struct{}{}
		}
	}
}
