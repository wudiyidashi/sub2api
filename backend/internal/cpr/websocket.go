package cpr

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/coder/websocket"
)

func (c *Client) WebSocketURL() string {
	target := *c.base
	if target.Scheme == "https" {
		target.Scheme = "wss"
	} else {
		target.Scheme = "ws"
	}
	target.Path = "/v1/responses"
	return target.String()
}

// DialExecutionWebSocket 只连接同一内部 CPR 数据面，不继承出站代理或浏览器认证。
func (c *Client) DialExecutionWebSocket(ctx context.Context, secret string, source http.Header) (*websocket.Conn, int, http.Header, error) {
	if secret == "" || strings.ContainsAny(secret, "\r\n") {
		return nil, 0, nil, errors.New("invalid CPR execution authorization")
	}
	headers := dataHeaders(source)
	headers.Set("Authorization", "Bearer "+secret)
	headers.Set("OpenAI-Beta", "responses_websockets=2025-02-09")
	conn, response, err := websocket.Dial(ctx, c.WebSocketURL(), &websocket.DialOptions{HTTPClient: c.data, HTTPHeader: headers, CompressionMode: websocket.CompressionContextTakeover})
	status, responseHeaders := 0, http.Header(nil)
	if response != nil {
		status, responseHeaders = response.StatusCode, response.Header.Clone()
	}
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		if ctx.Err() != nil {
			return nil, status, responseHeaders, ctx.Err()
		}
		return nil, status, responseHeaders, errors.New("CPR websocket execution is unavailable")
	}
	conn.SetReadLimit(16 << 20)
	return conn, status, responseHeaders, nil
}
