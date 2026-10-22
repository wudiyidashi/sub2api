package service

import (
	"context"
	"errors"
	"net/http"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
)

type cprExecutionWSDialer struct{ binding cprExecutionBinding }

func (d cprExecutionWSDialer) Dial(ctx context.Context, _ string, headers http.Header, _ string) (openAIWSClientConn, int, http.Header, error) {
	conn, status, responseHeaders, err := d.binding.service.client.DialExecutionWebSocket(ctx, d.binding.key.Secret(), headers)
	if err != nil {
		return nil, status, responseHeaders, err
	}
	return &coderOpenAIWSClientConn{conn: conn}, status, responseHeaders, nil
}

func (s *OpenAIGatewayService) proxyCPRWebSocket(ctx context.Context, c *gin.Context, client *websocket.Conn, account *Account, first []byte, hooks *OpenAIWSIngressHooks) error {
	ctx, prepared, err := s.prepareCPRGatewayAccount(ctx, c, account)
	if err != nil {
		return err
	}
	binding, _ := cprBindingFromContext(ctx)
	if _, err := s.prepareCodexAccountIdentitySource(ctx, c, prepared); err != nil {
		return err
	}
	guarded := OpenAIWSIngressHooks{}
	if hooks != nil {
		guarded = *hooks
	}
	beforeRequest := guarded.BeforeRequest
	guarded.BeforeRequest = func(turn int, payload []byte, model string) error {
		if beforeRequest != nil {
			if err := beforeRequest(turn, payload, model); err != nil {
				return err
			}
		}
		// 已打开的连接也不能沿用被停用或扩缩过的授权；新范围需要重新建立内部连接。
		nextCtx, _, err := s.prepareCPRGatewayAccount(ctx, c, account)
		if err != nil {
			return err
		}
		next, _ := cprBindingFromContext(nextCtx)
		if next.key.Name() != binding.key.Name() {
			return errors.New("CPR pool authorization changed; reconnect required")
		}
		return nil
	}
	err = s.proxyResponsesWebSocketV2Passthrough(ctx, c, client, prepared, binding.key.Secret(), first, &guarded, OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2, Reason: "cpr_managed_pool"})
	var failover *UpstreamFailoverError
	if errors.As(err, &failover) {
		return errors.New("CPR websocket execution failed; host retry is disabled")
	}
	return err
}
