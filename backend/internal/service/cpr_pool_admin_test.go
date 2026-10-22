package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/cpr"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type cprAdminAccounts struct {
	AdminAccountRepository
	pool   *Account
	native []Account
	writes int
}

func (r *cprAdminAccounts) CreateWithAccountGroups(_ context.Context, account *Account, groups []AccountGroup) error {
	copy := *account
	copy.ID = 17
	account.ID = 17
	r.pool = &copy
	r.writes++
	return nil
}
func (r *cprAdminAccounts) GetByID(context.Context, int64) (*Account, error) {
	copy := *r.pool
	return &copy, nil
}
func (r *cprAdminAccounts) ListByGroup(context.Context, int64) ([]Account, error) {
	return r.native, nil
}
func (r *cprAdminAccounts) UpdateCPRPoolBinding(_ context.Context, account *Account, revision int64) error {
	if r.pool.AccountDriverRevision != revision {
		return infraerrors.Conflict("CPR_POOL_CHANGED", "pool changed")
	}
	copy := *account
	copy.AccountDriverRevision = revision + 1
	account.AccountDriverRevision = copy.AccountDriverRevision
	r.pool = &copy
	r.writes++
	return nil
}

type cprAdminGroups struct{ GroupRepository }

func (cprAdminGroups) GetByID(context.Context, int64) (*Group, error) {
	return &Group{ID: 5, Platform: PlatformOpenAI, Status: StatusActive}, nil
}

func TestCPRPoolBindingsKeepBusinessScopeAndRejectInvalidChanges(t *testing.T) {
	counts := map[string]int{"openai": 1}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, string(cpr.ListGroups), r.URL.Path)
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"items": []any{map[string]any{"id": "pool-a", "enabled": true, "providerCounts": counts}}, "page": map[string]int{"totalPages": 1}}}))
	}))
	defer server.Close()
	client, err := cpr.New(cpr.Config{BaseURL: server.URL, AdminKey: "fixture"}, nil)
	require.NoError(t, err)
	repo := &cprAdminAccounts{}
	s := NewCPRPoolAdminService(client, repo, cprAdminGroups{})
	input := CPRPoolInput{Name: "Business pool", CPRPoolBinding: CPRPoolBinding{GroupID: 5, Provider: "openai", AccountGroupIDs: []string{"pool-a"}}, Enabled: true, Concurrency: 0}
	pool, err := s.Save(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, int64(17), pool.ID)
	require.Equal(t, []int64{5}, repo.pool.GroupIDs)
	require.Empty(t, repo.pool.Credentials)
	require.Nil(t, repo.pool.ProxyID)
	require.Equal(t, "cpr-managed", repo.pool.AccountDriverID)
	input.ID, input.ExpectedRevision = 17, 1
	input.Enabled = false
	input.Name = "Paused business pool"
	pool, err = s.Save(context.Background(), input)
	require.NoError(t, err)
	require.False(t, pool.Enabled)
	require.Equal(t, int64(2), pool.Revision)
	_, err = s.Save(context.Background(), input)
	require.Error(t, err)
	require.Equal(t, 2, repo.writes)
	input.ExpectedRevision = 2
	for _, mutate := range []func(*CPRPoolInput){
		func(p *CPRPoolInput) { p.AccountGroupIDs = nil },
		func(p *CPRPoolInput) { p.AccountGroupIDs = []string{"unknown-pool"} },
		func(p *CPRPoolInput) { p.Provider = "xai" },
		func(p *CPRPoolInput) { p.GroupID = 6 },
	} {
		invalid := input
		mutate(&invalid)
		_, err = s.Save(context.Background(), invalid)
		require.Error(t, err)
		require.Equal(t, 2, repo.writes)
	}
	counts["xai"] = 1
	_, err = s.Save(context.Background(), input)
	require.Error(t, err)
	delete(counts, "xai")
	repo.native = []Account{{ID: 88, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true}}
	input.Enabled = true
	_, err = s.Save(context.Background(), input)
	require.Error(t, err)
	require.Equal(t, 2, repo.writes)
}
