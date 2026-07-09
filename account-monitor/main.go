package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed web/*
var embeddedWeb embed.FS

const (
	stateUnknown   = "unknown"
	stateHealthy   = "healthy"
	stateUnhealthy = "unhealthy"

	checkModeStatus = "status"
	checkModeTest   = "test"

	secretMask          = "********"
	monitorAPIKeyPrefix = "am_"
)

type Config struct {
	Listen          string                 `json:"listen"`
	AuthToken       string                 `json:"auth_token"`
	APIKey          string                 `json:"api_key"`
	StateFile       string                 `json:"state_file"`
	Sub2API         Sub2APIConfig          `json:"sub2api"`
	Monitor         MonitorConfig          `json:"monitor"`
	AccountDefaults AccountDefaultsConfig  `json:"account_defaults"`
	Email           EmailConfig            `json:"email"`
	Accounts        []InitialAccountConfig `json:"accounts"`
}

type Sub2APIConfig struct {
	BaseURL               string `json:"base_url"`
	AdminAPIKey           string `json:"admin_api_key"`
	RequestTimeoutSeconds int    `json:"request_timeout_seconds"`
	TestTimeoutSeconds    int    `json:"test_timeout_seconds"`
}

type MonitorConfig struct {
	Enabled              bool   `json:"enabled"`
	CheckIntervalSeconds int    `json:"check_interval_seconds"`
	FailureThreshold     int    `json:"failure_threshold"`
	RecoveryThreshold    int    `json:"recovery_threshold"`
	CheckMode            string `json:"check_mode"`
	DefaultModelID       string `json:"default_model_id"`
	NotifyOnRecovery     bool   `json:"notify_on_recovery"`
}

type AccountDefaultsConfig struct {
	UseProxy    bool    `json:"use_proxy"`
	ProxyID     *int64  `json:"proxy_id"`
	Priority    int     `json:"priority"`
	Concurrency int     `json:"concurrency"`
	GroupIDs    []int64 `json:"group_ids"`
}

type EmailConfig struct {
	Enabled  bool     `json:"enabled"`
	SMTPHost string   `json:"smtp_host"`
	SMTPPort int      `json:"smtp_port"`
	Username string   `json:"username"`
	Password string   `json:"password"`
	From     string   `json:"from"`
	FromName string   `json:"from_name"`
	UseTLS   bool     `json:"use_tls"`
	To       []string `json:"to"`
}

type InitialAccountConfig struct {
	AccountID int64  `json:"account_id"`
	Name      string `json:"name"`
	ModelID   string `json:"model_id"`
	Enabled   *bool  `json:"enabled"`
}

type State struct {
	Accounts map[string]*MonitoredAccount `json:"accounts"`
}

type MonitoredAccount struct {
	ID                   int64         `json:"id"`
	Name                 string        `json:"name"`
	Platform             string        `json:"platform"`
	Type                 string        `json:"type"`
	ModelID              string        `json:"model_id"`
	Enabled              bool          `json:"enabled"`
	ConsecutiveFailures  int           `json:"consecutive_failures"`
	ConsecutiveSuccesses int           `json:"consecutive_successes"`
	LastStatus           AccountStatus `json:"last_status"`
	CreatedAt            time.Time     `json:"created_at"`
	UpdatedAt            time.Time     `json:"updated_at"`
}

type AccountStatus struct {
	State          string    `json:"state"`
	AccountID      int64     `json:"account_id"`
	AccountName    string    `json:"account_name"`
	CheckedAt      time.Time `json:"checked_at"`
	LatencyMs      int64     `json:"latency_ms"`
	UpstreamStatus string    `json:"upstream_status"`
	Schedulable    *bool     `json:"schedulable,omitempty"`
	ErrorMessage   string    `json:"error_message,omitempty"`
	Detail         string    `json:"detail,omitempty"`
	HTTPStatus     int       `json:"http_status,omitempty"`
}

type UpstreamAccount struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Platform     string `json:"platform"`
	Type         string `json:"type"`
	Status       string `json:"status"`
	ErrorMessage string `json:"error_message"`
	Schedulable  *bool  `json:"schedulable"`
}

type AddAccountRequest struct {
	AccountID               *int64         `json:"account_id"`
	AccountName             string         `json:"account_name"`
	Name                    string         `json:"name"`
	Platform                string         `json:"platform"`
	Type                    string         `json:"type"`
	RefreshToken            string         `json:"refresh_token"`
	APIKey                  string         `json:"api_key"`
	BaseURL                 string         `json:"base_url"`
	ClientID                string         `json:"client_id"`
	Credentials             map[string]any `json:"credentials"`
	Extra                   map[string]any `json:"extra"`
	ProxyID                 *int64         `json:"proxy_id"`
	Concurrency             int            `json:"concurrency"`
	Priority                int            `json:"priority"`
	LoadFactor              *int           `json:"load_factor"`
	RateMultiplier          *float64       `json:"rate_multiplier"`
	GroupIDs                []int64        `json:"group_ids"`
	ExpiresAt               *int64         `json:"expires_at"`
	AutoPauseOnExpired      *bool          `json:"auto_pause_on_expired"`
	ConfirmMixedChannelRisk *bool          `json:"confirm_mixed_channel_risk"`
	ModelID                 string         `json:"model_id"`
	Enabled                 *bool          `json:"enabled"`
	Notes                   *string        `json:"notes"`
}

type UpdateMonitoredAccountRequest struct {
	Name    *string `json:"name"`
	ModelID *string `json:"model_id"`
	Enabled *bool   `json:"enabled"`
}

type APIEnvelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

type UpstreamAccountList struct {
	Items []UpstreamAccount `json:"items"`
	Total int64             `json:"total"`
}

type ConfigUpdateRequest struct {
	AuthToken       *string                `json:"auth_token"`
	APIKey          *string                `json:"api_key"`
	Sub2API         *Sub2APIConfig         `json:"sub2api"`
	Monitor         *MonitorConfig         `json:"monitor"`
	AccountDefaults *AccountDefaultsConfig `json:"account_defaults"`
	Email           *EmailConfig           `json:"email"`
}

type App struct {
	configPath string
	statePath  string

	cfgMu sync.RWMutex
	cfg   Config

	stateMu sync.RWMutex
	state   State

	httpClient *http.Client
}

func main() {
	configPath := flag.String("config", "config.json", "config file path")
	checkOnce := flag.Bool("check-once", false, "run one check cycle and exit")
	flag.Parse()

	app, err := NewApp(*configPath)
	if err != nil {
		log.Fatalf("start failed: %v", err)
	}

	if *checkOnce {
		app.checkAll(context.Background())
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go app.monitorLoop(ctx)

	addr := app.currentConfig().Listen
	server := &http.Server{
		Addr:              addr,
		Handler:           app.routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	log.Printf("account monitor listening on %s", addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("http server failed: %v", err)
	}
}

func NewApp(configPath string) (*App, error) {
	absConfigPath, err := filepath.Abs(configPath)
	if err != nil {
		return nil, err
	}

	cfg, err := loadConfig(absConfigPath)
	if err != nil {
		return nil, err
	}
	generatedCredentials := applyConfigDefaults(&cfg)
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	if generatedCredentials {
		data, err := marshalIndent(cfg)
		if err != nil {
			return nil, err
		}
		if err := atomicWrite(absConfigPath, data, 0o600); err != nil {
			return nil, fmt.Errorf("write generated credentials: %w", err)
		}
	}

	statePath := cfg.StateFile
	if !filepath.IsAbs(statePath) {
		statePath = filepath.Join(filepath.Dir(absConfigPath), statePath)
	}

	state, err := loadState(statePath)
	if err != nil {
		return nil, err
	}
	seedStateFromConfig(&state, cfg.Accounts)
	if err := saveState(statePath, state); err != nil {
		return nil, err
	}

	return &App{
		configPath: absConfigPath,
		statePath:  statePath,
		cfg:        cfg,
		state:      state,
		httpClient: &http.Client{},
	}, nil
}

func loadConfig(path string) (Config, error) {
	var cfg Config
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read config: %w", err)
	}
	data = trimUTF8BOM(data)
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config: %w", err)
	}
	return cfg, nil
}

func applyConfigDefaults(cfg *Config) bool {
	generatedCredentials := false
	if strings.TrimSpace(cfg.Listen) == "" {
		cfg.Listen = "127.0.0.1:8099"
	}
	if strings.TrimSpace(cfg.AuthToken) == "" {
		cfg.AuthToken = generateSecret("", 32)
		generatedCredentials = true
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		cfg.APIKey = generateSecret(monitorAPIKeyPrefix, 32)
		generatedCredentials = true
	}
	if strings.TrimSpace(cfg.StateFile) == "" {
		cfg.StateFile = "state.json"
	}
	if cfg.Sub2API.RequestTimeoutSeconds <= 0 {
		cfg.Sub2API.RequestTimeoutSeconds = 20
	}
	if cfg.Sub2API.TestTimeoutSeconds <= 0 {
		cfg.Sub2API.TestTimeoutSeconds = 120
	}
	if cfg.Monitor.CheckIntervalSeconds <= 0 {
		cfg.Monitor.CheckIntervalSeconds = 5
	}
	if cfg.Monitor.FailureThreshold <= 0 {
		cfg.Monitor.FailureThreshold = 1
	}
	if cfg.Monitor.RecoveryThreshold <= 0 {
		cfg.Monitor.RecoveryThreshold = 1
	}
	cfg.Monitor.CheckMode = strings.ToLower(strings.TrimSpace(cfg.Monitor.CheckMode))
	if cfg.Monitor.CheckMode == "" {
		cfg.Monitor.CheckMode = checkModeStatus
	}
	if cfg.AccountDefaults.Concurrency <= 0 {
		cfg.AccountDefaults.Concurrency = 30
	}
	if cfg.AccountDefaults.Priority <= 0 {
		cfg.AccountDefaults.Priority = 8
	}
	if len(cfg.AccountDefaults.GroupIDs) == 0 {
		cfg.AccountDefaults.GroupIDs = []int64{5, 7, 6}
	}
	if cfg.Email.SMTPPort <= 0 {
		cfg.Email.SMTPPort = 587
	}
	if strings.TrimSpace(cfg.Email.FromName) == "" {
		cfg.Email.FromName = "Account Monitor"
	}
	cfg.Email.To = cleanEmailRecipients(cfg.Email.To)
	return generatedCredentials
}

func generateSecret(prefix string, byteLen int) string {
	data := make([]byte, byteLen)
	if _, err := rand.Read(data); err != nil {
		fallback := sha256.Sum256([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
		data = fallback[:]
	}
	return prefix + base64.RawURLEncoding.EncodeToString(data)
}

func validateConfig(cfg Config) error {
	if strings.TrimSpace(cfg.AuthToken) == "" || cfg.AuthToken == secretMask {
		return errors.New("auth_token is required")
	}
	if strings.TrimSpace(cfg.APIKey) == "" || cfg.APIKey == secretMask {
		return errors.New("api_key is required")
	}
	if cfg.Monitor.CheckMode != checkModeStatus && cfg.Monitor.CheckMode != checkModeTest {
		return fmt.Errorf("monitor.check_mode must be %q or %q", checkModeStatus, checkModeTest)
	}
	if strings.TrimSpace(cfg.Sub2API.BaseURL) != "" {
		if _, err := url.ParseRequestURI(cfg.Sub2API.BaseURL); err != nil {
			return fmt.Errorf("sub2api.base_url is invalid: %w", err)
		}
	}
	if cfg.Monitor.CheckIntervalSeconds < 5 {
		return errors.New("monitor.check_interval_seconds must be >= 5")
	}
	return nil
}

func loadState(path string) (State, error) {
	state := State{Accounts: map[string]*MonitoredAccount{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return state, nil
		}
		return state, fmt.Errorf("read state: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return state, nil
	}
	data = trimUTF8BOM(data)
	if err := json.Unmarshal(data, &state); err != nil {
		return state, fmt.Errorf("parse state: %w", err)
	}
	if state.Accounts == nil {
		state.Accounts = map[string]*MonitoredAccount{}
	}
	return state, nil
}

func seedStateFromConfig(state *State, accounts []InitialAccountConfig) {
	now := time.Now()
	for _, seed := range accounts {
		if seed.AccountID <= 0 {
			continue
		}
		key := strconv.FormatInt(seed.AccountID, 10)
		if _, exists := state.Accounts[key]; exists {
			continue
		}
		enabled := true
		if seed.Enabled != nil {
			enabled = *seed.Enabled
		}
		state.Accounts[key] = &MonitoredAccount{
			ID:      seed.AccountID,
			Name:    strings.TrimSpace(seed.Name),
			ModelID: strings.TrimSpace(seed.ModelID),
			Enabled: enabled,
			LastStatus: AccountStatus{
				State:     stateUnknown,
				AccountID: seed.AccountID,
			},
			CreatedAt: now,
			UpdatedAt: now,
		}
	}
}

func saveState(path string, state State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := marshalIndent(state)
	if err != nil {
		return err
	}
	return atomicWrite(path, data, 0o600)
}

func marshalIndent(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func atomicWrite(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func trimUTF8BOM(data []byte) []byte {
	return bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
}

func (a *App) currentConfig() Config {
	a.cfgMu.RLock()
	defer a.cfgMu.RUnlock()
	return a.cfg
}

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("/api/", a.authMiddleware(a.handleAPI))
	mux.Handle("/", a.webHandler())
	return mux
}

func (a *App) webHandler() http.Handler {
	sub, err := fs.Sub(embeddedWeb, "web")
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeError(w, http.StatusInternalServerError, "web assets are not available")
		})
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.ServeFileFS(w, r, sub, "index.html")
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

func (a *App) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := a.currentConfig()
		token := strings.TrimSpace(r.Header.Get("x-monitor-token"))
		apiKey := strings.TrimSpace(r.Header.Get("x-monitor-api-key"))
		if token == "" {
			auth := strings.TrimSpace(r.Header.Get("Authorization"))
			parts := strings.SplitN(auth, " ", 2)
			if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
				token = strings.TrimSpace(parts[1])
			}
		}
		if token == "" && apiKey != "" {
			token = apiKey
		}
		if !constantTimeAny(token, cfg.AuthToken, cfg.APIKey) {
			writeError(w, http.StatusUnauthorized, "invalid monitor token")
			return
		}
		next(w, r)
	}
}

func (a *App) handleAPI(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api")
	switch {
	case path == "/accounts" && r.Method == http.MethodGet:
		a.handleListAccounts(w, r)
	case path == "/accounts" && r.Method == http.MethodPost:
		a.handleAddAccount(w, r)
	case path == "/monitor-accounts" && r.Method == http.MethodGet:
		a.handleListAccounts(w, r)
	case path == "/monitor-accounts" && r.Method == http.MethodPost:
		a.handleAddAccount(w, r)
	case path == "/config" && r.Method == http.MethodGet:
		a.handleGetConfig(w, r)
	case path == "/config" && r.Method == http.MethodPut:
		a.handleUpdateConfig(w, r)
	case strings.HasPrefix(path, "/accounts/"):
		a.handleAccountItem(w, r, strings.TrimPrefix(path, "/accounts/"))
	case strings.HasPrefix(path, "/monitor-accounts/"):
		a.handleAccountItem(w, r, strings.TrimPrefix(path, "/monitor-accounts/"))
	default:
		writeError(w, http.StatusNotFound, "not found")
	}
}

func (a *App) handleListAccounts(w http.ResponseWriter, _ *http.Request) {
	accounts := a.listAccounts()
	writeJSON(w, http.StatusOK, map[string]any{"items": accounts, "total": len(accounts)})
}

func (a *App) handleAddAccount(w http.ResponseWriter, r *http.Request) {
	var req AddAccountRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}

	account, created, err := a.addAccount(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"created_upstream_account": created, "account": account})
}

func (a *App) handleAccountItem(w http.ResponseWriter, r *http.Request, tail string) {
	parts := strings.Split(strings.Trim(tail, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid account id")
		return
	}

	if len(parts) == 1 && r.Method == http.MethodDelete {
		if err := a.removeAccount(id); err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
		return
	}

	if len(parts) == 1 && r.Method == http.MethodPatch {
		var req UpdateMonitoredAccountRequest
		if err := decodeJSON(r.Body, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
		account, err := a.updateMonitoredAccount(id, req)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, account)
		return
	}

	if len(parts) == 2 && parts[1] == "status" && r.Method == http.MethodGet {
		if r.URL.Query().Get("refresh") == "true" {
			status, err := a.checkOneByID(r.Context(), id)
			if err != nil {
				writeError(w, http.StatusNotFound, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, status)
			return
		}
		acc, ok := a.getAccount(id)
		if !ok {
			writeError(w, http.StatusNotFound, "account is not monitored")
			return
		}
		writeJSON(w, http.StatusOK, acc.LastStatus)
		return
	}

	if len(parts) == 2 && parts[1] == "check" && r.Method == http.MethodPost {
		status, err := a.checkOneByID(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, status)
		return
	}

	writeError(w, http.StatusNotFound, "not found")
}

func (a *App) handleGetConfig(w http.ResponseWriter, _ *http.Request) {
	cfg := a.currentConfig()
	cfg.AuthToken = maskSecret(cfg.AuthToken)
	cfg.APIKey = maskSecret(cfg.APIKey)
	cfg.Sub2API.AdminAPIKey = maskSecret(cfg.Sub2API.AdminAPIKey)
	cfg.Email.Password = maskSecret(cfg.Email.Password)
	writeJSON(w, http.StatusOK, cfg)
}

func (a *App) handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	var req ConfigUpdateRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}

	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()

	next := a.cfg
	if req.AuthToken != nil && *req.AuthToken != secretMask {
		next.AuthToken = strings.TrimSpace(*req.AuthToken)
	}
	if req.APIKey != nil && *req.APIKey != secretMask {
		next.APIKey = strings.TrimSpace(*req.APIKey)
	}
	if req.Sub2API != nil {
		sub := *req.Sub2API
		if sub.AdminAPIKey == secretMask {
			sub.AdminAPIKey = next.Sub2API.AdminAPIKey
		}
		next.Sub2API = sub
	}
	if req.Monitor != nil {
		next.Monitor = *req.Monitor
	}
	if req.AccountDefaults != nil {
		next.AccountDefaults = *req.AccountDefaults
	}
	if req.Email != nil {
		emailCfg := *req.Email
		if emailCfg.Password == secretMask {
			emailCfg.Password = next.Email.Password
		}
		next.Email = emailCfg
	}

	applyConfigDefaults(&next)
	if err := validateConfig(next); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	data, err := marshalIndent(next)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := atomicWrite(a.configPath, data, 0o600); err != nil {
		writeError(w, http.StatusInternalServerError, "save config: "+err.Error())
		return
	}
	a.cfg = next

	sanitized := next
	sanitized.AuthToken = maskSecret(sanitized.AuthToken)
	sanitized.APIKey = maskSecret(sanitized.APIKey)
	sanitized.Sub2API.AdminAPIKey = maskSecret(sanitized.Sub2API.AdminAPIKey)
	sanitized.Email.Password = maskSecret(sanitized.Email.Password)
	writeJSON(w, http.StatusOK, sanitized)
}

func (a *App) addAccount(ctx context.Context, req AddAccountRequest) (*MonitoredAccount, bool, error) {
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	var upstream *UpstreamAccount
	created := false
	if req.AccountID != nil && *req.AccountID > 0 {
		acc, err := a.fetchUpstreamAccount(ctx, *req.AccountID)
		if err != nil {
			return nil, false, fmt.Errorf("fetch upstream account: %w", err)
		}
		upstream = acc
	} else if existingName := strings.TrimSpace(firstNonEmpty(req.AccountName, existingAccountName(req))); existingName != "" {
		acc, err := a.fetchUpstreamAccountByName(ctx, existingName)
		if err != nil {
			return nil, false, fmt.Errorf("fetch upstream account by name: %w", err)
		}
		upstream = acc
	} else {
		acc, err := a.createUpstreamAccount(ctx, req)
		if err != nil {
			return nil, false, fmt.Errorf("create upstream account: %w", err)
		}
		upstream = acc
		created = true
	}

	modelID := strings.TrimSpace(req.ModelID)

	now := time.Now()
	account := &MonitoredAccount{
		ID:       upstream.ID,
		Name:     firstNonEmpty(req.AccountName, req.Name, upstream.Name),
		Platform: upstream.Platform,
		Type:     upstream.Type,
		ModelID:  modelID,
		Enabled:  enabled,
		LastStatus: AccountStatus{
			State:          stateUnknown,
			AccountID:      upstream.ID,
			AccountName:    firstNonEmpty(req.AccountName, req.Name, upstream.Name),
			UpstreamStatus: upstream.Status,
			Schedulable:    upstream.Schedulable,
			ErrorMessage:   upstream.ErrorMessage,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}

	a.stateMu.Lock()
	a.state.Accounts[strconv.FormatInt(account.ID, 10)] = account
	err := saveState(a.statePath, a.state)
	a.stateMu.Unlock()
	if err != nil {
		return nil, created, err
	}
	return account, created, nil
}

func (a *App) createUpstreamAccount(ctx context.Context, req AddAccountRequest) (*UpstreamAccount, error) {
	cfg := a.currentConfig()
	if strings.TrimSpace(req.Name) == "" {
		return nil, errors.New("name is required when creating upstream account")
	}
	platform := strings.ToLower(strings.TrimSpace(req.Platform))
	if platform == "" {
		platform = "openai"
	}
	accountType := strings.ToLower(strings.TrimSpace(req.Type))
	if accountType == "" {
		accountType = "oauth"
	}
	credentials, err := a.buildCreateCredentials(ctx, cfg, platform, accountType, req)
	if err != nil {
		return nil, err
	}
	if len(credentials) == 0 {
		return nil, errors.New("credentials are required when creating upstream account")
	}

	payload := map[string]any{
		"name":                  strings.TrimSpace(req.Name),
		"platform":              platform,
		"type":                  accountType,
		"credentials":           credentials,
		"extra":                 nonNilMap(req.Extra),
		"concurrency":           cfg.AccountDefaults.Concurrency,
		"priority":              cfg.AccountDefaults.Priority,
		"group_ids":             cfg.AccountDefaults.GroupIDs,
		"auto_pause_on_expired": false,
	}
	if req.Notes != nil {
		payload["notes"] = *req.Notes
	}
	if req.RateMultiplier != nil {
		payload["rate_multiplier"] = *req.RateMultiplier
	}
	if req.LoadFactor != nil {
		payload["load_factor"] = *req.LoadFactor
	}
	if req.ExpiresAt != nil {
		payload["expires_at"] = *req.ExpiresAt
	}
	if req.ConfirmMixedChannelRisk != nil {
		payload["confirm_mixed_channel_risk"] = *req.ConfirmMixedChannelRisk
	}
	if cfg.AccountDefaults.UseProxy && cfg.AccountDefaults.ProxyID != nil && *cfg.AccountDefaults.ProxyID > 0 {
		payload["proxy_id"] = *cfg.AccountDefaults.ProxyID
	} else {
		payload["proxy_id"] = nil
	}

	data, err := a.callSub2API(ctx, http.MethodPost, "/api/v1/admin/accounts", payload, requestTimeout(cfg))
	if err != nil {
		return nil, err
	}
	var account UpstreamAccount
	if err := json.Unmarshal(data, &account); err != nil {
		return nil, err
	}
	if account.ID <= 0 {
		return nil, errors.New("upstream did not return account id")
	}
	return &account, nil
}

func (a *App) buildCreateCredentials(ctx context.Context, cfg Config, platform, accountType string, req AddAccountRequest) (map[string]any, error) {
	credentials := nonNilMap(req.Credentials)
	switch accountType {
	case "oauth":
		refreshToken := strings.TrimSpace(req.RefreshToken)
		if refreshToken == "" {
			refreshToken = stringValue(credentials["refresh_token"])
		}
		clientID := strings.TrimSpace(req.ClientID)
		if clientID == "" {
			clientID = stringValue(credentials["client_id"])
		}
		if platform == "openai" && refreshToken != "" {
			refreshed, err := a.refreshOpenAIOAuthToken(ctx, cfg, refreshToken, clientID)
			if err != nil {
				return nil, fmt.Errorf("validate openai refresh token: %w", err)
			}
			credentials = mergeMaps(credentials, refreshed)
			if stringValue(credentials["refresh_token"]) == "" {
				credentials["refresh_token"] = refreshToken
			}
			if clientID != "" && stringValue(credentials["client_id"]) == "" {
				credentials["client_id"] = clientID
			}
		} else if refreshToken != "" {
			credentials["refresh_token"] = refreshToken
			if clientID != "" {
				credentials["client_id"] = clientID
			}
		}
		if len(credentials) == 0 {
			return nil, fmt.Errorf("refresh_token is required for %s oauth account", platform)
		}
	case "apikey":
		apiKey := strings.TrimSpace(req.APIKey)
		if apiKey == "" {
			apiKey = stringValue(credentials["api_key"])
		}
		if apiKey != "" {
			credentials["api_key"] = apiKey
		}
		baseURL := strings.TrimSpace(req.BaseURL)
		if baseURL == "" {
			baseURL = stringValue(credentials["base_url"])
		}
		if baseURL == "" {
			baseURL = defaultAPIBaseURL(platform)
		}
		if baseURL != "" {
			credentials["base_url"] = baseURL
		}
		if stringValue(credentials["api_key"]) == "" {
			return nil, fmt.Errorf("api_key is required for %s apikey account", platform)
		}
	default:
		if len(credentials) == 0 {
			return nil, fmt.Errorf("credentials are required for %s %s account", platform, accountType)
		}
	}
	return credentials, nil
}

func (a *App) refreshOpenAIOAuthToken(ctx context.Context, cfg Config, refreshToken, clientID string) (map[string]any, error) {
	payload := map[string]any{"refresh_token": refreshToken}
	if clientID != "" {
		payload["client_id"] = clientID
	}
	if cfg.AccountDefaults.UseProxy && cfg.AccountDefaults.ProxyID != nil && *cfg.AccountDefaults.ProxyID > 0 {
		payload["proxy_id"] = *cfg.AccountDefaults.ProxyID
	}
	data, err := a.callSub2API(ctx, http.MethodPost, "/api/v1/admin/openai/refresh-token", payload, requestTimeout(cfg))
	if err != nil {
		return nil, err
	}
	var tokenInfo map[string]any
	if err := json.Unmarshal(data, &tokenInfo); err != nil {
		return nil, err
	}
	credentials := map[string]any{}
	copyIfNotEmpty(credentials, tokenInfo, "access_token")
	copyIfNotEmpty(credentials, tokenInfo, "refresh_token")
	copyIfNotEmpty(credentials, tokenInfo, "id_token")
	copyIfNotEmpty(credentials, tokenInfo, "expires_at")
	copyIfNotEmpty(credentials, tokenInfo, "email")
	copyIfNotEmpty(credentials, tokenInfo, "chatgpt_account_id")
	copyIfNotEmpty(credentials, tokenInfo, "chatgpt_user_id")
	copyIfNotEmpty(credentials, tokenInfo, "organization_id")
	copyIfNotEmpty(credentials, tokenInfo, "plan_type")
	copyIfNotEmpty(credentials, tokenInfo, "subscription_expires_at")
	copyIfNotEmpty(credentials, tokenInfo, "client_id")
	return credentials, nil
}

func nonNilMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func existingAccountName(req AddAccountRequest) string {
	if strings.TrimSpace(req.Platform) != "" || strings.TrimSpace(req.Type) != "" {
		return ""
	}
	if strings.TrimSpace(req.RefreshToken) != "" || strings.TrimSpace(req.APIKey) != "" {
		return ""
	}
	if len(req.Credentials) > 0 || len(req.Extra) > 0 {
		return ""
	}
	if req.Notes != nil || req.ProxyID != nil || req.Concurrency != 0 || req.Priority != 0 || req.RateMultiplier != nil || req.LoadFactor != nil {
		return ""
	}
	if len(req.GroupIDs) > 0 || req.ExpiresAt != nil || req.AutoPauseOnExpired != nil || req.ConfirmMixedChannelRisk != nil {
		return ""
	}
	return strings.TrimSpace(req.Name)
}

func mergeMaps(base, overlay map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overlay {
		out[k] = v
	}
	return out
}

func copyIfNotEmpty(dst, src map[string]any, key string) {
	value, ok := src[key]
	if !ok || value == nil {
		return
	}
	if s, ok := value.(string); ok && strings.TrimSpace(s) == "" {
		return
	}
	dst[key] = value
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	if s, ok := value.(string); ok {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func defaultAPIBaseURL(platform string) string {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "openai":
		return "https://api.openai.com"
	case "gemini":
		return "https://generativelanguage.googleapis.com"
	case "anthropic":
		return "https://api.anthropic.com"
	default:
		return ""
	}
}

func (a *App) fetchUpstreamAccount(ctx context.Context, id int64) (*UpstreamAccount, error) {
	cfg := a.currentConfig()
	data, err := a.callSub2API(ctx, http.MethodGet, "/api/v1/admin/accounts/"+strconv.FormatInt(id, 10), nil, requestTimeout(cfg))
	if err != nil {
		return nil, err
	}
	var account UpstreamAccount
	if err := json.Unmarshal(data, &account); err != nil {
		return nil, err
	}
	if account.ID <= 0 {
		account.ID = id
	}
	return &account, nil
}

func (a *App) fetchUpstreamAccountByName(ctx context.Context, name string) (*UpstreamAccount, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("account name is required")
	}
	accounts, err := a.listUpstreamAccounts(ctx, name)
	if err != nil {
		return nil, err
	}
	matches := make([]UpstreamAccount, 0, 1)
	for _, account := range accounts {
		if strings.EqualFold(strings.TrimSpace(account.Name), name) {
			matches = append(matches, account)
		}
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("account %q not found in sub2api", name)
	}
	if len(matches) > 1 {
		return nil, fmt.Errorf("account name %q matched %d accounts; use account_id instead", name, len(matches))
	}
	if matches[0].ID <= 0 {
		return nil, fmt.Errorf("account %q has invalid id", name)
	}
	return &matches[0], nil
}

func (a *App) listUpstreamAccounts(ctx context.Context, search string) ([]UpstreamAccount, error) {
	cfg := a.currentConfig()
	query := url.Values{}
	query.Set("page", "1")
	query.Set("page_size", "1000")
	query.Set("sort_by", "name")
	query.Set("sort_order", "asc")
	if strings.TrimSpace(search) != "" {
		query.Set("search", strings.TrimSpace(search))
	}

	data, err := a.callSub2API(ctx, http.MethodGet, "/api/v1/admin/accounts?"+query.Encode(), nil, requestTimeout(cfg))
	if err != nil {
		return nil, err
	}
	return decodeUpstreamAccountList(data)
}

func decodeUpstreamAccountList(data json.RawMessage) ([]UpstreamAccount, error) {
	var list UpstreamAccountList
	if err := json.Unmarshal(data, &list); err == nil && list.Items != nil {
		return list.Items, nil
	}

	var wrapped struct {
		Items []UpstreamAccount `json:"items"`
		Data  []UpstreamAccount `json:"data"`
	}
	if err := json.Unmarshal(data, &wrapped); err == nil {
		if wrapped.Items != nil {
			return wrapped.Items, nil
		}
		if wrapped.Data != nil {
			return wrapped.Data, nil
		}
	}

	var items []UpstreamAccount
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, err
	}
	return items, nil
}

func (a *App) callSub2API(ctx context.Context, method, path string, payload any, timeout time.Duration) (json.RawMessage, error) {
	cfg := a.currentConfig()
	if strings.TrimSpace(cfg.Sub2API.BaseURL) == "" {
		return nil, errors.New("sub2api.base_url is not configured")
	}
	if strings.TrimSpace(cfg.Sub2API.AdminAPIKey) == "" {
		return nil, errors.New("sub2api.admin_api_key is not configured")
	}

	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(data)
	}

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, method, strings.TrimRight(cfg.Sub2API.BaseURL, "/")+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", cfg.Sub2API.AdminAPIKey)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("sub2api returned %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var env APIEnvelope
	if err := json.Unmarshal(raw, &env); err == nil && env.Message != "" {
		if env.Code != 0 {
			return nil, fmt.Errorf("sub2api error: %s", env.Message)
		}
		if env.Data != nil {
			return env.Data, nil
		}
	}
	return raw, nil
}

func requestTimeout(cfg Config) time.Duration {
	return time.Duration(cfg.Sub2API.RequestTimeoutSeconds) * time.Second
}

func testTimeout(cfg Config) time.Duration {
	return time.Duration(cfg.Sub2API.TestTimeoutSeconds) * time.Second
}

func (a *App) monitorLoop(ctx context.Context) {
	cfg := a.currentConfig()
	if !cfg.Monitor.Enabled {
		log.Printf("monitor loop disabled")
	}
	interval := time.Duration(cfg.Monitor.CheckIntervalSeconds) * time.Second
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			cfg = a.currentConfig()
			if cfg.Monitor.Enabled {
				a.checkAll(ctx)
			}
			interval = time.Duration(cfg.Monitor.CheckIntervalSeconds) * time.Second
			timer.Reset(interval)
		}
	}
}

func (a *App) checkAll(ctx context.Context) {
	accounts := a.listAccounts()
	for i := range accounts {
		if !accounts[i].Enabled {
			continue
		}
		if _, err := a.checkOneByID(ctx, accounts[i].ID); err != nil {
			log.Printf("check account %d failed: %v", accounts[i].ID, err)
		}
	}
}

func (a *App) checkOneByID(ctx context.Context, id int64) (AccountStatus, error) {
	acc, ok := a.getAccount(id)
	if !ok {
		return AccountStatus{}, errors.New("account is not monitored")
	}
	status := a.runCheck(ctx, acc)
	a.updateAccountStatus(acc.ID, status)
	return status, nil
}

func (a *App) runCheck(ctx context.Context, account *MonitoredAccount) AccountStatus {
	start := time.Now()
	status := AccountStatus{
		State:       stateUnhealthy,
		AccountID:   account.ID,
		AccountName: account.Name,
		CheckedAt:   start,
	}

	upstream, err := a.fetchUpstreamAccount(ctx, account.ID)
	status.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		status.ErrorMessage = err.Error()
		status.Detail = "fetch upstream account failed"
		return status
	}

	status.AccountName = firstNonEmpty(account.Name, upstream.Name)
	status.UpstreamStatus = upstream.Status
	status.Schedulable = upstream.Schedulable
	status.ErrorMessage = upstream.ErrorMessage

	if strings.ToLower(strings.TrimSpace(upstream.Status)) != "active" {
		status.Detail = "upstream account is not active"
		return status
	}
	if upstream.Schedulable != nil && !*upstream.Schedulable {
		status.Detail = "upstream account is not schedulable"
		return status
	}

	cfg := a.currentConfig()
	if cfg.Monitor.CheckMode == checkModeTest {
		testStatus := a.runAccountTest(ctx, account)
		testStatus.UpstreamStatus = status.UpstreamStatus
		testStatus.Schedulable = status.Schedulable
		if testStatus.AccountName == "" {
			testStatus.AccountName = status.AccountName
		}
		return testStatus
	}

	status.State = stateHealthy
	status.Detail = "status check passed"
	return status
}

func (a *App) runAccountTest(ctx context.Context, account *MonitoredAccount) AccountStatus {
	cfg := a.currentConfig()
	start := time.Now()
	status := AccountStatus{
		State:       stateUnhealthy,
		AccountID:   account.ID,
		AccountName: account.Name,
		CheckedAt:   start,
	}

	modelID := strings.TrimSpace(account.ModelID)
	if modelID == "" {
		modelID = strings.TrimSpace(cfg.Monitor.DefaultModelID)
	}
	payload := map[string]any{}
	if modelID != "" {
		payload["model_id"] = modelID
	}

	data, _ := json.Marshal(payload)
	reqCtx, cancel := context.WithTimeout(ctx, testTimeout(cfg))
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, strings.TrimRight(cfg.Sub2API.BaseURL, "/")+"/api/v1/admin/accounts/"+strconv.FormatInt(account.ID, 10)+"/test", bytes.NewReader(data))
	if err != nil {
		status.ErrorMessage = err.Error()
		return status
	}
	req.Header.Set("x-api-key", cfg.Sub2API.AdminAPIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		status.ErrorMessage = err.Error()
		status.LatencyMs = time.Since(start).Milliseconds()
		return status
	}
	defer func() { _ = resp.Body.Close() }()
	status.HTTPStatus = resp.StatusCode

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		status.ErrorMessage = fmt.Sprintf("test endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
		status.LatencyMs = time.Since(start).Milliseconds()
		return status
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		raw := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if raw == "" || raw == "[DONE]" {
			continue
		}
		var event struct {
			Type    string `json:"type"`
			Success bool   `json:"success"`
			Error   string `json:"error"`
			Text    string `json:"text"`
		}
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			continue
		}
		if event.Type == "error" {
			status.ErrorMessage = firstNonEmpty(event.Error, event.Text, "account test failed")
			status.Detail = "test event returned error"
			status.LatencyMs = time.Since(start).Milliseconds()
			return status
		}
		if event.Type == "test_complete" {
			status.LatencyMs = time.Since(start).Milliseconds()
			if event.Success {
				status.State = stateHealthy
				status.Detail = "account test passed"
			} else {
				status.ErrorMessage = firstNonEmpty(event.Error, "account test failed")
				status.Detail = "test_complete success=false"
			}
			return status
		}
	}
	if err := scanner.Err(); err != nil {
		status.ErrorMessage = err.Error()
		status.Detail = "read test stream failed"
	} else {
		status.ErrorMessage = "test stream ended without test_complete"
	}
	status.LatencyMs = time.Since(start).Milliseconds()
	return status
}

func (a *App) updateAccountStatus(id int64, status AccountStatus) {
	cfg := a.currentConfig()
	var notify *emailNotification

	a.stateMu.Lock()
	account := a.state.Accounts[strconv.FormatInt(id, 10)]
	if account != nil {
		previousState := account.LastStatus.State
		account.LastStatus = status
		account.Name = firstNonEmpty(account.Name, status.AccountName)
		account.UpdatedAt = time.Now()

		if status.State == stateHealthy {
			account.ConsecutiveFailures = 0
			account.ConsecutiveSuccesses++
			if previousState == stateUnhealthy && cfg.Monitor.NotifyOnRecovery && account.ConsecutiveSuccesses == cfg.Monitor.RecoveryThreshold {
				notify = &emailNotification{Recovered: true, Account: *account, Status: status}
			}
		} else {
			account.ConsecutiveSuccesses = 0
			account.ConsecutiveFailures++
			if account.ConsecutiveFailures == cfg.Monitor.FailureThreshold {
				notify = &emailNotification{Recovered: false, Account: *account, Status: status}
			}
		}
		if err := saveState(a.statePath, a.state); err != nil {
			log.Printf("save state failed: %v", err)
		}
	}
	a.stateMu.Unlock()

	if notify != nil {
		if err := a.sendNotification(context.Background(), *notify); err != nil {
			log.Printf("send notification failed: %v", err)
		}
	}
}

func (a *App) listAccounts() []MonitoredAccount {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()

	out := make([]MonitoredAccount, 0, len(a.state.Accounts))
	for _, acc := range a.state.Accounts {
		if acc != nil {
			out = append(out, *acc)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (a *App) getAccount(id int64) (*MonitoredAccount, bool) {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()

	acc, ok := a.state.Accounts[strconv.FormatInt(id, 10)]
	if !ok || acc == nil {
		return nil, false
	}
	cp := *acc
	return &cp, true
}

func (a *App) removeAccount(id int64) error {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()

	key := strconv.FormatInt(id, 10)
	if _, ok := a.state.Accounts[key]; !ok {
		return errors.New("account is not monitored")
	}
	delete(a.state.Accounts, key)
	return saveState(a.statePath, a.state)
}

func (a *App) updateMonitoredAccount(id int64, req UpdateMonitoredAccountRequest) (*MonitoredAccount, error) {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()

	key := strconv.FormatInt(id, 10)
	account, ok := a.state.Accounts[key]
	if !ok || account == nil {
		return nil, errors.New("account is not monitored")
	}
	if req.Name != nil {
		account.Name = strings.TrimSpace(*req.Name)
		account.LastStatus.AccountName = account.Name
	}
	if req.ModelID != nil {
		account.ModelID = strings.TrimSpace(*req.ModelID)
	}
	if req.Enabled != nil {
		account.Enabled = *req.Enabled
	}
	account.UpdatedAt = time.Now()
	if err := saveState(a.statePath, a.state); err != nil {
		return nil, err
	}
	cp := *account
	return &cp, nil
}

type emailNotification struct {
	Recovered bool
	Account   MonitoredAccount
	Status    AccountStatus
}

func (a *App) sendNotification(ctx context.Context, n emailNotification) error {
	cfg := a.currentConfig()
	emailCfg := cfg.Email
	if !emailCfg.Enabled {
		return nil
	}
	recipients := cleanEmailRecipients(emailCfg.To)
	if strings.TrimSpace(emailCfg.SMTPHost) == "" || strings.TrimSpace(emailCfg.From) == "" || len(recipients) == 0 {
		return errors.New("email is enabled but smtp_host/from/to is incomplete")
	}

	stateText := "异常"
	if n.Recovered {
		stateText = "恢复"
	}
	subject := fmt.Sprintf("[Account Monitor] 账号%s: %s(%d)", stateText, firstNonEmpty(n.Account.Name, n.Status.AccountName, "unknown"), n.Account.ID)
	body := buildEmailBody(n)

	return sendMail(ctx, emailCfg, recipients, subject, body)
}

func buildEmailBody(n emailNotification) string {
	title := "账号状态异常"
	if n.Recovered {
		title = "账号状态已恢复"
	}
	rows := [][2]string{
		{"账号 ID", strconv.FormatInt(n.Account.ID, 10)},
		{"账号名称", firstNonEmpty(n.Account.Name, n.Status.AccountName, "-")},
		{"平台", firstNonEmpty(n.Account.Platform, "-")},
		{"类型", firstNonEmpty(n.Account.Type, "-")},
		{"状态", n.Status.State},
		{"上游状态", firstNonEmpty(n.Status.UpstreamStatus, "-")},
		{"详情", firstNonEmpty(n.Status.Detail, "-")},
		{"错误", firstNonEmpty(n.Status.ErrorMessage, "-")},
		{"检查时间", n.Status.CheckedAt.Format(time.RFC3339)},
		{"延迟", strconv.FormatInt(n.Status.LatencyMs, 10) + "ms"},
	}

	var b strings.Builder
	b.WriteString("<!doctype html><html><body style=\"font-family:Arial,sans-serif;line-height:1.6\">")
	b.WriteString("<h2>")
	b.WriteString(html.EscapeString(title))
	b.WriteString("</h2><table cellspacing=\"0\" cellpadding=\"6\" style=\"border-collapse:collapse\">")
	for _, row := range rows {
		b.WriteString("<tr><td style=\"border:1px solid #ddd;font-weight:bold;background:#f7f7f7\">")
		b.WriteString(html.EscapeString(row[0]))
		b.WriteString("</td><td style=\"border:1px solid #ddd\">")
		b.WriteString(html.EscapeString(row[1]))
		b.WriteString("</td></tr>")
	}
	b.WriteString("</table><p style=\"color:#777\">This email was sent by account-monitor.</p></body></html>")
	return b.String()
}

func sendMail(ctx context.Context, cfg EmailConfig, to []string, subject, body string) error {
	addr := net.JoinHostPort(cfg.SMTPHost, strconv.Itoa(cfg.SMTPPort))
	fromHeader := sanitizeHeader(cfg.From)
	if strings.TrimSpace(cfg.FromName) != "" {
		fromHeader = fmt.Sprintf("%s <%s>", sanitizeHeader(cfg.FromName), sanitizeHeader(cfg.From))
	}
	toHeader := sanitizeHeader(strings.Join(to, ", "))
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n%s",
		fromHeader, toHeader, sanitizeHeader(subject), body)

	var auth smtp.Auth
	if strings.TrimSpace(cfg.Username) != "" {
		auth = smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.SMTPHost)
	}

	if cfg.UseTLS {
		return sendMailTLS(ctx, addr, auth, cfg.From, to, []byte(msg), cfg.SMTPHost)
	}

	done := make(chan error, 1)
	go func() {
		done <- smtp.SendMail(addr, auth, cfg.From, to, []byte(msg))
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		return err
	}
}

func sendMailTLS(ctx context.Context, addr string, auth smtp.Auth, from string, to []string, msg []byte, host string) error {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	}

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	if auth != nil {
		if err := client.Auth(auth); err != nil {
			return err
		}
	}
	if err := client.Mail(from); err != nil {
		return err
	}
	for _, recipient := range to {
		if err := client.Rcpt(recipient); err != nil {
			return err
		}
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	_ = client.Quit()
	return nil
}

func sanitizeHeader(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", "")
	return strings.TrimSpace(s)
}

func cleanEmailRecipients(values []string) []string {
	seen := map[string]struct{}{}
	recipients := make([]string, 0, len(values))
	for _, value := range values {
		for _, item := range splitRecipients(value) {
			key := strings.ToLower(item)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			recipients = append(recipients, item)
		}
	}
	return recipients
}

func splitRecipients(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field != "" {
			out = append(out, field)
		}
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}

func decodeJSON(body io.Reader, v any) error {
	dec := json.NewDecoder(io.LimitReader(body, 4<<20))
	dec.UseNumber()
	return dec.Decode(v)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func maskSecret(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	return secretMask
}

func constantTimeAny(value string, candidates ...string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}

	valueHash := sha256.Sum256([]byte(value))
	matched := 0
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || candidate == secretMask {
			continue
		}
		candidateHash := sha256.Sum256([]byte(candidate))
		matched |= subtle.ConstantTimeCompare(valueHash[:], candidateHash[:])
	}
	return matched == 1
}
