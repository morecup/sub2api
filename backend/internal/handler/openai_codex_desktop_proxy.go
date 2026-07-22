package handler

import (
	"io"
	"net/http"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// CodexDesktopProxy handles the non-Responses REST calls made by the latest
// Codex Desktop App (plugins, MCP, analytics, wham usage/tasks and connectors).
// Responses/models keep their specialized handlers; this path deliberately
// avoids their request-body normalization.
func (h *OpenAIGatewayHandler) CodexDesktopProxy(c *gin.Context) {
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || apiKey == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"type": "authentication_error", "message": "Invalid API key"}})
		return
	}
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request_error", "message": "Failed to read request body"}})
		return
	}

	// Desktop backend endpoints are account-scoped and require ChatGPT OAuth
	// credentials. Iterate through scheduler candidates until an OAuth account
	// is found; API-key accounts must never receive these internal paths.
	excluded := make(map[int64]struct{})
	for attempt := 0; attempt < h.maxAccountSwitches+1; attempt++ {
		selection, _, selectErr := h.gatewayService.SelectAccountWithSchedulerForCapability(
			c.Request.Context(),
			apiKey.GroupID,
			"",
			"",
			"",
			excluded,
			service.OpenAIUpstreamTransportHTTPSSE,
			service.OpenAIEndpointCapabilityChatCompletions,
			false,
			true,
			false,
			service.PlatformOpenAI,
		)
		if selectErr != nil || selection == nil || selection.Account == nil {
			status := http.StatusBadGateway
			if len(excluded) == 0 {
				status = http.StatusServiceUnavailable
			}
			c.JSON(status, gin.H{"error": gin.H{"type": "upstream_error", "message": "No OpenAI OAuth account is available for Codex Desktop"}})
			return
		}
		account := selection.Account
		if account.Type != service.AccountTypeOAuth || account.Platform != service.PlatformOpenAI {
			excluded[account.ID] = struct{}{}
			continue
		}

		resp, proxyErr := h.gatewayService.ProxyCodexDesktopEndpoint(c.Request.Context(), c, account, c.Request.URL.Path, body)
		if proxyErr != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"type": "upstream_error", "message": proxyErr.Error()}})
			return
		}
		service.WriteCodexDesktopProxyResponse(c, resp)
		return
	}

	c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"type": "upstream_error", "message": "No OpenAI OAuth account is available for Codex Desktop"}})
}
