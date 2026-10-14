package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestTeamStatsSameOriginEmbedding(t *testing.T) {
	for _, path := range []string{"/team-stats/", "/team-stats/index.html", "/admin/team-stats", "/team-stats-backup/index.html", "/login"} {
		t.Run(path, func(t *testing.T) {
			router := gin.New()
			router.Use(SecurityHeaders(config.CSPConfig{Enabled: true}, nil))
			router.GET(path, func(c *gin.Context) { c.Status(http.StatusOK) })
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
			if path == "/team-stats/" || path == "/team-stats/index.html" {
				require.Equal(t, "SAMEORIGIN", w.Header().Get("X-Frame-Options"))
				require.Contains(t, w.Header().Get("Content-Security-Policy"), "frame-ancestors 'self'")
				require.NotContains(t, w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'")
			} else {
				require.Equal(t, "DENY", w.Header().Get("X-Frame-Options"))
				require.Contains(t, w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'")
			}
		})
	}
}
