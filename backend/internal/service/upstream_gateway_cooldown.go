package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

const upstreamGatewayCooldownExtraKey = "upstream_502_503_cooldown_enabled"
const upstreamGatewayCooldown = 12 * time.Hour
const upstreamGatewayCooldownMarker = "HTTP 502/503 (12h)"

func (a *Account) ShouldCooldownUpstreamGatewayError(statusCode int) bool {
	if a == nil || (statusCode != http.StatusBadGateway && statusCode != http.StatusServiceUnavailable) {
		return false
	}
	enabled, _ := a.Extra[upstreamGatewayCooldownExtraKey].(bool)
	return enabled
}

// A later setting change or token refresh must not release an active cooldown.
func hasUpstreamGatewayCooldown(account *Account) bool {
	if account == nil || account.TempUnschedulableUntil == nil || !account.TempUnschedulableUntil.After(time.Now()) {
		return false
	}
	var state TempUnschedState
	return json.Unmarshal([]byte(account.TempUnschedulableReason), &state) == nil && state.MatchedKeyword == upstreamGatewayCooldownMarker
}

// observeUpstreamGatewayErrorCooldown only accepts an actual HTTP response.
// Transport errors and gateway-generated 502/503 errors must not trigger it.
// Run this before response classification or any same-account retry.
func (s *RateLimitService) observeUpstreamGatewayErrorCooldown(ctx context.Context, account *Account, resp *http.Response) bool {
	if s == nil || resp == nil || !account.ShouldCooldownUpstreamGatewayError(resp.StatusCode) {
		return false
	}
	now := time.Now()
	until := now.Add(upstreamGatewayCooldown)
	if account.TempUnschedulableUntil != nil && account.TempUnschedulableUntil.After(until) {
		until = *account.TempUnschedulableUntil
	}
	state := &TempUnschedState{
		UntilUnix: until.Unix(), TriggeredAtUnix: now.Unix(), StatusCode: resp.StatusCode,
		MatchedKeyword: upstreamGatewayCooldownMarker,
		RuleIndex:      -1,
		ErrorMessage:   fmt.Sprintf("Upstream HTTP %d %s: automatic 12-hour account cooldown (502/503 cooldown enabled)", resp.StatusCode, http.StatusText(resp.StatusCode)),
	}
	raw, _ := json.Marshal(state)
	// Block the local scheduler immediately, even if persistence is unavailable.
	s.notifyAccountSchedulingBlocked(account, until, upstreamGatewayCooldownExtraKey)
	if ctx == nil {
		ctx = context.Background()
	}
	stateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if s.accountRepo == nil {
		slog.Error("upstream_gateway_cooldown_persist_failed", "account_id", account.ID, "error", "account repository unavailable")
		return true
	}
	if err := s.accountRepo.SetTempUnschedulable(stateCtx, account.ID, until, string(raw)); err != nil {
		slog.Error("upstream_gateway_cooldown_persist_failed", "account_id", account.ID, "status_code", resp.StatusCode, "until", until, "error", err)
		return true
	}
	if s.tempUnschedCache != nil {
		if err := s.tempUnschedCache.SetTempUnsched(stateCtx, account.ID, state); err != nil {
			slog.Warn("upstream_gateway_cooldown_cache_failed", "account_id", account.ID, "error", err)
		}
	}
	slog.Warn("upstream_gateway_account_cooldown", "account_id", account.ID, "status_code", resp.StatusCode, "until", until, "reason", state.ErrorMessage)
	return true
}
