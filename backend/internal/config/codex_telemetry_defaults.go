package config

import (
	"net/url"
	"strings"
)

const DefaultCodexTelemetryEndpoint = "https://ab.chatgpt.com/otlp/v1/metrics"
const DefaultCodexTelemetryLocalPath = "data/codex-telemetry.jsonl"

// Public client SDK key embedded in the distributed Codex 0.155 executable,
// verified against the 2026-09-17 OTLP capture. This is not an OAuth token,
// account credential, or a Statsig server secret. Keep private capture values out.
const DefaultCodexTelemetryClientKey = "client-MkRuleRQBd6qakfnDYqJVR9JuXcY57Ljly3vi5JVUIO"

// WithDefaults gives file/env configuration and direct service construction
// the same zero-config behavior. Overrides are retained. A custom collector
// does not inherit the official collector's public client key.
func (c CodexTelemetryConfig) WithDefaults() CodexTelemetryConfig {
	c.Mode = strings.TrimSpace(c.Mode)
	if c.Mode == "" {
		c.Mode = "remote"
	}
	c.Endpoint = strings.TrimSpace(c.Endpoint)
	if c.Endpoint == "" {
		c.Endpoint = DefaultCodexTelemetryEndpoint
	}
	if strings.TrimSpace(c.LocalPath) == "" {
		c.LocalPath = DefaultCodexTelemetryLocalPath
	}
	if strings.Trim(c.StatsigAPIKey, " \t") == "" {
		c.StatsigAPIKey = ""
		if u, err := url.Parse(c.Endpoint); err == nil && strings.EqualFold(u.Hostname(), "ab.chatgpt.com") {
			c.StatsigAPIKey = DefaultCodexTelemetryClientKey
		}
	}
	return c
}
