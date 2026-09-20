package service

import (
	"context"
	"strings"
	"time"
)

// Spending-limit is recoverable at the end of the observed billing period.
// When no billing snapshot is available, use a short probe rather than
// fabricating a 24h boundary from the error arrival time.
const grokSpendingLimitProbeCooldown = 10 * time.Minute

// grokExhaustedWeeklyResetAt returns the authoritative reset boundary for a
// billing snapshot that explicitly reports the included seven-day allowance
// as exhausted. A future period end is required so a stale 100% snapshot does
// not install a new cooldown after its original window has already reset.
func grokExhaustedWeeklyResetAt(account *Account, now time.Time) (time.Time, bool) {
	if account == nil {
		return time.Time{}, false
	}
	billing, err := grokBillingSnapshotFromExtra(account.Extra)
	if err != nil || billing == nil || billing.UsagePercent == nil || *billing.UsagePercent < 100 {
		return time.Time{}, false
	}
	if !strings.EqualFold(strings.TrimSpace(billing.PeriodType), "weekly") {
		return time.Time{}, false
	}
	resetAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(billing.PeriodEnd))
	if err != nil || !resetAt.After(now) {
		return time.Time{}, false
	}
	return resetAt, true
}

func grokSpendingLimitResetAt(account *Account, now time.Time) time.Time {
	if account != nil {
		if billing, err := grokBillingSnapshotFromExtra(account.Extra); err == nil && billing != nil {
			for _, raw := range []string{billing.PeriodEnd, billing.BillingPeriodEnd} {
				if resetAt, err := time.Parse(time.RFC3339, strings.TrimSpace(raw)); err == nil && resetAt.After(now) {
					return resetAt
				}
			}
		}
	}
	return now.Add(grokSpendingLimitProbeCooldown)
}

// clearGrokNeedsReauthExtra drops the soft reauth flag after successful refresh
// or reauth. Best-effort; never fails the request path.
func clearGrokNeedsReauthExtra(ctx context.Context, repo AccountRepository, accountID int64) {
	if repo == nil || accountID <= 0 {
		return
	}
	stateCtx, cancel := openAIAccountStateContext(ctx)
	defer cancel()
	_ = repo.UpdateExtra(stateCtx, accountID, map[string]any{
		"grok_needs_reauth":        false,
		"grok_needs_reauth_reason": "",
		"grok_needs_reauth_at":     "",
	})
}

func accountGrokNeedsReauth(account *Account) bool {
	if account == nil {
		return false
	}
	if account.Status == StatusError {
		msg := strings.ToLower(account.ErrorMessage)
		if strings.Contains(msg, "spending limit") || strings.Contains(msg, "reauthorize") {
			return true
		}
	}
	if v, ok := account.Extra["grok_needs_reauth"].(bool); ok && v {
		return true
	}
	if s, ok := account.Extra["grok_needs_reauth"].(string); ok {
		return strings.EqualFold(s, "true") || s == "1"
	}
	return false
}
