package admin

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// GetTeamStatsConfig reads the shared admin dashboard configuration.
func (h *SettingHandler) GetTeamStatsConfig(c *gin.Context) {
	cfg, err := h.settingService.GetTeamStatsConfig(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, cfg)
}

func (h *SettingHandler) UpdateTeamStatsConfig(c *gin.Context) {
	cfg := service.DefaultTeamStatsConfig()
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil || cfg == nil {
		response.BadRequest(c, "Invalid team statistics configuration")
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		response.BadRequest(c, "Expected one JSON object")
		return
	}
	if err := h.settingService.UpdateTeamStatsConfig(c.Request.Context(), cfg); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, cfg)
}
