package admin

import (
	"context"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// ListDrivers returns ready account management implementations. It is registered
// under the same administrator authentication as the account management routes.
func (h *AccountHandler) ListDrivers(c *gin.Context) {
	response.Success(c, service.BuildAccountDriverCatalog())
}

func (h *AccountHandler) listAccountsWithDriver(ctx context.Context, page, pageSize int, platform, accountType, status, search string, groupID int64, privacyMode, sortBy, sortOrder, driverID string) ([]service.Account, int64, error) {
	if err := service.ValidateAccountDriverFilter(driverID); err != nil {
		return nil, 0, err
	}
	if listing, ok := h.adminService.(service.AccountDriverListingService); ok {
		return listing.ListAccountsWithDriver(ctx, page, pageSize, platform, accountType, status, search, groupID, privacyMode, sortBy, sortOrder, driverID)
	}
	if strings.TrimSpace(driverID) == "" {
		return h.adminService.ListAccounts(ctx, page, pageSize, platform, accountType, status, search, groupID, privacyMode, sortBy, sortOrder)
	}
	return nil, 0, infraerrors.BadRequest("ACCOUNT_DRIVER_FILTER_UNSUPPORTED", "account service does not support account driver filtering")
}
