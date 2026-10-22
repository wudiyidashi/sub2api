package admin

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/cpr"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// CPRHandler 只代理明确支持的管理合同，浏览器不接触内部管理密钥。
type CPRHandler struct {
	client  *cpr.Client
	enabled bool
	pools   *service.CPRPoolAdminService
}

func (h *CPRHandler) Close() {
	if h != nil && h.client != nil {
		h.client.CloseIdleConnections()
	}
}

func NewCPRHandler(cfg *config.Config, accounts service.AdminAccountRepository, groups service.GroupRepository) *CPRHandler {
	h := &CPRHandler{}
	if cfg == nil || !cfg.AccountDrivers.CPRManaged.Enabled {
		return h
	}
	h.enabled = true
	settings := cfg.AccountDrivers.CPRManaged
	if settings.Validate() != nil {
		return h
	}
	client, err := cpr.New(cpr.Config{
		BaseURL: settings.BaseURL, AdminKey: settings.AdminAPIKey,
		AdminTimeout: time.Duration(settings.AdminTimeoutSeconds) * time.Second,
	}, nil)
	if err == nil {
		h.client = client
		h.pools = service.NewCPRPoolAdminService(client, accounts, groups)
	}
	return h
}

func (h *CPRHandler) Pools(c *gin.Context) {
	if h == nil || h.pools == nil {
		response.Error(c, http.StatusServiceUnavailable, "CPR pool management is unavailable")
		return
	}
	items, err := h.pools.List(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusBadGateway, "CPR pool bindings could not be loaded")
		return
	}
	response.Success(c, items)
}

func (h *CPRHandler) SavePool(c *gin.Context) {
	if h == nil || h.pools == nil {
		response.Error(c, http.StatusServiceUnavailable, "CPR pool management is unavailable")
		return
	}
	var input service.CPRPoolInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, "Invalid CPR pool binding")
		return
	}
	if input.ID < 0 || (input.ID > 0 && input.ExpectedRevision < 1) {
		response.BadRequest(c, "Invalid CPR pool revision")
		return
	}
	pool, err := h.pools.Save(c.Request.Context(), input)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, pool)
}

func (h *CPRHandler) Status(c *gin.Context) {
	if h == nil || h.client == nil {
		response.Success(c, gin.H{"enabled": h != nil && h.enabled, "connected": false})
		return
	}
	query := url.Values{"page": {"1"}, "pageSize": {"1"}}
	version, err := h.client.CheckRuntime(c.Request.Context())
	if err != nil {
		code := "CPR_UNAVAILABLE"
		if version != "" {
			code = "CPR_RUNTIME_INCOMPATIBLE"
		}
		response.Success(c, gin.H{"enabled": true, "connected": false, "error": code, "version": version})
		return
	}
	if err := h.client.Admin(c.Request.Context(), cpr.ListAccounts, query, nil, nil); err != nil {
		response.Success(c, gin.H{"enabled": true, "connected": false, "error": "CPR_UNAVAILABLE"})
		return
	}
	response.Success(c, gin.H{"enabled": true, "connected": true, "version": version, "providers": []string{"openai", "xai"}})
}

func (h *CPRHandler) Operation(operation cpr.Operation) gin.HandlerFunc {
	return func(c *gin.Context) {
		if h == nil || h.client == nil {
			response.Error(c, http.StatusServiceUnavailable, "CPR account management is not configured")
			return
		}
		query := c.Request.URL.Query()
		// 面板通用 HTTP client 自动附带的展示时区不是 CPR 管理查询参数。
		query.Del("timezone")
		if provider := query.Get("provider"); provider != "" && provider != "openai" && provider != "xai" {
			response.BadRequest(c, "Unsupported CPR provider")
			return
		}
		if operation.Kind() != "json" {
			h.resource(c, operation, query)
			return
		}
		var input json.RawMessage
		if c.Request.Method == http.MethodPost {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<20)
			if err := c.ShouldBindJSON(&input); err != nil {
				response.BadRequest(c, "Invalid CPR management request")
				return
			}
			switch operation {
			case cpr.ImportAccounts, cpr.StartAuthorization, cpr.CompleteAuthorization:
				var owner struct {
					Provider string `json:"provider"`
				}
				if json.Unmarshal(input, &owner) != nil || (owner.Provider != "openai" && owner.Provider != "xai") {
					response.BadRequest(c, "Unsupported CPR provider")
					return
				}
			case cpr.Operation("/api/admin/accounts/import-tasks#POST"):
				var task struct {
					Items []struct {
						Provider string `json:"provider"`
					} `json:"items"`
				}
				if json.Unmarshal(input, &task) != nil || len(task.Items) == 0 || len(task.Items) > 1000 {
					response.BadRequest(c, "Invalid CPR import task")
					return
				}
				for _, item := range task.Items {
					if item.Provider != "openai" && item.Provider != "xai" {
						response.BadRequest(c, "Unsupported CPR provider")
						return
					}
				}
			}
		}
		var data json.RawMessage
		err := h.client.Admin(c.Request.Context(), operation, query, inputOrNil(input), &data)
		if err != nil {
			status := http.StatusBadGateway
			var remoteError *cpr.Error
			if errors.As(err, &remoteError) {
				// CPR 内部凭据失效不应被转换成用户登录失效或引起浏览器清空会话。
				switch remoteError.Status {
				case http.StatusBadRequest, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity:
					status = remoteError.Status
				}
			}
			metadata := gin.H{}
			if remoteError != nil {
				metadata["cpr_code"] = remoteError.Code
			}
			c.JSON(status, gin.H{"code": status, "message": "CPR management operation failed", "metadata": metadata})
			return
		}
		response.Success(c, data)
	}
}

func inputOrNil(input json.RawMessage) any {
	if len(input) == 0 {
		return nil
	}
	return input
}

func (h *CPRHandler) resource(c *gin.Context, operation cpr.Operation, query url.Values) {
	upstream, err := h.client.OpenAdminResource(c.Request.Context(), operation, query)
	if err != nil {
		response.Error(c, http.StatusBadGateway, "CPR management resource failed")
		return
	}
	defer func() { _ = upstream.Body.Close() }()
	c.Header("Cache-Control", "private, no-store")
	c.Header("Content-Type", upstream.Header.Get("Content-Type"))
	if operation.Kind() == "binary" || operation.Kind() == "download" {
		limit := int64(8 << 20)
		if operation.Kind() == "download" {
			limit = 16 << 20
		}
		raw, readErr := io.ReadAll(io.LimitReader(upstream.Body, limit+1))
		if readErr != nil || int64(len(raw)) > limit {
			response.Error(c, http.StatusBadGateway, "Invalid CPR resource")
			return
		}
		if operation.Kind() == "download" {
			c.Header("Content-Disposition", `attachment; filename="cpr-diagnostics.json"`)
		}
		c.Data(http.StatusOK, upstream.Header.Get("Content-Type"), raw)
		return
	}
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	buffer := make([]byte, 32<<10)
	for {
		n, readErr := upstream.Body.Read(buffer)
		if n > 0 {
			if _, writeErr := c.Writer.Write(buffer[:n]); writeErr != nil {
				return
			}
			c.Writer.Flush()
		}
		if readErr != nil {
			return
		}
	}
}
