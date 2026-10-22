package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/cpr"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"strings"
)

func registerCPRRoutes(admin *gin.RouterGroup, h *handler.Handlers, stepUpAuth middleware.StepUpAuthMiddleware) {
	if h.Admin.CPR == nil {
		return
	}
	group := admin.Group("/cpr")
	group.GET("/status", h.Admin.CPR.Status)
	group.GET("/pools", h.Admin.CPR.Pools)
	group.POST("/pools/save", h.Admin.CPR.SavePool)
	for _, endpoint := range cpr.ManagementEndpoints() {
		handlers := []gin.HandlerFunc{}
		if endpoint.Sensitive {
			handlers = append(handlers, gin.HandlerFunc(stepUpAuth))
		}
		handlers = append(handlers, h.Admin.CPR.Operation(endpoint.Operation))
		group.Handle(endpoint.Method, strings.TrimPrefix(endpoint.Path, "/api/admin"), handlers...)
	}
}
