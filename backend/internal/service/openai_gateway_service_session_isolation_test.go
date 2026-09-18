package service

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsolateOpenAISessionID(t *testing.T) {
	t.Run("empty_raw_returns_empty", func(t *testing.T) {
		assert.Equal(t, "", isolateOpenAISessionID(1, ""))
		assert.Equal(t, "", isolateOpenAISessionID(1, "   "))
	})

	t.Run("deterministic", func(t *testing.T) {
		a := isolateOpenAISessionID(42, "sess_abc123")
		b := isolateOpenAISessionID(42, "sess_abc123")
		assert.Equal(t, a, b)
	})

	t.Run("different_apiKeyID_different_result", func(t *testing.T) {
		a := isolateOpenAISessionID(1, "same_session")
		b := isolateOpenAISessionID(2, "same_session")
		require.NotEqual(t, a, b, "不同 API Key 使用相同 session_id 应产生不同隔离值")
	})

	t.Run("different_raw_different_result", func(t *testing.T) {
		a := isolateOpenAISessionID(1, "session_a")
		b := isolateOpenAISessionID(1, "session_b")
		require.NotEqual(t, a, b)
	})

	t.Run("format_is_16_hex_chars", func(t *testing.T) {
		result := isolateOpenAISessionID(99, "test_session")
		assert.Len(t, result, 16, "应为 16 字符的 hex 字符串")
		for _, ch := range result {
			assert.True(t, (ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f'),
				"应仅包含 hex 字符: %c", ch)
		}
	})

	t.Run("zero_apiKeyID_still_works", func(t *testing.T) {
		result := isolateOpenAISessionID(0, "session")
		assert.NotEmpty(t, result)
		// apiKeyID=0 与 apiKeyID=1 应产生不同结果
		other := isolateOpenAISessionID(1, "session")
		assert.NotEqual(t, result, other)
	})
}

// isolateOpenAISessionIDForAccount：同 (apiKeyID, raw) 在不同账号下派生不同值，
// accountID<=0 时与 isolateOpenAISessionID 完全一致。
func TestIsolateOpenAISessionIDForAccount(t *testing.T) {
	t.Run("different_accountID_different_result", func(t *testing.T) {
		a := isolateOpenAISessionIDForAccount(1, 0, "same_session")
		b := isolateOpenAISessionIDForAccount(2, 0, "same_session")
		require.NotEqual(t, a, b, "同一 seed 在不同上游账号下应产生不同隔离值")
	})

	t.Run("zero_accountID_falls_back", func(t *testing.T) {
		assert.Equal(t, isolateOpenAISessionID(7, "sess_x"), isolateOpenAISessionIDForAccount(0, 7, "sess_x"))
		assert.Equal(t, isolateOpenAISessionID(7, "sess_x"), isolateOpenAISessionIDForAccount(-1, 7, "sess_x"))
	})

	t.Run("deterministic", func(t *testing.T) {
		a := isolateOpenAISessionIDForAccount(42, 1, "sess_abc123")
		b := isolateOpenAISessionIDForAccount(42, 1, "sess_abc123")
		assert.Equal(t, a, b)
	})
}

// generateCodexSessionUUID：同 seed 不同账号派生不同 UUID，同输入幂等。
func TestGenerateCodexSessionUUIDAccountIsolation(t *testing.T) {
	before := time.Now().Add(-time.Second).UnixMilli()
	a := generateCodexSessionUUID(1, 0, "seed-1")
	b := generateCodexSessionUUID(2, 0, "seed-1")
	require.NotEqual(t, a, b, "同一 seed 在不同上游账号下应派生不同 session UUID")
	require.Equal(t, a, generateCodexSessionUUID(1, 0, "seed-1"), "同输入应幂等")
	// accountID=0 时与原 (apiKeyID, seed) 行为一致。
	require.Equal(t, a, generateCodexSessionUUID(0, 0, "a1:seed-1"))
	parsed, err := uuid.Parse(a)
	require.NoError(t, err)
	require.Equal(t, uuid.Version(7), parsed.Version())
	require.GreaterOrEqual(t, codexUUIDV7Timestamp(parsed), before)
	require.LessOrEqual(t, codexUUIDV7Timestamp(parsed), time.Now().Add(time.Second).UnixMilli())
}

func TestGenerateCodexSessionUUIDPreservesPlausibleV7Timestamp(t *testing.T) {
	const source = "01a052d7-b018-7630-ad5b-f23494429b7a"
	sourceUUID, err := uuid.Parse(source)
	require.NoError(t, err)

	a := generateCodexSessionUUID(1, 0, source)
	b := generateCodexSessionUUID(2, 0, source)
	require.NotEqual(t, a, b)
	for _, generated := range []string{a, b} {
		parsed, parseErr := uuid.Parse(generated)
		require.NoError(t, parseErr)
		require.Equal(t, uuid.Version(7), parsed.Version())
		require.Equal(t, sourceUUID[:6], parsed[:6])
	}
}

func TestResolveCodexSessionUUIDRejectsNonV7FixedShape(t *testing.T) {
	const fixedV4 = "00e9ffcb-88d7-4ee8-aeca-1982d91a1330"
	generated := resolveCodexSessionUUID(7, 42, "ignored", fixedV4)
	parsed, err := uuid.Parse(generated)
	require.NoError(t, err)
	require.Equal(t, uuid.Version(7), parsed.Version())
	require.NotEqual(t, fixedV4, generated)
	require.Equal(t, generated, resolveCodexSessionUUID(7, 42, "ignored", fixedV4))
	require.NotEqual(t, generated, resolveCodexSessionUUID(7, 42, "other", fixedV4), "fixed namespace must isolate tasks")
	require.NotEqual(t, generated, resolveCodexSessionUUID(7, 99, "ignored", fixedV4), "fixed namespace must isolate API keys")
	require.NotEqual(t, generated, resolveCodexSessionUUID(8, 42, "other", fixedV4), "legacy fixed IDs must remain account-isolated")
}

func TestCodexSessionUUIDCacheExpiresAndEvictsLRU(t *testing.T) {
	cache := &codexSessionUUIDCache{
		entries:    make(map[string]codexSessionUUIDCacheEntry),
		ttl:        time.Minute,
		maxEntries: 2,
	}
	now := time.Unix(1_800_000_000, 0)
	a := cache.getOrCreate("a", now)
	_ = cache.getOrCreate("b", now.Add(time.Second))
	require.Equal(t, a, cache.getOrCreate("a", now.Add(2*time.Second)))
	_ = cache.getOrCreate("c", now.Add(3*time.Second))

	cache.mu.Lock()
	_, hasA := cache.entries["a"]
	_, hasB := cache.entries["b"]
	_, hasC := cache.entries["c"]
	cache.mu.Unlock()
	require.True(t, hasA)
	require.False(t, hasB, "least recently used entry should be evicted first")
	require.True(t, hasC)

	require.NotEqual(t, a, cache.getOrCreate("a", now.Add(2*time.Minute)), "expired seed should receive a fresh v7")
}
