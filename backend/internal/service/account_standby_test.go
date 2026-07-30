package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type standbyAccountRepoStub struct {
	AccountRepository
	accounts      map[int64]*Account
	listed        []Account
	getByIDsCalls int
	getByIDsIDs   []int64
	getByIDsErr   error
}

func (s *standbyAccountRepoStub) GetByID(_ context.Context, id int64) (*Account, error) {
	account := s.accounts[id]
	if account == nil {
		return nil, errors.New("account not found")
	}
	return account, nil
}

func (s *standbyAccountRepoStub) GetByIDs(_ context.Context, ids []int64) ([]*Account, error) {
	s.getByIDsCalls++
	s.getByIDsIDs = append([]int64{}, ids...)
	if s.getByIDsErr != nil {
		return nil, s.getByIDsErr
	}
	accounts := make([]*Account, 0, len(ids))
	for _, id := range ids {
		if account := s.accounts[id]; account != nil {
			accounts = append(accounts, account)
		}
	}
	return accounts, nil
}

func (s *standbyAccountRepoStub) ListSchedulable(_ context.Context) ([]Account, error) {
	return append([]Account(nil), s.listed...), nil
}

func standbyTestAccounts(now time.Time) (*Account, *Account) {
	primary := &Account{
		ID:          1,
		Platform:    PlatformOpenAI,
		Status:      StatusActive,
		Schedulable: true,
		Extra: map[string]any{
			"codex_5h_used_percent":  50.0,
			"codex_7d_used_percent":  40.0,
			"codex_usage_updated_at": now.Format(time.RFC3339),
		},
	}
	primaryID := primary.ID
	standby := &Account{
		ID:                  2,
		Platform:            PlatformOpenAI,
		Status:              StatusActive,
		Schedulable:         true,
		StandbyForAccountID: &primaryID,
		StandbyTriggerTypes: []string{string(StandbyTriggerQuota5hExhausted), string(StandbyTriggerRateLimited)},
	}
	return standby, primary
}

func TestAccountSchedulableWithStandbyHealthyPrimaryWaits(t *testing.T) {
	now := time.Now()
	standby, primary := standbyTestAccounts(now)
	lookup := func(context.Context, int64) (*Account, error) { return primary, nil }

	require.False(t, accountSchedulableWithStandby(context.Background(), standby, lookup))
}

func TestEvaluateStandbyActivationUsesORConditions(t *testing.T) {
	now := time.Now()
	standby, primary := standbyTestAccounts(now)
	resetAt := now.Add(time.Minute)
	primary.RateLimitResetAt = &resetAt

	result := EvaluateStandbyActivation(context.Background(), standby, primary, now)
	require.True(t, result.Active)
	require.Equal(t, []string{string(StandbyTriggerRateLimited)}, result.MatchedTriggers)
}

func TestEvaluateStandbyActivationUsesConfiguredQuotaThreshold(t *testing.T) {
	now := time.Now()
	standby, primary := standbyTestAccounts(now)
	standby.StandbyTriggerTypes = []string{string(StandbyTriggerQuota5hExhausted)}
	primary.Extra["codex_5h_used_percent"] = 80.0
	primary.Extra["auto_pause_5h_threshold"] = 0.75

	result := EvaluateStandbyActivation(context.Background(), standby, primary, now)
	require.True(t, result.Active)
	require.Equal(t, []string{string(StandbyTriggerQuota5hExhausted)}, result.MatchedTriggers)
}

func TestEvaluateStandbyActivationDefaultsQuotaThresholdToFullUsage(t *testing.T) {
	now := time.Now()
	standby, primary := standbyTestAccounts(now)
	standby.StandbyTriggerTypes = []string{string(StandbyTriggerQuota5hExhausted)}
	primary.Extra["codex_5h_used_percent"] = 99.0
	require.False(t, EvaluateStandbyActivation(context.Background(), standby, primary, now).Active)

	primary.Extra["codex_5h_used_percent"] = 100.0
	require.True(t, EvaluateStandbyActivation(context.Background(), standby, primary, now).Active)
}

func TestEvaluateStandbyActivationFailsBackWhenAllConditionsClear(t *testing.T) {
	now := time.Now()
	standby, primary := standbyTestAccounts(now)
	resetAt := now.Add(time.Minute)
	primary.RateLimitResetAt = &resetAt
	require.True(t, EvaluateStandbyActivation(context.Background(), standby, primary, now).Active)

	resetAt = now.Add(-time.Second)
	primary.RateLimitResetAt = &resetAt
	require.False(t, EvaluateStandbyActivation(context.Background(), standby, primary, now).Active)
}

func TestAccountSchedulableWithStandbyRespectsManualHardSwitch(t *testing.T) {
	now := time.Now()
	standby, primary := standbyTestAccounts(now)
	resetAt := now.Add(time.Minute)
	primary.RateLimitResetAt = &resetAt
	standby.Schedulable = false
	lookup := func(context.Context, int64) (*Account, error) { return primary, nil }

	require.False(t, accountSchedulableWithStandby(context.Background(), standby, lookup))
}

func TestEvaluateStandbyActivationIgnoresUnselectedConditions(t *testing.T) {
	now := time.Now()
	standby, primary := standbyTestAccounts(now)
	standby.StandbyTriggerTypes = []string{string(StandbyTriggerAccountError)}
	resetAt := now.Add(time.Minute)
	primary.RateLimitResetAt = &resetAt

	require.False(t, EvaluateStandbyActivation(context.Background(), standby, primary, now).Active)
}

func TestEvaluateStandbyActivationSupportsEveryOperationalTrigger(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name    string
		trigger StandbyTriggerType
		prepare func(*Account)
	}{
		{
			name:    "7d quota exhausted",
			trigger: StandbyTriggerQuota7dExhausted,
			prepare: func(primary *Account) {
				primary.Extra["codex_7d_used_percent"] = 100.0
			},
		},
		{
			name:    "configured quota exhausted",
			trigger: StandbyTriggerQuotaExhausted,
			prepare: func(primary *Account) {
				primary.Type = AccountTypeAPIKey
				primary.Extra["quota_limit"] = 10.0
				primary.Extra["quota_used"] = 10.0
			},
		},
		{
			name:    "account expired",
			trigger: StandbyTriggerAccountExpired,
			prepare: func(primary *Account) {
				expiresAt := now.Add(-time.Second)
				primary.ExpiresAt = &expiresAt
			},
		},
		{
			name:    "account error",
			trigger: StandbyTriggerAccountError,
			prepare: func(primary *Account) {
				primary.Status = StatusError
			},
		},
		{
			name:    "temporarily unschedulable",
			trigger: StandbyTriggerTempUnschedulable,
			prepare: func(primary *Account) {
				until := now.Add(time.Minute)
				primary.TempUnschedulableUntil = &until
			},
		},
		{
			name:    "manually disabled",
			trigger: StandbyTriggerManualDisabled,
			prepare: func(primary *Account) {
				primary.Schedulable = false
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			standby, primary := standbyTestAccounts(now)
			standby.StandbyTriggerTypes = []string{string(tt.trigger)}
			tt.prepare(primary)

			result := EvaluateStandbyActivation(context.Background(), standby, primary, now)
			require.True(t, result.Active)
			require.Equal(t, []string{string(tt.trigger)}, result.MatchedTriggers)
		})
	}
}

func TestAccountSchedulableWithStandbyFailsClosedForMissingPrimary(t *testing.T) {
	now := time.Now()
	standby, _ := standbyTestAccounts(now)
	lookup := func(context.Context, int64) (*Account, error) { return nil, nil }

	require.False(t, accountSchedulableWithStandby(context.Background(), standby, lookup))
}

func TestNormalizeStandbyTriggerTypes(t *testing.T) {
	got, err := NormalizeStandbyTriggerTypes([]string{" rate_limited ", "RATE_LIMITED", "account_error"})
	require.NoError(t, err)
	require.Equal(t, []string{"rate_limited", "account_error"}, got)

	_, err = NormalizeStandbyTriggerTypes([]string{"unknown"})
	require.Error(t, err)
}

func TestApplyStandbyAccountUpdateCanClearConfiguration(t *testing.T) {
	primaryID := int64(10)
	clearID := int64(0)
	account := &Account{
		StandbyForAccountID: &primaryID,
		StandbyTriggerTypes: []string{string(StandbyTriggerRateLimited)},
	}

	updated, err := applyStandbyAccountUpdate(account, &UpdateAccountInput{StandbyForAccountID: &clearID})
	require.NoError(t, err)
	require.True(t, updated)
	require.Nil(t, account.StandbyForAccountID)
	require.Empty(t, account.StandbyTriggerTypes)
}

func TestValidateStandbyAccountConfigurationRejectsInvalidRelationships(t *testing.T) {
	ctx := context.Background()
	primaryID := int64(1)
	standbyID := int64(2)
	primary := &Account{ID: primaryID, Platform: PlatformOpenAI}
	repo := &standbyAccountRepoStub{accounts: map[int64]*Account{primaryID: primary}}
	admin := &adminServiceImpl{accountRepo: repo}

	valid := &Account{
		ID:                  standbyID,
		Platform:            PlatformOpenAI,
		StandbyForAccountID: &primaryID,
		StandbyTriggerTypes: []string{string(StandbyTriggerRateLimited)},
	}
	require.NoError(t, admin.validateStandbyAccountConfiguration(ctx, valid))

	self := *valid
	self.StandbyForAccountID = &standbyID
	require.Error(t, admin.validateStandbyAccountConfiguration(ctx, &self))

	mismatch := *valid
	mismatch.Platform = PlatformGemini
	require.Error(t, admin.validateStandbyAccountConfiguration(ctx, &mismatch))

	primary.StandbyForAccountID = &standbyID
	require.Error(t, admin.validateStandbyAccountConfiguration(ctx, valid))
}

func TestOpenAIAccountSchedulerFiltersDormantStandbyAndAllowsTakeover(t *testing.T) {
	now := time.Now()
	standby, primary := standbyTestAccounts(now)
	standby.Type = AccountTypeAPIKey
	primary.Type = AccountTypeAPIKey
	repo := &standbyAccountRepoStub{accounts: map[int64]*Account{primary.ID: primary, standby.ID: standby}}
	scheduler := &defaultOpenAIAccountScheduler{service: &OpenAIGatewayService{accountRepo: repo}}

	compatible, reason := scheduler.isAccountRequestCompatibleReason(context.Background(), standby, OpenAIAccountScheduleRequest{})
	require.False(t, compatible)
	require.Equal(t, "standby_inactive", reason)

	resetAt := now.Add(time.Minute)
	primary.RateLimitResetAt = &resetAt
	compatible, reason = scheduler.isAccountRequestCompatibleReason(context.Background(), standby, OpenAIAccountScheduleRequest{})
	require.True(t, compatible)
	require.Empty(t, reason)
}

func TestGatewayCapabilityListsExcludeDormantStandbyAndIncludeActiveTakeover(t *testing.T) {
	now := time.Now()
	standby, primary := standbyTestAccounts(now)
	standby.Credentials = map[string]any{
		"model_mapping": map[string]any{"standby-model": "upstream-model"},
	}
	repo := &standbyAccountRepoStub{
		accounts: map[int64]*Account{primary.ID: primary, standby.ID: standby},
		listed:   []Account{*standby},
	}
	gateway := &GatewayService{accountRepo: repo}

	require.Nil(t, gateway.GetAvailableModels(context.Background(), nil, PlatformOpenAI))
	require.Empty(t, gateway.GetSchedulablePlatforms(context.Background(), nil))

	resetAt := now.Add(time.Minute)
	primary.RateLimitResetAt = &resetAt
	require.Equal(t, []string{"standby-model"}, gateway.GetAvailableModels(context.Background(), nil, PlatformOpenAI))
	require.Equal(t, map[string]struct{}{PlatformOpenAI: {}}, gateway.GetSchedulablePlatforms(context.Background(), nil))
}

func TestGatewayStandbyUsesGlobalQuotaAutoPauseThreshold(t *testing.T) {
	now := time.Now()
	standby, primary := standbyTestAccounts(now)
	standby.StandbyTriggerTypes = []string{string(StandbyTriggerQuota5hExhausted)}
	primary.Extra["codex_5h_used_percent"] = 80.0
	repo := &standbyAccountRepoStub{accounts: map[int64]*Account{primary.ID: primary, standby.ID: standby}}
	settings := &SettingService{}
	settings.SetOpenAIQuotaAutoPauseSettings(OpsOpenAIAccountQuotaAutoPauseSettings{DefaultThreshold5h: 0.75})
	gateway := &GatewayService{accountRepo: repo, settingService: settings}

	require.True(t, gateway.isAccountSchedulableForSelection(context.Background(), standby))
}

func TestPopulateStandbyRuntimeState(t *testing.T) {
	now := time.Now()

	t.Run("waiting includes primary identity", func(t *testing.T) {
		standby, primary := standbyTestAccounts(now)
		primary.Name = "Main OpenAI"

		populateStandbyRuntimeState(context.Background(), standby, primary, now)

		require.Equal(t, StandbyRuntimeStateWaiting, standby.StandbyRuntimeState)
		require.Equal(t, "Main OpenAI", standby.StandbyPrimaryName)
		require.Empty(t, standby.StandbyMatchedTriggerTypes)
	})

	t.Run("active keeps every matched OR condition", func(t *testing.T) {
		standby, primary := standbyTestAccounts(now)
		standby.StandbyTriggerTypes = []string{
			string(StandbyTriggerRateLimited),
			string(StandbyTriggerAccountError),
		}
		resetAt := now.Add(time.Minute)
		primary.RateLimitResetAt = &resetAt
		primary.Status = StatusError

		populateStandbyRuntimeState(context.Background(), standby, primary, now)

		require.Equal(t, StandbyRuntimeStateActive, standby.StandbyRuntimeState)
		require.Equal(t, []string{"rate_limited", "account_error"}, standby.StandbyMatchedTriggerTypes)
	})

	t.Run("unavailable wins while preserving matched conditions", func(t *testing.T) {
		standby, primary := standbyTestAccounts(now)
		resetAt := now.Add(time.Minute)
		primary.RateLimitResetAt = &resetAt
		standby.Schedulable = false

		populateStandbyRuntimeState(context.Background(), standby, primary, now)

		require.Equal(t, StandbyRuntimeStateUnavailable, standby.StandbyRuntimeState)
		require.Equal(t, []string{"rate_limited"}, standby.StandbyMatchedTriggerTypes)
	})

	t.Run("invalid configuration is explicit", func(t *testing.T) {
		standby, _ := standbyTestAccounts(now)
		standby.StandbyTriggerTypes = []string{"unsupported"}

		populateStandbyRuntimeState(context.Background(), standby, nil, now)

		require.Equal(t, StandbyRuntimeStateInvalid, standby.StandbyRuntimeState)
		require.Empty(t, standby.StandbyPrimaryName)
	})

	t.Run("missing primary is invalid", func(t *testing.T) {
		standby, _ := standbyTestAccounts(now)

		populateStandbyRuntimeState(context.Background(), standby, nil, now)

		require.Equal(t, StandbyRuntimeStateInvalid, standby.StandbyRuntimeState)
	})

	t.Run("cross-platform primary is invalid but remains identifiable", func(t *testing.T) {
		standby, primary := standbyTestAccounts(now)
		primary.Name = "Wrong Platform"
		primary.Platform = PlatformGemini

		populateStandbyRuntimeState(context.Background(), standby, primary, now)

		require.Equal(t, StandbyRuntimeStateInvalid, standby.StandbyRuntimeState)
		require.Equal(t, "Wrong Platform", standby.StandbyPrimaryName)
	})
}

func TestAdminStandbyRuntimeEnrichmentLoadsPrimariesInOneBatch(t *testing.T) {
	now := time.Now()
	standby1, primary := standbyTestAccounts(now)
	primary.Name = "Primary"
	primary.Extra["codex_5h_used_percent"] = 80.0
	standby1.StandbyTriggerTypes = []string{string(StandbyTriggerQuota5hExhausted)}
	standby2 := *standby1
	standby2.ID = 3

	repo := &standbyAccountRepoStub{accounts: map[int64]*Account{primary.ID: primary}}
	settings := &SettingService{}
	settings.SetOpenAIQuotaAutoPauseSettings(OpsOpenAIAccountQuotaAutoPauseSettings{DefaultThreshold5h: 0.75})
	admin := &adminServiceImpl{accountRepo: repo, settingService: settings}

	err := admin.enrichStandbyRuntimeStates(context.Background(), []*Account{standby1, &standby2})

	require.NoError(t, err)
	require.Equal(t, 1, repo.getByIDsCalls)
	require.Equal(t, []int64{primary.ID}, repo.getByIDsIDs)
	require.Equal(t, StandbyRuntimeStateActive, standby1.StandbyRuntimeState)
	require.Equal(t, StandbyRuntimeStateActive, standby2.StandbyRuntimeState)
	require.Equal(t, "Primary", standby1.StandbyPrimaryName)
	require.Equal(t, []string{"quota_5h_exhausted"}, standby1.StandbyMatchedTriggerTypes)
}

func TestStandbyRuntimeFieldsAreExcludedFromSchedulerCacheJSON(t *testing.T) {
	account := &Account{
		StandbyRuntimeState:        StandbyRuntimeStateActive,
		StandbyPrimaryName:         "Primary",
		StandbyMatchedTriggerTypes: []string{"rate_limited"},
	}

	raw, err := json.Marshal(account)

	require.NoError(t, err)
	require.NotContains(t, string(raw), "StandbyRuntimeState")
	require.NotContains(t, string(raw), "StandbyPrimaryName")
	require.NotContains(t, string(raw), "StandbyMatchedTriggerTypes")
}
