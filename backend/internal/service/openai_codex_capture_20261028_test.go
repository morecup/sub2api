package service

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCodexOctober28VisibleCatalog(t *testing.T) {
	data, err := os.ReadFile("../../../docs/codex-desktop-capture/2026-10-28_0.165.0-alpha.1.2/model-catalog.json")
	require.NoError(t, err)
	var models []struct {
		Slug       string                          `json:"slug"`
		Visibility string                          `json:"visibility"`
		Default    string                          `json:"default_reasoning_level"`
		Levels     []configuredCodexReasoningLevel `json:"supported_reasoning_levels"`
		Context    int64                           `json:"context_window"`
		MaxContext int64                           `json:"max_context_window"`
		Modalities []string                        `json:"input_modalities"`
	}
	require.NoError(t, json.Unmarshal(data, &models))
	account := Account{ID: 1028, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	svc := &GatewayService{accountRepo: codexModelsVisibilityAccountRepo{
		byGroup: map[int64][]Account{1028: {account}},
	}}
	visible := 0
	for _, model := range models {
		if model.Visibility != "list" {
			continue
		}
		visible++
		t.Run(model.Slug, func(t *testing.T) {
			body, err := svc.BuildCodexModelsManifestForGroup(context.Background(), &Group{ID: 1028, Platform: PlatformOpenAI}, "", []string{model.Slug})
			require.NoError(t, err)
			var manifest struct {
				Models []configuredCodexModelDescriptor `json:"models"`
			}
			require.NoError(t, json.Unmarshal(body, &manifest))
			require.Len(t, manifest.Models, 1)
			d := manifest.Models[0]
			require.Equal(t, model.Default, *d.DefaultReasoningLevel)
			require.Equal(t, effortsFromConfiguredCodexLevels(model.Levels), effortsFromConfiguredCodexLevels(d.SupportedReasoningLevels))
			require.Equal(t, model.Context, d.ContextWindow)
			require.Equal(t, model.MaxContext, d.MaxContextWindow)
			require.Equal(t, model.Modalities, d.InputModalities)
			require.True(t, isCodexResponsesLiteModel(model.Slug))
		})
	}
	require.Equal(t, 8, visible)
}

func TestCodexOctober28HistoryAndPredictionMetadata(t *testing.T) {
	const session = "019f85a3-654e-7542-8941-95713900af32"
	const source = "019f85a3-654e-7542-8941-95713900af33"
	const turn = "019f85a3-654e-7542-8941-95713900af34"
	for _, meta := range []string{
		buildCodexTurnMetadata(session, session+":0", nil, ""),
		buildCodexWSPrewarmMetadata(session, session+":0", "", ""),
		buildCodexCompactionMetadata(session, session+":0", "", nil),
	} {
		require.Equal(t, "new", gjson.Get(meta, "history_initialization").String())
		require.False(t, gjson.Get(meta, "forked_from_thread_id").Exists())
		require.False(t, gjson.Get(meta, "prediction_trigger").Exists())
	}
	inbound := `{"thread_source":"thread_sample","turn_trigger":"composer_predictions","history_initialization":"warm_fork","forked_from_thread_id":"` + source + `","prediction_source_thread_id":"` + source + `","prediction_source_turn_id":"` + turn + `","prediction_trigger":"live"}`
	meta := buildCodexTurnMetadata(session, session+":0", nil, "", inbound)
	for key, expected := range map[string]string{
		"thread_source": "thread_sample", "turn_trigger": "composer_predictions",
		"history_initialization": "warm_fork", "forked_from_thread_id": source,
		"prediction_source_thread_id": source, "prediction_source_turn_id": turn, "prediction_trigger": "live",
	} {
		require.Equal(t, expected, gjson.Get(meta, key).String(), key)
	}
	for _, key := range []string{"client_type", "source", "workspace_kind"} {
		require.False(t, gjson.Get(meta, key).Exists(), key)
	}
	prewarm := buildCodexWSPrewarmMetadata(session, session+":0", "", inbound)
	require.Equal(t, "warm_fork", gjson.Get(prewarm, "history_initialization").String())
	require.Equal(t, source, gjson.Get(prewarm, "forked_from_thread_id").String())
	for _, key := range []string{"turn_trigger", "prediction_source_thread_id", "prediction_source_turn_id", "prediction_trigger"} {
		require.False(t, gjson.Get(prewarm, key).Exists(), key)
	}
	for _, invalid := range []string{
		`{"history_initialization":"warm_fork","forked_from_thread_id":"` + source + `","prediction_trigger":"live"}`,
		`{"thread_source":"thread_sample","history_initialization":"untrusted","forked_from_thread_id":"invalid","prediction_source_thread_id":"invalid","prediction_source_turn_id":"invalid","prediction_trigger":"untrusted"}`,
	} {
		meta = buildCodexTurnMetadata(session, session+":0", nil, "", invalid)
		require.Equal(t, "new", gjson.Get(meta, "history_initialization").String())
		for _, key := range []string{"forked_from_thread_id", "prediction_source_thread_id", "prediction_source_turn_id", "prediction_trigger"} {
			require.False(t, gjson.Get(meta, key).Exists(), key)
		}
	}
}

func TestCodexOctober28TerraAliasesAndRouteCapabilities(t *testing.T) {
	const model = "gpt-6.1-terra"
	for _, alias := range []string{model, "openai/" + model, "GPT-6.1-TERRA", model + "-max", model + "-ultra-openai-compact"} {
		require.Equal(t, model, normalizeKnownOpenAICodexModel(alias))
		require.True(t, isOpenAIGPT6Model(alias))
		require.True(t, isCodexResponsesLiteModel(alias))
		require.True(t, isOpenAICodexImageInputModel(alias))
		require.Equal(t, "max", normalizeOpenAIReasoningEffortForModel("max", alias))
	}
	for _, invalid := range []string{model + "-unknown", "gpt-6.1-astra", "gpt-6.1"} {
		require.Empty(t, normalizeKnownOpenAICodexModel(invalid))
	}
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	caps := accountCodexToolCapabilities(account, model)
	require.JSONEq(t, "true", string(caps["use_responses_lite"]))
	require.JSONEq(t, `"xhigh"`, string(caps["multi_agent_reasoning_effort"]))
	require.JSONEq(t, `"v2"`, string(caps["multi_agent_version"]))
	account.Type = AccountTypeAPIKey
	require.JSONEq(t, "false", string(accountCodexToolCapabilities(account, model)["use_responses_lite"]))
}

func TestCodexOctober28ProfileUpgradePreservesEnvironment(t *testing.T) {
	for i := range 16 {
		account := testCodexProfileAccount(int64(i+1), i)
		latest := codexClientProfileForAccount(account)
		require.Equal(t, "0.165.0-alpha.1.2", latest.CodexVersion)
		require.Equal(t, "26.1023.50603", latest.AppVersion)
		require.Equal(t, "156.0.0.0", latest.BrowserVersion)
		old := latest
		old.TemplateID = strings.Replace(old.TemplateID, "windows-v4-", "windows-v3-", 1)
		old.CodexVersion, old.AppVersion, old.BrowserVersion = "0.159.0", "26.928.20755", "154.0.0.0"
		require.Equal(t, latest, upgradeCapturedCodexClientProfile(old))
		require.Equal(t, latest, upgradeCapturedCodexClientProfile(latest))
		for _, mutate := range []func(*CodexClientProfile){
			func(p *CodexClientProfile) { p.TemplateID = "custom" },
			func(p *CodexClientProfile) { p.TemplateID = "windows-v3-16" },
			func(p *CodexClientProfile) { p.CodexVersion = "custom" },
			func(p *CodexClientProfile) { p.AppVersion = "custom" },
			func(p *CodexClientProfile) { p.BrowserVersion = "custom" },
			func(p *CodexClientProfile) { p.OTelSDKVersion = "custom" },
		} {
			custom := old
			mutate(&custom)
			require.Equal(t, custom, upgradeCapturedCodexClientProfile(custom))
		}
	}
}
