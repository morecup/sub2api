//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type gatewayCooldownRepo struct {
	AccountRepository
	account *Account
	calls   int
	id      int64
	until   time.Time
	reason  string
	ctxErr  error
	err     error
}

func (r *gatewayCooldownRepo) GetByID(context.Context, int64) (*Account, error) {
	return r.account, nil
}

func (r *gatewayCooldownRepo) SetTempUnschedulable(ctx context.Context, id int64, until time.Time, reason string) error {
	r.calls++
	r.id, r.until, r.reason, r.ctxErr = id, until, reason, ctx.Err()
	return r.err
}

func gatewayCooldownAccount(platform string) *Account {
	return &Account{ID: 918, Platform: platform, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
		Extra:       map[string]any{upstreamGatewayCooldownExtraKey: true},
		Credentials: map[string]any{"pool_mode": true, "custom_error_codes_enabled": true, "custom_error_codes": []any{401}},
	}
}

func TestUpstreamGatewayCooldownStatusAndOptIn(t *testing.T) {
	for _, platform := range []string{PlatformOpenAI, PlatformGrok, PlatformAnthropic, PlatformGemini, PlatformAntigravity} {
		for _, status := range []int{200, 400, 429, 500, 502, 503, 504, 529} {
			for _, enabled := range []any{nil, false, true, "true"} {
				t.Run(fmt.Sprintf("%s/%d/%v", platform, status, enabled), func(t *testing.T) {
					repo := &gatewayCooldownRepo{}
					rl := &RateLimitService{accountRepo: repo}
					account := gatewayCooldownAccount(platform)
					account.Extra[upstreamGatewayCooldownExtraKey] = enabled
					before := time.Now()
					matched := rl.observeUpstreamGatewayErrorCooldown(context.Background(), account, &http.Response{StatusCode: status})
					want := enabled == true && (status == 502 || status == 503)
					require.Equal(t, want, matched)
					if !want {
						require.Zero(t, repo.calls)
						return
					}
					require.Equal(t, 1, repo.calls)
					require.Equal(t, account.ID, repo.id)
					require.WithinRange(t, repo.until, before.Add(12*time.Hour), time.Now().Add(12*time.Hour))
					var state TempUnschedState
					require.NoError(t, json.Unmarshal([]byte(repo.reason), &state))
					require.Equal(t, status, state.StatusCode)
					require.Equal(t, int64(12*60*60), state.UntilUnix-state.TriggeredAtUnix)
					require.Contains(t, state.ErrorMessage, fmt.Sprintf("HTTP %d", status))
					require.Contains(t, state.ErrorMessage, "12-hour")
					require.False(t, account.IsPoolModeRetryableStatus(status))
					// Neither custom error policies nor a known model may shorten/widen it.
					require.Equal(t, ErrorPolicyTempUnscheduled, rl.CheckErrorPolicy(context.Background(), account, status, nil, "model"))
					require.True(t, rl.HandleUpstreamError(context.Background(), account, status, nil, nil, "model"))
					require.Equal(t, 1, repo.calls)
				})
			}
		}
	}
}

func TestUpstreamGatewayCooldownPersistsAfterClientCancellation(t *testing.T) {
	repo := &gatewayCooldownRepo{}
	rl := &RateLimitService{accountRepo: repo}
	svc := &OpenAIGatewayService{rateLimitService: rl}
	rl.SetAccountRuntimeBlocker(svc)
	account := gatewayCooldownAccount(PlatformOpenAI)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.True(t, rl.observeUpstreamGatewayErrorCooldown(ctx, account, &http.Response{StatusCode: 503}))
	require.NoError(t, repo.ctxErr)
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
	// Persistence failure still blocks local selection and does not retry this account.
	repo.err = errors.New("database unavailable")
	require.True(t, rl.observeUpstreamGatewayErrorCooldown(ctx, account, &http.Response{StatusCode: 502}))
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
}

func TestUpstreamGatewayCooldownKeepsLongerExistingWindow(t *testing.T) {
	repo := &gatewayCooldownRepo{}
	rl := &RateLimitService{accountRepo: repo}
	account := gatewayCooldownAccount(PlatformOpenAI)
	until := time.Now().Add(24 * time.Hour)
	account.TempUnschedulableUntil = &until
	require.True(t, rl.observeUpstreamGatewayErrorCooldown(context.Background(), account, &http.Response{StatusCode: 503}))
	require.Equal(t, until, repo.until)
}

func TestUpstreamGatewayCooldownSurvivesTokenRefreshAndSettingChange(t *testing.T) {
	repo := &gatewayCooldownRepo{}
	cache := &tempUnschedCacheStub{}
	rl := &RateLimitService{accountRepo: repo, tempUnschedCache: cache}
	account := gatewayCooldownAccount(PlatformGemini)
	account.Type = AccountTypeOAuth
	require.True(t, rl.observeUpstreamGatewayErrorCooldown(context.Background(), account, &http.Response{StatusCode: 502}))
	require.Equal(t, 1, cache.setCalls)
	require.Equal(t, 502, cache.lastState.StatusCode)
	account.TempUnschedulableUntil, account.TempUnschedulableReason = &repo.until, repo.reason
	account.Extra[upstreamGatewayCooldownExtraKey] = false
	refreshRepo := &tokenRefreshAccountRepo{}
	refresh := &TokenRefreshService{accountRepo: refreshRepo, tempUnschedCache: cache}
	refresh.postRefreshActions(context.Background(), account)
	require.Zero(t, refreshRepo.clearTempCalls)
	require.Zero(t, cache.deleteCalls)
	require.False(t, account.IsSchedulable())
	repo.account = account
	recovery, err := rl.RecoverAccountAfterSuccessfulTest(context.Background(), account.ID)
	require.NoError(t, err)
	require.False(t, recovery.ClearedRateLimit)
	require.Zero(t, cache.deleteCalls)
	past := time.Now().Add(-time.Second)
	account.TempUnschedulableUntil = &past
	require.True(t, account.IsSchedulable())
}

func TestUpstreamGatewayCooldownRealHTTPAndTransportFailures(t *testing.T) {
	for _, status := range []int{0, 200, 502, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			repo := &gatewayCooldownRepo{}
			upstream := &cookieIntegrationUpstream{}
			upstream.send = func(req *http.Request) (*http.Response, error) {
				if status == 0 {
					return nil, errors.New("connection refused")
				}
				return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("original response"))}, nil
			}
			rl := &RateLimitService{accountRepo: repo}
			svc := &OpenAIGatewayService{httpUpstream: upstream, rateLimitService: rl}
			req, err := http.NewRequest(http.MethodPost, "https://example.invalid/v1/responses", nil)
			require.NoError(t, err)
			resp, err := svc.doUpstreamRequest(req, "", gatewayCooldownAccount(PlatformOpenAI))
			if status == 0 {
				require.Error(t, err)
				require.Zero(t, repo.calls)
				return
			}
			require.NoError(t, err)
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, "original response", string(body))
			if status == 200 {
				require.Zero(t, repo.calls)
			} else {
				require.Equal(t, 1, repo.calls)
			}
		})
	}
}

func TestUpstreamGatewayCooldownOverridesCapacityRetryAndWSDialFallback(t *testing.T) {
	repo := &gatewayCooldownRepo{}
	svc := &OpenAIGatewayService{rateLimitService: &RateLimitService{accountRepo: repo}}
	account := gatewayCooldownAccount(PlatformOpenAI)
	body := []byte(`{"error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded"}}`)
	err := svc.newOpenAIAccountFailoverError(account, 503, nil, body, "", true, true)
	require.False(t, err.RetryableOnSameAccount)
	require.False(t, err.RequestScopedTransient)
	require.Equal(t, GatewayFailureScopeAccount, err.Scope)
	for _, status := range []int{502, 503} {
		failover := svc.handleOpenAIWSDialTransientFailure(context.Background(), account, "model", &openAIWSDialError{StatusCode: status, Err: errors.New("handshake rejected")})
		require.NotNil(t, failover)
		require.Equal(t, status, failover.StatusCode)
		require.False(t, failover.RetryableOnSameAccount)
	}
	require.Equal(t, 2, repo.calls)
	require.Nil(t, svc.handleOpenAIWSDialTransientFailure(context.Background(), account, "model", errors.New("dial timeout")))
	require.Equal(t, 2, repo.calls)
}

func TestUpstreamGatewayCooldownAntigravityStopsFirstAttempt(t *testing.T) {
	saveAndSetBaseURLs(t)
	for _, status := range []int{502, 503} {
		upstream := &epFixedUpstream{statusCode: status}
		repo := &gatewayCooldownRepo{}
		svc := &AntigravityGatewayService{rateLimitService: &RateLimitService{accountRepo: repo}}
		account := gatewayCooldownAccount(PlatformAntigravity)
		params := newRetryParams(account, upstream, nil)
		_, err := svc.antigravityRetryLoop(params)
		var switchErr *AntigravityAccountSwitchError
		require.ErrorAs(t, err, &switchErr)
		require.Equal(t, 1, upstream.calls)
		require.Equal(t, 1, repo.calls)
	}
}

func TestUpstreamGatewayCooldownForwardingStopsFirstAttempt(t *testing.T) {
	for _, platform := range []string{PlatformOpenAI, PlatformGrok, PlatformAnthropic, PlatformGemini} {
		for _, status := range []int{502, 503} {
			t.Run(fmt.Sprintf("%s/%d", platform, status), func(t *testing.T) {
				upstream := &epFixedUpstream{statusCode: status, body: `{"error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded"}}`}
				repo := &gatewayCooldownRepo{}
				rl := &RateLimitService{accountRepo: repo}
				account := gatewayCooldownAccount(platform)
				account.Credentials["api_key"] = "fixture-key"
				account.Credentials["base_url"] = "https://example.invalid"
				c, _ := newOpenAIUpstreamErrorTestContext(t)
				cfg := &config.Config{}
				var err error
				switch platform {
				case PlatformOpenAI, PlatformGrok:
					svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream, rateLimitService: rl}
					rl.SetAccountRuntimeBlocker(svc)
					model := "gpt-6.1-terra"
					if platform == PlatformGrok {
						model = "grok-4"
					}
					_, err = svc.Forward(context.Background(), c, account, []byte(fmt.Sprintf(`{"model":%q,"input":"test","stream":false}`, model)))
					require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
				case PlatformAnthropic:
					svc := &GatewayService{cfg: cfg, httpUpstream: upstream, rateLimitService: rl, deferredService: &DeferredService{}}
					body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":10,"messages":[{"role":"user","content":"test"}]}`)
					parsed, parseErr := ParseGatewayRequest(NewRequestBodyRef(body), platform)
					require.NoError(t, parseErr)
					_, err = svc.Forward(context.Background(), c, account, parsed)
				case PlatformGemini:
					svc := &GeminiMessagesCompatService{cfg: cfg, httpUpstream: upstream, rateLimitService: rl}
					_, err = svc.ForwardAsChatCompletions(context.Background(), c, account, []byte(`{"model":"gemini-2.5-pro","messages":[{"role":"user","content":"test"}]}`))
				}
				var failover *UpstreamFailoverError
				require.ErrorAs(t, err, &failover)
				require.Equal(t, status, failover.StatusCode)
				require.False(t, failover.RetryableOnSameAccount)
				require.False(t, failover.IsGrokFastTransient())
				require.Equal(t, 1, upstream.calls)
				require.Equal(t, 1, repo.calls)
			})
		}
	}
}
