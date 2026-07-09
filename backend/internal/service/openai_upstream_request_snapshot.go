package service

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const (
	opsOpenAIUpstreamRequestBodyKey          = "ops_openai_upstream_request_body"
	openAIUpstreamRequestSnapshotPreviewSize = 4096
)

type OpenAIUpstreamRequestSnapshot struct {
	BodySHA256           string `json:"body_sha256,omitempty"`
	BodyBytes            int    `json:"body_bytes,omitempty"`
	Model                string `json:"model,omitempty"`
	Stream               bool   `json:"stream"`
	InputItems           int    `json:"input_items"`
	ToolsCount           int    `json:"tools_count"`
	HasToolFrame         bool   `json:"has_tool_frame"`
	HasPreviousResponse  bool   `json:"has_previous_response_id"`
	HasPromptCacheKey    bool   `json:"has_prompt_cache_key"`
	RequestPreview       string `json:"request_preview,omitempty"`
	RequestPreviewCutoff bool   `json:"request_preview_cutoff,omitempty"`
}

func setOpsOpenAIUpstreamRequestBody(c *gin.Context, body []byte) {
	if c == nil {
		return
	}
	c.Set(opsOpenAIUpstreamRequestBodyKey, body)
	c.Set(OpsUpstreamRequestSnapshotKey, nil)
}

func currentOpsOpenAIUpstreamRequestBody(c *gin.Context) []byte {
	if c == nil {
		return nil
	}
	v, ok := c.Get(opsOpenAIUpstreamRequestBodyKey)
	if !ok {
		return nil
	}
	body, ok := v.([]byte)
	if !ok || len(body) == 0 {
		return nil
	}
	return body
}

func currentOpsOpenAIUpstreamRequestSnapshot(c *gin.Context) *OpenAIUpstreamRequestSnapshot {
	if c == nil {
		return nil
	}
	if v, ok := c.Get(OpsUpstreamRequestSnapshotKey); ok {
		if snapshot, ok := v.(*OpenAIUpstreamRequestSnapshot); ok && snapshot != nil {
			copy := *snapshot
			return &copy
		}
	}
	v, ok := c.Get(opsOpenAIUpstreamRequestBodyKey)
	if !ok {
		return nil
	}
	body, ok := v.([]byte)
	if !ok || len(body) == 0 {
		return nil
	}
	snapshot := buildOpenAIUpstreamRequestSnapshot(body)
	if snapshot == nil {
		return nil
	}
	c.Set(OpsUpstreamRequestSnapshotKey, snapshot)
	copy := *snapshot
	return &copy
}

func buildOpenAIUpstreamRequestSnapshot(body []byte) *OpenAIUpstreamRequestSnapshot {
	if len(body) == 0 {
		return nil
	}
	sum := sha256.Sum256(body)
	snapshot := &OpenAIUpstreamRequestSnapshot{
		BodySHA256:          hex.EncodeToString(sum[:]),
		BodyBytes:           len(body),
		Model:               strings.TrimSpace(gjson.GetBytes(body, "model").String()),
		Stream:              gjson.GetBytes(body, "stream").Bool(),
		InputItems:          openAIJSONArrayLen(body, "input"),
		ToolsCount:          openAIJSONArrayLen(body, "tools"),
		HasToolFrame:        openAIRequestBodyHasCodexToolFrame(body),
		HasPreviousResponse: openAIJSONFieldHasValue(body, "previous_response_id"),
		HasPromptCacheKey:   openAIJSONFieldHasValue(body, "prompt_cache_key"),
	}
	if preview, truncated, _ := sanitizeAndTrimJSONPayload(body, openAIUpstreamRequestSnapshotPreviewSize); strings.TrimSpace(preview) != "" {
		snapshot.RequestPreview = preview
		snapshot.RequestPreviewCutoff = truncated
	}
	return snapshot
}

func openAIJSONArrayLen(body []byte, path string) int {
	result := gjson.GetBytes(body, path)
	if !result.IsArray() {
		return 0
	}
	return len(result.Array())
}

func openAIJSONFieldHasValue(body []byte, path string) bool {
	result := gjson.GetBytes(body, path)
	if !result.Exists() {
		return false
	}
	if result.Type == gjson.String {
		return strings.TrimSpace(result.String()) != ""
	}
	return strings.TrimSpace(result.Raw) != ""
}

func openAIRequestBodyHasCodexToolFrame(body []byte) bool {
	hasNoopTool := false
	for _, tool := range gjson.GetBytes(body, "tools").Array() {
		if strings.TrimSpace(tool.Get("type").String()) == "function" &&
			strings.TrimSpace(tool.Get("name").String()) == codexToolFrameStubToolName {
			hasNoopTool = true
			break
		}
	}
	if !hasNoopTool {
		return false
	}
	hasNoopCall := false
	hasCallOutput := false
	for _, item := range gjson.GetBytes(body, "input").Array() {
		switch strings.TrimSpace(item.Get("type").String()) {
		case "function_call":
			if strings.TrimSpace(item.Get("name").String()) == codexToolFrameStubToolName {
				hasNoopCall = true
			}
		case "function_call_output":
			hasCallOutput = true
		}
	}
	return hasNoopCall && hasCallOutput
}
