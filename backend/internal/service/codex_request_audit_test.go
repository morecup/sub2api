package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCodexRequestAuditAdminGateAndRedaction(t *testing.T) {
	temp := t.TempDir()
	t.Setenv("TEMP", temp)
	t.Setenv("TMP", temp)
	t.Setenv("TMPDIR", temp)
	const auditID = "019f85a3-654e-7542-8941-95713900af32"
	for _, role := range []string{"", "user", "admin"} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		c.Request.Header.Set("X-Sub2API-Codex-Audit", auditID)
		if role != "" {
			c.Set("api_key", &APIKey{User: &User{Role: role}})
		}
		ctx := withCodexRequestAudit(context.Background(), c)
		if role == "admin" {
			require.Equal(t, auditID, ctx.Value(codexRequestAuditKey{}))
		} else {
			require.Nil(t, ctx.Value(codexRequestAuditKey{}))
		}
		c.Request.Header.Set("X-Sub2API-Codex-Audit", "../../invalid")
		require.Nil(t, withCodexRequestAudit(context.Background(), c).Value(codexRequestAuditKey{}))
	}
	account := testCodexProfileAccount(1028, 0)
	account.codexEgressProfile = &codexEgressProfile{locale: "en-US", timezone: "America/New_York"}
	body := []byte(`{"model":"gpt-6.1-terra","input":[{"role":"developer","content":[{"type":"input_text","text":"<codex_apps_client_time_context><timezone>America/New_York</timezone><current_date>2026-10-28</current_date></codex_apps_client_time_context>"}]},{"role":"user","content":[{"type":"input_text","text":"PRIVATE PROMPT"}]}]}`)
	ctx := context.WithValue(context.Background(), codexRequestAuditKey{}, auditID)
	req := httptest.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", strings.NewReader(string(body))).WithContext(ctx)
	req.Header.Set("authorization", "Bearer PRIVATE TOKEN")
	applyCodexOAuthMimicHeadersForAccount(req, account, 1, "audit", "", codexWorkDesktopOriginator, false, true, "gpt-6.1-terra")
	body, err := syncCodexOAuthMimicRequestBody(req, body, false)
	require.NoError(t, err)
	req.Header.Set("cookie", "PRIVATE COOKIE")
	applyCodexRequestCompressionRaw(req, body)
	wire, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.NotEmpty(t, wire)
	observeCodexRequestAudit(req, &http.Response{StatusCode: 200}, account, "http://PRIVATE PROXY", tlsfingerprint.CodexDesktopProfile(), nil)
	files, err := filepath.Glob(filepath.Join(temp, "sub2api-codex-audit-*.json"))
	require.NoError(t, err)
	require.Len(t, files, 1)
	raw, err := os.ReadFile(files[0])
	require.NoError(t, err)
	require.NotContains(t, string(raw), "PRIVATE")
	var snapshot codexRequestAuditSnapshot
	require.NoError(t, json.Unmarshal(raw, &snapshot))
	require.Equal(t, "America/New_York", snapshot.Timezone)
	require.True(t, snapshot.AttestationTimezoneMatches)
	require.True(t, snapshot.AttestationLocaleMatches)
	require.True(t, snapshot.MetadataBodyMatches)
	require.True(t, snapshot.SessionCacheMatches)
	require.True(t, snapshot.WSMetadataAbsent)
	require.Len(t, snapshot.Contexts, 1)
	require.Equal(t, snapshot.Timezone, snapshot.Contexts[0].Timezone)
	require.True(t, snapshot.Sent)
	require.True(t, snapshot.ProxyEnabled)
	require.Equal(t, 200, snapshot.Status)
}
