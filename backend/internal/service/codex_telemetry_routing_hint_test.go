package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Exercise final request construction and the actual HTTP observation boundary:
// a tiered routing hint must never turn a known model into the "other" label.
func TestCodexTelemetryTieredRoutingPreservesCapturedModelLabels(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tier := range []string{"priority", "flex", "ultrafast", "default"} {
		for _, transport := range []string{"http", "passthrough", "compact"} {
			t.Run(tier+"/"+transport, func(t *testing.T) {
				account := testCodexProfileAccount(7315, 5)
				account.Credentials = map[string]any{"chatgpt_account_id": "test-account"}
				body := []byte(`{"model":"gpt-5.6-sol","service_tier":"` + tier + `","input":[],"stream":true}`)
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				path := "/v1/responses"
				if transport == "compact" {
					path += "/compact"
				}
				c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
				payload := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-test\"}}\n\n" +
					"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-test\"}}\n\n"
				exporter := newTestCodexTelemetry()
				svc := &OpenAIGatewayService{cfg: &config.Config{}, codexTelemetry: exporter}
				svc.codexTelemetryOnce.Do(func() {})
				svc.httpUpstream = &cookieIntegrationUpstream{send: func(req *http.Request) (*http.Response, error) {
					want := "model=gpt-5.6-sol"
					if tier != "default" {
						want += ";tier=" + tier
					}
					require.Equal(t, want, req.Header.Get("x-codex-routing-hint"))
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(payload))}, nil
				}}
				var req *http.Request
				var err error
				if transport == "passthrough" {
					req, err = svc.buildUpstreamRequestOpenAIPassthrough(context.Background(), c, account, body, "fake-token")
				} else {
					req, err = svc.buildUpstreamRequest(context.Background(), c, account, body, "fake-token", true, "test-task", false)
				}
				require.NoError(t, err)
				defer req.Body.Close()
				resp, err := svc.doOpenAIUpstream(req, "", account)
				require.NoError(t, err)
				got, err := io.ReadAll(resp.Body)
				require.NoError(t, err)
				require.NoError(t, resp.Body.Close())
				require.Equal(t, payload, string(got))
				require.NotEmpty(t, exporter.series)
				names := make(map[string]bool)
				for key := range exporter.series {
					require.Equal(t, "gpt-5.6-sol", key.model)
					names[key.name] = true
				}
				require.True(t, names["codex.api_request"])
				if transport != "compact" {
					require.True(t, names["codex.sse_event"])
				}
			})
		}
	}
}

func TestCodexTelemetryRoutingHintModel(t *testing.T) {
	for _, hint := range []string{"model=gpt-6-astra", "model=gpt-6-astra;tier=priority", "model=gpt-6-astra;tier=flex", "model=gpt-6-astra;tier=ultrafast"} {
		require.Equal(t, "gpt-6-astra", codexTelemetryModelFromRoutingHint(hint))
	}
	require.Empty(t, codexTelemetryModelFromRoutingHint(""))
}
