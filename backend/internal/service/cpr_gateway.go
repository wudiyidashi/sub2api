package service

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func (s *OpenAIGatewayService) prepareCPRGatewayAccount(ctx context.Context, c *gin.Context, account *Account) (context.Context, *Account, error) {
	if s == nil || c == nil {
		return ctx, nil, errors.New("CPR execution requires an authenticated request")
	}
	return s.cprPools.Prepare(ctx, account, getAPIKeyIDFromContext(c), getOpenAIGroupIDFromContext(c))
}

// Responses 正文直接交给 CPR；宿主只解析交付和用量，不执行账号指纹、修复或重放。
func (s *OpenAIGatewayService) forwardCPRResponses(ctx context.Context, c *gin.Context, account *Account, body []byte) (*OpenAIForwardResult, error) {
	ctx, prepared, err := s.prepareCPRGatewayAccount(ctx, c, account)
	if err != nil {
		return nil, err
	}
	model := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	if model == "" {
		return nil, errors.New("model is required")
	}
	if isOpenAIResponsesCompactPath(c) {
		return nil, writeCPRUnsupportedEndpoint(c, "compact")
	}
	start := time.Now()
	beginUpstreamResponseModelObservation(c)
	SetActualOpenAIUpstreamEndpoint(c, "/v1/responses")
	SetOpsUpstreamModel(c, model)
	binding, _ := cprBindingFromContext(ctx)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://cpr.invalid/v1/responses", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header = c.Request.Header.Clone()
	request.Header.Set("Content-Type", "application/json")
	resp, err := executeCPRPreparedRequest(request, prepared, binding)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return nil, s.handleErrorResponsePassthrough(ctx, resp, c, prepared, body, s.readUpstreamErrorBody(resp))
	}
	stream := gjson.GetBytes(body, "stream").Bool()
	result := &OpenAIForwardResult{
		RequestID: resp.Header.Get("x-request-id"), UpstreamHeaders: resp.Header,
		Model: model, UpstreamModel: model, Stream: stream,
		ServiceTier:     extractOpenAIServiceTierFromBody(body),
		ReasoningEffort: extractOpenAIReasoningEffortFromBody(body, model),
	}
	if stream {
		parsed, parseErr := s.handleStreamingResponsePassthrough(ctx, resp, c, prepared, start, model, model)
		if parseErr != nil {
			return finishCPRGatewayResult(ctx, c, nil, parseErr)
		}
		if parsed.usage != nil {
			result.Usage = *parsed.usage
		}
		result.FirstTokenMs, result.ResponseID, result.ImageCount = parsed.firstTokenMs, parsed.responseID, parsed.imageCount
		result.ImageOutputSizes = parsed.imageOutputSizes
	} else {
		parsed, parseErr := s.handleNonStreamingResponsePassthrough(ctx, resp, c, prepared, model, model)
		if parseErr != nil {
			return finishCPRGatewayResult(ctx, c, nil, parseErr)
		}
		if parsed.usage != nil {
			result.Usage = *parsed.usage
		}
		result.ResponseID, result.ImageCount, result.ImageOutputSizes = parsed.responseID, parsed.imageCount, parsed.imageOutputSizes
	}
	result.UpstreamResponseModel = observedUpstreamResponseModel(c)
	result.UpstreamResponseModelConflict = observedUpstreamResponseModelConflict(c)
	result.UpstreamResponseServiceTier = observedUpstreamResponseServiceTier(c)
	result.Duration = time.Since(start)
	return result, nil
}

func writeCPRUnsupportedEndpoint(c *gin.Context, endpoint string) error {
	if c != nil && !c.Writer.Written() {
		c.JSON(http.StatusNotImplemented, gin.H{"error": gin.H{"type": "unsupported_endpoint", "message": "CPR does not expose the requested " + endpoint + " endpoint"}})
	}
	return errors.New("CPR endpoint is unavailable")
}

// finishCPRGatewayResult 不暴露可触发宿主换号的错误类型；CPR 已经拥有完整账号池重试。
func finishCPRGatewayResult(ctx context.Context, c *gin.Context, result *OpenAIForwardResult, err error) (*OpenAIForwardResult, error) {
	if err == nil || ctx.Err() != nil {
		return result, err
	}
	var failover *UpstreamFailoverError
	if errors.As(err, &failover) {
		if c != nil && c.Writer != nil && !c.Writer.Written() {
			status := failover.StatusCode
			if status < 400 || status > 599 || status == http.StatusUnauthorized || status == http.StatusForbidden {
				status = http.StatusBadGateway
			}
			c.JSON(status, gin.H{"error": gin.H{"type": "upstream_error", "message": "CPR account pool execution failed"}})
		}
		return result, errors.New("CPR account pool execution failed; host retry is disabled")
	}
	return result, err
}

func executeCPRPreparedRequest(request *http.Request, account *Account, binding cprExecutionBinding) (*http.Response, error) {
	if account == nil || account.ID != binding.accountID || binding.service == nil || binding.service.client == nil {
		return nil, errors.New("CPR execution binding does not match the selected pool")
	}
	if binding.attempted == nil || !binding.attempted.CompareAndSwap(false, true) {
		return nil, errors.New("CPR execution already attempted; host replay is disabled")
	}
	prepared, err := binding.service.client.PrepareExecution(request.Context(), request.URL.Path, binding.key.Secret(), request.Header, request.Body)
	if err != nil {
		return nil, err
	}
	prepared.GetBody = request.GetBody
	prepared.ContentLength = request.ContentLength
	recordAPIRequestAuditUpstreamRequest(prepared)
	response, err := binding.service.client.SendExecution(prepared)
	if response != nil && response.Body != nil {
		response.Body = newAPIRequestAuditReadCloser(response.Body, request.Context(), APIRequestAuditDirectionUpstreamResponse, response.Header)
	}
	return response, err
}
