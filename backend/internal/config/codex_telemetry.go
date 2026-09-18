package config

import (
	"fmt"
	"net/url"
	"strings"
)

func (c CodexTelemetryConfig) Validate() error {
	c = c.WithDefaults()
	switch c.Mode {
	case "off":
		return nil
	case "local":
		if strings.TrimSpace(c.LocalPath) == "" {
			return fmt.Errorf("gateway.codex_telemetry.local_path is required in local mode")
		}
	case "remote":
		u, err := url.Parse(c.Endpoint)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
			(u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1"))) {
			return fmt.Errorf("gateway.codex_telemetry.endpoint must use HTTPS (HTTP allowed for loopback collectors only), without credentials, query or fragment")
		}
		if strings.ContainsAny(c.StatsigAPIKey, "\r\n") {
			return fmt.Errorf("gateway.codex_telemetry.statsig_api_key contains invalid characters")
		}
	default:
		return fmt.Errorf("gateway.codex_telemetry.mode must be off, local or remote")
	}
	return nil
}
