package service

import (
	"testing"

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
	a := generateCodexSessionUUID(1, 0, "seed-1")
	b := generateCodexSessionUUID(2, 0, "seed-1")
	require.NotEqual(t, a, b, "同一 seed 在不同上游账号下应派生不同 session UUID")
	require.Equal(t, a, generateCodexSessionUUID(1, 0, "seed-1"), "同输入应幂等")
	// accountID=0 时与原 (apiKeyID, seed) 行为一致。
	require.Equal(t, a, generateCodexSessionUUID(0, 0, "a1:seed-1"))
}
