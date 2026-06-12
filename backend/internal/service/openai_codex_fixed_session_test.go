package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestShouldSkipFailoverForCodexFixed(t *testing.T) {
	oauth := &Account{Type: AccountTypeOAuth, Platform: PlatformOpenAI}
	apiKey := &Account{Type: AccountTypeAPIKey, Platform: PlatformOpenAI}
	anthropicOAuth := &Account{Type: AccountTypeOAuth, Platform: PlatformAnthropic}

	cases := []struct {
		name   string
		acc    *Account
		status int
		want   bool
	}{
		{"oauth openai 429", oauth, 429, true},
		{"oauth openai 403", oauth, 403, true},
		{"oauth openai 500", oauth, 500, true},
		{"oauth openai 503", oauth, 503, true},
		{"oauth openai 401 NOT skipped", oauth, 401, false},
		{"oauth openai 200 NOT skipped", oauth, 200, false},
		{"apikey openai 429 NOT skipped", apiKey, 429, false},
		{"oauth anthropic 429 NOT skipped", anthropicOAuth, 429, false},
		{"nil account", nil, 429, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, shouldSkipFailoverForCodexFixed(tc.acc, tc.status))
		})
	}
}

func TestApplyCodexBypassToolFrame(t *testing.T) {
	t.Run("appends fc + fc_output when last is user message", func(t *testing.T) {
		body := []byte(`{"model":"gpt-5.5","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
		out := applyCodexBypassToolFrame(body)
		input := gjson.GetBytes(out, "input").Array()
		require.Len(t, input, 3, "should have user_msg + fc + fc_output")
		require.Equal(t, "function_call", input[1].Get("type").String())
		require.Equal(t, "function_call_output", input[2].Get("type").String())
		// call_id pairing
		require.Equal(t, input[1].Get("call_id").String(), input[2].Get("call_id").String())
		// stub tool added
		tools := gjson.GetBytes(out, "tools").Array()
		found := false
		for _, tool := range tools {
			if tool.Get("name").String() == codexBypassStubToolName {
				found = true
				break
			}
		}
		require.True(t, found, "stub function tool must be appended")
	})

	t.Run("noop when last is already function_call_output", func(t *testing.T) {
		body := []byte(`{"model":"gpt-5.5","input":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"shell","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"x"}]}`)
		out := applyCodexBypassToolFrame(body)
		require.Equal(t, body, out, "must not modify body when already a continuation frame")
	})

	t.Run("noop when last is compaction_trigger", func(t *testing.T) {
		body := []byte(`{"model":"gpt-5.5","input":[{"type":"message","role":"user","content":"hi"},{"type":"compaction_trigger"}]}`)
		out := applyCodexBypassToolFrame(body)
		require.Equal(t, body, out, "remote compaction v2 body must not gain bypass tool frames")
	})

	t.Run("preserves existing tools and appends stub once", func(t *testing.T) {
		body := []byte(`{"model":"gpt-5.5","tools":[{"type":"function","name":"shell"}],"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
		out := applyCodexBypassToolFrame(body)
		tools := gjson.GetBytes(out, "tools").Array()
		require.Len(t, tools, 2, "shell + stub")
		require.Equal(t, "shell", tools[0].Get("name").String())
		require.Equal(t, codexBypassStubToolName, tools[1].Get("name").String())
	})

	t.Run("idempotent: existing stub tool not duplicated", func(t *testing.T) {
		body := []byte(`{"model":"gpt-5.5","tools":[{"type":"function","name":"noop"}],"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
		out := applyCodexBypassToolFrame(body)
		tools := gjson.GetBytes(out, "tools").Array()
		require.Len(t, tools, 1, "stub already present, no dup")
	})

	t.Run("noop when input missing or not array", func(t *testing.T) {
		body := []byte(`{"model":"gpt-5.5"}`)
		require.Equal(t, body, applyCodexBypassToolFrame(body))
		body2 := []byte(`{"model":"gpt-5.5","input":"a string"}`)
		require.Equal(t, body2, applyCodexBypassToolFrame(body2))
	})

	t.Run("noop on empty body", func(t *testing.T) {
		require.Nil(t, applyCodexBypassToolFrame(nil))
	})
}
