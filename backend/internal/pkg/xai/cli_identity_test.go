package xai

import (
	"fmt"
	"github.com/stretchr/testify/require"
	"net/http"
	"runtime"
	"testing"
)

// TestCLIUserAgentMatchesCapturedFormat pins the User-Agent shape to what was
// captured from the official Grok Build CLI 0.2.112. The observed value on a
// Windows host was:
//
//	grok-shell/0.2.112 (windows; x86_64)
//
// on every request, in API-key mode and on the OAuth CLI-proxy path alike. The
// previous value, "xai-grok-workspace/<version>", was never observed.
func TestCLIUserAgentMatchesCapturedFormat(t *testing.T) {
	got := CLIUserAgent()
	want := fmt.Sprintf("grok-shell/%s (%s; %s)", EffectiveCLIClientVersion(), cliPlatformOS(), cliPlatformArch())
	if got != want {
		t.Fatalf("CLIUserAgent() = %q, want %q", got, want)
	}
	if got == "xai-grok-workspace/"+CLIClientVersion {
		t.Fatal("CLIUserAgent() still returns the unobserved workspace form")
	}
}

// TestCLIPlatformMappingMirrorsTheCLI checks the OS/arch mapping against the
// CLI's PlatformInfo::current(): darwin renders as macos, arm64 as aarch64 and
// amd64 as x86_64, so the advertised platform matches what a real install on the
// same host would report.
func TestCLIPlatformMappingMirrorsTheCLI(t *testing.T) {
	wantOS := runtime.GOOS
	switch runtime.GOOS {
	case "darwin":
		wantOS = "macos"
	case "windows":
		wantOS = "windows"
	}
	if got := cliPlatformOS(); got != wantOS {
		t.Fatalf("cliPlatformOS() = %q, want %q", got, wantOS)
	}

	wantArch := runtime.GOARCH
	switch runtime.GOARCH {
	case "amd64":
		wantArch = "x86_64"
	case "arm64":
		wantArch = "aarch64"
	}
	if got := cliPlatformArch(); got != wantArch {
		t.Fatalf("cliPlatformArch() = %q, want %q", got, wantArch)
	}
}

// TestCLIClientIdentifier pins the x-grok-client-identifier header the capture
// showed on the inference POST and on GET /v1/settings.
func TestCLIClientIdentifier(t *testing.T) {
	if CLIClientIdentifier != "grok-shell" {
		t.Fatalf("CLIClientIdentifier = %q, want grok-shell", CLIClientIdentifier)
	}
	if CLIClientIdentifierHeader != "x-grok-client-identifier" {
		t.Fatalf("CLIClientIdentifierHeader = %q, want x-grok-client-identifier", CLIClientIdentifierHeader)
	}
}

func TestResolveCLIVersionDefaultsToPinnedClientVersion(t *testing.T) {
	t.Setenv(CLIVersionEnv, "")
	// Default advertise pin is CLIClientVersion; CLIStableVersion is only the floor.
	require.Equal(t, CLIClientVersion, ResolveCLIVersion())
	require.True(t, IsSupportedCLIVersion(CLIClientVersion))
	require.True(t, IsSupportedCLIVersion(CLIStableVersion))
}

func TestResolveCLIVersionAcceptsValidOverride(t *testing.T) {
	t.Setenv(CLIVersionEnv, "0.2.113-alpha.1")
	require.Equal(t, "0.2.113-alpha.1", ResolveCLIVersion())
}

func TestResolveCLIVersionRejectsUnsafeOrTooOld(t *testing.T) {
	for _, version := range []string{
		"0.2.92",
		"0.2.93-beta.1",
		"0.2.95\r\nX-Injected: true",
		"0.2.093",
		"0.3",
		"1",
	} {
		t.Run(version, func(t *testing.T) {
			t.Setenv(CLIVersionEnv, version)
			require.Equal(t, CLIClientVersion, ResolveCLIVersion())
		})
	}
}

func TestApplyCLIProxyHeaders(t *testing.T) {
	t.Setenv(CLIVersionEnv, "")

	req, err := http.NewRequest(http.MethodPost, "https://cli-chat-proxy.grok.com/v1/responses", nil)
	require.NoError(t, err)
	req.Header.Set("User-Agent", "legacy-client/1.0")

	ApplyCLIProxyHeaders(req)

	require.Equal(t, CLIClientVersion, req.Header.Get("x-grok-client-version"))
	require.Equal(t, CLIClientIdentifier, req.Header.Get("x-grok-client-identifier"))
	require.Empty(t, req.Header.Get("X-XAI-Token-Auth"))
	require.Empty(t, req.Header.Get("x-grok-client-mode"))
	require.Equal(t, CLIUserAgent(CLIClientVersion), req.Header.Get("User-Agent"))
}

func TestApplyCLIProxyHeadersLeavesAPIHostUnchanged(t *testing.T) {
	t.Setenv(CLIVersionEnv, "0.2.95")

	req, err := http.NewRequest(http.MethodPost, "https://api.x.ai/v1/responses", nil)
	require.NoError(t, err)
	req.Header.Set("User-Agent", "direct-api-client/1.0")

	ApplyCLIProxyHeaders(req)

	require.Empty(t, req.Header.Get("x-grok-client-version"))
	require.Empty(t, req.Header.Get("x-grok-client-identifier"))
	require.Empty(t, req.Header.Get("X-XAI-Token-Auth"))
	require.Equal(t, "direct-api-client/1.0", req.Header.Get("User-Agent"))
}
