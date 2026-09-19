//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func weeklySchedulingAccount(id int64, reset time.Time) Account {
	return Account{ID: id, Platform: PlatformGrok, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true, Concurrency: 10, Priority: 1, GroupIDs: []int64{10113},
		Extra: map[string]any{grokBillingExtraKey: map[string]any{
			"period_type": "weekly", "period_end": reset.Format(time.RFC3339Nano),
		}}}
}

func TestGrokWeeklyResetOrder(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name, period, end string
		valid             bool
	}{
		{"future", "weekly", now.Add(time.Hour).Format(time.RFC3339Nano), true},
		{"expired", "weekly", now.Add(-time.Hour).Format(time.RFC3339Nano), false},
		{"boundary", "weekly", now.Format(time.RFC3339Nano), false},
		{"monthly", "monthly", now.Add(time.Hour).Format(time.RFC3339Nano), false},
		{"invalid", "weekly", "invalid", false},
		{"missing", "weekly", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := weeklySchedulingAccount(1, now)
			a.Extra[grokBillingExtraKey] = map[string]any{"period_type": tc.period, "period_end": tc.end}
			require.Equal(t, tc.valid, !grokWeeklyResetOrder(&a, now).IsZero())
			a.Platform = PlatformOpenAI
			require.True(t, grokWeeklyResetOrder(&a, now).IsZero())
		})
	}
}

func TestGrokWeeklyResetPreservesPriorityAndOtherPlatforms(t *testing.T) {
	now := time.Now()
	early := weeklySchedulingAccount(1, now.Add(time.Hour))
	late := weeklySchedulingAccount(2, now.Add(24*time.Hour))
	unknown := weeklySchedulingAccount(3, now.Add(-time.Hour))
	other := weeklySchedulingAccount(4, now.Add(48*time.Hour))
	other.Platform = PlatformOpenAI
	high := weeklySchedulingAccount(5, now.Add(48*time.Hour))
	high.Priority = 0
	items := []*Account{&high, &late, &other, &unknown, &early}
	preferGrokWeeklyReset(items, schedulingAccount)
	require.Equal(t, []*Account{&high, &early, &other, &late, &unknown}, items)
}

func TestGrokWeeklySchedulingAllPaths(t *testing.T) {
	for _, path := range []string{"legacy", "load", "load_error", "advanced"} {
		for _, scenario := range []string{"earlier", "priority", "cooldown", "disabled", "busy", "sticky"} {
			t.Run(path+"/"+scenario, func(t *testing.T) {
				resetOpenAIAdvancedSchedulerSettingCacheForTest()
				defer resetOpenAIAdvancedSchedulerSettingCacheForTest()
				now := time.Now()
				early := weeklySchedulingAccount(1, now.Add(time.Hour))
				late := weeklySchedulingAccount(2, now.Add(96*time.Hour))
				early.LastUsedAt = &now
				late.Weight = 10000 // Neither weighting nor LRU may override the weekly order.
				expected := int64(1)
				cache := &schedulerTestGatewayCache{}
				cc := schedulerTestConcurrencyCache{loadMap: map[int64]*AccountLoadInfo{
					1: {AccountID: 1, LoadRate: 80}, 2: {AccountID: 2, LoadRate: 0},
				}}
				session := ""
				switch scenario {
				case "priority":
					late.Priority = 0
					expected = 2
				case "cooldown":
					until := now.Add(time.Hour)
					early.RateLimitResetAt = &until
					expected = 2
				case "disabled":
					early.Schedulable = false
					expected = 2
				case "busy":
					if path == "legacy" {
						t.Skip("legacy without load uses a wait plan on slot contention")
					}
					cc.acquireResults = map[int64]bool{1: false, 2: true}
					expected = 2
				case "sticky":
					session = "weekly-sticky"
					expected = 2
				}
				cfg := &config.Config{}
				cfg.Gateway.Scheduling.LoadBatchEnabled = path != "legacy"
				if path == "load_error" {
					cc.loadBatchErr = errors.New("load unavailable")
				}
				svc := &OpenAIGatewayService{accountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{late, early}},
					cache: cache, cfg: cfg, concurrencyService: NewConcurrencyService(cc)}
				if path == "advanced" {
					svc.rateLimitService = newOpenAIAdvancedSchedulerRateLimitService("true")
				}
				groupID := int64(10113)
				if session != "" {
					require.NoError(t, svc.setStickySessionAccountID(context.Background(), &groupID, session, late.ID, time.Hour))
				}
				for i := 0; i < 10; i++ {
					selection, _, err := svc.SelectAccountWithSchedulerForCapability(context.Background(), &groupID,
						"", session, "grok-4.3", nil, OpenAIUpstreamTransportAny,
						OpenAIEndpointCapabilityChatCompletions, false, false, false, PlatformGrok)
					require.NoError(t, err)
					require.NotNil(t, selection)
					require.Equal(t, expected, selection.Account.ID, fmt.Sprintf("iteration %d", i))
					if selection.ReleaseFunc != nil {
						selection.ReleaseFunc()
					}
				}
			})
		}
	}
}

func TestGrokWeeklyAdvancedTopKCannotDiscardEarlierWindow(t *testing.T) {
	now := time.Now()
	early := weeklySchedulingAccount(1, now.Add(time.Hour))
	late := weeklySchedulingAccount(2, now.Add(24*time.Hour))
	s := &defaultOpenAIAccountScheduler{}
	pool := []openAIAccountCandidateScore{
		{account: &late, score: 100, loadInfo: &AccountLoadInfo{}},
		{account: &early, score: 0, loadInfo: &AccountLoadInfo{}},
	}
	order := s.buildOpenAISelectionOrder(OpenAIAccountScheduleRequest{Platform: PlatformGrok},
		openAIAccountLoadPlan{candidates: pool, topK: 1})
	require.Len(t, order, 2)
	require.Equal(t, int64(1), order[0].account.ID)
	require.Equal(t, int64(2), order[1].account.ID)
}

func TestGrokWeeklyResetTiesKeepExistingOrder(t *testing.T) {
	now := time.Now()
	first := weeklySchedulingAccount(1, now.Add(time.Hour))
	second := weeklySchedulingAccount(2, now.Add(time.Hour))
	for _, known := range []bool{true, false} {
		if !known {
			first.Extra = nil
			second.Extra = nil
		}
		items := []*Account{&second, &first}
		preferGrokWeeklyReset(items, schedulingAccount)
		require.Equal(t, []*Account{&second, &first}, items)
	}
}
