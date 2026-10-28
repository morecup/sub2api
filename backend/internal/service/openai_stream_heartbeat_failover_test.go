package service

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpenAIStreamFailoverAfterHeartbeatAndPreviousAttempt(t *testing.T) {
	for _, path := range []string{"native", "passthrough"} {
		t.Run(path, func(t *testing.T) {
			c, rec := newCompactBridgeTestContext(t, false)
			// Queue/native heartbeats and a previous passthrough attempt can all
			// have used the same downstream connection before the current attempt.
			n, err := c.Writer.WriteString(":\n\n")
			require.NoError(t, err)
			RecordOpenAIStreamKeepaliveBytes(c, n)
			stop := startOpenAISSEKeepalive(c, time.Hour)
			t.Cleanup(stop)
			value, _ := c.Get(openAICompactSSEKeepaliveKey)
			require.True(t, value.(*openAICompactSSEKeepalive).beat())
			stop()
			heartbeats := rec.Body.String()
			require.Equal(t, -1, OpenAICompactKeepaliveAdjustedWrittenSize(c))

			svc := &OpenAIGatewayService{cfg: &config.Config{
				Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize, StreamKeepaliveInterval: 3600},
			}}
			account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
			run := func(body string) error {
				resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
				if path == "passthrough" {
					_, err := svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
					return err
				}
				_, err := svc.handleStreamingResponse(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
				return err
			}
			preamble := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_heartbeat\"}}\n\n" +
				"event: response.metadata\ndata: {\"type\":\"response.metadata\",\"metadata\":{}}\n\n"
			failure := "event: error\ndata: {\"type\":\"error\",\"error\":{\"code\":\"server_is_overloaded\",\"message\":\"Our servers are currently overloaded. Please try again later.\"}}\n\n" +
				"event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"output\":[],\"error\":{\"code\":\"server_is_overloaded\",\"message\":\"Our servers are currently overloaded. Please try again later.\"}}}\n\n"
			for range 2 {
				err := run(preamble + failure)
				var failoverErr *UpstreamFailoverError
				require.ErrorAs(t, err, &failoverErr)
				require.True(t, failoverErr.RequestScopedTransient)
				require.True(t, failoverErr.RetryableOnSameAccount)
				require.Equal(t, heartbeats, rec.Body.String(), "failed attempts must not flush metadata or terminal errors")
				require.Equal(t, -1, OpenAICompactKeepaliveAdjustedWrittenSize(c))
			}
			success := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"answer\"}\n\n" +
				"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_heartbeat\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"
			require.NoError(t, run(preamble+success))
			require.Equal(t, 1, strings.Count(rec.Body.String(), `"type":"response.metadata"`))
			require.Equal(t, 1, strings.Count(rec.Body.String(), `"delta":"answer"`))
			require.Contains(t, rec.Body.String(), `"type":"response.completed"`)
			require.NotContains(t, rec.Body.String(), `"type":"response.failed"`)
			require.True(t, openAIStreamClientOutputStarted(c, false))
		})
	}
}
