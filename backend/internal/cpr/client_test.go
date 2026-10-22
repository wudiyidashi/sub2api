package cpr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestManagementContractAndErrorRedaction(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.Equal(t, "/api/admin/accounts", r.URL.Path)
		require.Equal(t, "openai", r.URL.Query().Get("provider"))
		require.Equal(t, "admin-private", r.Header.Get("x-api-key"))
		require.NotEmpty(t, r.Header.Get("X-Request-ID"))
		require.Empty(t, r.Header.Get("Authorization"))
		if calls.Load() == 1 {
			_, _ = io.WriteString(w, `{"code":200,"message":"OK","data":{"items":[{"id":"cpr-account","provider":"openai"}]}}`)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"code":40001,"message":"sensitive-credential-marker","data":null}`)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, AdminKey: "admin-private"}, nil)
	require.NoError(t, err)
	var result struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	query := url.Values{"provider": {"openai"}}
	require.NoError(t, client.Admin(context.Background(), ListAccounts, query, nil, &result))
	require.Equal(t, "cpr-account", result.Items[0].ID)
	err = client.Admin(context.Background(), ListAccounts, query, nil, &result)
	var remoteError *Error
	require.ErrorAs(t, err, &remoteError)
	require.Equal(t, uint32(40001), remoteError.Code)
	require.NotContains(t, err.Error(), "sensitive")
	require.Error(t, client.Admin(context.Background(), Operation("https://other.test/"), nil, nil, nil))
	require.EqualValues(t, 2, calls.Load())
}

func TestNoRedirectAndNoExecutionReplay(t *testing.T) {
	var leaked atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked.Add(1)
	}))
	defer other.Close()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, AdminKey: "private"}, nil)
	require.NoError(t, err)
	require.Error(t, client.Admin(context.Background(), ListAccounts, nil, nil, nil))
	response, err := client.Execute(context.Background(), "/v1/responses", "scope-private", nil, strings.NewReader("{}"))
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, http.StatusTemporaryRedirect, response.StatusCode)
	require.EqualValues(t, 2, calls.Load())
	require.Zero(t, leaked.Load())
}

func TestExecutionKeepsPayloadAndLetsCPROwnUpstreamIdentity(t *testing.T) {
	raw := ` {"input":[{"role":"user","content":"hello"}],"unknown":1.0000} `
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/responses", r.URL.Path)
		require.Equal(t, "Bearer internal-key", r.Header.Get("Authorization"))
		for _, name := range []string{"x-api-key", "Cookie", "Proxy-Authorization", "originator", "version", "x-codex-installation-id"} {
			require.Empty(t, r.Header.Get(name), name)
		}
		require.NotEqual(t, "private-browser-agent", r.Header.Get("User-Agent"))
		require.Equal(t, "session-one", r.Header.Get("Session-Id"))
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.Equal(t, raw, string(body))
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"unknown\":true}\n\n")
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, AdminKey: "admin-private"}, nil)
	require.NoError(t, err)
	headers := http.Header{}
	for _, name := range []string{"Authorization", "x-api-key", "Cookie", "Proxy-Authorization", "originator", "version", "x-codex-installation-id"} {
		headers.Set(name, "must-not-pass")
	}
	headers.Set("User-Agent", "private-browser-agent")
	headers.Set("Session-Id", "session-one")
	response, err := client.Execute(context.Background(), "/v1/responses", "internal-key", headers, strings.NewReader(raw))
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), "response.completed")
	require.Equal(t, "private-browser-agent", headers.Get("User-Agent"))
}

func TestScopeIsolationAndEmptyPoolFailClosed(t *testing.T) {
	secret := []byte(strings.Repeat("s", 32))
	scope := Scope{Deployment: "prod", APIKeyID: 7, GroupID: 2, Provider: "openai", AccountGroupIDs: []string{"pool-b", "pool-a"}}
	first, err := scope.Key(secret)
	require.NoError(t, err)
	scope.AccountGroupIDs = []string{"pool-a", "pool-b", "pool-a"}
	again, err := scope.Key(secret)
	require.NoError(t, err)
	require.Equal(t, first.Secret(), again.Secret())
	for _, change := range []func(*Scope){
		func(s *Scope) { s.APIKeyID++ },
		func(s *Scope) { s.GroupID++ },
		func(s *Scope) { s.Deployment = "another-prod" },
		func(s *Scope) { s.Provider = "xai" },
		func(s *Scope) { s.AccountGroupIDs = []string{"pool-a"} },
	} {
		next := scope
		change(&next)
		key, err := next.Key(secret)
		require.NoError(t, err)
		require.NotEqual(t, first.Secret(), key.Secret())
	}
	scope.AccountGroupIDs = nil
	_, err = scope.Key(secret)
	require.Error(t, err)
	require.NotContains(t, fmt.Sprintf("%v %#v", first, first), first.Secret())
	encoded, err := json.Marshal(first)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), first.Secret())
}

func TestManagementCancellationAndInvalidOrigin(t *testing.T) {
	for _, base := range []string{"http://u:secret@cpr.test", "https://cpr.test/prefix", "https://cpr.test?token=secret", "file:///tmp/cpr"} {
		_, err := New(Config{BaseURL: base, AdminKey: "private"}, nil)
		require.Error(t, err)
		require.NotContains(t, err.Error(), "secret")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, AdminKey: "private"}, nil)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, client.Admin(ctx, ListAccounts, nil, nil, nil), context.DeadlineExceeded)
}
