package service

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
)

func TestAppendOpenAICompatClaudeCodeTodoGuard_SkipsCompactionTrigger(t *testing.T) {
	input := []apicompat.ResponsesInputItem{
		{
			Type: "message",
			Role: "user",
			Content: mustMarshalRawMessageForTest(t, []apicompat.ResponsesContentPart{{
				Type: "input_text",
				Text: "hi",
			}}),
		},
		{Type: "compaction_trigger"},
	}
	rawInput, err := json.Marshal(input)
	require.NoError(t, err)

	req := &apicompat.ResponsesRequest{Input: rawInput}
	require.False(t, appendOpenAICompatClaudeCodeTodoGuard(req))
	require.JSONEq(t, string(rawInput), string(req.Input))
}

func TestAppendOpenAICompatClaudeCodeTodoGuardToRequestBody_SkipsCompactionTrigger(t *testing.T) {
	reqBody := map[string]any{
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": "hi"},
			map[string]any{"type": "compaction_trigger"},
		},
	}

	require.False(t, appendOpenAICompatClaudeCodeTodoGuardToRequestBody(reqBody))
	input, ok := reqBody["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 2)
	last, ok := input[1].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "compaction_trigger", last["type"])
}

func mustMarshalRawMessageForTest(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}
