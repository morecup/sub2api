package service

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// codexInstallationIDForAccount：按账号确定性派生 installation_id，
// 不同账号互不相同的稳定 UUID，缺失种子时回退实抓固定值。
func TestCodexInstallationIDForAccount(t *testing.T) {
	// 不同账号 ID 派生出不同 installation_id。
	id1 := codexInstallationIDForAccount(1, "")
	id2 := codexInstallationIDForAccount(2, "")
	require.NotEqual(t, id1, id2)
	require.NotEqual(t, codexInstallationID, id1)

	// 同一账号 ID 多次派生结果一致且是合法 UUID。
	require.Equal(t, id1, codexInstallationIDForAccount(1, ""))
	parsed, err := uuid.Parse(id1)
	require.NoError(t, err)
	require.Equal(t, uuid.Version(5), parsed.Version())

	// accountID=0 且 chatgptAccountID 非空时用 chatgpt 种子。
	idChatgpt := codexInstallationIDForAccount(0, "chatgpt-acc")
	require.NotEqual(t, codexInstallationID, idChatgpt)
	require.NotEqual(t, id1, idChatgpt)
	require.Equal(t, idChatgpt, codexInstallationIDForAccount(0, "  chatgpt-acc  "))
	_, err = uuid.Parse(idChatgpt)
	require.NoError(t, err)

	// 账号 ID 优先于 chatgpt-account-id。
	require.Equal(t, id1, codexInstallationIDForAccount(1, "chatgpt-acc"))

	// 两者皆空时回退 codexInstallationID。
	require.Equal(t, codexInstallationID, codexInstallationIDForAccount(0, ""))
	require.Equal(t, codexInstallationID, codexInstallationIDForAccount(0, "   "))
}
