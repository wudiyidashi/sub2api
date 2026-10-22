package admin

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *AccountHandler) SetRequestEnvironments(resolver *service.AccountRequestEnvironmentService) {
	h.requestEnvironments = resolver
}

// GetRequestEnvironment 支持管理员预览待保存的代理，探测不修改账号绑定
func (h *AccountHandler) GetRequestEnvironment(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	account, err := h.adminService.GetAccount(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if !account.UsesOpenAICodexProtocol() {
		response.BadRequest(c, "Request environment is only available for Codex accounts")
		return
	}
	if h.requestEnvironments == nil {
		response.Error(c, 503, "Request environment service unavailable")
		return
	}
	snapshot := *account
	if raw, exists := c.GetQuery("proxy_id"); exists {
		proxyID, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || proxyID < 0 {
			response.BadRequest(c, "Invalid proxy ID")
			return
		}
		snapshot.ProxyID = nil
		snapshot.Proxy = nil
		if proxyID > 0 {
			proxy, getErr := h.adminService.GetProxy(c.Request.Context(), proxyID)
			if getErr != nil {
				response.ErrorFrom(c, getErr)
				return
			}
			snapshot.ProxyID = &proxyID
			snapshot.Proxy = proxy
		}
	}
	var environment *service.CodexRequestEnvironment
	if c.Query("refresh") == "1" {
		environment, err = h.requestEnvironments.Refresh(c.Request.Context(), &snapshot)
	} else {
		environment, err = h.requestEnvironments.Resolve(c.Request.Context(), &snapshot)
	}
	if err != nil {
		response.Error(c, 503, err.Error())
		return
	}
	response.Success(c, environment)
}
