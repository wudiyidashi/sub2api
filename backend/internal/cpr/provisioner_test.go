package cpr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

type bindingStoreStub struct {
	mu    sync.Mutex
	value string
	fail  bool
}

func (s *bindingStoreStub) Set(_ context.Context, _, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return errors.New("storage unavailable")
	}
	s.value = value
	return nil
}

func TestProvisioningRecoversInterruptedCreationWithoutSecondIdentity(t *testing.T) {
	key, err := (Scope{
		Deployment: "prod", APIKeyID: 1, GroupID: 2, Provider: "openai", AccountGroupIDs: []string{"pool-a"},
	}).Key([]byte(strings.Repeat("s", 32)))
	require.NoError(t, err)
	var created atomic.Int32
	var groups = key.Groups()
	var enabled = true
	var remoteSecret = key.Secret()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		write := func(data any) {
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": data}))
		}
		switch r.URL.Path {
		case string(ListClientKeys):
			if created.Load() == 0 {
				write(map[string]any{"items": []any{}})
				return
			}
			groupViews := []map[string]string{}
			for _, id := range groups {
				groupViews = append(groupViews, map[string]string{"id": id})
			}
			write(map[string]any{"items": []any{map[string]any{"id": "remote-key", "name": key.Name(), "enabled": enabled, "groups": groupViews}}})
		case string(CreateClientKey):
			created.Add(1)
			var input struct {
				CustomKey      string   `json:"customKey"`
				GroupIDs       []string `json:"groupIds"`
				DailyLimitUSD  *string  `json:"dailyLimitUsd"`
				WeeklyLimitUSD *string  `json:"weeklyLimitUsd"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
			require.Equal(t, key.Secret(), input.CustomKey)
			require.Equal(t, key.Groups(), input.GroupIDs)
			require.Nil(t, input.DailyLimitUSD)
			require.Nil(t, input.WeeklyLimitUSD)
			write(map[string]any{"id": "remote-key", "plaintextKey": key.Secret()})
		case string(RevealClientKey):
			require.Equal(t, "remote-key", r.URL.Query().Get("id"))
			write(map[string]string{"id": "remote-key", "plaintextKey": remoteSecret})
		default:
			t.Errorf("unexpected operation %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, AdminKey: "admin"}, nil)
	require.NoError(t, err)
	store := &bindingStoreStub{fail: true}
	require.Error(t, NewProvisioner(client, store).Ensure(context.Background(), key))
	require.EqualValues(t, 1, created.Load())
	store.fail = false
	// 模拟本地持久化失败后重启，远端现有身份通过配套权限和真实 Key 复验后恢复。
	require.NoError(t, NewProvisioner(client, store).Ensure(context.Background(), key))
	require.EqualValues(t, 1, created.Load())
	require.Contains(t, store.value, "remote-key")
	require.NotContains(t, store.value, key.Secret())
	require.NotContains(t, fmt.Sprintf("%+v %#v", key, key), key.Secret())
	for _, mutate := range []func(){
		func() { groups = []string{} },
		func() { groups = []string{"pool-other"} },
		func() { groups = key.Groups(); enabled = false },
		func() { enabled = true; remoteSecret = "another-private-key" },
	} {
		mutate()
		err := NewProvisioner(client, store).Ensure(context.Background(), key)
		require.Error(t, err)
		require.NotContains(t, err.Error(), "another-private-key")
		require.EqualValues(t, 1, created.Load())
	}
}

func TestScopedKeyCannotBeExpandedByMutatingReturnedGroups(t *testing.T) {
	source := []string{"pool-a"}
	key, err := (Scope{Deployment: "prod", APIKeyID: 1, GroupID: 1, Provider: "openai", AccountGroupIDs: source}).Key([]byte(strings.Repeat("s", 32)))
	require.NoError(t, err)
	source[0] = "all"
	copy := key.Groups()
	copy[0] = "all"
	require.Equal(t, []string{"pool-a"}, key.Groups())
}
