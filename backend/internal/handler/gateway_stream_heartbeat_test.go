//go:build unit

package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGatewayQueueHeartbeatPreservesOpenAIFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, afterOutput := range []bool{false, true} {
		name := "only_heartbeats"
		if afterOutput {
			name = "after_output"
		}
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			before := service.OpenAICompactKeepaliveAdjustedWrittenSize(c)
			for range 3 {
				n, err := c.Writer.Write([]byte(SSEPingFormatComment))
				require.NoError(t, err)
				recordGatewayStreamHeartbeat(c, n)
				c.Writer.Flush()
			}
			require.True(t, gatewayStreamHasOnlyHeartbeats(c))
			require.Equal(t, before, service.OpenAICompactKeepaliveAdjustedWrittenSize(c),
				"queue heartbeats must use the service's failover accounting")

			if afterOutput {
				_, err := c.Writer.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"answer\"}\n\n"))
				require.NoError(t, err)
				require.False(t, gatewayStreamHasOnlyHeartbeats(c))
			}
			failoverErr := &service.UpstreamFailoverError{
				StatusCode:             http.StatusServiceUnavailable,
				RequestScopedTransient: true,
			}
			require.Equal(t, !afterOutput, openAIForwardMayFailover(c, before, failoverErr))
			if !afterOutput {
				(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, failoverErr, true)
				require.Equal(t, 1, strings.Count(rec.Body.String(), "event: response.failed"))
			}
		})
	}
}
