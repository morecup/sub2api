package service

import (
	"net/http"
	"strings"

	"github.com/imroc/req/v3"
)

const (
	// Stable wire values captured from Codex Desktop 26.715.61943's Electron
	// WebView (Chrome 150). Session cookies and Sentry tracing values are
	// intentionally not synthesized.
	codexDesktopWebviewUserAgent      = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0.0.0 Safari/537.36"
	codexDesktopWebviewAcceptEncoding = "gzip, deflate, br, zstd"
	codexDesktopWebviewAcceptLanguage = "zh-CN,zh;q=0.9"
	codexDesktopWebviewPriority       = "u=4, i"
	codexDesktopWebviewSecFetchSite   = "none"
	codexDesktopWebviewSecFetchMode   = "no-cors"
	codexDesktopWebviewSecFetchDest   = "empty"
)

func buildCodexDesktopWebviewHeaders(accessToken, chatGPTAccountID, language string, fedRAMP bool) map[string]string {
	headers := map[string]string{
		"authorization":      "Bearer " + accessToken,
		"chatgpt-account-id": chatGPTAccountID,
		"oai-language":       language,
		"originator":         codexDesktopOriginator,
		"sec-fetch-site":     codexDesktopWebviewSecFetchSite,
		"sec-fetch-mode":     codexDesktopWebviewSecFetchMode,
		"sec-fetch-dest":     codexDesktopWebviewSecFetchDest,
		"priority":           codexDesktopWebviewPriority,
	}
	if fedRAMP {
		headers["x-openai-fedramp"] = "true"
	}
	return headers
}

func codexDesktopOptionalCookie(account *Account) string {
	if account == nil {
		return ""
	}
	for _, key := range []string{"chatgpt_cookie", "chatgpt_browser_cookie", "browser_cookie", "cookie"} {
		if value := strings.TrimSpace(account.GetCredential(key)); value != "" {
			return value
		}
	}
	return ""
}

func applyCodexDesktopSessionHeaders(headers map[string]string, account *Account) {
	if headers == nil || account == nil {
		return
	}
	if cookie := codexDesktopOptionalCookie(account); cookie != "" {
		headers["cookie"] = cookie
	}
	for _, key := range []string{"chatgpt_user_agent", "chatgpt_browser_user_agent", "browser_user_agent"} {
		if userAgent := strings.TrimSpace(account.GetCredential(key)); userAgent != "" {
			headers["user-agent"] = userAgent
			break
		}
	}
}

// withCodexDesktopWebviewProfile preserves the factory-provided proxy and TLS
// transport while replacing req/v3's generic macOS Chrome defaults. The order
// is the stable subset visible on the wire; omitted dynamic headers retain
// their relative positions when a real session supplies them.
func withCodexDesktopWebviewProfile(client *req.Client) *req.Client {
	if client == nil {
		return nil
	}
	profiled := client.Clone()
	profiled.Headers = http.Header{
		"User-Agent":      {codexDesktopWebviewUserAgent},
		"Accept-Encoding": {codexDesktopWebviewAcceptEncoding},
		"Accept-Language": {codexDesktopWebviewAcceptLanguage},
		"Sec-Fetch-Site":  {codexDesktopWebviewSecFetchSite},
		"Sec-Fetch-Mode":  {codexDesktopWebviewSecFetchMode},
		"Sec-Fetch-Dest":  {codexDesktopWebviewSecFetchDest},
		"Priority":        {codexDesktopWebviewPriority},
	}
	profiled.SetCommonHeaderOrder(
		"content-length",
		"authorization",
		"chatgpt-account-id",
		"content-type",
		"oai-language",
		"originator",
		"sec-fetch-site",
		"sec-fetch-mode",
		"sec-fetch-dest",
		"user-agent",
		"accept-encoding",
		"accept-language",
		"cookie",
		"priority",
	)
	return profiled
}
