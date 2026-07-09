package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestCleanEmailRecipients(t *testing.T) {
	got := cleanEmailRecipients([]string{
		"admin@example.com, ops@example.com",
		"ADMIN@example.com; dev@example.com\nqa@example.com",
		"",
	})
	want := []string{
		"admin@example.com",
		"ops@example.com",
		"dev@example.com",
		"qa@example.com",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cleanEmailRecipients() = %#v, want %#v", got, want)
	}
}

func TestAddExistingAccountDoesNotUseDefaultModelID(t *testing.T) {
	schedulable := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/admin/accounts/42" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(UpstreamAccount{
			ID:          42,
			Name:        "existing",
			Platform:    "openai",
			Type:        "apikey",
			Status:      "active",
			Schedulable: &schedulable,
		})
	}))
	defer server.Close()

	accountID := int64(42)
	app := &App{
		statePath: t.TempDir() + "/state.json",
		cfg: Config{
			AuthToken: "token",
			APIKey:    "api-key",
			Sub2API: Sub2APIConfig{
				BaseURL:               server.URL,
				AdminAPIKey:           "admin",
				RequestTimeoutSeconds: 1,
			},
			Monitor: MonitorConfig{
				CheckMode:      checkModeStatus,
				DefaultModelID: "gpt-test-default",
			},
		},
		state:      State{Accounts: map[string]*MonitoredAccount{}},
		httpClient: server.Client(),
	}

	account, created, err := app.addAccount(context.Background(), AddAccountRequest{AccountID: &accountID})
	if err != nil {
		t.Fatalf("addAccount() error = %v", err)
	}
	if created {
		t.Fatal("addAccount() created upstream account for existing account")
	}
	if account.ModelID != "" {
		t.Fatalf("ModelID = %q, want empty for status-only existing account monitoring", account.ModelID)
	}
}

func TestAddExistingAccountByName(t *testing.T) {
	schedulable := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/admin/accounts" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("search"); got != "existing-name" {
			t.Fatalf("search query = %q, want existing-name", got)
		}
		_ = json.NewEncoder(w).Encode(APIEnvelope{
			Code:    0,
			Message: "success",
			Data: mustRawMessage(t, UpstreamAccountList{
				Items: []UpstreamAccount{{
					ID:          42,
					Name:        "existing-name",
					Platform:    "openai",
					Type:        "oauth",
					Status:      "active",
					Schedulable: &schedulable,
				}},
				Total: 1,
			}),
		})
	}))
	defer server.Close()

	app := &App{
		statePath: t.TempDir() + "/state.json",
		cfg: Config{
			AuthToken: "token",
			APIKey:    "api-key",
			Sub2API: Sub2APIConfig{
				BaseURL:               server.URL,
				AdminAPIKey:           "admin",
				RequestTimeoutSeconds: 1,
			},
			Monitor: MonitorConfig{CheckMode: checkModeStatus},
		},
		state:      State{Accounts: map[string]*MonitoredAccount{}},
		httpClient: server.Client(),
	}

	account, created, err := app.addAccount(context.Background(), AddAccountRequest{AccountName: "existing-name"})
	if err != nil {
		t.Fatalf("addAccount() error = %v", err)
	}
	if created {
		t.Fatal("addAccount() created upstream account for existing account name")
	}
	if account.ID != 42 {
		t.Fatalf("account.ID = %d, want 42", account.ID)
	}
	if account.Name != "existing-name" {
		t.Fatalf("account.Name = %q, want existing-name", account.Name)
	}
}

func TestCreateUpstreamAccountUsesSub2APIPayloadAndOverridesDefaults(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/admin/accounts" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if gotKey := r.Header.Get("x-api-key"); gotKey != "admin" {
			t.Fatalf("x-api-key = %q, want admin", gotKey)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(UpstreamAccount{
			ID:       88,
			Name:     "created",
			Platform: "openai",
			Type:     "apikey",
			Status:   "active",
		})
	}))
	defer server.Close()

	defaultProxyID := int64(12)
	requestProxyID := int64(99)
	rateMultiplier := 0.5
	loadFactor := 7
	expiresAt := int64(1893456000)
	autoPause := true
	confirmRisk := true
	notes := "native request"
	app := &App{
		cfg: Config{
			AuthToken: "token",
			APIKey:    "api-key",
			Sub2API: Sub2APIConfig{
				BaseURL:               server.URL,
				AdminAPIKey:           "admin",
				RequestTimeoutSeconds: 1,
			},
			AccountDefaults: AccountDefaultsConfig{
				UseProxy:    true,
				ProxyID:     &defaultProxyID,
				Priority:    8,
				Concurrency: 30,
				GroupIDs:    []int64{5, 7, 6},
			},
		},
		httpClient: server.Client(),
	}

	account, err := app.createUpstreamAccount(context.Background(), AddAccountRequest{
		Name:                    "created",
		Notes:                   &notes,
		Platform:                "openai",
		Type:                    "apikey",
		Credentials:             map[string]any{"api_key": "sk-test", "base_url": "https://example.test"},
		Extra:                   map[string]any{"quota_limit": 10},
		ProxyID:                 &requestProxyID,
		Concurrency:             99,
		Priority:                1,
		GroupIDs:                []int64{99},
		RateMultiplier:          &rateMultiplier,
		LoadFactor:              &loadFactor,
		ExpiresAt:               &expiresAt,
		AutoPauseOnExpired:      &autoPause,
		ConfirmMixedChannelRisk: &confirmRisk,
	})
	if err != nil {
		t.Fatalf("createUpstreamAccount() error = %v", err)
	}
	if account.ID != 88 {
		t.Fatalf("account.ID = %d, want 88", account.ID)
	}

	if got["name"] != "created" || got["notes"] != notes || got["platform"] != "openai" || got["type"] != "apikey" {
		t.Fatalf("basic payload fields mismatch: %#v", got)
	}
	if got["concurrency"] != float64(30) || got["priority"] != float64(8) || got["proxy_id"] != float64(defaultProxyID) {
		t.Fatalf("global defaults were not applied: %#v", got)
	}
	if !reflect.DeepEqual(numberSlice(t, got["group_ids"]), []int64{5, 7, 6}) {
		t.Fatalf("group_ids = %#v, want [5 7 6]", got["group_ids"])
	}
	if got["rate_multiplier"] != rateMultiplier || got["load_factor"] != float64(loadFactor) || got["expires_at"] != float64(expiresAt) {
		t.Fatalf("native numeric fields mismatch: %#v", got)
	}
	if got["auto_pause_on_expired"] != false || got["confirm_mixed_channel_risk"] != true {
		t.Fatalf("native boolean fields mismatch: %#v", got)
	}
}

func TestAuthMiddlewareAcceptsAuthTokenAndAPIKey(t *testing.T) {
	app := &App{
		cfg: Config{
			AuthToken: "front-token",
			APIKey:    "external-api-key",
		},
	}
	handler := app.authMiddleware(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	tests := []struct {
		name   string
		header string
		value  string
		want   int
	}{
		{name: "bearer auth token", header: "Authorization", value: "Bearer front-token", want: http.StatusNoContent},
		{name: "monitor token header", header: "x-monitor-token", value: "front-token", want: http.StatusNoContent},
		{name: "monitor api key header", header: "x-monitor-api-key", value: "external-api-key", want: http.StatusNoContent},
		{name: "bearer api key", header: "Authorization", value: "Bearer external-api-key", want: http.StatusNoContent},
		{name: "invalid", header: "Authorization", value: "Bearer wrong", want: http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/monitor-accounts", nil)
			req.Header.Set(tt.header, tt.value)
			rec := httptest.NewRecorder()

			handler(rec, req)

			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

func TestConstantTimeAny(t *testing.T) {
	if !constantTimeAny("secret", "", "secret") {
		t.Fatal("constantTimeAny() rejected a matching candidate")
	}
	if constantTimeAny("", "secret") {
		t.Fatal("constantTimeAny() accepted an empty value")
	}
	if constantTimeAny("********", "********") {
		t.Fatal("constantTimeAny() accepted the secret mask")
	}
	if constantTimeAny("wrong", "secret") {
		t.Fatal("constantTimeAny() accepted a non-matching value")
	}
}

func mustRawMessage(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func numberSlice(t *testing.T, value any) []int64 {
	t.Helper()
	raw, ok := value.([]any)
	if !ok {
		t.Fatalf("value = %#v, want []any", value)
	}
	out := make([]int64, 0, len(raw))
	for _, item := range raw {
		n, ok := item.(float64)
		if !ok {
			t.Fatalf("item = %#v, want number", item)
		}
		out = append(out, int64(n))
	}
	return out
}
