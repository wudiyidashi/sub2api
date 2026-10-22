package admin

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/cpr"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCPRManagementPreservesHostEnvelopeAndDoesNotExposeInternalAuthFailures(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.Equal(t, "admin-private", r.Header.Get("x-api-key"))
		require.Empty(t, r.URL.Query().Get("timezone"))
		if calls.Load() > 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"code":40102,"message":"private-key-echo","data":null}`)
			return
		}
		_, _ = io.WriteString(w, `{"code":200,"message":"OK","data":{"items":[{"id":"remote-one"}]}}`)
	}))
	defer server.Close()
	h := NewCPRHandler(&config.Config{AccountDrivers: config.AccountDriversConfig{CPRManaged: config.CPRManagedConfig{
		Enabled: true, BaseURL: server.URL, AdminAPIKey: "admin-private",
		DeploymentID: "test", BridgeSecret: strings.Repeat("s", 32), AdminTimeoutSeconds: 10,
	}}}, nil, nil)
	router := gin.New()
	router.GET("/accounts", h.Operation(cpr.ListAccounts))
	router.POST("/import", h.Operation(cpr.ImportAccounts))
	read := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}
	rec := read("/accounts?timezone=Asia%2FShanghai")
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"code":0,"message":"success","data":{"items":[{"id":"remote-one"}]}}`, rec.Body.String())
	rec = read("/accounts")
	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.NotContains(t, rec.Body.String(), "private")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/import", strings.NewReader(`{"provider":"anthropic","data":{}}`)))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.EqualValues(t, 2, calls.Load())
}

func TestCPRManagementDisabledIsExplicitAndDoesNotFallBack(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewCPRHandler(nil, nil, nil)
	router := gin.New()
	router.GET("/status", h.Status)
	router.GET("/accounts", h.Operation(cpr.ListAccounts))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"enabled":false`)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/accounts", nil))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestCPRResourcesKeepRawContentAndRejectHTML(t *testing.T) {
	gin.SetMode(gin.TestMode)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/admin/diagnostics/export":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"fixture":"diagnostics"}`)
		case "/api/admin/accounts/connection-test":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: completed\ndata: {\"ok\":true}\n\n")
		default:
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, "<script>private</script>")
		}
	}))
	defer server.Close()
	h := NewCPRHandler(&config.Config{AccountDrivers: config.AccountDriversConfig{CPRManaged: config.CPRManagedConfig{Enabled: true, BaseURL: server.URL, AdminAPIKey: "private-admin", DeploymentID: "fixture", BridgeSecret: strings.Repeat("s", 32), AdminTimeoutSeconds: 10}}}, nil, nil)
	router := gin.New()
	for _, endpoint := range cpr.ManagementEndpoints() {
		if endpoint.Operation.Kind() != "json" {
			router.Handle(endpoint.Method, endpoint.Path, h.Operation(endpoint.Operation))
		}
	}
	for _, tc := range []struct {
		path    string
		status  int
		content string
	}{
		{"/api/admin/diagnostics/export", 200, `{"fixture":"diagnostics"}`},
		{"/api/admin/accounts/connection-test", 200, "event: completed"},
		{"/api/admin/accounts/profile-avatar", 502, "CPR management resource failed"},
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		require.Equal(t, tc.status, rec.Code)
		require.Contains(t, rec.Body.String(), tc.content)
		require.NotContains(t, rec.Body.String(), "private")
		if tc.status == 200 {
			require.Equal(t, "private, no-store", rec.Header().Get("Cache-Control"))
		}
	}
}

func TestCPRImportTasksValidateEveryProviderBeforeForwarding(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"code":200,"data":{"taskId":"fixture"}}`)
	}))
	defer server.Close()
	h := NewCPRHandler(&config.Config{AccountDrivers: config.AccountDriversConfig{CPRManaged: config.CPRManagedConfig{Enabled: true, BaseURL: server.URL, AdminAPIKey: "fixture", DeploymentID: "fixture", BridgeSecret: strings.Repeat("s", 32), AdminTimeoutSeconds: 10}}}, nil, nil)
	router := gin.New()
	router.POST("/tasks", h.Operation(cpr.Operation("/api/admin/accounts/import-tasks#POST")))
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"items":[{"provider":"openai","data":{}},{"provider":"xai","data":{}}]}`, 200},
		{`{"items":[{"provider":"openai","data":{}},{"provider":"anthropic","data":{}}]}`, 400},
		{`{"items":[]}`, 400},
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/tasks", strings.NewReader(tc.body)))
		require.Equal(t, tc.status, rec.Code)
	}
	require.EqualValues(t, 1, calls.Load())
}
