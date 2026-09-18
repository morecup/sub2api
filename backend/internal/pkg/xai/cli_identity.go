package xai

import (
	"net/http"
	"strings"
)

// Compatibility names for the upstream routing helpers. All Grok surfaces use
// the locally captured CLI identity and the same validated version override.
const (
	CLIProxyHost     = "cli-chat-proxy.grok.com"
	CLIStableVersion = CLIClientVersion
	CLIVersionEnv    = EnvCLIVersionOverride
	CLITokenAuth     = CLITokenAuthValue
	CLIClientMode    = "interactive"
)

func ResolveCLIVersion() string                 { return EffectiveCLIClientVersion() }
func IsSupportedCLIVersion(version string) bool { return isSupportedCLIClientVersion(version) }

// ApplyCLIProxyHeaders applies the captured inference identity. Token-auth and
// client-mode belong to the separately configured control-plane requests.
func ApplyCLIProxyHeaders(req *http.Request) {
	if req == nil || req.URL == nil || !strings.EqualFold(strings.TrimSpace(req.URL.Hostname()), CLIProxyHost) {
		return
	}
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	req.Header.Set("x-grok-client-version", ResolveCLIVersion())
	req.Header.Set(CLIClientIdentifierHeader, CLIClientIdentifier)
	req.Header.Set("User-Agent", CLIUserAgent())
}
