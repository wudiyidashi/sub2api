//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountDriverBindingCRUDAndSQLFilter(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	native := &service.Account{Name: "native-driver", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: map[string]any{"access_token": "native"}, Status: service.StatusActive}
	enhanced := &service.Account{Name: "enhanced-driver", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, AccountDriverID: service.AccountDriverCodexHTTP, Credentials: map[string]any{"access_token": "enhanced"}, Status: service.StatusActive}
	require.NoError(t, repo.Create(ctx, native))
	require.NoError(t, repo.Create(ctx, enhanced))
	require.Equal(t, "native", native.AccountDriverID)
	require.Equal(t, int64(1), native.AccountDriverRevision)
	loaded, err := repo.GetByID(ctx, enhanced.ID)
	require.NoError(t, err)
	require.Equal(t, service.AccountDriverCodexHTTP, loaded.AccountDriverID)
	require.Equal(t, int64(1), loaded.AccountDriverRevision)
	// Simulate an old snapshot writer: ordinary updates must not replace the
	// persisted binding even when its service projection lacks the new fields.
	loaded.AccountDriverID = ""
	loaded.AccountDriverRevision = 0
	loaded.Name = "edited-enhanced"
	require.NoError(t, repo.Update(ctx, loaded))
	require.Equal(t, service.AccountDriverCodexHTTP, loaded.AccountDriverID)
	require.Equal(t, int64(1), loaded.AccountDriverRevision)
	require.NoError(t, repo.UpdateCredentials(ctx, loaded.ID, map[string]any{"access_token": "refreshed-by-native-host"}))
	refreshed, err := repo.GetByID(ctx, loaded.ID)
	require.NoError(t, err)
	require.Equal(t, service.AccountDriverCodexHTTP, refreshed.AccountDriverID)
	require.Equal(t, int64(1), refreshed.AccountDriverRevision)
	for _, id := range []string{"native", service.AccountDriverCodexHTTP} {
		rows, page, err := repo.ListWithDriverFilters(ctx, pagination.PaginationParams{Page: 1, PageSize: 20}, service.PlatformOpenAI, "", "", "", 0, "", id)
		require.NoError(t, err)
		require.Equal(t, int64(1), page.Total)
		require.Len(t, rows, 1)
		require.Equal(t, id, rows[0].AccountDriverID)
		all, err := repo.ListAllWithDriverFilters(ctx, service.PlatformOpenAI, "", "", "", 0, "", id)
		require.NoError(t, err)
		require.Len(t, all, 1)
	}
	bulk := service.AccountBulkUpdate{Credentials: map[string]any{"refresh_token": "new-refresh"}}
	_, err = repo.BulkUpdate(ctx, []int64{enhanced.ID}, bulk)
	require.NoError(t, err)
	final, err := repo.GetByID(ctx, enhanced.ID)
	require.NoError(t, err)
	require.Equal(t, service.AccountDriverCodexHTTP, final.AccountDriverID)
	require.Equal(t, int64(1), final.AccountDriverRevision)
}
