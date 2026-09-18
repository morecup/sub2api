package repository

import (
	"context"
	"encoding/json"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCodexClientProfileSurvivesExternalSyncUnderRowLock(t *testing.T) {
	stored := &service.Account{ID: 37, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth}
	require.NoError(t, service.InitializeCodexClientProfile(stored))
	profile := stored.Extra[service.CodexClientProfileExtraKey]
	raw, err := json.Marshal(profile)
	require.NoError(t, err)
	for _, name := range []string{"omitted", "unchanged", "another account"} {
		t.Run(name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
			defer client.Close()
			incoming := &service.Account{ID: stored.ID, Platform: stored.Platform, Type: stored.Type,
				Credentials: map[string]any{"access_token": "refreshed"}, Extra: map[string]any{"crs_synced_at": "new-sync"}}
			if name == "unchanged" {
				incoming.Extra[service.CodexClientProfileExtraKey] = profile
			} else if name == "another account" {
				other := &service.Account{Platform: stored.Platform, Type: stored.Type}
				require.NoError(t, service.InitializeCodexClientProfile(other))
				incoming.Extra[service.CodexClientProfileExtraKey] = other.Extra[service.CodexClientProfileExtraKey]
			}
			mock.ExpectQuery(`(?s)SELECT.*openai_codex_client_profile.*FOR NO KEY UPDATE`).
				WithArgs(stored.ID, stored.Platform, stored.Type, `{"access_token":"refreshed"}`, nil).
				WillReturnRows(sqlmock.NewRows([]string{"same", "ollama_same", "proxy_same", "enabled", "snapshot", "session", "auto", "usage", "profile"}).
					AddRow(false, false, true, nil, nil, nil, nil, nil, raw))
			got, err := lockAndMergeAccountProbeExtra(context.Background(), client, incoming, nil)
			if name == "another account" {
				require.ErrorContains(t, err, "cannot be replaced")
			} else {
				require.NoError(t, err)
				require.Equal(t, profile, got[service.CodexClientProfileExtraKey])
				require.Equal(t, "new-sync", got["crs_synced_at"])
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
