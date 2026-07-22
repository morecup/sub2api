package openai

import (
	"net/url"
	"sync"
	"testing"
	"time"
)

func TestSessionStore_Stop_Idempotent(t *testing.T) {
	store := NewSessionStore()

	store.Stop()
	store.Stop()

	select {
	case <-store.stopCh:
		// ok
	case <-time.After(time.Second):
		t.Fatal("stopCh 未关闭")
	}
}

func TestSessionStore_Stop_Concurrent(t *testing.T) {
	store := NewSessionStore()

	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			store.Stop()
		}()
	}

	wg.Wait()

	select {
	case <-store.stopCh:
		// ok
	case <-time.After(time.Second):
		t.Fatal("stopCh 未关闭")
	}
}

func TestBuildAuthorizationURLForPlatform_OpenAI(t *testing.T) {
	authURL := BuildAuthorizationURLForPlatform("state-1", "challenge-1", DefaultRedirectURI, OAuthPlatformOpenAI)
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("Parse URL failed: %v", err)
	}
	q := parsed.Query()
	if got := q.Get("client_id"); got != ClientID {
		t.Fatalf("client_id mismatch: got=%q want=%q", got, ClientID)
	}
	if got := q.Get("codex_cli_simplified_flow"); got != "true" {
		t.Fatalf("codex flow mismatch: got=%q want=true", got)
	}
	if got := q.Get("id_token_add_organizations"); got != "true" {
		t.Fatalf("id_token_add_organizations mismatch: got=%q want=true", got)
	}
}

// 桌面画像参数：scope 含 connectors、originator/app_version/streamlined 恒定，
// stable_id 按登录会话派生（不同 state 不同值、同 state 幂等、两参数同值）。
func TestBuildAuthorizationURL_DesktopProfileAndSurface(t *testing.T) {
	parse := func(state string) url.Values {
		u := BuildAuthorizationURLForPlatform(state, "challenge-1", DefaultRedirectURI, OAuthPlatformOpenAI)
		parsed, err := url.Parse(u)
		if err != nil {
			t.Fatalf("Parse URL failed: %v", err)
		}
		return parsed.Query()
	}

	q1 := parse("state-1")
	if got := q1.Get("scope"); got != DefaultScopes {
		t.Fatalf("scope mismatch: got=%q want=%q", got, DefaultScopes)
	}
	for key, want := range map[string]string{
		"originator":              "Codex Desktop",
		"codex_app_version":       codexDesktopAppVersion,
		"codex_streamlined_login": "true",
	} {
		if got := q1.Get(key); got != want {
			t.Fatalf("%s mismatch: got=%q want=%q", key, got, want)
		}
	}
	sid1 := q1.Get("source_surface_stable_id")
	if sid1 == "" {
		t.Fatal("source_surface_stable_id must be present")
	}
	if got := q1.Get("codex_origin_stable_id"); got != sid1 {
		t.Fatalf("codex_origin_stable_id must equal source_surface_stable_id: got=%q want=%q", got, sid1)
	}
	// 同 state 幂等；不同 state 不同 surface。
	if again := parse("state-1").Get("source_surface_stable_id"); again != sid1 {
		t.Fatalf("same state must derive same surface id: %q vs %q", again, sid1)
	}
	if other := parse("state-2").Get("source_surface_stable_id"); other == sid1 {
		t.Fatalf("different states must derive different surface ids, both %q", other)
	}
}
