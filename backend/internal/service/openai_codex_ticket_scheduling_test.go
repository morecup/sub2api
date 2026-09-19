package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpenAICodexTicket_AccountDisabledOverridesGlobal(t *testing.T) {
	account := ticketTestAccount(41)
	account.Status = StatusActive
	account.Extra = map[string]any{OpenAICodexTicketDisabledExtraKey: true}
	model := openAICodexTicketDefaultModel
	cfg := config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true, Models: []string{model}, HarvestProxyURL: "http://harvest.example:8080"}
	upstream := &httpUpstreamRecorder{responses: []*http.Response{codexTicketResponse()}}
	svc := ticketTestService(t, cfg, upstream)
	svc.accountRepo = &codexTicketRefreshRepo{accounts: []Account{*account}}
	svc.refreshOpenAICodexTickets(context.Background())
	svc.probeOnceOpenAICodexTicket(context.Background(), account, model)
	require.Empty(t, upstream.requests)
	require.False(t, svc.openAICodexTicketBlocksAccount(account, model))
	require.Empty(t, OpenAICodexTicketStatuses(account, cfg, time.Now()))
	h := http.Header{}
	h.Set(openAICodexTurnStateHeader, "client-state")
	require.NoError(t, svc.applyOpenAICodexTicket(context.Background(), account, model, h))
	svc.storeOpenAICodexTicket(context.Background(), account, &openAICodexTicket{Model: model, State: fakeCodexTicketState(332), Length: 332, ExpiresAt: time.Now().Add(time.Hour)})
	require.NoError(t, svc.applyOpenAICodexTicket(context.Background(), account, model, h))
	require.Equal(t, "client-state", h.Get(openAICodexTurnStateHeader))
	account.Extra[OpenAICodexTicketDisabledExtraKey] = false
	require.NoError(t, svc.applyOpenAICodexTicket(context.Background(), account, model, h))
	require.Equal(t, fakeCodexTicketState(332), h.Get(openAICodexTurnStateHeader))
}

func TestOpenAICodexTicket_PausedSchedulingSkipsHarvest(t *testing.T) {
	for _, direct := range []bool{false, true} {
		name := "background"
		if direct {
			name = "direct_probe"
		}
		t.Run(name, func(t *testing.T) {
			account := ticketTestAccount(41)
			account.Status = StatusActive
			account.Schedulable = false
			repo := &codexTicketRefreshRepo{accounts: []Account{*account}}
			upstream := &httpUpstreamRecorder{responses: []*http.Response{codexTicketResponse()}}
			model := openAICodexTicketDefaultModel
			svc := ticketTestService(t, config.OpenAICodexTicketConfig{
				Enabled: true, HarvestProxyURL: "http://harvest.example:8080", Models: []string{model},
			}, upstream)
			svc.accountRepo = repo
			probe := func() {
				if direct {
					svc.probeOnceOpenAICodexTicket(context.Background(), account, model)
				} else {
					svc.refreshOpenAICodexTickets(context.Background())
				}
			}
			probe()
			require.Empty(t, upstream.requests)
			require.Empty(t, repo.updates)
			require.Nil(t, svc.lookupOpenAICodexTicket(account, model))

			account.Schedulable = true
			repo.accounts = []Account{*account}
			probe()
			require.Len(t, upstream.requests, 1)
			require.Len(t, repo.updates, 1)
			require.NotNil(t, svc.lookupOpenAICodexTicket(account, model))
		})
	}
}
