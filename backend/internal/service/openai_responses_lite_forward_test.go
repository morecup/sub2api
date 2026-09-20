package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Exercise Forward rather than only the sink: validation must not reject
// standard Responses tools before the non-passthrough Lite conversion runs.
func TestOpenAIGatewayService_LiteHostedToolForwarding(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		name := "non_passthrough_converts"
		if passthrough {
			name = "passthrough_still_validates"
		}
		t.Run(name, func(t *testing.T) {
			body := []byte(`{"model":"gpt-5.6-sol","stream":false,"instructions":"Search when necessary.","tools":[{"type":"web_search","search_context_size":"low"}],"tool_choice":{"type":"web_search"},"input":"Find recent news."}`)
			upstream := &httpUpstreamRecorder{responses: []*http.Response{
				newOpenAIRejectedFieldTestResponse(http.StatusOK, namespaceForwardOKResponse),
			}}
			account := newOpenAIOAuthNamespaceTestAccount()
			account.Extra = map[string]any{"openai_passthrough": passthrough}
			c := newOpenAIRejectedFieldTestContext(body)
			if passthrough {
				c.Request.Header.Set(responsesLiteHeader, "true")
			}
			result, err := newOpenAIRejectedFieldTestService(upstream).Forward(context.Background(), c, account, body)
			if passthrough {
				require.ErrorContains(t, err, `top-level tool type "web_search"`)
				require.Equal(t, http.StatusBadRequest, c.Writer.Status())
				require.Empty(t, upstream.bodies)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Len(t, upstream.bodies, 1)
			forwarded := upstream.bodies[0]
			require.False(t, gjson.GetBytes(forwarded, "tools").Exists())
			require.False(t, gjson.GetBytes(forwarded, "instructions").Exists())
			require.Equal(t, "additional_tools", gjson.GetBytes(forwarded, "input.0.type").String())
			require.Equal(t, "web_search", gjson.GetBytes(forwarded, "input.0.tools.0.type").String())
			require.Equal(t, "low", gjson.GetBytes(forwarded, "input.0.tools.0.search_context_size").String())
			require.Equal(t, "web_search", gjson.GetBytes(forwarded, "tool_choice.type").String())
			require.Equal(t, "developer", gjson.GetBytes(forwarded, "input.1.role").String())
			require.Equal(t, "all_turns", gjson.GetBytes(forwarded, "reasoning.context").String())
			require.False(t, gjson.GetBytes(forwarded, "parallel_tool_calls").Bool())
			require.Equal(t, "true", upstream.lastReq.Header.Get(responsesLiteHeader))
		})
	}
}
