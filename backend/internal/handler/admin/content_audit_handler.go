package admin

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/contentaudit"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type ContentAuditHandler struct{ facade *contentaudit.Facade }

func NewContentAuditHandler(facade *contentaudit.Facade) *ContentAuditHandler {
	return &ContentAuditHandler{facade: facade}
}

func (h *ContentAuditHandler) ListRequests(c *gin.Context) {
	if h == nil || h.facade == nil {
		response.Error(c, http.StatusServiceUnavailable, "Content audit is unavailable")
		return
	}
	now := time.Now().UTC()
	start, end := auditTimeRange(c, now)
	filter := &service.OpsRequestDetailFilter{
		StartTime: ptrAuditTime(start), EndTime: ptrAuditTime(end),
		Kind: strings.TrimSpace(c.Query("kind")), Platform: strings.TrimSpace(c.Query("platform")),
		Model: strings.TrimSpace(c.Query("model")), RequestID: strings.TrimSpace(c.Query("request_id")),
		Query: strings.TrimSpace(c.Query("q")), Sort: strings.TrimSpace(c.Query("sort")), Page: 1, PageSize: 20,
	}
	filter.GroupID = auditInt64Ptr(c.Query("group_id"))
	filter.UserID = auditInt64Ptr(c.Query("user_id"))
	filter.APIKeyID = auditInt64Ptr(c.Query("api_key_id"))
	filter.AccountID = auditInt64Ptr(c.Query("account_id"))
	filter.MinDurationMs = auditIntPtr(c.Query("min_duration_ms"))
	filter.MaxDurationMs = auditIntPtr(c.Query("max_duration_ms"))
	if v, err := strconv.Atoi(c.Query("page")); err == nil && v > 0 {
		filter.Page = v
	}
	if v, err := strconv.Atoi(c.Query("page_size")); err == nil && v > 0 {
		filter.PageSize = v
	}
	result, err := h.facade.ListRequests(c.Request.Context(), filter)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, result.Items, result.Total, result.Page, result.PageSize)
}

func (h *ContentAuditHandler) GetRequest(c *gin.Context) {
	if h == nil || h.facade == nil {
		response.Error(c, http.StatusServiceUnavailable, "Content audit is unavailable")
		return
	}
	detail, err := h.facade.GetRequest(c.Request.Context(), c.Param("request_id"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, detail)
}

func (h *ContentAuditHandler) ListAttempts(c *gin.Context) {
	if h == nil || h.facade == nil {
		response.Error(c, http.StatusServiceUnavailable, "Content audit is unavailable")
		return
	}
	page, pageSize := 1, 50
	if v, err := strconv.Atoi(c.Query("page")); err == nil && v > 0 {
		page = v
	}
	if v, err := strconv.Atoi(c.Query("page_size")); err == nil && v > 0 {
		pageSize = v
	}
	items, total, err := h.facade.ListUpstreamAttempts(c.Request.Context(), c.Param("request_id"), c.Query("client_request_id"), page, pageSize)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, items, total, page, pageSize)
}

func (h *ContentAuditHandler) GetErrorPayload(c *gin.Context) {
	if h == nil || h.facade == nil {
		response.Error(c, http.StatusServiceUnavailable, "Content audit is unavailable")
		return
	}
	id, err := strconv.ParseInt(c.Param("error_id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "invalid error id")
		return
	}
	payload, err := h.facade.GetErrorPayload(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, payload)
}

// ListRawPayloads returns the bounded raw client/upstream payload chunks kept
// by the request audit store. JSON encoding represents []byte as base64.
func (h *ContentAuditHandler) ListRawPayloads(c *gin.Context) {
	if h == nil || h.facade == nil {
		response.Error(c, http.StatusServiceUnavailable, "Content audit is unavailable")
		return
	}
	payloads, err := h.facade.ListRawPayloads(c.Request.Context(), c.Param("request_id"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, payloads)
}

func ptrAuditTime(value time.Time) *time.Time { return &value }

func auditInt64Ptr(raw string) *int64 {
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value <= 0 {
		return nil
	}
	return &value
}

func auditIntPtr(raw string) *int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < 0 {
		return nil
	}
	return &value
}

// auditTimeRange mirrors the usage/Ops time controls while also accepting the
// DateRangePicker's YYYY-MM-DD values for a custom inclusive date range.
func auditTimeRange(c *gin.Context, now time.Time) (time.Time, time.Time) {
	end := now
	start := now.Add(-time.Hour)
	if raw := strings.TrimSpace(c.Query("time_range")); raw != "" {
		if duration, ok := map[string]time.Duration{
			"5m": 5 * time.Minute, "30m": 30 * time.Minute, "1h": time.Hour,
			"6h": 6 * time.Hour, "24h": 24 * time.Hour,
		}[raw]; ok {
			start = end.Add(-duration)
		}
	}
	if raw := strings.TrimSpace(c.Query("start_time")); raw != "" {
		if parsed, ok := parseAuditTime(raw, false); ok {
			start = parsed
		}
	}
	if raw := strings.TrimSpace(c.Query("end_time")); raw != "" {
		if parsed, ok := parseAuditTime(raw, true); ok {
			end = parsed
		}
	}
	if start.After(end) {
		start, end = end, start
	}
	return start, end
}

func parseAuditTime(raw string, endOfDate bool) (time.Time, bool) {
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return parsed.UTC(), true
	}
	parsed, err := time.ParseInLocation("2006-01-02", raw, time.UTC)
	if err != nil {
		return time.Time{}, false
	}
	if endOfDate {
		parsed = parsed.Add(24 * time.Hour)
	}
	return parsed, true
}
