//go:build embed

package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Validate the actual embedded release assets, including the directory URL to
// which net/http redirects requests for /team-stats/index.html.
func TestTeamStatsEmbeddedDirectory(t *testing.T) {
	server, err := NewFrontendServer(&mockSettingsProvider{settings: map[string]string{"site_name": "Host dashboard"}})
	require.NoError(t, err)
	for name, handler := range map[string]gin.HandlerFunc{
		"settings_provider": server.Middleware(),
		"legacy":            ServeEmbeddedFrontend(),
	} {
		t.Run(name, func(t *testing.T) {
			router := gin.New()
			router.Use(middleware.SecurityHeaders(config.CSPConfig{Enabled: true}, nil), handler)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/team-stats/", nil))
			require.Equal(t, http.StatusOK, w.Code)
			require.Contains(t, w.Body.String(), `/team-stats/assets/`)
			require.NotContains(t, w.Body.String(), `window.__APP_CONFIG__`)
			require.Equal(t, "SAMEORIGIN", w.Header().Get("X-Frame-Options"))
			require.Contains(t, w.Header().Get("Content-Security-Policy"), "frame-ancestors 'self'")
		})
	}
}
