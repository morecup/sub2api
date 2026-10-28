package service

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIStreamMetadataPreservesFailoverBoundary(t *testing.T) {
	gin.SetMode(gin.TestMode)

	paths := []struct {
		name string
		run  func(*OpenAIGatewayService, *gin.Context, *http.Response, *Account) error
	}{
		{
			name: "native",
			run: func(s *OpenAIGatewayService, c *gin.Context, resp *http.Response, account *Account) error {
				_, err := s.handleStreamingResponse(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
				return err
			},
		},
		{
			name: "passthrough",
			run: func(s *OpenAIGatewayService, c *gin.Context, resp *http.Response, account *Account) error {
				_, err := s.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
				return err
			},
		},
	}
	events := []string{
		"response.metadata",
		"response.queued",
		"codex.rate_limits",
		"codex.response.metadata",
		"responsesapi.websocket_timing",
	}
	outcomes := []string{"early_overload", "success", "overload_after_content"}

	for _, path := range paths {
		for _, eventType := range events {
			for _, outcome := range outcomes {
				t.Run(path.name+"/"+eventType+"/"+outcome, func(t *testing.T) {
					// The production failure was created(0), in_progress(1),
					// metadata(2), error(3), failed(4), with no model output.
					// A large prelude also verifies that buffering cannot flush
					// metadata merely because it exceeds the writer's buffer.
					metadata := fmt.Sprintf(`{"type":%q,"sequence_number":2,"metadata":{"padding":%q}}`,
						eventType, strings.Repeat("x", 16*1024))
					stream := "event: response.created\n" +
						"data: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"resp_metadata\"}}\n\n" +
						"event: response.in_progress\n" +
						"data: {\"type\":\"response.in_progress\",\"sequence_number\":1,\"response\":{\"id\":\"resp_metadata\"}}\n\n" +
						"event: " + eventType + "\ndata: " + metadata + "\n\n"
					if outcome != "early_overload" {
						stream += "event: response.output_text.delta\n" +
							"data: {\"type\":\"response.output_text.delta\",\"delta\":\"answer\"}\n\n"
					}
					if outcome == "success" {
						stream += "event: response.completed\n" +
							"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_metadata\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"answer\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"
					} else {
						stream += "event: error\n" +
							"data: {\"type\":\"error\",\"sequence_number\":3,\"error\":{\"type\":\"service_unavailable_error\",\"code\":\"server_is_overloaded\",\"message\":\"Our servers are currently overloaded. Please try again later.\"}}\n\n" +
							"event: response.failed\n" +
							"data: {\"type\":\"response.failed\",\"sequence_number\":4,\"response\":{\"id\":\"resp_metadata\",\"status\":\"failed\",\"output\":[],\"error\":{\"type\":\"service_unavailable_error\",\"code\":\"server_is_overloaded\",\"message\":\"Our servers are currently overloaded. Please try again later.\"}}}\n\n"
					}

					svc := &OpenAIGatewayService{cfg: &config.Config{
						Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize},
					}}
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
					account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
					resp := &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"X-Request-Id": []string{"metadata-overload"}},
						Body:       io.NopCloser(strings.NewReader(stream)),
					}
					err := path.run(svc, c, resp, account)
					var failoverErr *UpstreamFailoverError
					if outcome == "early_overload" {
						require.ErrorAs(t, err, &failoverErr)
						require.True(t, failoverErr.RetryableOnSameAccount)
						require.True(t, failoverErr.RequestScopedTransient)
						require.Equal(t, http.StatusServiceUnavailable, failoverErr.ClientStatusCode)
						require.False(t, c.Writer.Written())
						require.Empty(t, rec.Body.String())
						return
					}

					body := rec.Body.String()
					require.Equal(t, 1, strings.Count(body, metadata))
					require.Contains(t, body, `"delta":"answer"`)
					require.Less(t, strings.Index(body, metadata), strings.Index(body, `"delta":"answer"`))
					if outcome == "success" {
						require.NoError(t, err)
						require.Contains(t, body, `"type":"response.completed"`)
					} else {
						require.Error(t, err)
						require.False(t, errors.As(err, &failoverErr), "content already delivered must not be replayed")
						require.Equal(t, 1, strings.Count(body, `"type":"response.failed"`))
					}
				})
			}
		}
	}
}
