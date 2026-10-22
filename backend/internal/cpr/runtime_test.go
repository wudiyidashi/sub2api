package cpr

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRuntimeChecksThePinnedVersionAndSource(t *testing.T) {
	for _, version := range []string{"v3.29.7", "3.29.7", "3.29.3", ""} {
		for _, sha := range []string{SourceCommit, SourceCommit[:8], "wrong-source"} {
			t.Run(version+"/"+sha, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					require.Equal(t, "/api/admin/system/version", r.URL.Path)
					require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]string{"version": version, "gitSha": sha}}))
				}))
				defer server.Close()
				client, err := New(Config{BaseURL: server.URL, AdminKey: "fixture"}, nil)
				require.NoError(t, err)
				_, err = client.CheckRuntime(context.Background())
				if (version == "v3.29.7" || version == "3.29.7") && sha != "wrong-source" {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}
			})
		}
	}
}
