package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/cpr"
	"github.com/stretchr/testify/require"
)

type cprCleanupKeys struct {
	APIKeyRepository
	key *APIKey
	err error
}

func (r *cprCleanupKeys) GetByID(context.Context, int64) (*APIKey, error) { return r.key, r.err }

type cprCleanupSettings struct {
	SettingRepository
	values map[string]string
	err    error
}

func (r *cprCleanupSettings) Set(_ context.Context, key, value string) error {
	r.values[key] = value
	return nil
}
func (r *cprCleanupSettings) GetAll(context.Context) (map[string]string, error) {
	return r.values, r.err
}
func (r *cprCleanupSettings) Delete(_ context.Context, key string) error {
	delete(r.values, key)
	return nil
}

func TestCPRInternalAuthorizationCleanupRevokesOnlyConfirmedStaleBindings(t *testing.T) {
	deletes := 0
	remoteStatus := 200
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, string(cpr.DeleteClientKey), r.URL.Path)
		deletes++
		w.WriteHeader(remoteStatus)
		_, _ = w.Write([]byte(`{"code":200,"data":{}}`))
	}))
	defer server.Close()
	client, err := cpr.New(cpr.Config{BaseURL: server.URL, AdminKey: "private"}, nil)
	require.NoError(t, err)
	secret := strings.Repeat("s", 32)
	scope := cpr.Scope{Deployment: "fixture", APIKeyID: 3, GroupID: 5, Provider: "openai", AccountGroupIDs: []string{"pool-a"}}
	key, err := scope.Key([]byte(secret))
	require.NoError(t, err)
	encoded, err := json.Marshal(cpr.PersistedKeyBinding{ID: "remote-fixture", Scope: scope})
	require.NoError(t, err)
	storeKey := cpr.KeyBindingPrefix + key.Name()
	settings := &cprCleanupSettings{values: map[string]string{storeKey: string(encoded)}}
	groupID := int64(5)
	keys := &cprCleanupKeys{key: &APIKey{ID: 3, Status: StatusActive, GroupID: &groupID}}
	account := &Account{ID: 17, AccountDriverID: "cpr-managed", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, GroupIDs: []int64{5}, Extra: map[string]any{"cpr_pool": CPRPoolBinding{GroupID: 5, Provider: "openai", AccountGroupIDs: []string{"pool-a"}}}}
	s := &CPRPoolService{client: client, settings: settings, keys: keys, accounts: &cprTestAccounts{account: account}, config: config.CPRManagedConfig{DeploymentID: "fixture", BridgeSecret: secret}, provisioner: cpr.NewProvisioner(client, settings)}
	require.NoError(t, s.RevokeStaleKeys(context.Background()))
	require.Zero(t, deletes)
	keys.err = errors.New("native database unavailable")
	require.Error(t, s.RevokeStaleKeys(context.Background()))
	require.Zero(t, deletes)
	keys.err = nil
	keys.key.Status = "inactive"
	remoteStatus = 502
	require.Error(t, s.RevokeStaleKeys(context.Background()))
	require.Contains(t, settings.values, storeKey)
	remoteStatus = 200
	require.NoError(t, s.RevokeStaleKeys(context.Background()))
	require.NotContains(t, settings.values, storeKey)
	require.Equal(t, 2, deletes)
	settings.values[storeKey] = string(encoded)
	keys.key.Status = StatusActive
	account.Extra["cpr_pool"] = CPRPoolBinding{GroupID: 5, Provider: "openai", AccountGroupIDs: []string{"new-pool"}}
	require.NoError(t, s.RevokeStaleKeys(context.Background()))
	require.NotContains(t, settings.values, storeKey)
	require.Equal(t, 3, deletes)
}
