package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/accountdriver"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func accountDriverTestRouter(svc service.AdminService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	h := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router.GET("/account-drivers", h.ListDrivers)
	router.GET("/accounts", h.List)
	router.GET("/accounts/upstream-billing-rates", h.GetUpstreamBillingRates)
	router.GET("/accounts/data", h.ExportData)
	router.POST("/accounts", h.Create)
	router.PUT("/accounts/:id", h.Update)
	router.POST("/accounts/bulk-update", h.BulkUpdate)
	router.POST("/accounts/data", h.ImportData)
	router.POST("/accounts/import/codex-session", h.ImportCodexSession)
	return router
}

func TestAccountDriverHandlerCatalogContainsOnlyReadyNative(t *testing.T) {
	router := accountDriverTestRouter(nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/account-drivers", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var payload struct {
		Data accountdriver.Catalog `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Equal(t, "native", payload.Data.DefaultDriverID)
	require.Len(t, payload.Data.Drivers, 1)
	require.Equal(t, "native", payload.Data.Drivers[0].ID)
	require.True(t, payload.Data.Drivers[0].Available)
	require.False(t, payload.Data.Drivers[0].Capabilities.Switch)
}

func TestAccountDriverHandlerFiltersRejectUnknownBeforeLoadingAccounts(t *testing.T) {
	// No service is wired: invalid filters must fail before any read/export.
	router := accountDriverTestRouter(nil)
	for _, path := range []string{"/accounts", "/accounts/upstream-billing-rates", "/accounts/data"} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path+"?account_driver_id=codex-enhanced", nil))
			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Contains(t, rec.Body.String(), "ACCOUNT_DRIVER_UNKNOWN")
		})
	}
}

func TestAccountDriverHandlerNativeFilterPreservesExistingAccounts(t *testing.T) {
	svc := newStubAdminService()
	svc.accounts = []service.Account{{ID: 1, Name: "old", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth}}
	router := accountDriverTestRouter(svc)
	for _, query := range []string{"", "?account_driver_id=native", "?account_driver_id=native&lite=1"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/accounts"+query, nil))
		require.Equal(t, http.StatusOK, rec.Code)
		var payload struct {
			Data struct {
				Total int `json:"total"`
				Items []struct {
					AccountDriver accountdriver.AccountView `json:"account_driver"`
				} `json:"items"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
		require.Equal(t, 1, payload.Data.Total)
		require.Len(t, payload.Data.Items, 1)
		require.Equal(t, "native", payload.Data.Items[0].AccountDriver.ID)
		require.Zero(t, payload.Data.Items[0].AccountDriver.BindingRevision)
	}
}

func TestAccountDriverHandlerRejectsUnsupportedCreateUpdateAndBulk(t *testing.T) {
	svc := newStubAdminService()
	router := accountDriverTestRouter(svc)
	for _, tc := range []struct {
		method, path, body, reason string
	}{
		{http.MethodPost, "/accounts", `{"name":"new","platform":"openai","type":"oauth","credentials":{"access_token":"secret"},"account_driver_id":"missing"}`, "ACCOUNT_DRIVER_UNKNOWN"},
		{http.MethodPost, "/accounts", `{"name":"new","platform":"openai","type":"oauth","credentials":{"access_token":"secret"},"driver_settings":{"token":"must-not-persist"}}`, "ACCOUNT_DRIVER_SETTINGS_UNSUPPORTED"},
		{http.MethodPut, "/accounts/1", `{"name":"edit","account_driver_id":"missing"}`, "ACCOUNT_DRIVER_UNKNOWN"},
		{http.MethodPut, "/accounts/1", `{"driver_settings":{"mode":"enhanced"}}`, "ACCOUNT_DRIVER_SETTINGS_UNSUPPORTED"},
		{http.MethodPost, "/accounts/bulk-update", `{"filters":{"account_driver_id":"missing"},"status":"inactive"}`, "ACCOUNT_DRIVER_UNKNOWN"},
	} {
		t.Run(tc.method+tc.path+tc.reason, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(rec, req)
			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Contains(t, rec.Body.String(), tc.reason)
		})
	}
	require.Empty(t, svc.createdAccounts)
	require.Zero(t, svc.updateAccountCalls)
}

func TestAccountDriverHandlerPassesExplicitNativeChoiceToService(t *testing.T) {
	svc := newStubAdminService()
	router := accountDriverTestRouter(svc)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/accounts", bytes.NewBufferString(`{"name":"new","platform":"openai","type":"apikey","credentials":{"api_key":"secret"},"account_driver_id":"native","driver_settings":{}}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Len(t, svc.createdAccounts, 1)
	require.Equal(t, "native", svc.createdAccounts[0].AccountDriverID)
	require.Empty(t, svc.createdAccounts[0].DriverSettings)
}

func TestAccountDriverHandlerUnavailableBindingRemainsReadableAndFiltered(t *testing.T) {
	svc := newStubAdminService()
	svc.accounts = []service.Account{
		{ID: 1, Name: "old-native", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth},
		{ID: 2, Name: "enhanced", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, AccountDriverID: service.AccountDriverCodexHTTP, AccountDriverRevision: 4},
	}
	router := accountDriverTestRouter(svc)
	for _, id := range []string{"native", service.AccountDriverCodexHTTP} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/accounts?lite=1&account_driver_id="+id, nil))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var payload struct {
			Data struct {
				Items []struct {
					AccountDriver accountdriver.AccountView `json:"account_driver"`
				} `json:"items"`
				Total int `json:"total"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
		require.Equal(t, 1, payload.Data.Total)
		require.Len(t, payload.Data.Items, 1)
		require.Equal(t, id, payload.Data.Items[0].AccountDriver.ID)
		if id == service.AccountDriverCodexHTTP {
			require.False(t, payload.Data.Items[0].AccountDriver.Available)
			require.Equal(t, int64(4), payload.Data.Items[0].AccountDriver.BindingRevision)
		}
	}
	// The emitted binding metadata participates in the list ETag even though
	// an availability transition does not update the account row timestamp.
	before := httptest.NewRecorder()
	router.ServeHTTP(before, httptest.NewRequest(http.MethodGet, "/accounts", nil))
	require.NoError(t, service.SetAccountDriverRuntimeAvailability(service.AccountDriverCodexHTTP, true, "ready-version"))
	t.Cleanup(func() {
		_ = service.SetAccountDriverRuntimeAvailability(service.AccountDriverCodexHTTP, false, "unconfigured")
	})
	after := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/accounts", nil)
	req.Header.Set("If-None-Match", before.Header().Get("ETag"))
	router.ServeHTTP(after, req)
	require.Equal(t, http.StatusOK, after.Code)
	require.NotEqual(t, before.Header().Get("ETag"), after.Header().Get("ETag"))
}

func TestAccountDriverDataExportImportPreservesExplicitBindingAndFailsClosed(t *testing.T) {
	svc := newStubAdminService()
	svc.accounts = []service.Account{{ID: 2, Name: "enhanced", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		AccountDriverID: service.AccountDriverCodexHTTP, Credentials: map[string]any{"access_token": "host-access"},
	}}
	router := accountDriverTestRouter(svc)
	exported := httptest.NewRecorder()
	router.ServeHTTP(exported, httptest.NewRequest(http.MethodGet, "/accounts/data?include_proxies=false&account_driver_id="+service.AccountDriverCodexHTTP, nil))
	require.Equal(t, http.StatusOK, exported.Code)
	var data struct {
		Data DataPayload `json:"data"`
	}
	require.NoError(t, json.Unmarshal(exported.Body.Bytes(), &data))
	require.Len(t, data.Data.Accounts, 1)
	require.Equal(t, service.AccountDriverCodexHTTP, data.Data.Accounts[0].AccountDriverID)
	body, err := json.Marshal(DataImportRequest{Data: data.Data})
	require.NoError(t, err)
	for _, ready := range []bool{false, true} {
		require.NoError(t, service.SetAccountDriverRuntimeAvailability(service.AccountDriverCodexHTTP, ready, "test"))
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/accounts/data", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		if !ready {
			require.Empty(t, svc.createdAccounts)
			require.Contains(t, rec.Body.String(), "ACCOUNT_DRIVER_UNAVAILABLE")
		} else {
			require.Len(t, svc.createdAccounts, 1)
			require.Equal(t, service.AccountDriverCodexHTTP, svc.createdAccounts[0].AccountDriverID)
		}
	}
	t.Cleanup(func() {
		_ = service.SetAccountDriverRuntimeAvailability(service.AccountDriverCodexHTTP, false, "unconfigured")
	})
}

func TestAccountDriverCodexImportRejectsSwitchOnMatchedAccount(t *testing.T) {
	require.NoError(t, service.SetAccountDriverRuntimeAvailability(service.AccountDriverCodexHTTP, true, "test"))
	t.Cleanup(func() {
		_ = service.SetAccountDriverRuntimeAvailability(service.AccountDriverCodexHTTP, false, "unconfigured")
	})
	value := buildCodexAccessOnlyImportValue(t, "driver-workspace", "driver-user")
	svc := newCodexImportMemoryAdminService(nil)
	h := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	created, err := h.importCodexSessions(context.Background(), CodexSessionImportRequest{SkipDefaultGroupBind: boolPtr(true)}, []codexImportEntry{{Index: 1, Value: value}})
	require.NoError(t, err)
	require.Equal(t, 1, created.Created)
	result, err := h.importCodexSessions(context.Background(), CodexSessionImportRequest{AccountDriverID: service.AccountDriverCodexHTTP, UpdateExisting: boolPtr(true)}, []codexImportEntry{{Index: 1, Value: value}})
	require.NoError(t, err)
	require.Equal(t, 1, result.Failed)
	require.Zero(t, result.Updated)
	require.Contains(t, result.Errors[0].Message, "ACCOUNT_DRIVER_SWITCH_UNSUPPORTED")
}
