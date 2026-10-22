package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCPRManagementRoutesRequireHostAdminAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	h := &handler.Handlers{Admin: &handler.AdminHandlers{CPR: admin.NewCPRHandler(nil, nil, nil)}}
	auth := middleware.AdminAuthMiddleware(func(c *gin.Context) {
		switch c.GetHeader("Authorization") {
		case "Bearer admin":
			c.Next()
		case "":
			c.AbortWithStatus(http.StatusUnauthorized)
		default:
			c.AbortWithStatus(http.StatusForbidden)
		}
	})
	RegisterAdminRoutes(router.Group("/api/v1"), h, auth, func(c *gin.Context) { c.Next() }, func(c *gin.Context) { c.Next() }, nil, nil)
	for _, path := range []string{"/status", "/accounts", "/account-groups"} {
		for _, tc := range []struct {
			auth   string
			status int
		}{
			{"", http.StatusUnauthorized}, {"Bearer user", http.StatusForbidden}, {"Bearer admin", http.StatusOK},
		} {
			if path != "/status" && tc.auth == "Bearer admin" {
				tc.status = http.StatusServiceUnavailable
			}
			rec := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/cpr"+path, nil)
			request.Header.Set("Authorization", tc.auth)
			router.ServeHTTP(rec, request)
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
		}
	}
}

func TestCPRExportsRequireHostStepUpBeforeReadingSensitiveData(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	h := &handler.Handlers{Admin: &handler.AdminHandlers{CPR: admin.NewCPRHandler(nil, nil, nil)}}
	RegisterAdminRoutes(router.Group("/api/v1"), h, func(c *gin.Context) { c.Next() }, func(c *gin.Context) { c.Next() }, func(c *gin.Context) { c.AbortWithStatus(http.StatusPreconditionRequired) }, nil, nil)
	for _, path := range []string{"/accounts/export", "/diagnostics/export"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/cpr"+path, nil))
		require.Equal(t, http.StatusPreconditionRequired, rec.Code)
	}
}
