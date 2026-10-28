//go:build unit

package service

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGPT6AstraPricingSourcesAndAliases(t *testing.T) {
	data, err := os.ReadFile("../../resources/model-pricing/model_prices_and_context_window.json")
	require.NoError(t, err)
	filePricing := &PricingService{}
	filePricing.pricingData, err = filePricing.parsePricingData(data)
	require.NoError(t, err)
	require.Contains(t, filePricing.ListModelNamesByProvider("openai"), "gpt-6-astra")

	// Missing catalogs must not fall through to the generic GPT-6 family or
	// default test model. All three price sources must agree for known aliases.
	missingPricing := &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"gpt-6":   {InputCostPerToken: 99},
		"gpt-5.4": {InputCostPerToken: 2.5e-6},
	}}
	for name, source := range map[string]*PricingService{"file": filePricing, "missing": missingPricing, "hardcoded": nil} {
		t.Run(name, func(t *testing.T) {
			svc := NewBillingService(&config.Config{}, source)
			for _, model := range []string{"gpt-6-astra", "openai/gpt-6-astra", "GPT-6-ASTRA", "gpt-6-astra-high", "gpt-6-astra-max", "gpt-6-astra-ultra", "gpt-6-astra-openai-compact", "gpt-6-astra-max-openai-compact"} {
				pricing, err := svc.GetModelPricing(model)
				require.NoError(t, err, model)
				require.InDelta(t, 10e-6, pricing.InputPricePerToken, 1e-12, model)
				require.InDelta(t, 50e-6, pricing.OutputPricePerToken, 1e-12, model)
				require.InDelta(t, 12.5e-6, pricing.CacheCreationPricePerToken, 1e-12, model)
				require.InDelta(t, 1e-6, pricing.CacheReadPricePerToken, 1e-12, model)
				require.InDelta(t, 20e-6, pricing.InputPricePerTokenPriority, 1e-12, model)
				require.InDelta(t, 100e-6, pricing.OutputPricePerTokenPriority, 1e-12, model)
				require.InDelta(t, 25e-6, pricing.CacheCreationPricePerTokenPriority, 1e-12, model)
				require.InDelta(t, 2e-6, pricing.CacheReadPricePerTokenPriority, 1e-12, model)
			}
		})
	}
	require.Nil(t, missingPricing.GetModelPricing("gpt-6-unknown"))
	require.Nil(t, missingPricing.GetModelPricing("gpt-6-astra-unknown"))
}

func TestGPT6AstraBillingTiersAndLongContextBoundary(t *testing.T) {
	svc := NewBillingService(&config.Config{}, nil)
	for tier, multiplier := range map[string]float64{"": 1, "priority": 2, "fast": 2, "flex": 0.5} {
		for _, cachedTokens := range []int{72000, 72001} {
			tokens := UsageTokens{InputTokens: 100000, CacheCreationTokens: 100000, CacheReadTokens: cachedTokens, OutputTokens: 1000}
			cost, err := svc.CalculateCostWithServiceTier("gpt-6-astra", tokens, 1.2, tier)
			require.NoError(t, err)
			inputMultiplier, outputMultiplier := multiplier, multiplier
			if cachedTokens > 72000 {
				inputMultiplier *= 2
				outputMultiplier *= 1.5
			}
			require.InDelta(t, 1*inputMultiplier, cost.InputCost, 1e-12, tier)
			require.InDelta(t, 1.25*inputMultiplier, cost.CacheCreationCost, 1e-12, tier)
			require.InDelta(t, float64(cachedTokens)*1e-6*inputMultiplier, cost.CacheReadCost, 1e-12, tier)
			require.InDelta(t, 0.05*outputMultiplier, cost.OutputCost, 1e-12, tier)
			require.InDelta(t, cost.TotalCost*1.2, cost.ActualCost, 1e-12)
			require.Equal(t, cachedTokens > 72000, cost.LongContextBillingApplied)
		}
	}
}

func TestGPT6AstraDynamicPricingPreservesOverridesAndFillsMissingPolicy(t *testing.T) {
	source := &LiteLLMModelPricing{InputCostPerToken: 8e-6, OutputCostPerToken: 40e-6, InputCostPerTokenPriority: 16e-6}
	svc := NewBillingService(&config.Config{}, &PricingService{pricingData: map[string]*LiteLLMModelPricing{"gpt-6-astra": source}})
	pricing, err := svc.GetModelPricing("gpt-6-astra-max")
	require.NoError(t, err)
	require.Equal(t, 8e-6, pricing.InputPricePerToken)
	require.InDelta(t, 10e-6, pricing.CacheCreationPricePerToken, 1e-12)
	require.InDelta(t, 20e-6, pricing.CacheCreationPricePerTokenPriority, 1e-12)
	require.Equal(t, 272000, pricing.LongContextInputThreshold)
	require.Equal(t, 2.0, pricing.LongContextInputMultiplier)
	require.Equal(t, 1.5, pricing.LongContextOutputMultiplier)
	require.Zero(t, source.CacheCreationInputTokenCost, "catalog entries must not be mutated")

	explicit := &ModelPricing{InputPricePerToken: 7e-6, CacheCreationPriceExplicit: true}
	withPolicy := svc.applyModelSpecificPricingPolicy("gpt-6-astra", explicit)
	require.Zero(t, withPolicy.CacheCreationPricePerToken, "explicit zero cache-write price must be retained")
}

func TestGPT6AstraFallbackCatalogMergePreservesRemotePrice(t *testing.T) {
	svc := &PricingService{cfg: &config.Config{Pricing: config.PricingConfig{FallbackFile: "../../resources/model-pricing/model_prices_and_context_window.json"}}}
	merged := svc.mergeFallbackPricingData(map[string]*LiteLLMModelPricing{})
	require.Contains(t, merged, "gpt-6-astra")
	require.InDelta(t, 10e-6, merged["gpt-6-astra"].InputCostPerToken, 1e-12)
	remote := &LiteLLMModelPricing{InputCostPerToken: 9e-6}
	merged = svc.mergeFallbackPricingData(map[string]*LiteLLMModelPricing{"gpt-6-astra": remote})
	require.Same(t, remote, merged["gpt-6-astra"])
}

func TestGPT6AstraAccountTestUsesSelectedModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, recorder := newTestContext()
	resp := newJSONResponse(http.StatusOK, "")
	resp.Body = io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\"}\n\n"))
	upstream := &queuedHTTPUpstream{responses: []*http.Response{resp}}
	svc := &AccountTestService{httpUpstream: upstream}
	account := &Account{ID: 90, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1, Credentials: map[string]any{"access_token": "test-token"}}
	require.NoError(t, svc.testOpenAIAccountConnection(ctx, account, "gpt-6-astra", "", ""))
	require.Len(t, upstream.requests, 1)
	body, err := io.ReadAll(upstream.requests[0].Body)
	require.NoError(t, err)
	requestBody := decodeRecorderRequestBody(upstream.requests[0].Header.Get("Content-Encoding"), body)
	require.Equal(t, "gpt-6-astra", gjson.GetBytes(requestBody, "model").String())
	require.Contains(t, recorder.Body.String(), "test_complete")
}

func TestGPT6SolLunaPricingAndLongContext(t *testing.T) {
	data, err := os.ReadFile("../../resources/model-pricing/model_prices_and_context_window.json")
	require.NoError(t, err)
	file := &PricingService{}
	file.pricingData, err = file.parsePricingData(data)
	require.NoError(t, err)
	missing := &PricingService{pricingData: map[string]*LiteLLMModelPricing{"gpt-6": {InputCostPerToken: 99}}}
	for _, model := range []struct {
		name                 string
		input, output, cache float64
	}{
		{"gpt-6.1-terra", 1e-6, 5e-6, .05e-6}, {"gpt-6.1-sol", 2e-6, 10e-6, .1e-6}, {"gpt-6-sol", 2e-6, 10e-6, .2e-6}, {"gpt-6-luna", .1e-6, .5e-6, .01e-6},
	} {
		for sourceName, source := range map[string]*PricingService{"file": file, "missing": missing, "hardcoded": nil} {
			t.Run(model.name+"/"+sourceName, func(t *testing.T) {
				svc := NewBillingService(&config.Config{}, source)
				for _, alias := range []string{model.name, "openai/" + model.name, strings.ToUpper(model.name), model.name + "-max-openai-compact"} {
					price, err := svc.GetModelPricing(alias)
					require.NoError(t, err, alias)
					require.InDelta(t, model.input, price.InputPricePerToken, 1e-15)
					require.InDelta(t, model.output, price.OutputPricePerToken, 1e-15)
					require.InDelta(t, model.input*1.25, price.CacheCreationPricePerToken, 1e-15)
					require.InDelta(t, model.cache, price.CacheReadPricePerToken, 1e-15)
				}
				for tier, multiplier := range map[string]float64{"": 1, "priority": 2, "fast": 2, "flex": .5} {
					for _, cached := range []int{72000, 72001} {
						usage := UsageTokens{InputTokens: 100000, CacheCreationTokens: 100000, CacheReadTokens: cached, OutputTokens: 1000}
						cost, err := svc.CalculateCostWithServiceTier(model.name, usage, 1.2, tier)
						require.NoError(t, err)
						inMultiplier, outMultiplier := multiplier, multiplier
						if cached > 72000 {
							inMultiplier *= 2
							outMultiplier *= 1.5
						}
						require.Equal(t, cached > 72000, cost.LongContextBillingApplied)
						require.InDelta(t, 100000*model.input*inMultiplier, cost.InputCost, 1e-12)
						require.InDelta(t, 100000*model.input*1.25*inMultiplier, cost.CacheCreationCost, 1e-12)
						require.InDelta(t, float64(cached)*model.cache*inMultiplier, cost.CacheReadCost, 1e-12)
						require.InDelta(t, 1000*model.output*outMultiplier, cost.OutputCost, 1e-12)
						require.InDelta(t, cost.TotalCost*1.2, cost.ActualCost, 1e-12)
					}
				}
			})
		}
		custom := &LiteLLMModelPricing{InputCostPerToken: 3e-6, OutputCostPerToken: 7e-6}
		svc := NewBillingService(&config.Config{}, &PricingService{pricingData: map[string]*LiteLLMModelPricing{model.name: custom}})
		price, err := svc.GetModelPricing(model.name + "-high")
		require.NoError(t, err)
		require.Equal(t, 3e-6, price.InputPricePerToken)
		require.Equal(t, 272000, price.LongContextInputThreshold)
		require.Zero(t, custom.CacheCreationInputTokenCost, "never mutate source price overrides")
	}
	require.Nil(t, missing.GetModelPricing("gpt-6-sol-unknown"))
	require.Nil(t, missing.GetModelPricing("gpt-6.1-sol-unknown"))
	require.Nil(t, missing.GetModelPricing("gpt-6-luna-ultra"))
}

func TestGPT6SolLunaAccountTestsUseSelectedModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, model := range []string{"gpt-6.1-sol", "gpt-6-sol", "gpt-6-luna"} {
		ctx, recorder := newTestContext()
		resp := newJSONResponse(http.StatusOK, "")
		resp.Body = io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\"}\n\n"))
		upstream := &queuedHTTPUpstream{responses: []*http.Response{resp}}
		svc := &AccountTestService{httpUpstream: upstream}
		account := &Account{ID: 90, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1, Credentials: map[string]any{"access_token": "test-token"}}
		require.NoError(t, svc.testOpenAIAccountConnection(ctx, account, model, "", ""))
		require.Len(t, upstream.requests, 1)
		body, err := io.ReadAll(upstream.requests[0].Body)
		require.NoError(t, err)
		body = decodeRecorderRequestBody(upstream.requests[0].Header.Get("Content-Encoding"), body)
		require.Equal(t, model, gjson.GetBytes(body, "model").String())
		require.Equal(t, model, gjson.Get(upstream.requests[0].Header.Get(openAIWSTurnMetadataHeader), "model").String())
		require.Contains(t, recorder.Body.String(), "test_complete")
	}
}
