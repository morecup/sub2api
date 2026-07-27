package xai

import (
	"os"
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

// CLIWorkspaceUserAgent returns the User-Agent presented by the official
// Grok Build CLI workspace client.
func CLIWorkspaceUserAgent() string {
	return "xai-grok-workspace/" + EffectiveCLIClientVersion()
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
