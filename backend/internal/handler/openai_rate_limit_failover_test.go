//go:build unit

package handler

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIRateLimitRetryFallsBackToLowerPriorityAccount(t *testing.T) {
	testOpenAIRateLimitRetryFallsBackToLowerPriorityAccount(t,
		`{"detail":"Rate limit exceeded"}`, "Rate limit exceeded")
}

func TestCodexCapacity429RetryFallsBackToLowerPriorityAccount(t *testing.T) {
	testOpenAIRateLimitRetryFallsBackToLowerPriorityAccount(t,
		`{"detail":"Codex is at capacity, please try again later"}`, "Codex is at capacity, please try again later")
}

func testOpenAIRateLimitRetryFallsBackToLowerPriorityAccount(t *testing.T, errorBody, errorMessage string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	for _, endpoint := range []string{"responses", "chat_completions", "messages"} {
		t.Run(endpoint, func(t *testing.T) {
			for _, outcome := range []string{"success", "same_account_success", "exhausted", "switch_budget_exhausted"} {
				t.Run(outcome, func(t *testing.T) {
					limited := func() *http.Response {
						return &http.Response{
							StatusCode: http.StatusTooManyRequests,
							Header: http.Header{
								"Content-Type": {"application/json"},
								"Retry-After":  {"0.001"},
							},
							Body: io.NopCloser(bytes.NewBufferString(errorBody)),
						}
					}
					success := astra200()
					if endpoint != "responses" {
						success.Header.Set("Content-Type", "text/event-stream")
						success.Body = io.NopCloser(bytes.NewBufferString("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":" + astraProSuccessResponse + "}\n\n"))
					}
					answers := []*http.Response{limited(), limited(), success}
					wantAccounts := []int64{1, 1, 2}
					wantStatus := http.StatusOK
					if outcome == "same_account_success" {
						answers = []*http.Response{limited(), success}
						wantAccounts = []int64{1, 1}
					}
					if outcome == "exhausted" {
						answers = []*http.Response{limited(), limited(), limited(), limited()}
						wantAccounts = []int64{1, 1, 2, 2}
						wantStatus = http.StatusTooManyRequests
					}
					if outcome == "switch_budget_exhausted" {
						wantAccounts = []int64{1, 1}
						wantStatus = http.StatusTooManyRequests
					}
					upstream := newAstraProCapturedUpstream(answers...)
					// This fixture has account 1 at priority 0 and account 2 at
					// priority 1, so it exercises the real priority-based selector.
					handler := newOpenAIResponsesFailoverTestHandler(t, upstream)
					if outcome == "switch_budget_exhausted" {
						handler.maxAccountSwitches = 0
					}
					body := astraProRequestBody
					if endpoint != "responses" {
						body = `{"model":"gpt-6-astra","stream":false,"max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`
					}
					c, rec := newAstraProFailoverContext(t, body)
					switch endpoint {
					case "responses":
						handler.Responses(c)
					case "chat_completions":
						c.Request.URL.Path = "/v1/chat/completions"
						handler.ChatCompletions(c)
					case "messages":
						apiKey, ok := middleware.GetAPIKeyFromContext(c)
						require.True(t, ok)
						apiKey.Group.AllowMessagesDispatch = true
						c.Request.URL.Path = "/v1/messages"
						handler.Messages(c)
					}
					_, accountIDs, _ := upstream.snapshot()
					require.Equal(t, wantAccounts, accountIDs, "each account gets only one same-account replay before priority fallback: %s", rec.Body.String())
					require.Equal(t, wantStatus, rec.Code, rec.Body.String())
					if wantStatus != http.StatusOK {
						wantMessage := errorMessage
						if endpoint == "messages" {
							wantMessage = "rate limit exceeded"
						}
						require.Contains(t, strings.ToLower(rec.Body.String()), strings.ToLower(wantMessage))
					}
					require.Empty(t, c.GetString(service.OpsUpstreamRetryPhaseKey))
				})
			}
		})
	}
}

func TestCodexCapacity429ExhaustionKeepsAccountAvailableForNextRequest(t *testing.T) {
	limited := func() *http.Response {
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     http.Header{"Content-Type": {"application/json"}, "Retry-After": {"0.001"}},
			Body:       io.NopCloser(strings.NewReader(`{"detail":"Codex is at capacity, please try again later"}`)),
		}
	}
	upstream := newAstraProCapturedUpstream(limited(), limited(), limited(), limited(), astra200())
	handler := newOpenAIResponsesFailoverTestHandler(t, upstream)
	failed, failedResponse := newAstraProFailoverContext(t, astraProRequestBody)
	handler.Responses(failed)
	require.Equal(t, http.StatusTooManyRequests, failedResponse.Code, failedResponse.Body.String())

	next, nextResponse := newAstraProFailoverContext(t, astraProRequestBody)
	handler.Responses(next)
	require.Equal(t, http.StatusOK, nextResponse.Code, nextResponse.Body.String())
	_, accountIDs, _ := upstream.snapshot()
	require.Equal(t, []int64{1, 1, 2, 2, 1}, accountIDs,
		"exhausted retries must not prevent the next request from selecting the first account")
}
