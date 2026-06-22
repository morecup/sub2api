package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseInviteEligibilityUsesShouldShow(t *testing.T) {
	raw := map[string]any{
		"should_show":         true,
		"grant_action":        "rate_limit_reset_credit",
		"grant_amount":        float64(1),
		"remaining_referrals": nil,
	}

	got := parseInviteEligibility(raw)

	require.True(t, got.IsEligible)
	require.NotNil(t, got.ShouldShow)
	require.True(t, *got.ShouldShow)
	require.Equal(t, "rate_limit_reset_credit", got.GrantAction)
	require.NotNil(t, got.GrantAmount)
	require.EqualValues(t, 1, *got.GrantAmount)
	require.Nil(t, got.RemainingReferrals)
	require.Equal(t, raw, got.Raw)
}

func TestParseInviteEligibilityUsesIneligibleReason(t *testing.T) {
	raw := map[string]any{
		"should_show":            false,
		"ineligible_reason":      "not_enough_plan",
		"ineligible_reason_code": "PLAN_NOT_ELIGIBLE",
		"remaining_referrals":    float64(0),
	}

	got := parseInviteEligibility(raw)

	require.False(t, got.IsEligible)
	require.NotNil(t, got.ShouldShow)
	require.False(t, *got.ShouldShow)
	require.Equal(t, "not_enough_plan", got.IneligibleReason)
	require.Equal(t, "PLAN_NOT_ELIGIBLE", got.IneligibleReasonCode)
	require.Equal(t, "not_enough_plan", got.Reason)
	require.NotNil(t, got.RemainingReferrals)
	require.EqualValues(t, 0, *got.RemainingReferrals)
}

func TestParseInviteEligibilityKeepsLegacyIsEligibleFallback(t *testing.T) {
	got := parseInviteEligibility(map[string]any{
		"is_eligible": true,
		"reason":      "legacy-ok",
	})

	require.True(t, got.IsEligible)
	require.Nil(t, got.ShouldShow)
	require.Equal(t, "legacy-ok", got.Reason)
}
