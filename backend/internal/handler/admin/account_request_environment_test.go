package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type requestEnvironmentAdminStub struct {
	service.AdminService
	account *service.Account
}

func (s *requestEnvironmentAdminStub) GetAccount(context.Context, int64) (*service.Account, error) {
	return s.account, nil
}
func (s *requestEnvironmentAdminStub) GetProxy(context.Context, int64) (*service.Proxy, error) {
	return &service.Proxy{ID: 9, Protocol: "socks5", Host: "preview.test", Port: 1080}, nil
}

type requestEnvironmentProbeStub struct {
	route string
	fail  bool
}

func (p *requestEnvironmentProbeStub) ProbeRequestEnvironment(_ context.Context, route string) (*service.CodexRequestEnvironment, error) {
	p.route = route
	if p.fail {
		return nil, errors.New("proxy-password-must-not-leak")
	}
	return &service.CodexRequestEnvironment{IP: "1.2.3.4", CountryCode: "US", Timezone: "America/Chicago"}, nil
}

func TestAccountRequestEnvironmentPreviewAndValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	id := int64(4)
	account := &service.Account{ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, ProxyID: &id}
	probe := &requestEnvironmentProbeStub{}
	h := &AccountHandler{adminService: &requestEnvironmentAdminStub{account: account}, requestEnvironments: service.NewAccountRequestEnvironmentService(nil, probe)}
	router := gin.New()
	router.GET("/accounts/:id/request-environment", h.GetRequestEnvironment)
	for _, tc := range []struct {
		path   string
		status int
	}{
		{"/accounts/invalid/request-environment", http.StatusBadRequest},
		{"/accounts/1/request-environment?proxy_id=-1", http.StatusBadRequest},
		{"/accounts/1/request-environment?proxy_id=abc", http.StatusBadRequest},
		{"/accounts/1/request-environment?proxy_id=9", http.StatusOK},
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		require.Equal(t, tc.status, rec.Code, rec.Body.String())
	}
	require.Equal(t, "socks5://preview.test:1080", probe.route)
	require.Equal(t, int64(4), *account.ProxyID)
	require.Nil(t, account.Proxy)
	probe.fail = true
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/accounts/1/request-environment?proxy_id=9&refresh=1", nil))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.NotContains(t, rec.Body.String(), "proxy-password")
}
