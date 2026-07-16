package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"net/http"
	"strconv"
	"strings"
)

func resolveAccountExtraBoolDefault(extra map[string]any, key string, defaultValue bool) bool {
	if len(extra) == 0 {
		return defaultValue
	}
	value, ok := extra[key]
	if !ok || value == nil {
		return defaultValue
	}
	switch v := value.(type) {
	case bool:
		return v
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(v))
		if err == nil {
			return parsed
		}
	case float64:
		return v != 0
	case float32:
		return v != 0
	case int:
		return v != 0
	case int64:
		return v != 0
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return i != 0
		}
	}
	return defaultValue
}

func (s *OpenAIGatewayService) handleFailoverSideEffectsWithCodexToolFrameSuppression(ctx context.Context, resp *http.Response, account *Account, responseBody []byte, requestBody []byte, c *gin.Context, passthrough bool, upstreamMsg string, requestedModel ...string) {
	if resp != nil && resp.StatusCode == http.StatusTooManyRequests &&
		s.shouldSuppressCodexToolFrame429AccountMark(c, account, resp.Header, requestBody, passthrough, resp.Header.Get("x-request-id"), upstreamMsg) {
		return
	}
	s.handleFailoverSideEffects(ctx, resp, account, responseBody, requestedModel...)
}

func openAIStreamErrorEventShouldFailover(payload []byte, message string) bool {
	if strings.TrimSpace(gjson.GetBytes(payload, "type").String()) != "error" {
		return false
	}
	// type=error 也可能是已经可安全转发给客户端的终止事件；仅对 morecup
	// 原本要处理的瞬时过载/可重试错误换号，避免把普通 error 事件误判为 failover。
	return isOpenAITransientProcessingError(http.StatusBadRequest, message, payload)
}

func openAIStreamEventRetryableOnSameAccount(account *Account, statusCode int, payload []byte, message string) bool {
	if account == nil {
		return false
	}
	if isOpenAITransientProcessingError(http.StatusBadRequest, message, payload) {
		return true
	}
	return account.IsPoolMode() && account.IsPoolModeRetryableStatus(statusCode)
}

func openAIStreamFailoverErrorType(statusCode int) string {
	if statusCode == http.StatusTooManyRequests {
		return "rate_limit_error"
	}
	return "upstream_error"
}

func (s *OpenAIGatewayService) writeOpenAIUpstreamRawErrorResponse(c *gin.Context, resp *http.Response, body []byte, fallbackMessage string) {
	if c == nil || resp == nil {
		return
	}
	MarkResponseCommitted(c)
	writeOpenAIPassthroughResponseHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "application/json"
	}
	if len(bytes.TrimSpace(body)) > 0 {
		c.Data(resp.StatusCode, contentType, body)
		return
	}

	message := strings.TrimSpace(fallbackMessage)
	if message == "" {
		message = fmt.Sprintf("upstream error: %d", resp.StatusCode)
	}
	errType := "upstream_error"
	if resp.StatusCode == http.StatusTooManyRequests {
		errType = "rate_limit_error"
	} else if resp.StatusCode == http.StatusBadRequest {
		errType = "invalid_request_error"
	}
	c.JSON(resp.StatusCode, gin.H{
		"error": gin.H{
			"type":    errType,
			"message": message,
		},
	})
}

func compatErrorTypeForStatus(statusCode int) string {
	switch {
	case statusCode == http.StatusBadRequest:
		return "invalid_request_error"
	case statusCode == http.StatusNotFound:
		return "not_found_error"
	case statusCode == http.StatusTooManyRequests:
		return "rate_limit_error"
	default:
		return "api_error"
	}
}

func resolveOpenAICompactMimicSessionID(c *gin.Context) string {
	if c != nil {
		if seed, ok := c.Get(openAICompactSessionSeedKey); ok {
			if seedStr, ok := seed.(string); ok && strings.TrimSpace(seedStr) != "" {
				return strings.TrimSpace(seedStr)
			}
		}
	}
	return uuid.NewString()
}
