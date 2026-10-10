package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type accountDriverInvariantRepository struct {
	service.AdminAccountRepository
	accounts map[int64]*service.Account
	writes   int
}

func (r *accountDriverInvariantRepository) GetByID(_ context.Context, id int64) (*service.Account, error) {
	account, ok := r.accounts[id]
	if !ok {
		return nil, service.ErrAccountNotFound
	}
	return account, nil
}

func (r *accountDriverInvariantRepository) GetByIDs(_ context.Context, ids []int64) ([]*service.Account, error) {
	accounts := make([]*service.Account, 0, len(ids))
	for _, id := range ids {
		if account, ok := r.accounts[id]; ok {
			accounts = append(accounts, account)
		}
	}
	return accounts, nil
}

func (r *accountDriverInvariantRepository) ListShadowsByParent(context.Context, int64) ([]*service.Account, error) {
	return nil, nil
}

func (r *accountDriverInvariantRepository) BulkUpdate(context.Context, []int64, service.AccountBulkUpdate) (int64, error) {
	r.writes++
	return 2, nil
}

func (r *accountDriverInvariantRepository) UpdateWithAccountBillingSettings(_ context.Context, account *service.Account, _ *bool, _ *bool, _ *float64) error {
	r.writes++
	copy := *account
	r.accounts[account.ID] = &copy
	return nil
}

func TestAccountDriverHTTPWritesRejectInvalidMergedStateBeforeAnyMutation(t *testing.T) {
	// Use the real AdminService through Gin. Native is first in the selection:
	// rejecting the enhanced target must happen before any target is written.
	for _, tc := range []struct{ name, method, path, body, reason string }{
		{"bulk PAT", http.MethodPost, "/accounts/bulk-update", `{"account_ids":[1,2],"name":"must-not-change","credentials":{"auth_mode":"personal_access_token"}}`, "ACCOUNT_DRIVER_AUTH_MODE_UNSUPPORTED"},
		{"bulk Agent alias", http.MethodPost, "/accounts/bulk-update", `{"account_ids":[1,2],"name":"must-not-change","credentials":{"auth_mode":"agent_identity"}}`, "ACCOUNT_DRIVER_AUTH_MODE_UNSUPPORTED"},
		{"bulk legacy Agent", http.MethodPost, "/accounts/bulk-update", `{"account_ids":[1,2],"credentials":{"openai_auth_mode":"agentIdentity"}}`, "ACCOUNT_DRIVER_AUTH_MODE_UNSUPPORTED"},
		{"bulk boolean", http.MethodPost, "/accounts/bulk-update", `{"account_ids":[1,2],"credentials":{"auth_mode":true}}`, "ACCOUNT_DRIVER_AUTH_MODE_UNSUPPORTED"},
		{"bulk numeric Extra", http.MethodPost, "/accounts/bulk-update", `{"account_ids":[1,2],"name":"must-not-change","extra":{"auth_mode":12}}`, "ACCOUNT_DRIVER_AUTH_MODE_UNSUPPORTED"},
		{"bulk PAT Extra", http.MethodPost, "/accounts/bulk-update", `{"account_ids":[1,2],"extra":{"openai_auth_mode":"personalAccessToken"}}`, "ACCOUNT_DRIVER_AUTH_MODE_UNSUPPORTED"},
		{"PUT changes type", http.MethodPut, "/accounts/2", `{"name":"must-not-change","type":"apikey"}`, "ACCOUNT_DRIVER_ACCOUNT_UNSUPPORTED"},
		{"PUT Agent Extra", http.MethodPut, "/accounts/2", `{"name":"must-not-change","extra":{"auth_mode":"agent_identity"}}`, "ACCOUNT_DRIVER_AUTH_MODE_UNSUPPORTED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &accountDriverInvariantRepository{accounts: map[int64]*service.Account{
				1: {ID: 1, Name: "native", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: map[string]any{"access_token": "native-token"}},
				2: {ID: 2, Name: "enhanced", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, AccountDriverID: service.AccountDriverCodexHTTP, AccountDriverRevision: 1, Credentials: map[string]any{"access_token": "enhanced-token"}},
			}}
			svc := service.NewAdminService(nil, nil, nil, repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			router := accountDriverTestRouter(svc)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(rec, req)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			require.Contains(t, rec.Body.String(), tc.reason)
			require.Zero(t, repo.writes)
			require.Equal(t, "native", repo.accounts[1].Name)
			require.Equal(t, "enhanced", repo.accounts[2].Name)
			require.Equal(t, service.AccountTypeOAuth, repo.accounts[2].Type)
			require.Equal(t, map[string]any{"access_token": "native-token"}, repo.accounts[1].Credentials)
			require.Equal(t, map[string]any{"access_token": "enhanced-token"}, repo.accounts[2].Credentials)
			require.Nil(t, repo.accounts[1].Extra)
			require.Nil(t, repo.accounts[2].Extra)
		})
	}
}

// Keep this assertion in the HTTP test package: the panel must continue to use
// the ordinary edit endpoint when enhanced execution is stopped.
func TestAccountDriverHTTPAllowsStandardCredentialsWhileRuntimeUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &accountDriverInvariantRepository{accounts: map[int64]*service.Account{2: {
		ID: 2, Name: "enhanced", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive,
		AccountDriverID: service.AccountDriverCodexHTTP, AccountDriverRevision: 1, Credentials: map[string]any{"access_token": "old-token"},
	}}}
	svc := service.NewAdminService(nil, nil, nil, repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := accountDriverTestRouter(svc)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/accounts/2", bytes.NewBufferString(`{"name":"edited","credentials":{"auth_mode":"oauth","access_token":"new-token"}}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 1, repo.writes)
	require.Equal(t, service.AccountDriverCodexHTTP, repo.accounts[2].AccountDriverID)
	require.Equal(t, "new-token", repo.accounts[2].GetCredential("access_token"))
}

func TestAccountDriverHTTPImportWithoutDriverPreservesExistingEnhancedBinding(t *testing.T) {
	require.NoError(t, service.SetAccountDriverRuntimeAvailability(service.AccountDriverCodexHTTP, true, "test"))
	t.Cleanup(func() {
		_ = service.SetAccountDriverRuntimeAvailability(service.AccountDriverCodexHTTP, false, "unconfigured")
	})
	for _, mode := range []any{nil, "agent_identity", true} {
		incoming := buildCodexAccessOnlyImportValue(t, "import-workspace", "import-user")
		incoming["refresh_token"] = "incoming-refresh"
		if mode != nil {
			incoming["auth_mode"] = mode
		}
		existing := service.Account{ID: 7, Name: "existing", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, AccountDriverID: service.AccountDriverCodexHTTP, AccountDriverRevision: 3,
			Credentials: map[string]any{"access_token": "original-token", "refresh_token": "original-refresh", "chatgpt_account_id": "import-workspace", "chatgpt_user_id": "import-user"},
		}
		svc := newCodexImportMemoryAdminService([]service.Account{existing})
		h := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
		router := gin.New()
		router.POST("/import", h.ImportCodexSession)
		content, err := json.Marshal(incoming)
		require.NoError(t, err)
		body, err := json.Marshal(map[string]any{"content": string(content), "update_existing": true})
		require.NoError(t, err)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/import", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Empty(t, svc.createdAccounts)
		require.Equal(t, service.AccountDriverCodexHTTP, svc.accounts[0].AccountDriverID)
		require.Equal(t, int64(3), svc.accounts[0].AccountDriverRevision)
		if mode == nil {
			require.Len(t, svc.updatedAccounts, 1)
			require.Empty(t, svc.updatedAccounts[0].input.AccountDriverID, "omitted driver does not select native during an update")
		} else {
			require.Empty(t, svc.updatedAccounts)
			require.Equal(t, existing.Credentials, svc.accounts[0].Credentials)
			require.Contains(t, rec.Body.String(), "ACCOUNT_DRIVER_AUTH_MODE_UNSUPPORTED")
		}
	}
}
