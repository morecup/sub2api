package service

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestNormalizeOpenAIFixedSessionExtra(t *testing.T) {
	t.Run("enabled generates and persists a UUID", func(t *testing.T) {
		extra, err := normalizeOpenAIFixedSessionExtra(PlatformOpenAI, AccountTypeOAuth, map[string]any{
			openAIFixedSessionIDEnabledKey: true,
		})
		require.NoError(t, err)
		require.Equal(t, true, extra[openAIFixedSessionIDEnabledKey])
		_, err = uuid.Parse(extra[openAISessionIDKey].(string))
		require.NoError(t, err)
	})

	t.Run("enabled preserves an existing UUID", func(t *testing.T) {
		const fixed = "019ff4d1-0567-7630-ba3d-e564a4a519ac"
		extra, err := normalizeOpenAIFixedSessionExtra(PlatformOpenAI, AccountTypeOAuth, map[string]any{
			openAIFixedSessionIDEnabledKey: true,
			openAISessionIDKey:             fixed,
		})
		require.NoError(t, err)
		require.Equal(t, fixed, extra[openAISessionIDKey])
	})

	t.Run("disabled removes session settings", func(t *testing.T) {
		extra, err := normalizeOpenAIFixedSessionExtra(PlatformOpenAI, AccountTypeOAuth, map[string]any{
			openAIFixedSessionIDEnabledKey: false,
			openAISessionIDKey:             "019ff4d1-0567-7630-ba3d-e564a4a519ac",
		})
		require.NoError(t, err)
		require.NotContains(t, extra, openAIFixedSessionIDEnabledKey)
		require.NotContains(t, extra, openAISessionIDKey)
	})

	t.Run("non OAuth account cannot enable the setting", func(t *testing.T) {
		extra, err := normalizeOpenAIFixedSessionExtra(PlatformOpenAI, AccountTypeAPIKey, map[string]any{
			openAIFixedSessionIDEnabledKey: true,
			openAISessionIDKey:             "019ff4d1-0567-7630-ba3d-e564a4a519ac",
		})
		require.NoError(t, err)
		require.NotContains(t, extra, openAIFixedSessionIDEnabledKey)
		require.NotContains(t, extra, openAISessionIDKey)
	})

	t.Run("rejects invalid values", func(t *testing.T) {
		_, err := normalizeOpenAIFixedSessionExtra(PlatformOpenAI, AccountTypeOAuth, map[string]any{
			openAIFixedSessionIDEnabledKey: "true",
		})
		require.Error(t, err)

		_, err = normalizeOpenAIFixedSessionExtra(PlatformOpenAI, AccountTypeOAuth, map[string]any{
			openAIFixedSessionIDEnabledKey: true,
			openAISessionIDKey:             "not-a-uuid",
		})
		require.Error(t, err)
	})
}

func TestAccountGetOpenAIFixedSessionID(t *testing.T) {
	const fixed = "019ff4d1-0567-7630-ba3d-e564a4a519ac"
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			openAISessionIDKey: fixed,
		},
	}
	require.Empty(t, account.GetOpenAIFixedSessionID(), "legacy session ID without the switch must not activate fixed-session mode")

	account.Extra[openAIFixedSessionIDEnabledKey] = true
	require.Equal(t, fixed, account.GetOpenAIFixedSessionID())

	account.Extra[openAISessionIDKey] = "not-a-uuid"
	require.Empty(t, account.GetOpenAIFixedSessionID())
	account.Extra[openAISessionIDKey] = fixed

	account.Type = AccountTypeAPIKey
	require.Empty(t, account.GetOpenAIFixedSessionID())
}
