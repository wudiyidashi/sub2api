//go:build unit

package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestTeamStatsConfigRejectsMalformedPayload(t *testing.T) {
	for _, body := range []string{"null", "[]", `{"adminKey":"secret"}`, `{} {}`, `{"groups":[{"id":"x","name":"x","color":"#000000","apiKeyIds":[],"adminKey":"secret"}]}`, `{"timezone":"` + strings.Repeat("x", 1<<20) + `"}`} {
		t.Run(body[:min(len(body), 40)], func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			router := gin.New()
			h := &SettingHandler{} // Invalid input must never access the database.
			router.PUT("/config", h.UpdateTeamStatsConfig)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/config", strings.NewReader(body)))
			require.Equal(t, http.StatusBadRequest, w.Code)
		})
	}
}
