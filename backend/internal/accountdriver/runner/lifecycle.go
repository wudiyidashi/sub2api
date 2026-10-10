package runner

import (
	"context"
	"errors"

	api "github.com/Wei-Shaw/sub2api/pkg/accountdriverapi/v1"
	"google.golang.org/grpc/metadata"
)

// Done signals process or socket termination without changing its lifetime.
func (c *Client) Done() <-chan struct{}        { return c.done }
func (c *WebSocketConn) Done() <-chan struct{} { return c.done }
func (c *WebSocketConn) SetRuntimeGeneration(generation uint64) {
	c.runtimeGeneration.Store(generation)
}
func (c *WebSocketConn) RuntimeGeneration() uint64 { return c.runtimeGeneration.Load() }

// ValidateContract exercises both negotiated RPCs with invalid, unsent input.
// It does not resolve credentials, contact an upstream, or send model requests.
func (c *Client) ValidateContract(ctx context.Context) error {
	if !c.Available() {
		return errors.New("account driver candidate is unavailable")
	}
	ctx, cancel := context.WithCancel(metadata.AppendToOutgoingContext(ctx, "authorization", c.token))
	defer cancel()
	stream, err := c.api.Execute(ctx)
	if err != nil {
		return errors.New("account driver HTTP contract is unavailable")
	}
	if err = stream.Send(&api.ExecuteCommand{Frame: &api.ExecuteCommand_Start{Start: &api.ExecuteStart{RequestId: "runtime_probe", AttemptId: "runtime_probe", Method: "UNSUPPORTED"}}}); err != nil {
		return errors.New("account driver HTTP contract is unavailable")
	}
	event, err := stream.Recv()
	_ = stream.CloseSend()
	if err != nil || event.SendState != api.SendState_SEND_STATE_NOT_SENT || event.GetFailure() == nil || event.GetFailure().Code != "unsupported_method" {
		return errors.New("account driver HTTP contract is incompatible")
	}
	if !c.SupportsWebSocket() {
		return nil
	}
	ws, err := c.api.OpenWebSocket(ctx)
	if err != nil {
		return errors.New("account driver WebSocket contract is unavailable")
	}
	if err = ws.Send(&api.WebSocketCommand{Frame: &api.WebSocketCommand_Start{Start: &api.WebSocketStart{RequestId: "runtime_probe"}}}); err != nil {
		return errors.New("account driver WebSocket contract is unavailable")
	}
	wsEvent, err := ws.Recv()
	_ = ws.CloseSend()
	if err != nil || wsEvent.SendState != api.SendState_SEND_STATE_NOT_SENT || wsEvent.GetFailure() == nil || wsEvent.GetFailure().Code != "invalid_scope" {
		return errors.New("account driver WebSocket contract is incompatible")
	}
	return nil
}
