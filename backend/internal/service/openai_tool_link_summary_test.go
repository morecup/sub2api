package service

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestToolLinkSummaryIsStructuralAndSurvivesPreviewTruncation(t *testing.T) {
	body := []byte(`{"model":"gpt-6-astra","input":[{"type":"additional_tools","tools":[{"name":"SECRET-tool"}]},{"type":"function_call","call_id":"SECRET-call","name":"SECRET-tool","arguments":"SECRET"},{"type":"function_call_output","call_id":"SECRET-call","output":"SECRET"},{"type":"function_call_output","call_id":"missing","output":"SECRET"},{"type":"custom_tool_call_output"},{"type":"SECRET-type"}]}`)
	s := summarizeOpenAIToolLinks(body)
	require.Equal(t, 1, s.Calls)
	require.Equal(t, 3, s.Outputs)
	require.Equal(t, 1, s.PairedOutputs)
	require.Equal(t, 1, s.UnmatchedOutputs)
	require.Equal(t, 1, s.MissingCallIDs)
	require.Equal(t, 1, s.AdditionalTools)
	require.Equal(t, 1, s.InputTypes["other"])
	raw, err := json.Marshal(s)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "SECRET")
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	setOpsOpenAIInputToolSummary(c, body)
	setOpsOpenAIUpstreamRequestBody(c, body)
	snapshot := currentOpsOpenAIUpstreamRequestSnapshot(c)
	require.Equal(t, s, snapshot.ToolLinks)
	require.Equal(t, s, snapshot.PreTransformToolLinks)
}

func TestCodexIncrementalToolOutputsPreserveServerCallIDs(t *testing.T) {
	for _, model := range []string{"gpt-5.5", "gpt-6-astra"} {
		for _, typ := range []string{"function_call_output", "custom_tool_call_output", "tool_search_output"} {
			body := map[string]any{"model": model, "previous_response_id": "resp_server_context", "input": []any{map[string]any{"type": typ, "call_id": "call_original", "output": "unchanged-result"}}}
			applyCodexOAuthTransform(body, true, false)
			items := body["input"].([]any)
			found := false
			for _, raw := range items {
				item := raw.(map[string]any)
				if item["type"] == typ {
					found = true
					require.Equal(t, "call_original", item["call_id"])
					require.Equal(t, "unchanged-result", item["output"])
				}
			}
			require.True(t, found)
		}
	}
}
