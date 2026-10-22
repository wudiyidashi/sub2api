//go:build cprintegration

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 该验收连接隔离的真实 CPR 进程和模拟 Provider，不能指向生产凭据。
func TestCPRRuntimeIntegration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	path := os.Getenv("SUB2API_CPR_INTEGRATION_FILE")
	require.NotEmpty(t, path, "set SUB2API_CPR_INTEGRATION_FILE to the isolated fixture access file")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var access struct {
		BaseURL  string `json:"base_url"`
		AdminKey string `json:"admin_key"`
		GroupID  string `json:"account_group_id"`
	}
	require.NoError(t, json.Unmarshal(raw, &access))
	cfg := &config.Config{AccountDrivers: config.AccountDriversConfig{CPRManaged: config.CPRManagedConfig{Enabled: true, BaseURL: access.BaseURL, AdminAPIKey: access.AdminKey, DeploymentID: "real-integration", BridgeSecret: strings.Repeat("i", 32), AdminTimeoutSeconds: 30}}}
	pool := &Account{ID: 17, Name: "Isolated CPR integration", AccountDriverID: "cpr-managed", AccountDriverRevision: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, GroupIDs: []int64{5}, Extra: map[string]any{"cpr_pool": CPRPoolBinding{GroupID: 5, Provider: "openai", AccountGroupIDs: []string{access.GroupID}}}}
	repo := &cprTestAccounts{account: pool}
	pools := NewCPRPoolService(cprTestSettings{}, cfg)
	pools.accounts = repo
	defer pools.Stop()
	_, err = pools.client.CheckRuntime(context.Background())
	require.NoError(t, err)
	s := &OpenAIGatewayService{cfg: cfg, cprPools: pools, accountRepo: repo, settingService: NewSettingService(cprTestSettings{}, cfg)}
	for _, tc := range []struct{ name, body string }{
		{"responses-json", `{"model":"gpt-fixture","input":"hello","stream":false}`},
		{"responses-sse", `{"model":"gpt-fixture","input":"hello","stream":true}`},
		{"chat-completions", `{"model":"gpt-fixture","messages":[{"role":"user","content":"hello"}],"stream":false}`},
		{"anthropic-messages", `{"model":"gpt-fixture","messages":[{"role":"user","content":"hello"}],"max_tokens":100,"stream":false}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, recorder := cprTestContext(tc.body)
			var result *OpenAIForwardResult
			var err error
			switch tc.name {
			case "chat-completions":
				result, err = s.ForwardAsChatCompletions(context.Background(), c, pool, []byte(tc.body), "", "")
			case "anthropic-messages":
				result, err = s.ForwardAsAnthropic(context.Background(), c, pool, []byte(tc.body), "", "")
			default:
				result, err = s.Forward(context.Background(), c, pool, []byte(tc.body))
			}
			require.NoError(t, err)
			require.Equal(t, 200, recorder.Code)
			require.NotNil(t, result)
			require.Equal(t, 12, result.Usage.InputTokens)
			require.Equal(t, 4, result.Usage.OutputTokens)
			require.Equal(t, 3, result.Usage.CacheReadInputTokens)
			require.Contains(t, recorder.Body.String(), "fixture reply")
		})
	}
	t.Run("scoped-model-discovery", func(t *testing.T) {
		c, _ := cprTestContext("")
		ids, managed, err := s.CPRModelIDs(context.Background(), c, PlatformOpenAI)
		require.NoError(t, err)
		require.True(t, managed)
		require.Contains(t, ids, "gpt-fixture")
	})
	t.Run("websocket-billing-and-revocation", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		turns := make(chan *OpenAIForwardResult, 4)
		done := make(chan error, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				done <- err
				return
			}
			defer func() { _ = conn.CloseNow() }()
			_, first, err := conn.Read(ctx)
			if err != nil {
				done <- err
				return
			}
			c, _ := cprTestContext("")
			c.Request = r
			done <- s.ProxyResponsesWebSocketFromClient(ctx, c, conn, pool, "placeholder", first, &OpenAIWSIngressHooks{AfterTurn: func(_ int, result *OpenAIForwardResult, err error) {
				if err == nil {
					turns <- result
				}
			}})
		}))
		defer server.Close()
		conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
		require.NoError(t, err)
		defer func() { _ = conn.CloseNow() }()
		require.NoError(t, conn.Write(ctx, websocket.MessageText, []byte(`{"type":"response.create","model":"gpt-fixture","input":"hello","store":true}`)))
		for {
			_, message, err := conn.Read(ctx)
			require.NoError(t, err)
			eventType := gjson.GetBytes(message, "type").String()
			require.NotEqual(t, "error", eventType, "isolated CPR error: %s", string(message))
			if eventType == "response.completed" {
				break
			}
		}
		var result *OpenAIForwardResult
		select {
		case result = <-turns:
		case <-ctx.Done():
			t.Fatal("websocket usage was not reported")
		}
		require.NotNil(t, result)
		require.Equal(t, 12, result.Usage.InputTokens)
		require.Equal(t, 4, result.Usage.OutputTokens)
		pool.Schedulable = false
		require.NoError(t, conn.Write(ctx, websocket.MessageText, []byte(`{"type":"response.create","model":"gpt-fixture","input":"blocked","store":true}`)))
		select {
		case err := <-done:
			require.Error(t, err)
		case <-ctx.Done():
			t.Fatal("revoked websocket authorization was not rejected")
		}
		require.Empty(t, turns)
	})
}
