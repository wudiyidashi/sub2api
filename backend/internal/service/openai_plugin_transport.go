package service

import (
	"bytes"
	"context"
	"github.com/Wei-Shaw/sub2api/internal/accountdriver"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"io"
	"net/http"
	"sync"
)

func (s *OpenAIGatewayService) SetPluginManager(manager *PluginManager) {
	s.pluginManager = manager
}

// doOpenAIUpstream 只在 OpenAI OAuth 能力绑定已启用时把真实请求交给插件。
// 插件返回标准 http.Response，响应解析、错误映射、SSE 和计费仍由现有核心链处理。
func (s *OpenAIGatewayService) doOpenAIUpstream(request *http.Request, proxyURL string, account *Account) (response *http.Response, forwardErr error) {
	ctx, release, admissionErr := acquireOpenAIAccountDriverOperation(request.Context(), account)
	if admissionErr != nil {
		return nil, admissionErr
	}
	request = request.WithContext(ctx)
	recordAPIRequestAuditUpstreamRequest(request)
	defer func() {
		if response != nil && response.Body != nil {
			response.Body = newAPIRequestAuditReadCloser(response.Body, request.Context(), APIRequestAuditDirectionUpstreamResponse, response.Header)
		}
	}()
	defer func() {
		if response != nil && response.Body != nil {
			response.Body = newAccountDriverAdmissionBody(response.Body, release, ctx)
		} else {
			release()
		}
	}()
	if account != nil && account.EffectiveAccountDriverID() != accountdriver.NativeID && !usesCodexHTTPDriver(account) {
		return nil, &PluginTransportError{Code: "account_driver_unknown", Message: "Account driver is not supported"}
	}
	if usesCodexHTTPDriver(account) {
		if codexDriverHandlesRequest(request) {
			if s.pluginManager == nil {
				return nil, &PluginTransportError{Code: "account_driver_unavailable", Message: "Account driver is unavailable"}
			}
			return s.pluginManager.accountDriverManager.RoundTrip(request, proxyURL, account)
		}
		return s.httpUpstream.Do(request, proxyURL, account.ID, account.Concurrency)
	}
	if s.pluginManager != nil {
		response, handled, err := s.pluginManager.RoundTripOpenAIOAuth(request.Context(), request, proxyURL, account)
		if handled {
			return response, err
		}
	}
	return s.httpUpstream.Do(request, proxyURL, account.ID, account.Concurrency)
}

func recordAPIRequestAuditUpstreamRequest(request *http.Request) {
	if request == nil || request.GetBody == nil {
		return
	}
	body, err := request.GetBody()
	if err != nil || body == nil {
		return
	}
	defer body.Close()
	raw, _ := io.ReadAll(io.LimitReader(body, MaxAPIRequestAuditBodyBytes+1))
	CaptureAPIRequestAuditPayload(request.Context(), &APIRequestAuditPayload{
		RequestID: contextAuditRequestID(request.Context()), ClientRequestID: contextAuditClientRequestID(request.Context()),
		Direction: APIRequestAuditDirectionUpstreamRequest, ContentType: request.Header.Get("Content-Type"),
		Headers: auditHeaderValues(request.Header), Body: raw,
	})
}

type apiRequestAuditReadCloser struct {
	io.ReadCloser
	ctx       context.Context
	direction string
	headers   http.Header
	buf       bytes.Buffer
	total     int64
	truncated bool
	once      sync.Once
}

func newAPIRequestAuditReadCloser(body io.ReadCloser, ctx context.Context, direction string, headers http.Header) io.ReadCloser {
	return &apiRequestAuditReadCloser{ReadCloser: body, ctx: ctx, direction: direction, headers: headers}
}
func (r *apiRequestAuditReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.total += int64(n)
	if r.buf.Len() < MaxAPIRequestAuditBodyBytes {
		remaining := MaxAPIRequestAuditBodyBytes - r.buf.Len()
		if n > remaining {
			r.buf.Write(p[:remaining])
			r.truncated = true
		} else {
			r.buf.Write(p[:n])
		}
	} else if n > 0 {
		r.truncated = true
	}
	if err != nil {
		r.record()
	}
	return n, err
}
func (r *apiRequestAuditReadCloser) Close() error {
	err := r.ReadCloser.Close()
	r.record()
	return err
}
func (r *apiRequestAuditReadCloser) record() {
	r.once.Do(func() {
		CaptureAPIRequestAuditPayload(r.ctx, &APIRequestAuditPayload{RequestID: contextAuditRequestID(r.ctx), ClientRequestID: contextAuditClientRequestID(r.ctx), Direction: r.direction, ContentType: r.headers.Get("Content-Type"), Headers: auditHeaderValues(r.headers), Body: r.buf.Bytes(), OriginalBytes: r.total, Truncated: r.truncated})
	})
}
func contextAuditRequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if value, _ := ctx.Value(ctxkey.RequestID).(string); value != "" {
		return value
	}
	return ""
}
func contextAuditClientRequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if value, _ := ctx.Value(ctxkey.ClientRequestID).(string); value != "" {
		return value
	}
	return ""
}
func auditHeaderValues(headers http.Header) map[string][]string {
	out := make(map[string][]string, len(headers))
	for key, values := range headers {
		out[key] = append([]string(nil), values...)
	}
	return out
}

// doOpenAIAccountTestUpstream 让 OpenAI OAuth 账号测试与真实转发使用同一插件路径。
// API Key 和未命中插件的账号保持各自原有的 HTTPUpstream 行为。
func (s *AccountTestService) doOpenAIAccountTestUpstream(
	request *http.Request,
	proxyURL string,
	account *Account,
	useTLSFallback bool,
) (response *http.Response, forwardErr error) {
	ctx, release, admissionErr := acquireOpenAIAccountDriverOperation(request.Context(), account)
	if admissionErr != nil {
		return nil, admissionErr
	}
	request = request.WithContext(ctx)
	defer func() {
		if response != nil && response.Body != nil {
			response.Body = newAccountDriverAdmissionBody(response.Body, release, ctx)
		} else {
			release()
		}
	}()
	if account != nil && account.EffectiveAccountDriverID() != accountdriver.NativeID && !usesCodexHTTPDriver(account) {
		return nil, &PluginTransportError{Code: "account_driver_unknown", Message: "Account driver is not supported"}
	}
	if usesCodexHTTPDriver(account) {
		if codexDriverHandlesRequest(request) {
			if s.pluginManager == nil {
				return nil, &PluginTransportError{Code: "account_driver_unavailable", Message: "Account driver is unavailable"}
			}
			return s.pluginManager.accountDriverManager.RoundTrip(request, proxyURL, account)
		}
		return s.httpUpstream.Do(request, proxyURL, account.ID, account.Concurrency)
	}
	if s.pluginManager != nil {
		response, handled, err := s.pluginManager.RoundTripOpenAIOAuth(request.Context(), request, proxyURL, account)
		if handled {
			return response, err
		}
	}
	if useTLSFallback {
		return s.httpUpstream.DoWithTLS(
			request,
			proxyURL,
			account.ID,
			account.Concurrency,
			s.tlsFPProfileService.ResolveTLSProfile(account),
		)
	}
	return s.httpUpstream.Do(request, proxyURL, account.ID, account.Concurrency)
}
