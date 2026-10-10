//go:build unit

package repository

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountDriverSchedulerSnapshotsPreserveBinding(t *testing.T) {
	cache := newSchedulerCacheUnit(t)
	ctx := context.Background()
	account := service.Account{ID: 908, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		AccountDriverID: service.AccountDriverCodexHTTP, AccountDriverRevision: 7,
		Credentials: map[string]any{"access_token": "host-only-token"},
	}
	require.NoError(t, cache.SetAccount(ctx, &account))
	full, err := cache.GetAccount(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, service.AccountDriverCodexHTTP, full.EffectiveAccountDriverID())
	require.Equal(t, int64(7), full.AccountDriverRevision)
	metadata := buildSchedulerMetadataAccount(account)
	require.Equal(t, full.AccountDriverID, metadata.AccountDriverID)
	require.Equal(t, full.AccountDriverRevision, metadata.AccountDriverRevision)
	require.NotContains(t, metadata.Credentials, "access_token")
	var old service.Account
	require.NoError(t, json.Unmarshal([]byte(`{"ID":9,"Platform":"openai","Type":"oauth"}`), &old))
	require.Equal(t, "native", old.EffectiveAccountDriverID())
	require.Zero(t, old.AccountDriverRevision)
}
