package service

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	GrokFailureReasonCapacity     GatewayFailureReason = "grok_upstream_capacity"
	GrokFailureReasonConnection   GatewayFailureReason = "grok_upstream_connection"
	GrokFastSameAccountRetryCount                      = 3

	grokFastFailureOpsBodyMaxBytes = 4096
	grokFastRetryFollowupKey       = "grok_fast_retry_followup"
)

type grokFastTransientClass struct {
	scope  GatewayFailureScope
	reason GatewayFailureReason
}

// SetGrokFastRetryFollowup tells the WebSocket HTTP bridge that its first turn
// is the single cross-account follow-up and therefore must be attempted once.
func SetGrokFastRetryFollowup(c *gin.Context, followup bool) {
	if c == nil {
		return
	}
	c.Set(grokFastRetryFollowupKey, followup)
}

func isGrokFastRetryFollowup(c *gin.Context) bool {
	if c == nil {
		return false
	}
	value, ok := c.Get(grokFastRetryFollowupKey)
	followup, valid := value.(bool)
	return ok && valid && followup
}

// classifyGrokFastTransientFailure identifies the two response classes that are
// safe to retry immediately without changing account health state.
func classifyGrokFastTransientFailure(statusCode int, responseBody []byte) (grokFastTransientClass, bool) {
	text := strings.ToLower(strings.TrimSpace(string(responseBody)))
	if statusCode == http.StatusTooManyRequests && strings.Contains(text, "temporarily at capacity") {
		return grokFastTransientClass{
			scope:  GatewayFailureScopeProvider,
			reason: GrokFailureReasonCapacity,
		}, true
	}

	if statusCode == http.StatusServiceUnavailable && isGrokConnectionFailureBody(text) {
		return grokFastTransientClass{
			scope:  GatewayFailureScopeRoute,
			reason: GrokFailureReasonConnection,
		}, true
	}
	return grokFastTransientClass{}, false
}

func isGrokConnectionFailureBody(lowerBody string) bool {
	if strings.Contains(lowerBody, "upstream connect error or disconnect/reset before headers") {
		return true
	}
	for _, marker := range []string{
		"reset reason: remote connection failure",
		"delayed connect error: connection refused",
		"reset reason: connection termination",
		"reset reason: connection timeout",
	} {
		if strings.Contains(lowerBody, marker) {
			return true
		}
	}
	return false
}

func (e *UpstreamFailoverError) IsGrokFastTransient() bool {
	if e == nil {
		return false
	}
	return e.Reason == GrokFailureReasonCapacity || e.Reason == GrokFailureReasonConnection
}

func IsGrokFastTransientFailoverError(err error) bool {
	var failoverErr *UpstreamFailoverError
	return errors.As(err, &failoverErr) && failoverErr.IsGrokFastTransient()
}

func newGrokUpstreamFailoverError(
	account *Account,
	statusCode int,
	headers http.Header,
	responseBody []byte,
	legacyRetryableOnSameAccount bool,
) *UpstreamFailoverError {
	failoverErr := &UpstreamFailoverError{
		StatusCode:             statusCode,
		ResponseBody:           append([]byte(nil), responseBody...),
		ResponseHeaders:        headers.Clone(),
		RetryableOnSameAccount: legacyRetryableOnSameAccount,
	}
	if account == nil || account.Platform != PlatformGrok {
		return failoverErr
	}
	if class, ok := classifyGrokFastTransientFailure(statusCode, responseBody); ok {
		failoverErr.Stage = GatewayFailureStageInference
		failoverErr.Scope = class.scope
		failoverErr.Reason = class.reason
		failoverErr.NextAccountAction = NextAccountRetry
		failoverErr.RetryableOnSameAccount = false
		failoverErr.SuppressAccountScheduleFailure = true
	}
	return failoverErr
}

// appendGrokOpsUpstreamError retains the exact Grok 429/503 response body and a
// diagnostic header allowlist. The general error body logging switch still
// controls unrelated upstream responses.
func appendGrokOpsUpstreamError(c *gin.Context, event OpsUpstreamErrorEvent, headers http.Header, responseBody []byte) {
	if event.UpstreamStatusCode == http.StatusTooManyRequests || event.UpstreamStatusCode == http.StatusServiceUnavailable {
		event.UpstreamResponseBody = truncateString(string(responseBody), grokFastFailureOpsBodyMaxBytes)
		event.UpstreamResponseHeaders = grokDiagnosticResponseHeaders(headers)
	}
	if class, ok := classifyGrokFastTransientFailure(event.UpstreamStatusCode, responseBody); ok {
		event.Stage = string(GatewayFailureStageInference)
		event.Scope = string(class.scope)
		event.Reason = string(class.reason)
	}
	appendOpsUpstreamError(c, event)
}

func grokDiagnosticResponseHeaders(headers http.Header) map[string][]string {
	if headers == nil {
		return nil
	}
	allowed := []string{
		"Content-Type",
		"Retry-After",
		"X-Request-Id",
		"Xai-Request-Id",
		"X-Ratelimit-Limit-Requests",
		"X-Ratelimit-Remaining-Requests",
		"X-Ratelimit-Reset-Requests",
		"X-Ratelimit-Limit-Tokens",
		"X-Ratelimit-Remaining-Tokens",
		"X-Ratelimit-Reset-Tokens",
		"Cf-Ray",
	}
	out := make(map[string][]string)
	for _, name := range allowed {
		values := headers.Values(name)
		if len(values) == 0 {
			continue
		}
		out[http.CanonicalHeaderKey(name)] = append([]string(nil), values...)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
