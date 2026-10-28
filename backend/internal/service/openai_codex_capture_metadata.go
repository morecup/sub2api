package service

import (
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func codexMetadataReasoningEffort(effort string) string {
	switch effort {
	case "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra":
		return effort
	}
	return ""
}

// 2026-09-23: model/effort describe the actual outgoing request, including
// model mapping and effort policy. Compaction omits both fields in the capture.
// A missing effort stays absent: guessing medium would mislabel older models.
func codexMetadataRequestFields(metadata, model, effort string) string {
	if !gjson.Valid(metadata) || !gjson.Parse(metadata).IsObject() {
		metadata = "{}"
	}
	if gjson.Get(metadata, "request_kind").String() == "compaction" {
		metadata, _ = sjson.Delete(metadata, "model")
		metadata, _ = sjson.Delete(metadata, "reasoning_effort")
		return metadata
	}
	if model = strings.TrimSpace(model); model != "" && len(model) <= 200 {
		metadata, _ = sjson.Set(metadata, "model", model)
	}
	if effort = codexMetadataReasoningEffort(effort); effort != "" {
		metadata, _ = sjson.Set(metadata, "reasoning_effort", effort)
	}
	return metadata
}

// Explicit client opt-outs survive reconstruction; server opt-out wins too.
func applyCodexAnalyticsPolicy(headers http.Header, cfg *config.Config) {
	if cfg == nil || cfg.Gateway.CodexTelemetry.WithDefaults().Mode != "off" {
		return
	}
	if metadata := headers.Get(openAIWSTurnMetadataHeader); metadata != "" {
		metadata, _ = sjson.Set(metadata, "analytics_enabled", false)
		headers.Set(openAIWSTurnMetadataHeader, metadata)
	}
}
