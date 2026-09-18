package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpenAICodexTicket_AcceptedLengthsLifecycle(t *testing.T) {
	for _, targetLength := range []int{0, 292} {
		for _, length := range []int{292, 332} {
			t.Run(fmt.Sprintf("target_%d/length_%d", targetLength, length), func(t *testing.T) {
				ctx := context.Background()
				model := openAICodexTicketDefaultModel
				state := fakeCodexTicketState(length)
				response := func() *http.Response {
					h := http.Header{}
					h.Set(openAICodexTurnStateHeader, state)
					return &http.Response{StatusCode: http.StatusOK, Header: h, Body: io.NopCloser(strings.NewReader(""))}
				}
				upstream := &httpUpstreamRecorder{responses: []*http.Response{response(), response()}}
				cfg := config.OpenAICodexTicketConfig{
					Enabled: true, TargetLength: targetLength, FailClosed: true,
					HarvestProxyURL: "socks5h://harvest.example:1080", Models: []string{model},
				}
				account := ticketTestAccount(41)
				account.Status = StatusActive
				repo := &codexTicketRefreshRepo{accounts: []Account{*account}}
				svc := ticketTestService(t, cfg, upstream)
				svc.accountRepo = repo

				// Exercise background harvest, the memory cache and persistence together.
				svc.refreshOpenAICodexTickets(ctx)
				require.Len(t, upstream.requests, 1)
				ticket := svc.lookupOpenAICodexTicket(account, model)
				require.NotNil(t, ticket)
				require.Equal(t, state, ticket.State)
				require.Equal(t, length, ticket.Length)
				require.Equal(t, time.Hour, ticket.ExpiresAt.Sub(ticket.CapturedAt))
				require.Contains(t, repo.updates, openAICodexTicketExtraKey(model))
				require.Equal(t, cfg.HarvestProxyURL, upstream.lastProxyURL)

				// Round-trip stored JSON into a fresh service, as after a restart.
				persisted, err := json.Marshal(repo.updates)
				require.NoError(t, err)
				require.NoError(t, json.Unmarshal(persisted, &account.Extra))
				restarted := ticketTestService(t, cfg, upstream)
				restarted.accountRepo = &codexTicketRefreshRepo{accounts: []Account{*account}}
				statuses := OpenAICodexTicketStatuses(account, cfg, time.Now())
				require.Len(t, statuses, 1)
				require.True(t, statuses[0].Ready)
				require.False(t, statuses[0].Blocked)
				require.Equal(t, length, statuses[0].Length)
				require.Greater(t, statuses[0].RemainingSeconds, int64(3500))

				h := http.Header{}
				h.Set("User-Agent", "existing-profile")
				h.Set("session_id", "existing-session")
				h.Set("x-codex-thread-id", "existing-thread")
				h.Set(openAICodexTurnStateHeader, "stale")
				want := h.Clone()
				want.Set(openAICodexTurnStateHeader, state)
				require.NoError(t, restarted.applyOpenAICodexTicket(ctx, account, model, h))
				require.Equal(t, want, h)
				require.False(t, restarted.openAICodexTicketBlocksAccount(account, model))
				require.True(t, restarted.openAICodexTicketBlocksAccount(ticketTestAccount(42), model))

				// A valid 332 ticket must stop probing just like a 292 ticket.
				restarted.refreshOpenAICodexTickets(ctx)
				require.Len(t, upstream.requests, 1)

				// Both lengths retain the existing early-refresh window.
				cached := restarted.lookupOpenAICodexTicket(account, model)
				cached.ExpiresAt = time.Now().Add(5 * time.Minute)
				restarted.refreshOpenAICodexTickets(ctx)
				require.Len(t, upstream.requests, 2)
				require.True(t, restarted.lookupOpenAICodexTicket(account, model).ExpiresAt.After(time.Now().Add(50*time.Minute)))
			})
		}
	}
}

func TestOpenAICodexTicket_LengthAndExpiryPolicy(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name         string
		targetLength int
		length       int
		storedLength int
		expiresAt    time.Time
		wantValid    bool
	}{
		{"292", 292, 292, 292, now.Add(time.Hour), true},
		{"332", 292, 332, 332, now.Add(time.Hour), true},
		{"unsupported_312", 292, 312, 312, now.Add(time.Hour), false},
		{"332_stored_as_292", 292, 332, 292, now.Add(time.Hour), false},
		{"292_stored_as_332", 292, 292, 332, now.Add(time.Hour), false},
		{"332_expired", 292, 332, 332, now.Add(-time.Second), false},
		{"332_at_expiry", 292, 332, 332, now, false},
		{"332_without_expiry", 292, 332, 332, time.Time{}, false},
		{"custom_312", 312, 312, 312, now.Add(time.Hour), true},
		{"custom_rejects_292", 312, 292, 292, now.Add(time.Hour), false},
		{"custom_rejects_332", 312, 332, 332, now.Add(time.Hour), false},
		{"explicit_332", 332, 332, 332, now.Add(time.Hour), true},
		{"explicit_332_rejects_292", 332, 292, 292, now.Add(time.Hour), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ticket := &openAICodexTicket{State: fakeCodexTicketState(tc.length), Length: tc.storedLength, ExpiresAt: tc.expiresAt}
			require.Equal(t, tc.wantValid, ticket.valid(now, tc.targetLength))
		})
	}
}

func TestProbeOpenAICodexTicket_LengthPolicy(t *testing.T) {
	for _, tc := range []struct {
		name         string
		targetLength int
		state        string
		status       int
		wantStored   bool
	}{
		{"default_332", 0, fakeCodexTicketState(332), http.StatusOK, true},
		{"legacy_332", 292, fakeCodexTicketState(332), http.StatusOK, true},
		{"unsupported_312", 292, fakeCodexTicketState(312), http.StatusOK, false},
		{"332_bad_prefix", 292, strings.Repeat("X", 332), http.StatusOK, false},
		{"332_http_error", 292, fakeCodexTicketState(332), http.StatusServiceUnavailable, false},
		{"custom_312", 312, fakeCodexTicketState(312), http.StatusOK, true},
		{"custom_rejects_332", 312, fakeCodexTicketState(332), http.StatusOK, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			h.Set(openAICodexTurnStateHeader, tc.state)
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: tc.status, Header: h, Body: io.NopCloser(strings.NewReader(""))}}
			svc := ticketTestService(t, config.OpenAICodexTicketConfig{
				Enabled: true, TargetLength: tc.targetLength, HarvestProxyURL: "http://harvest.example:8080",
			}, upstream)
			repo := &codexTicketRefreshRepo{}
			svc.accountRepo = repo
			account := ticketTestAccount(41)
			svc.probeOnceOpenAICodexTicket(context.Background(), account, openAICodexTicketDefaultModel)
			ticket := svc.lookupOpenAICodexTicket(account, openAICodexTicketDefaultModel)
			if tc.wantStored {
				require.NotNil(t, ticket)
				require.Equal(t, tc.state, ticket.State)
				require.Len(t, repo.updates, 1)
			} else {
				require.Nil(t, ticket)
				require.Empty(t, repo.updates)
			}
		})
	}
}
