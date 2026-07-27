package xai

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"golang.org/x/mod/semver"
)

// EnvCLIVersionOverride lets operators bump the advertised Grok Build CLI
// version without waiting for a Sub2API release. The value must be canonical
// semver and not below CLIClientVersion; anything else falls back to the
// pinned stable version.
const EnvCLIVersionOverride = "XAI_GROK_CLI_VERSION"

// EffectiveCLIClientVersion resolves the Grok Build CLI version presented to
// xAI upstreams. Every client-identity surface (chat forwarding, billing
// probes, OAuth token traffic) shares this single source so one deployment
// never presents mixed CLI versions.
func EffectiveCLIClientVersion() string {
	version := strings.TrimSpace(os.Getenv(EnvCLIVersionOverride))
	if !isSupportedCLIClientVersion(version) {
		return CLIClientVersion
	}
	return version
}

// CLIClientIdentifier is the process-level client name the CLI reports, and the
// value of the x-grok-client-identifier header. It comes from GROK_CLIENT_NAME
// and defaults to "grok-shell" (crates/codegen/xai-grok-http/src/lib.rs
// process_client_identifier).
const CLIClientIdentifier = "grok-shell"

// CLIClientIdentifierHeader carries CLIClientIdentifier upstream.
const CLIClientIdentifierHeader = "x-grok-client-identifier"

// CLIUserAgent returns the User-Agent presented by the official Grok Build CLI.
//
// Captured from grok 0.2.112 on every request it makes, both in API-key mode and
// on the OAuth CLI-proxy path:
//
//	grok-shell/0.2.112 (windows; x86_64)
//
// The CLI only renders the longer "{origin}/{ver} grok-shell/{ver} (...)" form
// when an embedding client sets a different origin via GROK_CLIENT_NAME; a plain
// `grok` process collapses to this short form (UserAgent::render in
// crates/codegen/xai-grok-http/src/lib.rs).
func CLIUserAgent() string {
	return fmt.Sprintf("%s/%s (%s; %s)", CLIClientIdentifier, EffectiveCLIClientVersion(), cliPlatformOS(), cliPlatformArch())
}

// cliPlatformOS and cliPlatformArch mirror the CLI's PlatformInfo::current()
// mapping so the advertised platform stays self-consistent with the host the
// relay actually runs on, exactly as a real CLI install would report it.
func cliPlatformOS() string {
	switch runtime.GOOS {
	case "darwin":
		return "macos"
	case "windows":
		return "windows"
	default:
		return runtime.GOOS
	}
}

func cliPlatformArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	default:
		return runtime.GOARCH
	}
}

// CLIAcceptEncoding is the Accept-Encoding header reqwest attaches to every
// Grok Build CLI request that does not set one itself.
//
// The CLI builds reqwest with default-features = false, so only the compression
// features some workspace crate opts into are compiled in: gzip, brotli and
// deflate (crates/codegen/xai-grok-tools/Cargo.toml). reqwest renders that
// combination in this fixed order. zstd is absent on purpose — the workspace
// depends on async-compression's zstd directly, not on reqwest's zstd feature.
const CLIAcceptEncoding = "gzip, br, deflate"

func isSupportedCLIClientVersion(version string) bool {
	canonical := "v" + version
	minimum := "v" + CLIClientVersion
	return semver.IsValid(canonical) &&
		semver.Canonical(canonical) == canonical &&
		semver.Compare(canonical, minimum) >= 0
}
