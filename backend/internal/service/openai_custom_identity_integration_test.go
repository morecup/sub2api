package service

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Imported convergence flags must not change the captured OAuth profile on any
// forwarding path, even if an earlier attempt left a staged identity behind.
func TestCapturedOAuthIdentityIgnoresConvergenceAcrossTransports(t *testing.T) {
	account := testCodexProfileAccount(7315, 5)
	account.Credentials = map[string]any{"chatgpt_account_id": "account-7315", "chatgpt_account_is_fedramp": true}
	profile := codexClientProfileForAccount(account)
	svc := &OpenAIGatewayService{}
	for _, mode := range []string{"off", "device", "session", "full"} {
		t.Run(mode, func(t *testing.T) {
			account.Extra[codexFingerprintModeExtraKey] = mode
			account.Extra[codexFingerprintSeedExtraKey] = testCodexFingerprintSeed
			require.Equal(t, codexFingerprintOff, account.GetCodexFingerprintMode())
			var taskSessions []string
			for _, task := range []string{"task-one", "task-two"} {
				c := newFingerprintStageTestContext(t)
				c.Request.Header.Set("session-id", task)
				require.Nil(t, resolveCodexFingerprintIDsFromRequest(account, c.Request.Header))
				stageCodexFingerprintIDs(c, &codexFingerprintIDs{
					accountID: account.ID, mode: codexFingerprintFull,
					installationID: "wrong-installation", sessionID: "wrong-session", threadID: "wrong-thread",
				})
				body := []byte(`{"model":"gpt-5.6-sol","input":[],"stream":true,"prompt_cache_key":"` + task + `"}`)
				expectedSession := resolveCodexSessionUUID(account.ID, 0, task, "")
				taskSessions = append(taskSessions, expectedSession)
				check := func(h http.Header) {
					require.Equal(t, "true", h.Get("x-openai-fedramp"))
					require.Equal(t, profile.UserAgent(), h.Get("user-agent"))
					require.Equal(t, profile.CodexVersion, h.Get("version"))
					require.Empty(t, h.Get("x-codex-installation-id"), "captured installation ID travels in turn metadata and the body")
					require.Equal(t, expectedSession, h.Get("session-id"))
					require.Equal(t, expectedSession, h.Get("thread-id"))
					require.Empty(t, h.Get("session_id"))
					md := h.Get(openAIWSTurnMetadataHeader)
					require.Equal(t, profile.InstallationID, gjson.Get(md, "installation_id").String())
					require.Equal(t, expectedSession, gjson.Get(md, "session_id").String())
					require.Equal(t, expectedSession, gjson.Get(md, "thread_id").String())
				}
				for _, passthrough := range []bool{false, true} {
					var req *http.Request
					var err error
					if passthrough {
						req, err = svc.buildUpstreamRequestOpenAIPassthrough(context.Background(), c, account, body, "token")
					} else {
						req, err = svc.buildUpstreamRequest(context.Background(), c, account, body, "token", true, task, false)
					}
					require.NoError(t, err)
					check(req.Header)
					raw, err := io.ReadAll(req.Body)
					require.NoError(t, err)
					require.NoError(t, req.Body.Close())
					decoded := decodeRecorderRequestBody(req.Header.Get("content-encoding"), raw)
					require.Equal(t, expectedSession, gjson.GetBytes(decoded, "client_metadata.session_id").String())
					require.Equal(t, expectedSession, gjson.GetBytes(decoded, "client_metadata.thread_id").String())
					require.Equal(t, profile.InstallationID, gjson.GetBytes(decoded, "client_metadata.x-codex-installation-id").String())
				}
				h, _, err := svc.buildOpenAIWSHeaders(context.Background(), c, account, "token", OpenAIWSProtocolDecision{}, false, "", "", task, "gpt-5.6-sol")
				require.NoError(t, err)
				check(h)
			}
			require.NotEqual(t, taskSessions[0], taskSessions[1])
		})
	}
}
