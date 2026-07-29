package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

func TestParseGrokCompactionCatalogMatchesOfficialFieldSemantics(t *testing.T) {
	t.Parallel()

	catalog, err := parseGrokCompactionCatalog([]byte(`{
		"data": [
			{"id":"dynamic","contextWindow":500000,"autoCompactThresholdPercent":80,"compactionAtTokens":true,"compactionsRemaining":true},
			{"model":"fixed","context_window":1000,"compaction_at_tokens":321,"compactions_remaining":3},
			{"modelId":"independent-at","contextWindow":2000,"compactionAtTokens":true},
			{"id":"independent-remaining","sendCompactionsRemaining":true},
			{"id":"fallback-remaining","compactionsRemaining":"invalid","sendCompactionsRemaining":true},
			{"id":"disabled","compactionAtTokens":false,"compactionsRemaining":false},
			{"_meta":{"model":"meta-model","contextWindow":3000,"compactionAtTokens":true,"compactionsRemaining":2}},
			{"id":"default-context","compactionAtTokens":true},
			{"id":"zero-context","contextWindow":0,"compactionAtTokens":true},
			{"contextWindow":1000,"compactionAtTokens":true}
		]
	}`), 70)
	require.NoError(t, err)
	require.Len(t, catalog.Models, 8)

	dynamic := catalog.Models["dynamic"]
	require.Equal(t, uint64(500000), dynamic.ContextWindow)
	require.Equal(t, uint8(80), dynamic.ThresholdPercent)
	require.Equal(t, GrokCompactionAtConfig{Enabled: true, Value: 400000}, dynamic.CompactionAt)
	require.Equal(t, GrokCompactionsRemainingConfig{Enabled: true, Dynamic: true}, dynamic.CompactionsRemaining)

	fixed := catalog.Models["fixed"]
	require.Equal(t, GrokCompactionAtConfig{Enabled: true, Value: 321}, fixed.CompactionAt)
	require.Equal(t, GrokCompactionsRemainingConfig{Enabled: true, Value: 3}, fixed.CompactionsRemaining)

	require.True(t, catalog.Models["independent-at"].CompactionAt.Enabled)
	require.False(t, catalog.Models["independent-at"].CompactionsRemaining.Enabled)
	require.False(t, catalog.Models["independent-remaining"].CompactionAt.Enabled)
	require.True(t, catalog.Models["independent-remaining"].CompactionsRemaining.Dynamic)
	require.True(t, catalog.Models["fallback-remaining"].CompactionsRemaining.Dynamic)
	require.False(t, catalog.Models["disabled"].CompactionAt.Enabled)
	require.False(t, catalog.Models["disabled"].CompactionsRemaining.Enabled)

	meta := catalog.Models["meta-model"]
	require.Equal(t, uint64(3000), meta.ContextWindow)
	require.Equal(t, uint64(2100), meta.CompactionAt.Value)
	require.Equal(t, uint8(2), meta.CompactionsRemaining.Value)

	defaults := catalog.Models["default-context"]
	require.Equal(t, grokCompactionDefaultContextWindow, defaults.ContextWindow)
	require.Equal(t, uint64(179200), defaults.CompactionAt.Value)
	_, hasZero := catalog.Models["zero-context"]
	require.False(t, hasZero)
}

func TestParseGrokCompactionThresholdSupportsRemoteSpellings(t *testing.T) {
	t.Parallel()

	value, ok := parseGrokCompactionThreshold([]byte(`{"auto_compact_threshold_percent":80}`))
	require.True(t, ok)
	require.Equal(t, uint8(80), value)
	value, ok = parseGrokCompactionThreshold([]byte(`{"autoCompactThresholdPercent":81}`))
	require.True(t, ok)
	require.Equal(t, uint8(81), value)
	_, ok = parseGrokCompactionThreshold([]byte(`{"auto_compact_threshold_percent":256}`))
	require.False(t, ok)
}

func TestApplyGrokCompactionHeadersTreatsLoadedCatalogAsAuthoritative(t *testing.T) {
	t.Parallel()

	catalog := &GrokCompactionCatalog{Models: map[string]GrokCompactionModelConfig{
		"only-at": {
			CompactionAt: GrokCompactionAtConfig{Enabled: true, Value: 1234},
		},
		"only-remaining": {
			CompactionsRemaining: GrokCompactionsRemainingConfig{Enabled: true, Dynamic: true},
		},
	}}

	missing := make(http.Header)
	applyGrokCompactionHeaders(missing, "grok-4.5", nil, catalog)
	require.Empty(t, missing.Get(grokCompactionAtHeader), "a loaded catalog must not use the static fallback")
	require.Empty(t, missing.Get(grokCompactionsLeftHeader))

	onlyAt := make(http.Header)
	applyGrokCompactionHeaders(onlyAt, "ONLY-AT", nil, catalog)
	require.Equal(t, "1234", onlyAt.Get(grokCompactionAtHeader))
	require.Empty(t, onlyAt.Get(grokCompactionsLeftHeader))

	onlyRemaining := make(http.Header)
	compacted := []byte(`{"input":[{"type":"compaction"}]}`)
	applyGrokCompactionHeaders(onlyRemaining, "only-remaining", compacted, catalog)
	require.Empty(t, onlyRemaining.Get(grokCompactionAtHeader))
	require.Equal(t, "0", onlyRemaining.Get(grokCompactionsLeftHeader))
}

type grokCompactionCatalogStoreStub struct {
	GatewayCache
	mu      sync.Mutex
	catalog GrokCompactionCatalog
	found   bool
	gets    int
	sets    int
}

func (s *grokCompactionCatalogStoreStub) GetGrokCompactionCatalog(_ context.Context, _ int64) (GrokCompactionCatalog, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gets++
	return s.catalog, s.found, nil
}

func (s *grokCompactionCatalogStoreStub) SetGrokCompactionCatalog(_ context.Context, _ int64, catalog GrokCompactionCatalog, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.catalog = catalog
	s.found = true
	s.sets++
	return nil
}

type grokCompactionCatalogUpstreamStub struct {
	mu       sync.Mutex
	requests []*http.Request
}

func (s *grokCompactionCatalogUpstreamStub) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return s.respond(req)
}

func (s *grokCompactionCatalogUpstreamStub) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return s.respond(req)
}

func (s *grokCompactionCatalogUpstreamStub) respond(req *http.Request) (*http.Response, error) {
	s.mu.Lock()
	s.requests = append(s.requests, req.Clone(req.Context()))
	s.mu.Unlock()
	body := `{"auto_compact_threshold_percent":80}`
	if strings.HasSuffix(req.URL.Path, "/models") {
		body = `{"data":[{"id":"grok-4.5","contextWindow":500000,"compactionAtTokens":true,"compactionsRemaining":true}]}`
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}, nil
}

func (s *grokCompactionCatalogUpstreamStub) requestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func TestBuildGrokResponsesRequestLoadsAndSharesAccountCompactionCatalog(t *testing.T) {
	t.Parallel()

	store := &grokCompactionCatalogStoreStub{}
	upstream := &grokCompactionCatalogUpstreamStub{}
	account := grokCLISessionTestAccount(7190, "user-id")
	account.Credentials["base_url"] = "https://catalog.grok.example.test/v1"
	body := []byte(`{"model":"grok-4.5","stream":true,"input":[{"role":"user","content":"hi"}]}`)

	firstService := &OpenAIGatewayService{cache: store, httpUpstream: upstream}
	first, err := firstService.buildGrokResponsesRequest(context.Background(), nil, account, body, "synthetic-token", "conv-catalog", nil)
	require.NoError(t, err)
	require.Equal(t, "400000", first.Header.Get(grokCompactionAtHeader))
	require.Equal(t, "1", first.Header.Get(grokCompactionsLeftHeader))
	require.Equal(t, 2, upstream.requestCount(), "settings and models should each load once")
	upstream.mu.Lock()
	for _, request := range upstream.requests {
		require.Equal(t, HTTPUpstreamProfileGrokControlPlane, HTTPUpstreamProfileFromContext(request.Context()))
		require.Equal(t, "Bearer synthetic-token", request.Header.Get("Authorization"))
	}
	upstream.mu.Unlock()

	second, err := firstService.buildGrokResponsesRequest(context.Background(), nil, account, body, "synthetic-token", "conv-catalog", nil)
	require.NoError(t, err)
	require.Equal(t, "400000", second.Header.Get(grokCompactionAtHeader))
	require.Equal(t, 2, upstream.requestCount(), "the process-local hot cache should avoid another fetch")

	// A separate service instance has no L1 entry but reads the catalog written
	// by the first instance through the shared store.
	secondService := &OpenAIGatewayService{cache: store, httpUpstream: upstream}
	third, err := secondService.buildGrokResponsesRequest(context.Background(), nil, account, body, "synthetic-token", "conv-catalog", nil)
	require.NoError(t, err)
	require.Equal(t, "400000", third.Header.Get(grokCompactionAtHeader))
	require.Equal(t, 2, upstream.requestCount())
	require.Equal(t, 1, store.sets)
	require.GreaterOrEqual(t, store.gets, 2)
}
