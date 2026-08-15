package service

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestShouldUseCodexToolFrameByQuota(t *testing.T) {
	now := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	baseExtra := map[string]any{
		openAICodexToolFrameOn5hExhaustedKey: true,
		"codex_usage_updated_at":             now.Add(-time.Minute).Format(time.RFC3339),
		"codex_5h_used_percent":              100.0,
		"codex_5h_reset_at":                  now.Add(time.Hour).Format(time.RFC3339),
		"codex_7d_used_percent":              80.0,
		"codex_7d_reset_at":                  now.Add(24 * time.Hour).Format(time.RFC3339),
	}
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Extra:    baseExtra,
	}

	require.True(t, shouldUseCodexToolFrameByQuota(account, now))

	account.Extra = cloneStringAnyMap(baseExtra)
	account.Extra["codex_5h_used_percent"] = 40.0
	account.Extra["codex_7d_used_percent"] = 100.0
	require.True(t, shouldUseCodexToolFrameByQuota(account, now), "7d-only exhaustion must enable Tool Frame")

	account.Extra = cloneStringAnyMap(baseExtra)
	delete(account.Extra, "codex_5h_used_percent")
	delete(account.Extra, "codex_5h_reset_at")
	account.Extra["codex_7d_used_percent"] = 100.0
	require.True(t, shouldUseCodexToolFrameByQuota(account, now), "accounts without a 5h window must use Tool Frame when 7d is exhausted")

	account.Extra = cloneStringAnyMap(baseExtra)
	account.Extra["codex_7d_used_percent"] = 100.0
	require.False(t, shouldUseCodexToolFrameByQuota(account, now))

	account.Extra = cloneStringAnyMap(baseExtra)
	account.Extra["codex_7d_used_percent"] = 100.0
	account.Extra[openAICodexToolFrameForceAfter5hKey] = true
	require.True(t, shouldUseCodexToolFrameByQuota(account, now))
	require.True(t, shouldForceCodexToolFrameAfter5h(account, now))

	account.Extra = cloneStringAnyMap(baseExtra)
	account.Extra["codex_5h_reset_at"] = now.Add(-time.Second).Format(time.RFC3339)
	require.False(t, shouldUseCodexToolFrameByQuota(account, now))

	account.Extra = cloneStringAnyMap(baseExtra)
	delete(account.Extra, openAICodexToolFrameOn5hExhaustedKey)
	require.False(t, shouldUseCodexToolFrameByQuota(account, now))
}

func TestShouldRetryCodexToolFrameFrom429(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			openAICodexToolFrameOn5hExhaustedKey: true,
		},
	}
	headers := http.Header{}
	headers.Set("x-codex-primary-used-percent", "70")
	headers.Set("x-codex-primary-window-minutes", "10080")
	headers.Set("x-codex-secondary-used-percent", "100")
	headers.Set("x-codex-secondary-window-minutes", "300")

	require.True(t, shouldRetryCodexToolFrameFrom429(account, headers))

	headers.Set("x-codex-primary-used-percent", "100")
	require.False(t, shouldRetryCodexToolFrameFrom429(account, headers))

	headers.Set("x-codex-secondary-used-percent", "0")
	require.True(t, shouldRetryCodexToolFrameFrom429(account, headers), "7d-only 429 must retry with Tool Frame")

	headers.Del("x-codex-secondary-used-percent")
	headers.Del("x-codex-secondary-window-minutes")
	require.True(t, shouldRetryCodexToolFrameFrom429(account, headers), "a 7d-only header snapshot without a 5h window must retry with Tool Frame")

	headers.Set("x-codex-secondary-window-minutes", "300")
	headers.Set("x-codex-secondary-used-percent", "100")
	account.Extra[openAICodexToolFrameForceAfter5hKey] = true
	require.True(t, shouldRetryCodexToolFrameFrom429(account, headers))
}

func TestShouldSuppressCodexToolFrame429AccountMarkHonorsNoCooldownSwitch(t *testing.T) {
	body, changed := appendCodexToolFrameIfNeeded([]byte(`{"model":"gpt-5.1-codex","input":[{"type":"message","role":"user","content":"hi"}]}`))
	require.True(t, changed)
	plainBody := []byte(`{"model":"gpt-5.1-codex","input":[{"type":"message","role":"user","content":"hi"}]}`)

	headers := http.Header{}
	headers.Set("x-codex-primary-used-percent", "70")
	headers.Set("x-codex-primary-window-minutes", "10080")
	headers.Set("x-codex-secondary-used-percent", "100")
	headers.Set("x-codex-secondary-window-minutes", "300")

	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			openAICodexToolFrameOn5hExhaustedKey: true,
		},
	}
	require.True(t, shouldSuppressCodexToolFrame429AccountMark(account, headers, body))

	account.Extra[openAICodexToolFrame429NoCooldownKey] = false
	require.False(t, shouldSuppressCodexToolFrame429AccountMark(account, headers, body))

	account.Extra[openAICodexToolFrame429NoCooldownKey] = true
	headers.Set("x-codex-primary-used-percent", "100")
	require.False(t, shouldSuppressCodexToolFrame429AccountMark(account, headers, body))

	headers.Set("x-codex-secondary-used-percent", "0")
	require.True(t, shouldSuppressCodexToolFrame429AccountMark(account, headers, body), "7d-only Tool Frame 429 must not cooldown the account")
	require.False(t, shouldSuppressCodexToolFrame429AccountMark(account, headers, plainBody), "normal mode still requires the request to carry Tool Frame")

	account.Extra[openAICodexToolFrameForceAfter5hKey] = true
	require.True(t, shouldSuppressCodexToolFrame429AccountMark(account, headers, plainBody), "force mode must suppress a 7d-only 429 even without Tool Frame")

	account.Type = AccountTypeAPIKey
	require.False(t, shouldSuppressCodexToolFrame429AccountMark(account, headers, plainBody), "force mode only applies to OpenAI OAuth accounts")

	account.Type = AccountTypeOAuth
	account.Extra[openAICodexToolFrameOn5hExhaustedKey] = false
	require.False(t, shouldSuppressCodexToolFrame429AccountMark(account, headers, plainBody), "force mode requires the main Tool Frame switch")
}

func TestOpenAI429NoCooldownSwitchSuppressesAnyOAuth429(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			openAI429NoCooldownKey: true,
		},
	}
	body := []byte(`{"model":"gpt-5.6","input":[{"type":"message","role":"user","content":"hi"}]}`)
	headers := http.Header{}
	headers.Set("x-codex-primary-used-percent", "100")
	headers.Set("x-codex-primary-window-minutes", "10080")
	headers.Set("x-codex-secondary-used-percent", "0")
	headers.Set("x-codex-secondary-window-minutes", "300")

	require.True(t, isOpenAI429NoCooldownEnabled(account))
	require.True(t, shouldSuppressOpenAI429AccountCooldown(account))
	require.True(t, shouldSuppressCodexToolFrame429AccountMark(account, headers, body))
	require.False(t, openAIRequestBodyHasCodexToolFrame(body))
	require.False(t, isCodexToolFrameForceAfter5hEnabled(account))

	account.Type = AccountTypeAPIKey
	require.False(t, isOpenAI429NoCooldownEnabled(account))
	require.False(t, shouldSuppressOpenAI429AccountCooldown(account))
	require.False(t, shouldSuppressCodexToolFrame429AccountMark(account, headers, body))
}

func TestRewriteCodexToolFrame429FailoverForClientWhenEnabled(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			openAICodexToolFrameNever429Key: true,
		},
	}
	status, rewritten := rewriteCodexToolFrame429Failover(http.StatusTooManyRequests, []byte(`{"error":{"type":"rate_limit_error"}}`), account)
	require.Equal(t, http.StatusServiceUnavailable, status)
	require.Equal(t, "upstream_error", gjson.GetBytes(rewritten, "error.type").String())

	account.Extra[openAICodexToolFrameNever429Key] = false
	status, _ = rewriteCodexToolFrame429Failover(http.StatusTooManyRequests, []byte(`{}`), account)
	require.Equal(t, http.StatusTooManyRequests, status)

	account.Extra[openAICodexToolFrameNever429Key] = true
	account.Type = AccountTypeAPIKey
	status, _ = rewriteCodexToolFrame429Failover(http.StatusTooManyRequests, []byte(`{}`), account)
	require.Equal(t, http.StatusTooManyRequests, status)

	account.Type = AccountTypeOAuth
	status, _ = rewriteCodexToolFrame429Failover(http.StatusBadGateway, []byte(`{}`), account)
	require.Equal(t, http.StatusBadGateway, status)
}

func TestAppendCodexToolFrameIfNeeded(t *testing.T) {
	body := []byte(`{"model":"gpt-5.1-codex","input":[{"type":"message","role":"user","content":"hi"}]}`)

	next, changed := appendCodexToolFrameIfNeeded(body)
	require.True(t, changed)
	inputItems := gjson.GetBytes(next, "input").Array()
	require.Len(t, inputItems, 3)
	require.Equal(t, "function_call_output", inputItems[len(inputItems)-1].Get("type").String())
	require.Equal(t, "function", gjson.GetBytes(next, "tools.0.type").String())
	require.Equal(t, codexToolFrameStubToolName, gjson.GetBytes(next, "tools.0.name").String())

	again, changed := appendCodexToolFrameIfNeeded(next)
	require.False(t, changed)
	require.JSONEq(t, string(next), string(again))

	compaction := []byte(`{"input":[{"type":"message","role":"user"},{"type":"compaction_trigger"}]}`)
	_, changed = appendCodexToolFrameIfNeeded(compaction)
	require.False(t, changed)
}

func TestOpenAIUpstreamRequestSnapshotDetectsToolFrame(t *testing.T) {
	body := []byte(`{"model":"gpt-5.1-codex","stream":true,"prompt_cache_key":"secret-cache","input":[{"type":"message","role":"user","content":"hi"}]}`)
	next, changed := appendCodexToolFrameIfNeeded(body)
	require.True(t, changed)

	snapshot := buildOpenAIUpstreamRequestSnapshot(next)
	require.NotNil(t, snapshot)
	require.Equal(t, "gpt-5.1-codex", snapshot.Model)
	require.True(t, snapshot.Stream)
	require.Equal(t, 3, snapshot.InputItems)
	require.Equal(t, 1, snapshot.ToolsCount)
	require.True(t, snapshot.HasToolFrame)
	require.True(t, snapshot.HasPromptCacheKey)
	require.NotEmpty(t, snapshot.BodySHA256)
	require.NotContains(t, snapshot.RequestPreview, "secret-cache")
}

func cloneStringAnyMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
