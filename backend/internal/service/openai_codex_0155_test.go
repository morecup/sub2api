package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCodex0155WorkspaceMetadata(t *testing.T) {
	for _, kind := range []string{"project", "projectless"} {
		t.Run(kind, func(t *testing.T) {
			meta := buildCodexTurnMetadata(newCodexUUIDV7(), "window", nil, "", `{"workspace_kind":"`+kind+`","sandbox":"windows_elevated","sandbox_mode":"workspace-write"}`)
			require.Equal(t, kind, gjson.Get(meta, "workspace_kind").String())
			require.False(t, gjson.Get(meta, "workspaces").Exists())
			require.Equal(t, "windows_elevated", gjson.Get(meta, "sandbox").String())
			require.Equal(t, "workspace-write", gjson.Get(meta, "sandbox_mode").String())
		})
	}
}

func TestCodex0155CompactionRootAndKind(t *testing.T) {
	root := newCodexUUIDV7()
	inbound := `{"root_turn_id":"` + root + `","workspace_kind":"project","turn_trigger":"composer"}`
	for _, trigger := range []string{"manual", "auto"} {
		t.Run(trigger, func(t *testing.T) {
			meta := buildCodexCompactionMetadata(newCodexUUIDV7(), "window", "", json.RawMessage(`{"trigger":"`+trigger+`"}`), inbound)
			require.Equal(t, root, gjson.Get(meta, "root_turn_id").String())
			require.False(t, gjson.Get(meta, "workspaces").Exists())
			require.Equal(t, trigger == "auto", gjson.Get(meta, "workspace_kind").Exists())
			require.Equal(t, trigger == "auto", gjson.Get(meta, "turn_trigger").Exists())
			body := map[string]any{}
			applyCodexClientMetadata(body, "", meta)
			require.Equal(t, root, body["client_metadata"].(map[string]any)["root_turn_id"])
		})
	}
}

func TestCodex0155ModelsAndConditionalAccessPrograms(t *testing.T) {
	for _, model := range []string{"gpt-5.6-sol", "gpt-6-astra", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5"} {
		t.Run(model, func(t *testing.T) {
			for _, hasProgram := range []bool{false, true} {
				body := map[string]any{"model": model, "input": "test"}
				if hasProgram {
					body["access_programs"] = map[string]any{"cyber": "test-entitlement"}
				}
				applyCodexOAuthTransform(body, true, false)
				_, exists := body["access_programs"]
				require.Equal(t, hasProgram, exists, "conditional access programs must not be manufactured or removed")
				applyCodexWSRequestClientMetadata(body, model)
				_, lite := body["client_metadata"].(map[string]any)[responsesLiteWSMetadataKey]
				require.Equal(t, model != "gpt-5.5", lite)
			}
		})
	}
}
