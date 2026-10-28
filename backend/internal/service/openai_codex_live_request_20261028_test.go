package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCodexLiveRequestAsyncQuestionTrigger(t *testing.T) {
	for _, tc := range []struct {
		name, source, trigger, wantSource, wantTrigger string
		wantDesktop                                    bool
	}{
		{"async question", "user", "send_user_message_async_question", "user", "send_user_message_async_question", true},
		{"legacy source absent", "", "send_user_message_async_question", "user", "send_user_message_async_question", true},
		{"unknown trigger", "user", "not-a-captured-trigger", "user", "composer", true},
		{"title takes precedence", "thread_title", "send_user_message_async_question", "thread_title", "thread_title", false},
		{"prediction takes precedence", "thread_sample", "send_user_message_async_question", "thread_sample", "composer_predictions", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inbound := `{"model":"gpt-6.1-sol","thread_source":"` + tc.source + `","turn_trigger":"` + tc.trigger + `","workspace_kind":"project"}`
			metadata := buildCodexTurnMetadata("019f85a3-654e-7542-8941-95713900af32", "", nil, "fixture-installation", inbound)
			require.Equal(t, tc.wantSource, gjson.Get(metadata, "thread_source").String())
			require.Equal(t, tc.wantTrigger, gjson.Get(metadata, "turn_trigger").String())
			if tc.wantDesktop {
				require.Equal(t, "desktop_app", gjson.Get(metadata, "client_type").String())
				require.Equal(t, "codex", gjson.Get(metadata, "source").String())
				require.Equal(t, "project", gjson.Get(metadata, "workspace_kind").String())
			} else {
				require.False(t, gjson.Get(metadata, "client_type").Exists())
			}
			body, _, err := applyCodexClientMetadataBytes([]byte(`{"model":"gpt-6.1-sol","input":[]}`), metadata)
			require.NoError(t, err)
			require.Equal(t, metadata, gjson.GetBytes(body, "client_metadata.x-codex-turn-metadata").String())
			embedded := gjson.GetBytes(body, "client_metadata.x-codex-turn-metadata").String()
			require.Equal(t, tc.wantTrigger, gjson.Get(embedded, "turn_trigger").String())
		})
	}
}
