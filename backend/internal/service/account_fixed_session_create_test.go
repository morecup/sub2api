//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestAdminServiceCreateAccountDefaultsOpenAIFixedSessionEnabled(t *testing.T) {
	repo := &longContextBillingRepoStub{}
	svc := &adminServiceImpl{accountRepo: repo}

	account, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Name:                 "openai-oauth-account",
		Platform:             PlatformOpenAI,
		Type:                 AccountTypeOAuth,
		Credentials:          map[string]any{"access_token": "test"},
		SkipDefaultGroupBind: true,
	})

	require.NoError(t, err)
	require.Same(t, account, repo.createdAccount)
	require.Equal(t, true, account.Extra[openAIFixedSessionIDEnabledKey])
	parsed, err := uuid.Parse(account.Extra[openAISessionIDKey].(string))
	require.NoError(t, err)
	require.Equal(t, uuid.Version(7), parsed.Version())
}

func TestAdminServiceCreateAccountHonorsOpenAIFixedSessionOptOut(t *testing.T) {
	repo := &longContextBillingRepoStub{}
	svc := &adminServiceImpl{accountRepo: repo}

	account, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Name:                 "openai-oauth-account",
		Platform:             PlatformOpenAI,
		Type:                 AccountTypeOAuth,
		Credentials:          map[string]any{"access_token": "test"},
		Extra:                map[string]any{openAIFixedSessionIDEnabledKey: false},
		SkipDefaultGroupBind: true,
	})

	require.NoError(t, err)
	require.NotContains(t, account.Extra, openAIFixedSessionIDEnabledKey)
	require.NotContains(t, account.Extra, openAISessionIDKey)
}
