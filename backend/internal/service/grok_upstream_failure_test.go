//go:build unit

package service

import (
	"context"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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

func TestClassifyGrokUpstreamFailure_FreeUsage(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{
			name:   "code free-usage-exhausted",
			status: http.StatusTooManyRequests,
			body:   `{"error":{"code":"subscription:free-usage-exhausted","message":"You've used all the included free usage for model grok-4.5. Usage resets over a rolling 24-hour window."}}`,
		},
		{
			name:   "chinese body without 429",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"模型额度用完，请稍后再试"}}`,
		},
		{
			name:   "token pair with free marker",
			status: http.StatusOK,
			body:   `{"error":{"message":"free usage tokens (actual / limit): 2000000 / 2000000 for model grok-4.5"}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := classifyGrokUpstreamFailure(tc.status, []byte(tc.body), "grok-4.5")
			require.Equal(t, GrokFailureFreeUsage, d.Class)
			require.True(t, d.ShouldCooldown)
			require.True(t, d.ShouldFailover)
			require.False(t, d.BlockModel, "free-usage must not soft-block models")
			require.Equal(t, grokFreeUsageProbeCooldown, d.Cooldown)
		})
	}
}

func TestClassifyGrokUpstreamFailure_EmptyUpstream(t *testing.T) {
	d := classifyGrokUpstreamFailure(http.StatusBadGateway, []byte(`empty model output: no content/tool_calls`), "grok-4.5")
	require.Equal(t, GrokFailureEmptyUpstream, d.Class)
	require.True(t, d.ShouldCooldown)
	require.True(t, d.ShouldFailover)
	require.True(t, d.BlockModel)
	require.Equal(t, 4*time.Minute, d.Cooldown)
}

func TestClassifyGrokUpstreamFailure_ModelCapacityUsesShortCooldown(t *testing.T) {
	d := classifyGrokUpstreamFailure(http.StatusTooManyRequests,
		[]byte(`{"error":{"message":"The model is currently at capacity due to high demand"}}`), "grok-4.6")
	require.Equal(t, GrokFailureModelCapacity, d.Class)
	require.Equal(t, time.Minute, d.Cooldown)
	require.False(t, d.BlockModel)
}

func TestClassifyGrokUpstreamFailure_Billing(t *testing.T) {
	d := classifyGrokUpstreamFailure(http.StatusForbidden, []byte(`{"code":"personal-team-blocked:spending-limit","error":"spending limit reached"}`), "")
	require.Equal(t, GrokFailureBilling, d.Class)
	require.True(t, d.ShouldCooldown)
	require.True(t, d.ShouldFailover)
}

func TestClassifyGrokUpstreamFailure_GrokSubscriptionRequiredIsBilling(t *testing.T) {
	d := classifyGrokUpstreamFailure(http.StatusPaymentRequired,
		[]byte(`{"error":{"message":"You have run out of credits or need a Grok subscription"}}`), "grok-4.6")
	require.Equal(t, GrokFailureBilling, d.Class)
	require.True(t, d.ShouldFailover)
	require.True(t, d.ShouldCooldown)
}

func TestGrokRetryableOnSameAccount_CapacityAndRateLimit(t *testing.T) {
	account := &Account{ID: 9105, Platform: PlatformGrok, Type: AccountTypeOAuth}
	require.True(t, grokRetryableOnSameAccount(account, http.StatusTooManyRequests,
		[]byte(`{"error":{"message":"The model is currently at capacity due to high demand"}}`)))
	require.False(t, grokRetryableOnSameAccount(account, http.StatusTooManyRequests,
		[]byte(`{"error":{"message":"rate limit exceeded"}}`)))
	require.False(t, grokRetryableOnSameAccount(account, http.StatusPaymentRequired,
		[]byte(`{"error":{"message":"You have run out of credits or need a Grok subscription"}}`)))
	poolAccount := &Account{ID: 9108, Platform: PlatformGrok, Type: AccountTypeOAuth,
		Credentials: map[string]any{"pool_mode": true}}
	require.False(t, grokRetryableOnSameAccount(poolAccount, http.StatusTooManyRequests,
		[]byte(`{"error":{"code":"subscription:free-usage-exhausted"}}`)),
		"pool free-usage must fail over instead of retrying the exhausted account")
	require.False(t, grokRetryableOnSameAccount(account, http.StatusBadRequest,
		[]byte(`{"error":{"message":"capacity field is invalid"}}`)))
	nonGrok := &Account{ID: 9106, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	require.False(t, grokRetryableOnSameAccount(nonGrok, http.StatusTooManyRequests,
		[]byte(`{"error":{"message":"model at capacity"}}`)))
}

func TestShouldMarkGrokTeamModelRateLimit_ExcludesCapacity(t *testing.T) {
	require.False(t, shouldMarkGrokTeamModelRateLimit(http.StatusTooManyRequests,
		[]byte(`{"error":{"message":"The model is currently at capacity due to high demand"}}`)))
	require.True(t, shouldMarkGrokTeamModelRateLimit(http.StatusTooManyRequests,
		[]byte(`{"error":{"message":"rate limit exceeded"}}`)))
	require.True(t, shouldMarkGrokTeamModelRateLimit(http.StatusBadRequest,
		[]byte(`{"error":{"code":"subscription:free-usage-exhausted"}}`)))
	require.False(t, shouldMarkGrokTeamModelRateLimit(http.StatusBadRequest,
		[]byte(`{"error":{"message":"invalid request"}}`)))
}

func TestGrokSameAccountRetryMetadata_CapacityDeadline(t *testing.T) {
	account := &Account{ID: 9107, Platform: PlatformGrok, Type: AccountTypeOAuth}
	retryable, delay, deadline, retryMax := grokSameAccountRetryMetadata(account, http.StatusTooManyRequests,
		[]byte(`{"error":{"message":"model capacity exceeded"}}`))
	require.True(t, retryable)
	require.Equal(t, 500*time.Millisecond, delay)
	require.WithinDuration(t, time.Now().Add(30*time.Second), deadline, 2*time.Second)
	require.Equal(t, 1, retryMax)

	retryable, delay, deadline, retryMax = grokSameAccountRetryMetadata(account, http.StatusTooManyRequests,
		[]byte(`{"error":{"message":"rate limit exceeded"}}`))
	require.False(t, retryable)
	require.Zero(t, delay)
	require.True(t, deadline.IsZero())
	require.Zero(t, retryMax)
}

func TestClassifyGrokUpstreamFailure_ValidationNoCool(t *testing.T) {
	d := classifyGrokUpstreamFailure(http.StatusBadRequest, []byte(`{"error":{"message":"invalid tool schema"}}`), "")
	require.Equal(t, GrokFailureNone, d.Class)
	require.False(t, d.ShouldCooldown)
	require.False(t, d.ShouldFailover)
}

func TestClassifyGrokUpstreamFailure_FreeUsageWinsOver5xx(t *testing.T) {
	// Proxy may rewrite free-usage into synthetic 502; body must win.
	d := classifyGrokUpstreamFailure(http.StatusBadGateway, []byte(`subscription:free-usage-exhausted for model grok-4.3`), "grok-4.3")
	require.Equal(t, GrokFailureFreeUsage, d.Class)
	require.NotEqual(t, GrokFailureServer, d.Class)
}

func TestClassifyGrokUpstreamFailure_CompatibilityDoesNotCooldown(t *testing.T) {
	cases := []string{
		`{"error":{"message":"Could not decode the compaction blob. Ensure it is unmodified from the compact response"}}`,
		`{"code":"compaction_decode_error","message":"invalid response history"}`,
	}
	for _, body := range cases {
		d := classifyGrokUpstreamFailure(http.StatusUnprocessableEntity, []byte(body), "grok-4.6")
		require.Equal(t, GrokFailureCompatibility, d.Class, body)
		require.True(t, d.ShouldFailover, body)
		require.False(t, d.ShouldCooldown, body)
		require.Zero(t, d.Cooldown, body)
	}
}

func TestClassifyGrokUpstreamFailure_CompatibilityRequiresClientError(t *testing.T) {
	body := []byte(`{"error":{"message":"upstream failed while handling the compaction blob"}}`)
	for _, status := range []int{http.StatusBadGateway, http.StatusInternalServerError} {
		d := classifyGrokUpstreamFailure(status, body, "grok-4.6")
		require.NotEqual(t, GrokFailureCompatibility, d.Class)
		require.True(t, d.ShouldCooldown)
	}
}

func TestClassifyGrokUpstreamFailure_GenericShapeErrorDoesNotFailover(t *testing.T) {
	d := classifyGrokUpstreamFailure(http.StatusBadRequest,
		[]byte(`{"error":{"message":"data did not match any variant of the untagged enum content"}}`), "grok-4.6")
	require.NotEqual(t, GrokFailureCompatibility, d.Class)
	require.False(t, d.ShouldFailover)
}

func TestShouldFailoverGrokUpstreamError_FreeUsageBody(t *testing.T) {
	svc := &OpenAIGatewayService{}
	body := []byte(`{"error":{"code":"subscription:free-usage-exhausted","message":"free usage exhausted"}}`)
	require.True(t, svc.shouldFailoverGrokUpstreamError(http.StatusBadRequest, body))
}

func TestShouldFailoverGrokUpstreamError_CompatibilityBody(t *testing.T) {
	svc := &OpenAIGatewayService{}
	body := []byte(`{"error":{"message":"Could not decode the compaction blob"}}`)
	require.True(t, svc.shouldFailoverGrokUpstreamError(http.StatusUnprocessableEntity, body))
}

func TestShouldFailoverGrokUpstreamError_ContentPolicyStillNoFailover(t *testing.T) {
	svc := &OpenAIGatewayService{}
	body := []byte(`{"error":{"code":"new_sensitive","message":"text is sensitive"}}`)
	require.False(t, svc.shouldFailoverGrokUpstreamError(http.StatusForbidden, body))
}

func TestHandleGrokAccountUpstreamError_FreeUsageBodyCoolsAccount(t *testing.T) {
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := &Account{ID: 9101, Platform: PlatformGrok, Type: AccountTypeOAuth}
	before := time.Now()
	body := []byte(`{"error":{"code":"subscription:free-usage-exhausted","message":"You've used all the included free usage. Usage resets over a rolling 24-hour window."}}`)

	svc.handleGrokAccountUpstreamError(context.Background(), account, http.StatusBadRequest, nil, body)

	require.Equal(t, 1, repo.tempUnschedCalls)
	require.Equal(t, "grok free usage exhausted", repo.lastTempUnschedReason)
	// Rolling-window exhaustion must use a short probe cooldown when no
	// upstream absolute reset is available; it must not start a 24h lock here.
	require.Greater(t, repo.lastTempUnschedUntil, before.Add(grokFreeUsageProbeCooldown-time.Second))
	require.Less(t, repo.lastTempUnschedUntil, before.Add(grokFreeUsageProbeCooldown+time.Second))
}

func TestHandleGrokAccountUpstreamError_FreeUsageUsesUpstreamReset(t *testing.T) {
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := &Account{ID: 9102, Platform: PlatformGrok, Type: AccountTypeOAuth}
	body := []byte(`{"error":{"code":"subscription:free-usage-exhausted","message":"free usage exhausted; rolling 24-hour window"}}`)

	svc.handleGrokAccountUpstreamError(context.Background(), account, http.StatusTooManyRequests,
		http.Header{"Retry-After": []string{"3600"}}, body)

	require.Zero(t, repo.tempUnschedCalls)
	require.WithinDuration(t, time.Now().Add(time.Hour), repo.lastRateLimitResetAt, 2*time.Second)
}

func TestHandleGrokAccountUpstreamError_EmptyOutputCoolsAccount(t *testing.T) {
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := &Account{ID: 9102, Platform: PlatformGrok, Type: AccountTypeOAuth}
	before := time.Now()

	svc.handleGrokAccountUpstreamError(
		context.Background(), account, http.StatusBadGateway, nil,
		[]byte(`empty model output: no content/tool_calls`),
	)

	require.Equal(t, 1, repo.tempUnschedCalls)
	require.Equal(t, "grok empty model output", repo.lastTempUnschedReason)
	require.WithinDuration(t, before.Add(4*time.Minute), repo.lastTempUnschedUntil, time.Second)
}

func TestHandleGrokAccountUpstreamError_MultiAgentCapacityBlocksOnlyThatModel(t *testing.T) {
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := &Account{ID: 9120, Platform: PlatformGrok, Type: AccountTypeOAuth}
	ctx := withGrokTeamRateLimitModel(context.Background(), "grok-4.20-multi-agent-0309")

	svc.handleGrokAccountUpstreamError(
		ctx, account, http.StatusBadGateway, nil,
		[]byte(`{"error":{"message":"engine_overloaded"}}`),
	)

	require.Zero(t, repo.tempUnschedCalls)
	require.True(t, isGrokModelQuotaBlocked(account.ID, "grok-4.20-multi-agent-0309", time.Now()))
	require.False(t, isGrokModelQuotaBlocked(account.ID, "grok-4.5", time.Now()))
}

func TestHandleGrokAccountUpstreamError_CapacityNeverCoolsAccount(t *testing.T) {
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := &Account{ID: 9121, Platform: PlatformGrok, Type: AccountTypeOAuth}
	ctx := withGrokTeamRateLimitModel(context.Background(), "grok-4.6")

	svc.handleGrokAccountUpstreamError(ctx, account, http.StatusTooManyRequests, nil,
		[]byte(`{"error":{"message":"The model is currently at capacity due to high demand"}}`))

	require.Zero(t, repo.tempUnschedCalls)
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
}

func TestHandleGrokAccountUpstreamError_FreeUsageDoesNotCoolPoolMode(t *testing.T) {
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := &Account{
		ID:       9103,
		Platform: PlatformGrok,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"pool_mode": true,
		},
	}
	body := []byte(`{"error":{"code":"subscription:free-usage-exhausted","message":"free usage exhausted"}}`)

	svc.handleGrokAccountUpstreamError(context.Background(), account, http.StatusBadRequest, nil, body)

	require.Zero(t, repo.tempUnschedCalls)
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
}

func TestHandleGrokAccountUpstreamError_ContentPolicyStillNoMutation(t *testing.T) {
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := &Account{ID: 9104, Platform: PlatformGrok, Type: AccountTypeOAuth}
	body := []byte(`{"error":{"code":"new_sensitive","message":"text is sensitive"}}`)

	svc.handleGrokAccountUpstreamError(context.Background(), account, http.StatusForbidden, nil, body)

	require.Zero(t, repo.tempUnschedCalls)
}

func TestHandleGrokAccountUpstreamError_Entitlement403Unchanged(t *testing.T) {
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := &Account{ID: 9105, Platform: PlatformGrok, Type: AccountTypeOAuth}
	before := time.Now()

	svc.handleGrokAccountUpstreamError(
		context.Background(), account, http.StatusForbidden, nil,
		[]byte(`{"error":{"message":"subscription required"}}`),
	)

	require.Equal(t, 1, repo.tempUnschedCalls)
	require.Equal(t, "grok access or entitlement denied", repo.lastTempUnschedReason)
	require.Greater(t, repo.lastTempUnschedUntil, before.Add(29*time.Minute))
	require.Less(t, repo.lastTempUnschedUntil, before.Add(31*time.Minute))
}
