//go:build unit

package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodexVersionConstants_Consistency(t *testing.T) {
	require.Equal(t, codexCLIVersion, openAICodexProbeVersion,
		"codexCLIVersion and openAICodexProbeVersion must stay in sync")

	require.True(t, strings.Contains(codexCLIUserAgent, "codex_cli_rs/"+codexCLIVersion),
		"codexCLIUserAgent must embed codexCLIVersion")

	require.Equal(t, codexDesktopUserAgent, DefaultOpenAICodexUserAgent,
		"browser-UA fallback must preserve the Codex Desktop profile")
	require.True(t, strings.Contains(codexDesktopUserAgent, codexDesktopVersion),
		"codexDesktopUserAgent must embed codexDesktopVersion")
}
