//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestAccountDriverDatabaseConstraintMatchesHostAuthModeValidation(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	enhanced := &service.Account{Name: "constraint-enhanced", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, AccountDriverID: service.AccountDriverCodexHTTP, Credentials: map[string]any{"access_token": "host-token"}, Status: service.StatusActive}
	native := &service.Account{Name: "constraint-native", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: map[string]any{"access_token": "native-token"}, Status: service.StatusActive}
	require.NoError(t, repo.Create(ctx, enhanced))
	require.NoError(t, repo.Create(ctx, native))
	for _, mode := range []any{nil, "", "oauth", " OAUTH ", "\toauth\n", "\u00a0oauth\u3000", "personal_access_token", "agent_identity", true, 12.0, map[string]any{"kind": "oauth"}} {
		for _, field := range []string{"auth_mode", "openai_auth_mode"} {
			for _, column := range []string{"credentials", "extra"} {
				values := map[string]any{field: mode}
				candidate := *enhanced
				if column == "credentials" {
					candidate.Credentials = values
				} else {
					candidate.Extra = values
				}
				allowedByHost := service.ValidateAccountDriverState(&candidate) == nil
				payload, err := json.Marshal(values)
				require.NoError(t, err)
				_, err = tx.ExecContext(ctx, "SAVEPOINT driver_auth_mode")
				require.NoError(t, err)
				// The column comes only from the literal list above, never an API input.
				_, err = tx.ExecContext(ctx, "UPDATE accounts SET "+column+" = $1::jsonb WHERE id = $2", string(payload), enhanced.ID)
				if allowedByHost {
					require.NoError(t, err)
				} else {
					var pgErr *pq.Error
					require.ErrorAs(t, err, &pgErr)
					require.Equal(t, pq.ErrorCode("23514"), pgErr.Code)
					require.Equal(t, "accounts_codex_http_binding_compatible", pgErr.Constraint)
				}
				_, err = tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT driver_auth_mode")
				require.NoError(t, err)
				// Native accounts retain their prior provider-specific metadata behavior.
				_, err = tx.ExecContext(ctx, "UPDATE accounts SET "+column+" = $1::jsonb WHERE id = $2", string(payload), native.ID)
				require.NoError(t, err)
			}
		}
	}
	for _, mutation := range []struct {
		query string
		value any
	}{
		{"UPDATE accounts SET type = $1 WHERE id = $2", service.AccountTypeAPIKey},
		{"UPDATE accounts SET platform = $1 WHERE id = $2", service.PlatformAnthropic},
		{"UPDATE accounts SET parent_account_id = $1 WHERE id = $2", native.ID},
	} {
		_, err := tx.ExecContext(ctx, "SAVEPOINT driver_identity")
		require.NoError(t, err)
		_, err = tx.ExecContext(ctx, mutation.query, mutation.value, enhanced.ID)
		var pgErr *pq.Error
		require.ErrorAs(t, err, &pgErr)
		require.Equal(t, "accounts_codex_http_binding_compatible", pgErr.Constraint)
		_, err = tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT driver_identity")
		require.NoError(t, err)
	}
	stored, err := repo.GetByID(ctx, enhanced.ID)
	require.NoError(t, err)
	require.Equal(t, service.AccountDriverCodexHTTP, stored.AccountDriverID)
	require.Equal(t, service.AccountTypeOAuth, stored.Type)
	require.Equal(t, map[string]any{"access_token": "host-token"}, stored.Credentials)
}
