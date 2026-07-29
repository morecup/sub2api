package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	grokCompactionDefaultContextWindow    = uint64(256_000)
	grokCompactionDefaultThresholdPercent = uint8(85)
	grokCompactionCatalogTTL              = 6 * time.Hour
	grokCompactionCatalogLocalTTL         = 5 * time.Minute
	grokCompactionCatalogFailureTTL       = 30 * time.Second
	grokCompactionSettingsTimeout         = 5 * time.Second
	grokCompactionModelsTimeout           = 10 * time.Second
	grokCompactionSettingsMaxBytes        = 1 << 20
	grokCompactionModelsMaxBytes          = 4 << 20
)

// GrokCompactionCatalogStore is an optional Redis-backed extension to
// GatewayCache. A successful empty catalog is distinct from a cache miss: once
// /v1/models loaded, that remote directory is authoritative for the account.
type GrokCompactionCatalogStore interface {
	GetGrokCompactionCatalog(ctx context.Context, accountID int64) (GrokCompactionCatalog, bool, error)
	SetGrokCompactionCatalog(ctx context.Context, accountID int64, catalog GrokCompactionCatalog, ttl time.Duration) error
}

type GrokCompactionAtConfig struct {
	Enabled bool   `json:"enabled"`
	Value   uint64 `json:"value"`
}

type GrokCompactionsRemainingConfig struct {
	Enabled bool  `json:"enabled"`
	Dynamic bool  `json:"dynamic"`
	Value   uint8 `json:"value"`
}

type GrokCompactionModelConfig struct {
	ContextWindow        uint64                         `json:"context_window"`
	ThresholdPercent     uint8                          `json:"threshold_percent"`
	CompactionAt         GrokCompactionAtConfig         `json:"compaction_at"`
	CompactionsRemaining GrokCompactionsRemainingConfig `json:"compactions_remaining"`
}

type GrokCompactionCatalog struct {
	Models map[string]GrokCompactionModelConfig `json:"models"`
}

type grokCompactionCatalogCacheEntry struct {
	catalog   *GrokCompactionCatalog
	expiresAt time.Time
}

type grokCompactionCatalogRuntime struct {
	entries sync.Map
	flight  singleflight.Group
}

func (r *grokCompactionCatalogRuntime) load(accountID int64, now time.Time) (*GrokCompactionCatalog, bool) {
	if r == nil {
		return nil, false
	}
	value, ok := r.entries.Load(accountID)
	if !ok {
		return nil, false
	}
	entry, ok := value.(grokCompactionCatalogCacheEntry)
	if !ok || !entry.expiresAt.After(now) {
		r.entries.Delete(accountID)
		return nil, false
	}
	return entry.catalog, true
}

func (r *grokCompactionCatalogRuntime) store(accountID int64, catalog *GrokCompactionCatalog, ttl time.Duration, now time.Time) {
	if r == nil || ttl <= 0 {
		return
	}
	r.entries.Store(accountID, grokCompactionCatalogCacheEntry{catalog: catalog, expiresAt: now.Add(ttl)})
}

func (s *OpenAIGatewayService) resolveGrokCompactionCatalog(
	ctx context.Context,
	account *Account,
	token string,
) *GrokCompactionCatalog {
	if s == nil || account == nil || !account.IsGrokOAuth() || account.ID <= 0 || strings.TrimSpace(token) == "" {
		return nil
	}
	store, ok := s.cache.(GrokCompactionCatalogStore)
	if !ok || store == nil {
		return nil
	}
	if catalog, ok := s.grokCompactionCatalogRuntime.load(account.ID, time.Now()); ok {
		return catalog
	}

	value, err, _ := s.grokCompactionCatalogRuntime.flight.Do(strconv.FormatInt(account.ID, 10), func() (any, error) {
		if catalog, ok := s.grokCompactionCatalogRuntime.load(account.ID, time.Now()); ok {
			return catalog, nil
		}

		catalog, found, cacheErr := store.GetGrokCompactionCatalog(ctx, account.ID)
		if cacheErr == nil && found {
			normalizeGrokCompactionCatalog(&catalog)
			s.grokCompactionCatalogRuntime.store(account.ID, &catalog, grokCompactionCatalogLocalTTL, time.Now())
			return &catalog, nil
		}
		if cacheErr != nil {
			slog.Warn("grok_compaction_catalog_cache_read_failed", "account_id", account.ID, "error", cacheErr)
		}

		catalog, fetchErr := s.fetchGrokCompactionCatalog(ctx, account, token)
		if fetchErr != nil {
			return nil, fetchErr
		}
		normalizeGrokCompactionCatalog(&catalog)
		if cacheErr := store.SetGrokCompactionCatalog(ctx, account.ID, catalog, grokCompactionCatalogTTL); cacheErr != nil {
			slog.Warn("grok_compaction_catalog_cache_write_failed", "account_id", account.ID, "error", cacheErr)
		}
		s.grokCompactionCatalogRuntime.store(account.ID, &catalog, grokCompactionCatalogLocalTTL, time.Now())
		return &catalog, nil
	})
	if err != nil {
		s.grokCompactionCatalogRuntime.store(account.ID, nil, grokCompactionCatalogFailureTTL, time.Now())
		slog.Warn("grok_compaction_catalog_fetch_failed_using_captured_fallback", "account_id", account.ID, "error", err)
		return nil
	}
	catalog, _ := value.(*GrokCompactionCatalog)
	return catalog
}

func normalizeGrokCompactionCatalog(catalog *GrokCompactionCatalog) {
	if catalog == nil {
		return
	}
	if catalog.Models == nil {
		catalog.Models = make(map[string]GrokCompactionModelConfig)
		return
	}
	normalized := make(map[string]GrokCompactionModelConfig, len(catalog.Models))
	for model, config := range catalog.Models {
		model = strings.ToLower(strings.TrimSpace(model))
		if model != "" {
			normalized[model] = config
		}
	}
	catalog.Models = normalized
}

func (s *OpenAIGatewayService) fetchGrokCompactionCatalog(
	ctx context.Context,
	account *Account,
	token string,
) (GrokCompactionCatalog, error) {
	if s == nil || s.httpUpstream == nil {
		return GrokCompactionCatalog{}, errors.New("Grok upstream transport is unavailable")
	}
	validator, err := grokBaseURLValidator(account, s.cfg)
	if err != nil {
		return GrokCompactionCatalog{}, err
	}
	baseURL, err := validator(account.GetGrokBaseURL())
	if err != nil {
		return GrokCompactionCatalog{}, err
	}
	proxyURL := resolveAccountProxyURL(account)

	threshold := grokCompactionDefaultThresholdPercent
	settingsCtx, cancelSettings := context.WithTimeout(ctx, grokCompactionSettingsTimeout)
	settingsRaw, settingsErr := s.fetchGrokControlPlaneJSON(
		settingsCtx,
		account,
		token,
		proxyURL,
		buildOpenAIEndpointURL(baseURL, "/v1/settings"),
		grokCompactionSettingsMaxBytes,
	)
	cancelSettings()
	if settingsErr == nil {
		if parsed, ok := parseGrokCompactionThreshold(settingsRaw); ok {
			threshold = parsed
		}
	} else {
		slog.Debug("grok_compaction_settings_fetch_failed_using_default", "account_id", account.ID, "error", settingsErr)
	}

	modelsCtx, cancelModels := context.WithTimeout(ctx, grokCompactionModelsTimeout)
	modelsRaw, modelsErr := s.fetchGrokControlPlaneJSON(
		modelsCtx,
		account,
		token,
		proxyURL,
		buildOpenAIEndpointURL(baseURL, "/v1/models"),
		grokCompactionModelsMaxBytes,
	)
	cancelModels()
	if modelsErr != nil {
		return GrokCompactionCatalog{}, fmt.Errorf("fetch Grok model catalog: %w", modelsErr)
	}
	return parseGrokCompactionCatalog(modelsRaw, threshold)
}

func (s *OpenAIGatewayService) fetchGrokControlPlaneJSON(
	ctx context.Context,
	account *Account,
	token string,
	proxyURL string,
	targetURL string,
	maxBytes int64,
) ([]byte, error) {
	ctx = WithHTTPUpstreamProfile(ctx, HTTPUpstreamProfileGrokControlPlane)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	applyGrokCLIControlPlaneHeaders(req.Header)
	if isGrokCLIProxyTarget(targetURL) {
		if userID := strings.TrimSpace(account.GetCredential("sub")); userID != "" {
			req.Header.Set("X-UserID", userID)
		}
		if email := strings.TrimSpace(account.GetCredential("email")); email != "" {
			req.Header.Set("X-Email", email)
		}
	}
	account.ApplyHeaderOverrides(req.Header)

	resp, err := s.doUpstreamRequest(req, proxyURL, account)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return nil, fmt.Errorf("upstream returned status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > maxBytes {
		return nil, errors.New("response exceeded size limit")
	}
	if !json.Valid(raw) {
		return nil, errors.New("response was not valid JSON")
	}
	return raw, nil
}

func parseGrokCompactionThreshold(raw []byte) (uint8, bool) {
	var root map[string]json.RawMessage
	if json.Unmarshal(raw, &root) != nil {
		return 0, false
	}
	value, ok := firstRawField(root, "auto_compact_threshold_percent", "autoCompactThresholdPercent")
	if !ok {
		return 0, false
	}
	parsed, ok := rawUint64(value)
	if !ok || parsed > 255 {
		return 0, false
	}
	return uint8(parsed), true
}

func parseGrokCompactionCatalog(raw []byte, globalThreshold uint8) (GrokCompactionCatalog, error) {
	var response struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return GrokCompactionCatalog{}, err
	}
	catalog := GrokCompactionCatalog{Models: make(map[string]GrokCompactionModelConfig)}
	for _, item := range response.Data {
		model, config, ok := parseGrokCompactionModel(item, globalThreshold)
		if ok {
			catalog.Models[model] = config
		}
	}
	return catalog, nil
}

func parseGrokCompactionModel(raw json.RawMessage, globalThreshold uint8) (string, GrokCompactionModelConfig, bool) {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return "", GrokCompactionModelConfig{}, false
	}
	meta := rawObject(object["_meta"])
	model := firstStringField(object, "model", "modelId", "id")
	if model == "" {
		model = firstStringField(meta, "model", "modelId")
	}
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return "", GrokCompactionModelConfig{}, false
	}

	contextWindow := grokCompactionDefaultContextWindow
	if parsed, valid := firstUintField(object, "contextWindow", "context_window"); valid {
		contextWindow = parsed
	} else if parsed, valid := firstUintField(meta, "contextWindow", "totalContextTokens"); valid {
		contextWindow = parsed
	}
	if contextWindow == 0 {
		return "", GrokCompactionModelConfig{}, false
	}

	threshold := globalThreshold
	if parsed, valid := firstUintField(object, "autoCompactThresholdPercent", "auto_compact_threshold_percent"); valid && parsed <= 255 {
		threshold = uint8(parsed)
	}
	config := GrokCompactionModelConfig{ContextWindow: contextWindow, ThresholdPercent: threshold}

	if value, ok := firstRawFieldWithMeta(object, meta, []string{"compactionAtTokens", "compaction_at_tokens"}, "compactionAtTokens"); ok {
		if enabled, isBool := rawBool(value); isBool {
			if enabled {
				config.CompactionAt = GrokCompactionAtConfig{
					Enabled: true,
					Value:   contextWindow * uint64(threshold) / 100,
				}
			}
		} else if fixed, isUint := rawUint64(value); isUint {
			config.CompactionAt = GrokCompactionAtConfig{Enabled: true, Value: fixed}
		}
	}

	remainingParsed := false
	if value, ok := firstRawFieldWithMeta(object, meta, []string{"compactionsRemaining", "compactions_remaining"}, "compactionsRemaining"); ok {
		if dynamic, isBool := rawBool(value); isBool {
			config.CompactionsRemaining = GrokCompactionsRemainingConfig{Enabled: dynamic, Dynamic: dynamic}
			remainingParsed = true
		} else if fixed, isUint := rawUint64(value); isUint && fixed <= 255 {
			config.CompactionsRemaining = GrokCompactionsRemainingConfig{Enabled: true, Value: uint8(fixed)}
			remainingParsed = true
		}
	}
	if !remainingParsed {
		if value, ok := firstRawFieldWithMeta(object, meta, []string{"sendCompactionsRemaining", "send_compactions_remaining"}, "sendCompactionsRemaining"); ok {
			if dynamic, isBool := rawBool(value); isBool {
				config.CompactionsRemaining = GrokCompactionsRemainingConfig{Enabled: dynamic, Dynamic: dynamic}
			}
		}
	}

	return model, config, true
}

func firstRawField(object map[string]json.RawMessage, names ...string) (json.RawMessage, bool) {
	for _, name := range names {
		if value, ok := object[name]; ok {
			return value, true
		}
	}
	return nil, false
}

func firstRawFieldWithMeta(
	object map[string]json.RawMessage,
	meta map[string]json.RawMessage,
	names []string,
	metaName string,
) (json.RawMessage, bool) {
	if value, ok := firstRawField(object, names...); ok {
		return value, true
	}
	return firstRawField(meta, metaName)
}

func firstStringField(object map[string]json.RawMessage, names ...string) string {
	for _, name := range names {
		var value string
		if raw, ok := object[name]; ok && json.Unmarshal(raw, &value) == nil {
			return value
		}
	}
	return ""
}

func firstUintField(object map[string]json.RawMessage, names ...string) (uint64, bool) {
	for _, name := range names {
		if raw, ok := object[name]; ok {
			if value, valid := rawUint64(raw); valid {
				return value, true
			}
		}
	}
	return 0, false
}

func rawObject(raw json.RawMessage) map[string]json.RawMessage {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return nil
	}
	return object
}

func rawBool(raw json.RawMessage) (bool, bool) {
	var value bool
	if json.Unmarshal(raw, &value) != nil {
		return false, false
	}
	return value, true
}

func rawUint64(raw json.RawMessage) (uint64, bool) {
	var value uint64
	if json.Unmarshal(raw, &value) != nil {
		return 0, false
	}
	return value, true
}
