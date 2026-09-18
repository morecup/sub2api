package service

import "net/http"

// Captured OAuth profiles are the source of truth for this fork. Upstream's
// generic identity normalization must not replace a persisted Desktop profile.
func usesCapturedCodexClientProfile(account *Account) bool {
	return account != nil && account.Type == AccountTypeOAuth && (account.Platform == PlatformOpenAI || account.Platform == "")
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
