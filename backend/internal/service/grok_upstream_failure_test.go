//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const testGrokCapacityBody = `{"code":"Some resource has been exhausted","error":"The service is temporarily at capacity. Please retry your request shortly."}`

func TestClassifyGrokFastTransientFailure(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		scope      GatewayFailureScope
		reason     GatewayFailureReason
	}{
		{
			name:       "capacity 429",
			statusCode: http.StatusTooManyRequests,
			body:       testGrokCapacityBody,
			scope:      GatewayFailureScopeProvider,
			reason:     GrokFailureReasonCapacity,
		},
		{
			name:       "connection refused 503",
			statusCode: http.StatusServiceUnavailable,
			body:       "upstream connect error or disconnect/reset before headers. reset reason: remote connection failure, transport failure reason: delayed connect error: Connection refused",
			scope:      GatewayFailureScopeRoute,
			reason:     GrokFailureReasonConnection,
		},
		{
			name:       "connection termination 503",
			statusCode: http.StatusServiceUnavailable,
			body:       "upstream connect error or disconnect/reset before headers. reset reason: connection termination",
			scope:      GatewayFailureScopeRoute,
			reason:     GrokFailureReasonConnection,
		},
		{
			name:       "connection timeout 503",
			statusCode: http.StatusServiceUnavailable,
			body:       "upstream connect error or disconnect/reset before headers. reset reason: connection timeout",
			scope:      GatewayFailureScopeRoute,
			reason:     GrokFailureReasonConnection,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			class, ok := classifyGrokFastTransientFailure(tt.statusCode, []byte(tt.body))
			require.True(t, ok)
			require.Equal(t, tt.scope, class.scope)
			require.Equal(t, tt.reason, class.reason)
		})
	}

	_, ok := classifyGrokFastTransientFailure(http.StatusTooManyRequests, []byte(`{"error":{"message":"rate limited"}}`))
	require.False(t, ok, "a true quota 429 must retain the existing cooldown policy")
	_, ok = classifyGrokFastTransientFailure(http.StatusTooManyRequests, []byte(`{"code":"Some resource has been exhausted","error":"monthly request capacity exhausted"}`))
	require.False(t, ok, "capacity wording without the observed temporary-service marker must not bypass quota cooldown")
	_, ok = classifyGrokFastTransientFailure(http.StatusServiceUnavailable, []byte(`{"error":"maintenance"}`))
	require.False(t, ok, "an unrelated 503 must retain the existing temporary-unschedule policy")
}

func TestGrokFastTransientDoesNotMutateAccountHealth(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
	}{
		{name: "capacity", statusCode: http.StatusTooManyRequests, body: testGrokCapacityBody},
		{name: "connection", statusCode: http.StatusServiceUnavailable, body: "upstream connect error or disconnect/reset before headers. reset reason: connection timeout"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &grokQuotaAccountRepo{}
			svc := &OpenAIGatewayService{accountRepo: repo}
			account := &Account{ID: 7101, Platform: PlatformGrok, Type: AccountTypeOAuth}
			headers := http.Header{
				"Retry-After":                    []string{"600"},
				"X-Ratelimit-Remaining-Requests": []string{"0"},
			}

			svc.handleGrokAccountUpstreamError(context.Background(), account, tt.statusCode, headers, []byte(tt.body))

			require.Zero(t, repo.rateLimitedCalls)
			require.Zero(t, repo.tempUnschedCalls)
			require.Zero(t, repo.updateCalls)
			require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
			require.Nil(t, account.RateLimitedAt)
			require.Nil(t, account.RateLimitResetAt)
			require.Nil(t, account.TempUnschedulableUntil)
		})
	}
}

func TestGrokFastTransientFailoverErrorSuppressesAccountPenalty(t *testing.T) {
	account := &Account{ID: 7201, Platform: PlatformGrok, Type: AccountTypeOAuth}
	headers := http.Header{"X-Request-Id": []string{"req-capacity"}}
	failoverErr := newGrokUpstreamFailoverError(account, http.StatusTooManyRequests, headers, []byte(testGrokCapacityBody), true)

	require.True(t, failoverErr.IsGrokFastTransient())
	require.True(t, failoverErr.ShouldRetryNextAccount())
	require.False(t, failoverErr.RetryableOnSameAccount, "the dedicated zero-delay loop owns same-account retries")
	require.False(t, failoverErr.ShouldReportAccountScheduleFailure())
	require.Equal(t, GatewayFailureStageInference, failoverErr.Stage)
	require.Equal(t, GatewayFailureScopeProvider, failoverErr.Scope)
	require.Equal(t, GrokFailureReasonCapacity, failoverErr.Reason)
	require.Equal(t, testGrokCapacityBody, string(failoverErr.ResponseBody))
	require.Equal(t, "req-capacity", failoverErr.ResponseHeaders.Get("X-Request-Id"))
}

func TestAppendGrokOpsUpstreamErrorRetainsPerAttemptResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	SetOpsUpstreamRetryMetadata(c, 3, "primary")
	headers := http.Header{
		"Content-Type":  []string{"application/json"},
		"Retry-After":   []string{"0"},
		"X-Request-Id":  []string{"req-third-attempt"},
		"Authorization": []string{"must-not-be-retained"},
	}

	appendGrokOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform:           PlatformGrok,
		AccountID:          7301,
		UpstreamStatusCode: http.StatusTooManyRequests,
		Kind:               "failover",
	}, headers, []byte(testGrokCapacityBody))

	raw, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := raw.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 1)
	event := events[0]
	require.Equal(t, testGrokCapacityBody, event.UpstreamResponseBody)
	require.Equal(t, []string{"application/json"}, event.UpstreamResponseHeaders["Content-Type"])
	require.Equal(t, []string{"0"}, event.UpstreamResponseHeaders["Retry-After"])
	require.Equal(t, []string{"req-third-attempt"}, event.UpstreamResponseHeaders["X-Request-Id"])
	require.NotContains(t, event.UpstreamResponseHeaders, "Authorization")
	require.Equal(t, 3, event.RetryAttempt)
	require.Equal(t, "primary", event.RetryPhase)
	require.Equal(t, string(GatewayFailureScopeProvider), event.Scope)
	require.Equal(t, string(GrokFailureReasonCapacity), event.Reason)
}

func TestAppendGrokOpsUpstreamErrorRetainsTrueQuota429Response(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	body := `{"error":{"message":"monthly request quota exhausted","type":"rate_limit_error"}}`

	appendGrokOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform:           PlatformGrok,
		AccountID:          7302,
		UpstreamStatusCode: http.StatusTooManyRequests,
		Kind:               "failover",
	}, http.Header{"Retry-After": []string{"600"}}, []byte(body))

	raw, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := raw.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 1)
	require.Equal(t, body, events[0].UpstreamResponseBody)
	require.Equal(t, []string{"600"}, events[0].UpstreamResponseHeaders["Retry-After"])
	require.Empty(t, events[0].Reason, "retaining a body must not reclassify a true quota response as capacity")
}

func TestGrokFastTransientAllowsExactlyOneAccountFollowup(t *testing.T) {
	svc := &OpenAIGatewayService{}
	state := &OpenAIOAuth429FailoverState{}
	account := &Account{ID: 7401, Platform: PlatformGrok, Type: AccountTypeOAuth}
	fastErr := newGrokUpstreamFailoverError(account, http.StatusServiceUnavailable, nil, []byte("upstream connect error or disconnect/reset before headers. reset reason: connection timeout"), false)

	require.False(t, svc.ShouldStopOpenAIUpstreamFailover(account, fastErr, 1, state))
	require.True(t, state.GrokFollowupPending())
	require.True(t, svc.ShouldStopOpenAIUpstreamFailover(account, &UpstreamFailoverError{StatusCode: http.StatusInternalServerError}, 2, state), "any failure from the follow-up account must stop")
}
