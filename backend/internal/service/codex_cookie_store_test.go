package service

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func codexCookieTestRequest(t *testing.T, path string) *http.Request {
	t.Helper()
	r, err := http.NewRequest(http.MethodPost, "https://chatgpt.com"+path, nil)
	require.NoError(t, err)
	return r
}

func TestCodexCookieLifecycleAndRouteIsolation(t *testing.T) {
	var store codexCookieStore
	a := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"chatgpt_cookie": "seed=old"}}
	first := codexCookieTestRequest(t, "/backend-api/codex/responses")
	first.Header.Set("Cookie", "downstream=must-not-forward")
	jar := store.prepare(first, a, "http://proxy-a")
	require.Equal(t, "seed=old", first.Header.Get("Cookie"))
	jar.receive(first.URL, []*http.Cookie{{Name: "seed", Value: "updated", Path: "/", Secure: true}, {Name: "ephemeral", Value: "new", Path: "/backend-api/codex", Secure: true}})
	next := codexCookieTestRequest(t, "/backend-api/codex/responses")
	store.prepare(next, a, "http://proxy-a")
	require.Contains(t, next.Header.Get("Cookie"), "seed=updated")
	require.Contains(t, next.Header.Get("Cookie"), "ephemeral=new")
	require.NotContains(t, next.Header.Get("Cookie"), "downstream")
	jar.receive(next.URL, []*http.Cookie{{Name: "seed", Path: "/", MaxAge: -1}})
	deleted := codexCookieTestRequest(t, "/backend-api/codex/responses")
	store.prepare(deleted, a, "http://proxy-a")
	require.NotContains(t, deleted.Header.Get("Cookie"), "seed=", "deleted seed must not be re-imported")
	otherPath := codexCookieTestRequest(t, "/backend-api/me")
	store.prepare(otherPath, a, "http://proxy-a")
	require.Empty(t, otherPath.Header.Get("Cookie"), "cookie path must be honored")
	for _, other := range []struct {
		account *Account
		proxy   string
	}{{a, "http://proxy-b"}, {&Account{ID: 8, Platform: PlatformOpenAI, Type: AccountTypeOAuth}, "http://proxy-a"}} {
		r := codexCookieTestRequest(t, "/backend-api/codex/responses")
		store.prepare(r, other.account, other.proxy)
		require.NotContains(t, r.Header.Get("Cookie"), "ephemeral")
	}
}

func TestCodexCookiesWithoutSeedExpiryAndHostBoundary(t *testing.T) {
	var store codexCookieStore
	a := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	r := codexCookieTestRequest(t, "/backend-api/codex/responses")
	jar := store.prepare(r, a, "")
	require.Empty(t, r.Header.Get("Cookie"))
	jar.receive(r.URL, []*http.Cookie{{Name: "server", Value: "issued", Path: "/", Expires: time.Now().Add(time.Hour)}, {Name: "expired", Value: "no", Path: "/", Expires: time.Now().Add(-time.Hour)}, {Name: "foreign", Value: "no", Domain: "example.com", Path: "/"}})
	next := codexCookieTestRequest(t, "/backend-api/codex/responses")
	store.prepare(next, a, "")
	require.Equal(t, "server=issued", next.Header.Get("Cookie"))
	telemetry, err := http.NewRequest(http.MethodPost, "https://ab.chatgpt.com/otlp/v1/metrics", nil)
	require.NoError(t, err)
	require.Nil(t, store.prepare(telemetry, a, ""))
	require.Empty(t, telemetry.Header.Get("Cookie"))
	other := *a
	other.Type = AccountTypeAPIKey
	require.Nil(t, store.prepare(codexCookieTestRequest(t, "/backend-api/codex/responses"), &other, ""))
	a.Credentials = map[string]any{"chatgpt_cookie": "replacement=yes"}
	reset := codexCookieTestRequest(t, "/backend-api/codex/responses")
	store.prepare(reset, a, "")
	require.Equal(t, "replacement=yes", reset.Header.Get("Cookie"))
}
