package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodexTelemetryConfiguration(t *testing.T) {
	for _, c := range []CodexTelemetryConfig{
		{}, {Mode: "remote"}, {Mode: "off"}, {Mode: "local"}, {Mode: "local", LocalPath: "data/metrics.jsonl"},
		{Mode: "remote", Endpoint: "http://127.0.0.1:4318/v1/metrics"},
		{Mode: "remote", Endpoint: "https://ab.chatgpt.com/otlp/v1/metrics", StatsigAPIKey: "test"},
	} {
		require.NoError(t, c.Validate())
	}
	for _, c := range []CodexTelemetryConfig{
		{Mode: "bad"},
		{Mode: "remote", Endpoint: "http://external.invalid/metrics"},
		{Mode: "remote", Endpoint: "https://user:password@external.invalid/metrics"},
		{Mode: "remote", Endpoint: "https://external.invalid/metrics?secret=bad"},
		{Mode: "remote", Endpoint: "https://external.invalid/metrics", StatsigAPIKey: "bad\r\nheader"},
	} {
		require.Error(t, c.Validate())
	}
}

func TestCodexTelemetryLoadDefaultsAndEnvironment(t *testing.T) {
	resetViperWithJWTSecret(t)
	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, "remote", cfg.Gateway.CodexTelemetry.Mode)
	require.Equal(t, DefaultCodexTelemetryEndpoint, cfg.Gateway.CodexTelemetry.Endpoint)
	require.Equal(t, DefaultCodexTelemetryClientKey, cfg.Gateway.CodexTelemetry.StatsigAPIKey)
	require.NotEmpty(t, cfg.Gateway.CodexTelemetry.LocalPath)
	t.Setenv("GATEWAY_CODEX_TELEMETRY_MODE", "off")
	cfg, err = Load()
	require.NoError(t, err)
	require.Equal(t, "off", cfg.Gateway.CodexTelemetry.Mode)
}

func TestCodexTelemetryDefaultsAndOverrides(t *testing.T) {
	for _, raw := range []CodexTelemetryConfig{{}, {Mode: " ", Endpoint: "\t", StatsigAPIKey: " "}} {
		cfg := raw.WithDefaults()
		require.Equal(t, "remote", cfg.Mode)
		require.Equal(t, "https://ab.chatgpt.com/otlp/v1/metrics", cfg.Endpoint)
		require.Equal(t, DefaultCodexTelemetryClientKey, cfg.StatsigAPIKey)
		require.Contains(t, cfg.StatsigAPIKey, "client-")
		require.NoError(t, cfg.Validate())
	}
	for _, mode := range []string{"off", "local", "remote"} {
		cfg := (CodexTelemetryConfig{Mode: mode, Endpoint: "https://collector.example/v1/metrics"}).WithDefaults()
		require.Equal(t, mode, cfg.Mode)
		require.Equal(t, "https://collector.example/v1/metrics", cfg.Endpoint)
		require.Empty(t, cfg.StatsigAPIKey, "a custom collector must not inherit the bundled key")
	}
	cfg := (CodexTelemetryConfig{StatsigAPIKey: "explicit-client-key"}).WithDefaults()
	require.Equal(t, "explicit-client-key", cfg.StatsigAPIKey)
}

func TestCodexTelemetryCustomEndpointFromEnvironment(t *testing.T) {
	resetViperWithJWTSecret(t)
	t.Setenv("GATEWAY_CODEX_TELEMETRY_ENDPOINT", "http://127.0.0.1:4318/v1/metrics")
	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, "remote", cfg.Gateway.CodexTelemetry.Mode)
	require.Equal(t, "http://127.0.0.1:4318/v1/metrics", cfg.Gateway.CodexTelemetry.Endpoint)
	require.Empty(t, cfg.Gateway.CodexTelemetry.StatsigAPIKey)
}
