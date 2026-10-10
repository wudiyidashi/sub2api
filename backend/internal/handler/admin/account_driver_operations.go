package admin

import (
	"strconv"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *AccountHandler) SetAccountDriverRuntime(runtime *service.AccountDriverManager) {
	h.accountDriverRuntime = runtime
}

func (h *AccountHandler) SetAccountDriverMaintenanceService(maintenance *service.AccountDriverMaintenanceService) {
	if setter, ok := h.adminService.(interface {
		SetAccountDriverMaintenanceService(*service.AccountDriverMaintenanceService)
	}); ok {
		setter.SetAccountDriverMaintenanceService(maintenance)
	}
}

func (h *AccountHandler) accountDriverRuntimeOrError(c *gin.Context) *service.AccountDriverManager {
	if h.accountDriverRuntime == nil {
		response.ErrorFrom(c, infraerrors.ServiceUnavailable("ACCOUNT_DRIVER_RUNTIME_UNAVAILABLE", "account driver runtime is unavailable"))
		return nil
	}
	return h.accountDriverRuntime
}

func (h *AccountHandler) GetDriverRuntime(c *gin.Context) {
	if runtime := h.accountDriverRuntimeOrError(c); runtime != nil {
		response.Success(c, runtime.Status())
	}
}

func (h *AccountHandler) UpgradeDriverRuntime(c *gin.Context) {
	runtime := h.accountDriverRuntimeOrError(c)
	if runtime == nil {
		return
	}
	var input service.AccountDriverUpgradeInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.ErrorFrom(c, infraerrors.BadRequest("ACCOUNT_DRIVER_UPGRADE_INVALID", "invalid driver upgrade request"))
		return
	}
	status, err := runtime.Upgrade(c.Request.Context(), input)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, status)
}

func (h *AccountHandler) RollbackDriverRuntime(c *gin.Context) {
	runtime := h.accountDriverRuntimeOrError(c)
	if runtime == nil {
		return
	}
	var input service.AccountDriverRollbackInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.ErrorFrom(c, infraerrors.BadRequest("ACCOUNT_DRIVER_ROLLBACK_INVALID", "invalid driver rollback request"))
		return
	}
	status, err := runtime.Rollback(c.Request.Context(), input)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, status)
}

func (h *AccountHandler) DrainDriverRuntime(c *gin.Context) {
	runtime := h.accountDriverRuntimeOrError(c)
	if runtime == nil {
		return
	}
	var input struct {
		Generation uint64 `json:"generation" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		response.ErrorFrom(c, infraerrors.BadRequest("ACCOUNT_DRIVER_DRAIN_INVALID", "a retired generation is required"))
		return
	}
	status, err := runtime.ForceDrain(c.Request.Context(), input.Generation)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, status)
}

func (h *AccountHandler) accountDriverMaintenanceOrError(c *gin.Context) (service.AccountDriverMaintenanceAdminService, int64) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.ErrorFrom(c, infraerrors.BadRequest("ACCOUNT_ID_INVALID", "a valid account id is required"))
		return nil, 0
	}
	maintenance, ok := h.adminService.(service.AccountDriverMaintenanceAdminService)
	if !ok {
		response.ErrorFrom(c, infraerrors.ServiceUnavailable("ACCOUNT_DRIVER_MAINTENANCE_UNAVAILABLE", "account driver maintenance is unavailable"))
		return nil, 0
	}
	return maintenance, id
}

func (h *AccountHandler) GetDriverMaintenance(c *gin.Context) {
	maintenance, id := h.accountDriverMaintenanceOrError(c)
	if maintenance == nil {
		return
	}
	status, err := maintenance.GetAccountDriverMaintenance(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, status)
}

func (h *AccountHandler) SwitchDriver(c *gin.Context) {
	maintenance, id := h.accountDriverMaintenanceOrError(c)
	if maintenance == nil {
		return
	}
	var input service.AccountDriverSwitchInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.ErrorFrom(c, infraerrors.BadRequest("ACCOUNT_DRIVER_SWITCH_INVALID", "invalid account driver switch request"))
		return
	}
	if subject, ok := middleware.GetAuthSubjectFromContext(c); ok {
		input.ActorID = subject.UserID
	}
	status, err := maintenance.StartAccountDriverSwitch(c.Request.Context(), id, input)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, status)
}

func (h *AccountHandler) CancelDriverMaintenance(c *gin.Context) {
	maintenance, id := h.accountDriverMaintenanceOrError(c)
	if maintenance == nil {
		return
	}
	var input struct {
		OperationID string `json:"operation_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil || strings.TrimSpace(input.OperationID) == "" {
		response.ErrorFrom(c, infraerrors.BadRequest("ACCOUNT_DRIVER_OPERATION_INVALID", "an operation id is required"))
		return
	}
	status, err := maintenance.CancelAccountDriverMaintenance(c.Request.Context(), id, input.OperationID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, status)
}
