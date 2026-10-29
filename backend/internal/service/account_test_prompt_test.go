//go:build unit

package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAccountTextTestPayloadsUsePrompt(t *testing.T) {
	const prompt = "  最少取出多少个糖果？  "

	claudePayload, err := createTestPayloadWithPrompt("claude-sonnet-4-5", prompt)
	require.NoError(t, err)
	claudeBody, err := json.Marshal(claudePayload)
	require.NoError(t, err)
	require.Equal(t, "最少取出多少个糖果？", gjson.GetBytes(claudeBody, "messages.0.content.0.text").String())

	openAIBody, err := json.Marshal(createOpenAITestPayloadWithPrompt("gpt-5.5", true, prompt))
	require.NoError(t, err)
	require.Equal(t, "最少取出多少个糖果？", gjson.GetBytes(openAIBody, "input.0.content.0.text").String())

	grokBody, err := buildGrokTextTestBody("grok-4.5", prompt)
	require.NoError(t, err)
	require.Equal(t, "最少取出多少个糖果？", gjson.GetBytes(grokBody, "input").String())
	require.True(t, gjson.GetBytes(grokBody, "stream").Bool())
}

func TestAccountTextTestPayloadsDefaultToHi(t *testing.T) {
	claudePayload, err := createTestPayload("claude-sonnet-4-5")
	require.NoError(t, err)
	claudeBody, err := json.Marshal(claudePayload)
	require.NoError(t, err)
	require.Equal(t, "hi", gjson.GetBytes(claudeBody, "messages.0.content.0.text").String())

	openAIBody, err := json.Marshal(createOpenAITestPayload("gpt-5.5", true))
	require.NoError(t, err)
	require.Equal(t, "hi", gjson.GetBytes(openAIBody, "input.0.content.0.text").String())

	grokBody, err := buildGrokTextTestBody("grok-4.5", "   ")
	require.NoError(t, err)
	quotaBody, err := buildGrokQuotaProbeBody("grok-4.5")
	require.NoError(t, err)
	require.JSONEq(t, string(quotaBody), string(grokBody))
}
