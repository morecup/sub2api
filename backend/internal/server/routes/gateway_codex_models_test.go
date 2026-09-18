package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGatewayRoutesCodexModelsManifestPathIsRegistered(t *testing.T) {
	router := newGatewayRoutesTestRouter()

	registered := make(map[string]string)
	registeredPost := make(map[string]string)
	for _, route := range router.Routes() {
		if route.Method == http.MethodGet {
			registered[route.Path] = route.Handler
		} else if route.Method == http.MethodPost {
			registeredPost[route.Path] = route.Handler
		}
	}

	require.NotEmpty(t, registered["/backend-api/codex/models"], "GET /backend-api/codex/models should be registered")
	require.NotEmpty(t, registered["/backend-api/plugins/featured"], "GET /backend-api/plugins/featured should be registered")
	require.NotEmpty(t, registered["/backend-api/ps/plugins/*path"], "GET /backend-api/ps/plugins/*path should be registered")
	require.NotEmpty(t, registered["/backend-api/models"], "GET /backend-api/models should be registered")
	require.NotEmpty(t, registered["/backend-api/codex/remote/control/environments"], "GET /backend-api/codex/remote/control/environments should be registered")
	require.NotEmpty(t, registered["/backend-api/checkout_pricing_config/configs/:country"], "GET /backend-api/checkout_pricing_config/configs/:country should be registered")
	require.NotEmpty(t, registered["/backend-api/payments/payment_methods"], "GET /backend-api/payments/payment_methods should be registered")
	require.NotEmpty(t, registered["/backend-api/subscriptions/auto_top_up/settings"], "GET /backend-api/subscriptions/auto_top_up/settings should be registered")
	require.NotEmpty(t, registered["/backend-api/wham/accounts/check"], "GET /backend-api/wham/accounts/check should be registered")
	require.NotEmpty(t, registered["/backend-api/wham/onboarding/context"], "GET /backend-api/wham/onboarding/context should be registered")
	require.NotEmpty(t, registered["/backend-api/wham/profiles/me"], "GET /backend-api/wham/profiles/me should be registered")
	require.NotEmpty(t, registered["/backend-api/system_hints"], "GET /backend-api/system_hints should be registered")
	require.NotEmpty(t, registered["/backend-api/wham/usage"], "GET /backend-api/wham/usage should be registered")
	require.NotEmpty(t, registered["/backend-api/connectors/directory/list"], "GET /backend-api/connectors/directory/list should be registered")
	require.NotEmpty(t, registered["/v1/models"], "GET /v1/models should be registered")
	require.NotEmpty(t, registered["/models"], "GET /models should be registered")
	require.Equal(t, registered["/v1/models"], registered["/models"], "root alias should use the same platform-aware handler")
	for _, path := range []string{
		"/backend-api/ps/mcp",
		"/backend-api/codex/analytics-events/events",
		"/backend-api/f/conversation",
		"/backend-api/f/conversation/prepare",
		"/backend-api/sentinel/chat-requirements/prepare",
		"/backend-api/wham/analytics-events/events",
		"/backend-api/wham/onboarding/desktop/complete",
		"/backend-api/wham/remote/control/server/enroll",
		"/backend-api/wham/rate-limit-reset-credits/consume",
		"/backend-api/wham/statsig/bootstrap",
	} {
		require.NotEmpty(t, registeredPost[path], "POST %s should be registered", path)
	}
}

func TestDispatchCodexModelsGatewayKeepsOnlyOpenAIOnLiveManifestHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		platform   string
		wantOpenAI bool
	}{
		{platform: service.PlatformOpenAI, wantOpenAI: true},
		{platform: service.PlatformComposite},
		{platform: service.PlatformGrok},
		{platform: service.PlatformDeepseek},
	}

	for _, tt := range tests {
		t.Run(tt.platform, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodGet, "/models?client_version=0.147.0", nil)
			c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
				Group: &service.Group{Platform: tt.platform},
			})
			called := ""

			dispatchCodexModelsGateway(c,
				func(c *gin.Context) { called = "openai" },
				func(c *gin.Context) { called = "generated" },
			)

			if tt.wantOpenAI {
				require.Equal(t, "openai", called)
			} else {
				require.Equal(t, "generated", called)
			}
		})
	}
}
