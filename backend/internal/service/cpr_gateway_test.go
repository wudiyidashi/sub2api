package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/accountdriver"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/cpr"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type cprTestSettings struct{ SettingRepository }

func (cprTestSettings) Set(context.Context, string, string) error { return nil }
func (cprTestSettings) GetValue(context.Context, string) (string, error) {
	return "", ErrSettingNotFound
}
func (cprTestSettings) Get(context.Context, string) (*Setting, error) { return nil, ErrSettingNotFound }
func (cprTestSettings) GetAll(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}
func (cprTestSettings) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}

type cprTestAccounts struct {
	AccountRepository
	account *Account
}

func (r *cprTestAccounts) GetByID(context.Context, int64) (*Account, error) {
	copy := *r.account
	return &copy, nil
}
func (r *cprTestAccounts) ListByGroup(context.Context, int64) ([]Account, error) {
	return []Account{*r.account}, nil
}

func cprGatewayFixture(t *testing.T, execute http.HandlerFunc) (*OpenAIGatewayService, *Account, *cprTestAccounts) {
	t.Helper()
	var secret string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case string(cpr.ListClientKeys):
			_, _ = io.WriteString(w, `{"code":200,"data":{"items":[]}}`)
		case string(cpr.CreateClientKey):
			var input struct {
				CustomKey string   `json:"customKey"`
				GroupIDs  []string `json:"groupIds"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
			require.Equal(t, []string{"pool-a"}, input.GroupIDs)
			secret = input.CustomKey
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]string{"id": "internal-one", "plaintextKey": secret}}))
		default:
			require.Equal(t, "Bearer "+secret, r.Header.Get("Authorization"))
			require.Empty(t, r.Header.Get("Cookie"))
			require.Empty(t, r.Header.Get("X-Api-Key"))
			execute(w, r)
		}
	}))
	t.Cleanup(server.Close)
	cfg := &config.Config{AccountDrivers: config.AccountDriversConfig{CPRManaged: config.CPRManagedConfig{Enabled: true, BaseURL: server.URL, AdminAPIKey: "private-admin", DeploymentID: "fixture", BridgeSecret: strings.Repeat("s", 32), AdminTimeoutSeconds: 10}}}
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	pool := &Account{ID: 17, Name: "Business pool", AccountDriverID: accountdriver.CPRManagedID, AccountDriverRevision: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, GroupIDs: []int64{5}, Extra: map[string]any{"cpr_pool": CPRPoolBinding{GroupID: 5, Provider: "openai", AccountGroupIDs: []string{"pool-a"}}}}
	repo := &cprTestAccounts{account: pool}
	pools := NewCPRPoolService(cprTestSettings{}, cfg)
	pools.accounts = repo
	t.Cleanup(pools.Stop)
	return &OpenAIGatewayService{cfg: cfg, cprPools: pools}, pool, repo
}

func cprTestContext(body string) (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	c.Request.Header.Set("Authorization", "Bearer user-business-key")
	c.Request.Header.Set("Cookie", "user-cookie=private")
	groupID := int64(5)
	c.Set("api_key", &APIKey{ID: 3, GroupID: &groupID})
	return c, recorder
}

func TestCPRResponsesPreservePayloadAndReturnBillingUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := `{"model":"gpt-test","input":"hello","client_metadata":{"timezone":"client-zone"},"prompt_cache_key":"session-one"}`
	var calls atomic.Int32
	s, pool, _ := cprGatewayFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.Equal(t, "/v1/responses", r.URL.Path)
		raw, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.Equal(t, body, string(raw))
		_, _ = io.WriteString(w, `{"id":"resp_fixture","object":"response","model":"gpt-test","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":12,"output_tokens":4,"input_tokens_details":{"cached_tokens":3}}}`)
	})
	c, recorder := cprTestContext(body)
	result, err := s.Forward(context.Background(), c, pool, []byte(body))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.EqualValues(t, 12, result.Usage.InputTokens)
	require.EqualValues(t, 4, result.Usage.OutputTokens)
	require.EqualValues(t, 3, result.Usage.CacheReadInputTokens)
	require.Equal(t, "resp_fixture", result.ResponseID)
	require.Equal(t, 200, recorder.Code)
	require.EqualValues(t, 1, calls.Load())
	require.Empty(t, pool.Credentials)
}

func TestCPRPoolChecksLiveAuthorizationAndNeverReplays(t *testing.T) {
	s, pool, repo := cprGatewayFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		_, _ = io.WriteString(w, `{"error":{"message":"capacity unavailable"}}`)
	})
	pool.Extra[openAILongContextBillingEnabledKey] = false
	ctx, prepared, err := s.cprPools.Prepare(context.Background(), pool, 3, 5)
	require.NoError(t, err)
	defer require.Empty(t, pool.Credentials)
	binding, ok := cprBindingFromContext(ctx)
	require.True(t, ok)
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://placeholder/v1/responses", strings.NewReader(`{}`))
		require.NoError(t, err)
		resp, err := executeCPRPreparedRequest(req, prepared, binding)
		if attempt == 0 {
			require.NoError(t, err)
			_ = resp.Body.Close()
		} else {
			require.ErrorContains(t, err, "host replay is disabled")
		}
	}
	_, _, err = s.cprPools.Prepare(context.Background(), pool, 3, 6)
	require.Error(t, err)
	repo.account.Schedulable = false
	_, _, err = s.cprPools.Prepare(context.Background(), pool, 3, 5)
	require.Error(t, err)
}

func TestCPRFailuresDoNotInvokeNativeAccountMutationOrFailover(t *testing.T) {
	s, pool, _ := cprGatewayFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		_, _ = io.WriteString(w, `{"error":{"message":"capacity unavailable","type":"rate_limit_error"}}`)
	})
	// repo 的状态写方法没有实现；进入原生副作用将直接导致本测试 panic。
	s.accountRepo = s.cprPools.accounts
	c, recorder := cprTestContext(`{}`)
	result, err := s.Forward(context.Background(), c, pool, []byte(`{"model":"gpt-test","input":"hello"}`))
	require.Nil(t, result)
	require.Error(t, err)
	var failover *UpstreamFailoverError
	require.NotErrorAs(t, err, &failover)
	require.Equal(t, 429, recorder.Code)
}

func TestCPRGrokImagesUseOnlyTheCPRTransport(t *testing.T) {
	s, pool, _ := cprGatewayFixture(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/images/generations", r.URL.Path)
		_, _ = io.WriteString(w, `{"created":1,"data":[{"b64_json":"fixture-image"}]}`)
	})
	pool.Platform = PlatformGrok
	pool.Extra["cpr_pool"] = CPRPoolBinding{GroupID: 5, Provider: "xai", AccountGroupIDs: []string{"pool-a"}}
	c, recorder := cprTestContext(`{}`)
	result, err := s.ForwardGrokMedia(context.Background(), c, pool, GrokMediaEndpointImagesGenerations, "", []byte(`{"model":"grok-imagine-image","prompt":"fixture"}`), "application/json")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, result.ImageCount)
	require.Equal(t, 200, recorder.Code)
	require.Empty(t, pool.Credentials)
}
