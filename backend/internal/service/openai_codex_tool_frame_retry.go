package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func openAIStreamFailedEventUpstreamStatus(payload []byte, message string) int {
	if openAIStreamFailedEventIndicatesRateLimit(payload, message) {
		return http.StatusTooManyRequests
	}
	return http.StatusBadGateway
}

func openAIStreamFailedEventIndicatesRateLimit(payload []byte, message string) bool {
	combined := strings.ToLower(strings.TrimSpace(message))
	for _, path := range []string{
		"response.error.type",
		"response.error.code",
		"error.type",
		"error.code",
		"type",
		"code",
	} {
		if v := strings.TrimSpace(gjsonString(payload, path)); v != "" {
			combined += " " + strings.ToLower(v)
		}
	}
	return strings.Contains(combined, "usage_limit_reached") ||
		strings.Contains(combined, "rate_limit_exceeded")
}

func gjsonString(body []byte, path string) string {
	return strings.TrimSpace(gjson.GetBytes(body, path).String())
}

func isOpenAI429Failover(err error) bool {
	var failoverErr *UpstreamFailoverError
	return errors.As(err, &failoverErr) && failoverErr != nil && failoverErr.StatusCode == http.StatusTooManyRequests
}

func (s *OpenAIGatewayService) shouldRetryCodexToolFrameAfter429(
	account *Account,
	headers http.Header,
	body []byte,
	retryTried bool,
	err error,
) bool {
	if retryTried || !isOpenAI429Failover(err) {
		return false
	}
	return shouldRetryCodexToolFrameFromUsageLimit(account, headers, time.Now())
}

func shouldSuppressCodexToolFrame429AccountMark(account *Account, headers http.Header, requestBody []byte) bool {
	if !openAIRequestBodyHasCodexToolFrame(requestBody) {
		return false
	}
	if account == nil {
		return false
	}
	if isCodexToolFrameForceAfter5hEnabled(account) {
		return true
	}
	if !resolveAccountExtraBoolDefault(account.Extra, openAICodexToolFrame429NoCooldownKey, true) {
		return false
	}
	return shouldRetryCodexToolFrameFromUsageLimit(account, headers, time.Now())
}

func (s *OpenAIGatewayService) shouldSuppressCodexToolFrame429AccountMark(c *gin.Context, account *Account, headers http.Header, requestBody []byte, passthrough bool, upstreamRequestID string, message string) bool {
	s.recordUnexpectedCodexToolFrame429(c, account, passthrough, headers, upstreamRequestID, message, requestBody)
	if !shouldSuppressCodexToolFrame429AccountMark(account, headers, requestBody) {
		return false
	}
	return true
}

func (s *OpenAIGatewayService) appendCodexToolFrameRetryEvent(
	c *gin.Context,
	account *Account,
	passthrough bool,
	upstreamRequestID string,
	statusCode int,
	message string,
	body []byte,
	kind string,
) {
	if c == nil {
		return
	}
	if strings.TrimSpace(kind) == "" {
		kind = "tool_frame_retry"
	}
	event := OpsUpstreamErrorEvent{
		Platform:           PlatformOpenAI,
		UpstreamStatusCode: statusCode,
		UpstreamRequestID:  strings.TrimSpace(upstreamRequestID),
		Passthrough:        passthrough,
		Kind:               kind,
		Message:            message,
		RequestSnapshot:    buildOpenAIUpstreamRequestSnapshot(body),
	}
	if event.Message == "" {
		event.Message = "retrying once with Codex tool-frame after 5h-only usage limit"
	}
	if account != nil {
		event.Platform = account.Platform
		event.AccountID = account.ID
		event.AccountName = account.Name
	}
	appendOpsUpstreamError(c, event)
}

func (s *OpenAIGatewayService) recordUnexpectedCodexToolFrame429(
	c *gin.Context,
	account *Account,
	passthrough bool,
	headers http.Header,
	upstreamRequestID string,
	message string,
	body []byte,
) {
	if c == nil || !openAIRequestBodyHasCodexToolFrame(body) {
		return
	}
	if isCodexToolFrameForceAfter5hEnabled(account) {
		s.appendCodexToolFrameRetryEvent(
			c,
			account,
			passthrough,
			upstreamRequestID,
			http.StatusTooManyRequests,
			message,
			body,
			"tool_frame_unexpected_429",
		)
		return
	}
	if !shouldRetryCodexToolFrameFromUsageLimit(account, headers, time.Now()) {
		return
	}
	s.appendCodexToolFrameRetryEvent(
		c,
		account,
		passthrough,
		upstreamRequestID,
		http.StatusTooManyRequests,
		message,
		body,
		"tool_frame_unexpected_429",
	)
}

func (s *OpenAIGatewayService) applyCodexToolFrameForRetry(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	headers http.Header,
	passthrough bool,
	upstreamRequestID string,
	message string,
) ([]byte, bool) {
	nextBody, changed := appendCodexToolFrameIfNeeded(body)
	if !changed {
		s.recordUnexpectedCodexToolFrame429(c, account, passthrough, headers, upstreamRequestID, message, body)
		return body, false
	}
	s.persistCodexUsageSnapshotForRetry(ctx, account, headers)
	s.appendCodexToolFrameRetryEvent(c, account, passthrough, upstreamRequestID, http.StatusTooManyRequests, message, body, "tool_frame_retry")
	return nextBody, true
}
