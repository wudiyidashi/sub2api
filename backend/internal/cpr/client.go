// Package cpr 是 Sub2API 与内部 CPR 完整网关的边界，不执行账号选号或上游重试。
package cpr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

const maxAdminResponseBytes = 8 << 20

type Config struct {
	BaseURL      string
	AdminKey     string
	AdminTimeout time.Duration
}

type Client struct {
	base     *url.URL
	adminKey string
	admin    *http.Client
	data     *http.Client
}

func (c *Client) CloseIdleConnections() {
	c.admin.CloseIdleConnections()
	c.data.CloseIdleConnections()
}

// Error 不携带 CPR 响应正文或 URL，避免凭据及授权材料进入宿主错误日志。
type Error struct {
	Status int
	Code   uint32
}

func (e *Error) Error() string {
	return fmt.Sprintf("CPR request failed (status=%d code=%d)", e.Status, e.Code)
}

func New(cfg Config, transport http.RoundTripper) (*Client, error) {
	base, err := url.Parse(cfg.BaseURL)
	if err != nil || base == nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || (base.Path != "" && base.Path != "/") {
		return nil, errors.New("CPR base URL must be an HTTP(S) origin without credentials, path or query")
	}
	if strings.TrimSpace(cfg.AdminKey) == "" || strings.ContainsAny(cfg.AdminKey, "\r\n") {
		return nil, errors.New("CPR admin key is required and must be a valid header value")
	}
	if cfg.AdminTimeout <= 0 {
		cfg.AdminTimeout = 30 * time.Second
	}
	if transport == nil {
		// 内部控制/数据面不继承进程 HTTP_PROXY；真实账号代理完全归 CPR 管理。
		t, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			return nil, errors.New("CPR default HTTP transport is unavailable")
		}
		t = t.Clone()
		t.Proxy = nil
		transport = t
	}
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{
		base: base, adminKey: cfg.AdminKey,
		admin: &http.Client{Transport: transport, Timeout: cfg.AdminTimeout, CheckRedirect: noRedirect},
		// SSE/WS 的总期限来自下游 context，不能套用管理接口的短超时。
		data: &http.Client{Transport: transport, CheckRedirect: noRedirect},
	}, nil
}

type Operation string

const (
	ListAccounts          Operation = "/api/admin/accounts"
	AccountDetail         Operation = "/api/admin/accounts/detail"
	ImportAccounts        Operation = "/api/admin/accounts/import"
	RefreshAccount        Operation = "/api/admin/accounts/refresh"
	RecoverAccount        Operation = "/api/admin/accounts/recover"
	UpdateAccount         Operation = "/api/admin/accounts/update"
	DeleteAccounts        Operation = "/api/admin/accounts/delete"
	BatchUpdateAccounts   Operation = "/api/admin/accounts/batch-update"
	AccountQuota          Operation = "/api/admin/accounts/quota"
	RefreshAccountQuota   Operation = "/api/admin/accounts/quota/refresh"
	AccountModels         Operation = "/api/admin/accounts/models"
	AccountModelCatalog   Operation = "/api/admin/accounts/models/catalog"
	StartAuthorization    Operation = "/api/admin/accounts/oauth/start"
	CompleteAuthorization Operation = "/api/admin/accounts/oauth/complete"
	ListGroups            Operation = "/api/admin/account-groups"
	CreateGroup           Operation = "/api/admin/account-groups/create"
	UpdateGroup           Operation = "/api/admin/account-groups/update"
	ListClientKeys        Operation = "/api/admin/client-keys"
	RevealClientKey       Operation = "/api/admin/client-keys/reveal"
	CreateClientKey       Operation = "/api/admin/client-keys/create"
	UpdateClientKey       Operation = "/api/admin/client-keys/update"
	DeleteClientKey       Operation = "/api/admin/client-keys/delete"
)

var operationMethods = map[Operation]string{
	Operation("/api/admin/system/version"): http.MethodGet,
	ListAccounts:                           http.MethodGet, AccountDetail: http.MethodGet,
	ImportAccounts: http.MethodPost, RefreshAccount: http.MethodPost,
	RecoverAccount: http.MethodPost, UpdateAccount: http.MethodPost,
	DeleteAccounts: http.MethodPost, BatchUpdateAccounts: http.MethodPost,
	AccountQuota: http.MethodGet, RefreshAccountQuota: http.MethodPost,
	AccountModels: http.MethodGet, AccountModelCatalog: http.MethodGet,
	StartAuthorization: http.MethodPost, CompleteAuthorization: http.MethodPost,
	ListGroups: http.MethodGet, CreateGroup: http.MethodPost, UpdateGroup: http.MethodPost,
	ListClientKeys: http.MethodGet, CreateClientKey: http.MethodPost,
	RevealClientKey: http.MethodGet,
	UpdateClientKey: http.MethodPost, DeleteClientKey: http.MethodPost,
}

// Admin 只接受已固定的控制面合同，不能把浏览器提供的任意 URL 变成带管理凭据的请求。
func (c *Client) Admin(ctx context.Context, operation Operation, query url.Values, input any, output any) error {
	method, ok := operationMethods[operation]
	if !ok {
		return errors.New("unsupported CPR management operation")
	}
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return errors.New("invalid CPR management request")
		}
		body = bytes.NewReader(raw)
	}
	target := *c.base
	target.Path = operation.Path()
	target.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return errors.New("invalid CPR management request")
	}
	request.Header.Set("x-api-key", c.adminKey)
	request.Header.Set("X-Request-ID", uuid.NewString())
	request.Header.Set("Content-Type", "application/json")
	response, err := c.admin.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("CPR management service is unavailable")
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxAdminResponseBytes+1))
	if err != nil || len(raw) > maxAdminResponseBytes {
		return errors.New("invalid CPR management response")
	}
	var envelope struct {
		Code uint32          `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return &Error{Status: response.StatusCode}
	}
	// CPR 的成功业务码是 200，不能沿用 Sub2API 的 0。
	if response.StatusCode < 200 || response.StatusCode >= 300 || envelope.Code != 200 {
		return &Error{Status: response.StatusCode, Code: envelope.Code}
	}
	if output != nil && json.Unmarshal(envelope.Data, output) != nil {
		return errors.New("invalid CPR management response data")
	}
	return nil
}

// Execute 只连接内部 CPR 的固定数据面，真实 Provider URL 和账号凭据不进入此合同。
// 请求一经交付后任何错误均不得在本层自动重放。
func (c *Client) Execute(ctx context.Context, path, clientKey string, headers http.Header, body io.Reader) (*http.Response, error) {
	request, err := c.PrepareExecution(ctx, path, clientKey, headers, body)
	if err != nil {
		return nil, err
	}
	return c.SendExecution(request)
}

func (c *Client) PrepareExecution(ctx context.Context, path, clientKey string, headers http.Header, body io.Reader) (*http.Request, error) {
	if !dataPathAllowed(path) || clientKey == "" || strings.ContainsAny(clientKey, "\r\n") {
		return nil, errors.New("invalid CPR execution request")
	}
	target := *c.base
	target.Path = path
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), body)
	if err != nil {
		return nil, errors.New("invalid CPR execution request")
	}
	request.Header = dataHeaders(headers)
	request.Header.Set("Authorization", "Bearer "+clientKey)
	return request, nil
}

func (c *Client) SendExecution(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme != c.base.Scheme || request.URL.Host != c.base.Host || request.Method != http.MethodPost || !dataPathAllowed(request.URL.Path) {
		return nil, errors.New("invalid CPR execution destination")
	}
	response, err := c.data.Do(request)
	if err != nil {
		if request.Context().Err() != nil {
			return nil, request.Context().Err()
		}
		return nil, errors.New("CPR execution failed; request may have been sent")
	}
	return response, nil
}

func dataPathAllowed(path string) bool {
	switch path {
	case "/v1/responses", "/v1/images/generations", "/v1/images/edits", "/v1/alpha/search":
		return true
	default:
		return false
	}
}

func dataHeaders(source http.Header) http.Header {
	result := make(http.Header)
	// 这里只保留入站协议上下文；上游官方客户端身份由 CPR 生成。
	for _, name := range []string{"Content-Type", "Accept", "Accept-Language", "X-Request-ID", "Session-Id", "Session_id", "Conversation_id", "X-Codex-Turn-Metadata", "X-Codex-Window-Id", "X-Codex-Beta-Features", "X-OpenAI-Internal-Codex-Responses-Lite"} {
		for _, value := range source.Values(name) {
			result.Add(name, value)
		}
	}
	return result
}
