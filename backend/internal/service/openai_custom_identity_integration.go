package service

import (
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

// Captured OAuth profiles are the source of truth for this fork. Upstream's
// generic identity normalization must not replace a persisted Desktop profile.
func usesCapturedCodexClientProfile(account *Account) bool {
	return account != nil && account.Type == AccountTypeOAuth && (account.Platform == PlatformOpenAI || account.Platform == "")
}

// Live and the final Messages bridge used this fallback before the merge.
// They must not inherit upstream's global identity/version rewriting.
const preservedCodexLegacyUserAgent = "codex_cli_rs/0.144.1 (Ubuntu 22.4.0; x86_64) xterm-256color"
const preservedCodexLegacyVersion = "0.144.1"

func preservePreMergeCodexIdentityHeaders(headers http.Header) {
	if strings.TrimSpace(headers.Get("user-agent")) == "" {
		headers.Set("user-agent", preservedCodexLegacyUserAgent)
	}
	if strings.TrimSpace(headers.Get("version")) == "" {
		headers.Set("version", preservedCodexLegacyVersion)
	}
	originator, userAgent, ok := openai.PairCodexClientIdentity(headers.Get("user-agent"))
	if !ok {
		originator, userAgent = "codex_cli_rs", preservedCodexLegacyUserAgent
	}
	headers.Set("originator", originator)
	headers.Set("user-agent", userAgent)
	if version := strings.TrimSpace(headers.Get("version")); version != "" && CompareVersions(version, codexUpstreamMinVersion) < 0 {
		headers.Set("version", preservedCodexLegacyVersion)
	}
}

func enforceCodexIdentityHeadersForAccount(headers http.Header, account *Account, overrideUA ...string) {
	if headers == nil {
		return
	}
	if usesCapturedCodexClientProfile(account) {
		// The Messages bridge deliberately omits originator. Preserve that
		// contract instead of adding a second identity at the transport boundary.
		if headers.Get("originator") != "" {
			profile := codexClientProfileForAccount(account)
			headers.Set("User-Agent", profile.UserAgent())
			headers.Set("originator", codexDesktopOriginator)
			headers.Set("version", profile.CodexVersion)
		}
		return
	}
	ua := ""
	if len(overrideUA) > 0 {
		ua = overrideUA[0]
	}
	enforceCodexIdentityHeadersWithUA(headers, ua)
}
