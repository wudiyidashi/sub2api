package cpr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPinnedManagementContractKeepsKeysAndBusinessAPIsPrivate(t *testing.T) {
	seen := map[string]bool{}
	for _, endpoint := range ManagementEndpoints() {
		require.NotContains(t, endpoint.Path, "/client-keys")
		require.NotContains(t, endpoint.Path, "/admin-api-key")
		require.NotContains(t, endpoint.Path, "/system/restart")
		require.NotContains(t, endpoint.Path, "/users")
		require.False(t, seen[endpoint.Method+" "+endpoint.Path])
		seen[endpoint.Method+" "+endpoint.Path] = true
		require.Equal(t, endpoint.Path, endpoint.Operation.Path())
		require.Equal(t, endpoint.Method, operationMethods[endpoint.Operation])
		if endpoint.Path == "/api/admin/accounts/export" {
			require.True(t, endpoint.Sensitive)
		}
	}
	require.True(t, seen["GET /api/admin/accounts/reset-credits"])
	require.True(t, seen["POST /api/admin/accounts/reset-credits"])
}

func TestDualMethodManagementRouteUsesTheSameCPRPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/admin/accounts/reset-credits", r.URL.Path)
		require.Equal(t, "", r.URL.Fragment)
		_, _ = w.Write([]byte(`{"code":200,"data":{}}`))
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, AdminKey: "private"}, nil)
	require.NoError(t, err)
	for _, endpoint := range ManagementEndpoints() {
		if endpoint.Path == "/api/admin/accounts/reset-credits" {
			require.NoError(t, client.Admin(context.Background(), endpoint.Operation, nil, nil, nil))
		}
	}
}
