package runner

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	api "github.com/Wei-Shaw/sub2api/pkg/accountdriverapi/v1"
	coderws "github.com/coder/websocket"
	"golang.org/x/net/http/httpguts"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

const (
	WebSocketMessageLimit = 16 << 20
	webSocketQueueLimit   = 32 << 20
	webSocketQueueFrames  = 64
)

type WebSocketError struct {
	Code  string
	Sent  bool
	Cause error
}

func (e *WebSocketError) Error() string         { return "account driver websocket: " + e.Code }
func (e *WebSocketError) Unwrap() error         { return e.Cause }
func (e *WebSocketError) MayHaveBeenSent() bool { return e.Sent }

type WebSocketOptions struct {
	AccountID        int64
	BindingRevision  int64
	KeyScopeHash     string
	HandshakeTimeout time.Duration
}

type wsMessage struct {
	kind coderws.MessageType
	data []byte
}

// WebSocketConn exposes application messages; the host keeps connection pooling,
// response ownership, admission and billing in its existing WS execution path.
type WebSocketConn struct {
	stream            grpc.BidiStreamingClient[api.WebSocketCommand, api.WebSocketEvent]
	cancel            context.CancelFunc
	cleanup           func()
	done              chan struct{}
	messages          chan wsMessage
	sendGate          chan struct{}
	mu                sync.Mutex
	writeAcks         map[uint64]chan error
	pongs             map[string]chan struct{}
	err               error
	seq               atomic.Uint64
	businessSent      atomic.Bool
	pings             atomic.Int64
	queuedBytes       atomic.Int64
	runtimeGeneration atomic.Uint64
	closeOnce         sync.Once
}

func (c *Client) SupportsWebSocket() bool {
	return c.Available() && slices.Contains(c.Info().Transports, "websocket")
}

func (c *Client) DialWebSocket(ctx context.Context, url string, headers http.Header, proxyURL string, options WebSocketOptions) (*WebSocketConn, int, http.Header, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, nil, &WebSocketError{Code: "handshake_cancelled", Cause: err}
	}
	if !c.SupportsWebSocket() {
		return nil, 0, nil, &WebSocketError{Code: "unavailable"}
	}
	if options.AccountID <= 0 || options.BindingRevision <= 0 || len(options.KeyScopeHash) != 64 {
		return nil, 0, nil, &WebSocketError{Code: "invalid_binding_scope"}
	}
	if _, err := hex.DecodeString(options.KeyScopeHash); err != nil {
		return nil, 0, nil, &WebSocketError{Code: "invalid_binding_scope"}
	}
	authorization := headers.Values("Authorization")
	if len(authorization) != 1 || authorization[0] == "" {
		return nil, 0, nil, &WebSocketError{Code: "invalid_credential_identity"}
	}
	fingerprint := sha256.Sum256([]byte(authorization[0]))
	sessionKey := make([]byte, 16)
	if _, err := rand.Read(sessionKey); err != nil {
		return nil, 0, nil, err
	}
	sessionID := hex.EncodeToString(sessionKey)
	requestID := "ws_" + sessionID
	if options.HandshakeTimeout <= 0 {
		options.HandshakeTimeout = 15 * time.Second
	}
	dialCtx, dialCancel := context.WithTimeout(ctx, options.HandshakeTimeout)
	defer dialCancel()
	// Dial's deadline ends with the handshake. The RPC is owned by the connection
	// afterwards, because native connection-pool callers cancel their dial context.
	lifetime, cancel := context.WithCancel(context.WithoutCancel(ctx))
	lifetime = metadata.AppendToOutgoingContext(lifetime, "authorization", c.token)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		cancel()
		return nil, 0, nil, &WebSocketError{Code: "unavailable"}
	}
	c.identities[sessionID] = identity{requestID: requestID, accountID: options.AccountID, bindingRevision: options.BindingRevision, headers: headersToWire(headers, true), proxyURL: proxyURL, sessionID: sessionID, keyScopeHash: options.KeyScopeHash, credentialFingerprint: hex.EncodeToString(fingerprint[:])}
	client := c.api
	c.mu.Unlock()
	cleanup := func() { cancel(); c.mu.Lock(); delete(c.identities, sessionID); c.mu.Unlock() }
	handshakeDone := make(chan struct{})
	var handshakeMu sync.Mutex
	handshakeComplete := false
	go func() {
		select {
		case <-dialCtx.Done():
			handshakeMu.Lock()
			if !handshakeComplete {
				cancel()
			}
			handshakeMu.Unlock()
		case <-handshakeDone:
		}
	}()
	defer func() { handshakeMu.Lock(); handshakeComplete = true; close(handshakeDone); handshakeMu.Unlock() }()
	stream, err := client.OpenWebSocket(lifetime, grpc.MaxCallRecvMsgSize(WebSocketMessageLimit+(1<<20)), grpc.MaxCallSendMsgSize(WebSocketMessageLimit+(1<<20)))
	if err != nil {
		cleanup()
		return nil, 0, nil, &WebSocketError{Code: "connect", Cause: err}
	}
	start := &api.WebSocketStart{RequestId: requestID, SessionId: sessionID, AccountId: options.AccountID, BindingRevision: options.BindingRevision, KeyScopeHash: options.KeyScopeHash, CredentialFingerprint: hex.EncodeToString(fingerprint[:]), Url: url, Headers: headersToWire(headers, false), ProxyUrl: proxyURL, HandshakeTimeoutMs: options.HandshakeTimeout.Milliseconds()}
	if err = stream.Send(&api.WebSocketCommand{Frame: &api.WebSocketCommand_Start{Start: start}}); err != nil {
		cleanup()
		return nil, 0, nil, &WebSocketError{Code: "start", Cause: err}
	}
	permitted := false
	var rejectedStatus int
	var responseHeaders http.Header
	for {
		event, receiveErr := stream.Recv()
		if receiveErr != nil {
			cleanup()
			return nil, 0, nil, &WebSocketError{Code: "handshake_interrupted", Cause: receiveErr}
		}
		if event.SendState != api.SendState_SEND_STATE_NOT_SENT {
			cleanup()
			return nil, 0, nil, &WebSocketError{Code: "invalid_handshake_send_state"}
		}
		switch frame := event.Frame.(type) {
		case *api.WebSocketEvent_Ready:
			if permitted {
				cleanup()
				return nil, 0, nil, &WebSocketError{Code: "invalid_ready"}
			}
			permitted = true
			if err = stream.Send(&api.WebSocketCommand{Frame: &api.WebSocketCommand_PermitConnect{PermitConnect: true}}); err != nil {
				cleanup()
				return nil, 0, nil, &WebSocketError{Code: "permit_connect", Cause: err}
			}
		case *api.WebSocketEvent_Handshake:
			if frame.Handshake == nil || responseHeaders != nil {
				cleanup()
				return nil, 0, nil, &WebSocketError{Code: "invalid_handshake"}
			}
			responseHeaders = make(http.Header)
			for _, header := range frame.Handshake.Headers {
				if header == nil || !httpguts.ValidHeaderFieldName(header.Name) || !httpguts.ValidHeaderFieldValue(string(header.Value)) {
					cleanup()
					return nil, 0, nil, &WebSocketError{Code: "invalid_handshake_header"}
				}
				responseHeaders.Add(header.Name, string(header.Value))
			}
			if !permitted || frame.Handshake.StatusCode < 100 || frame.Handshake.StatusCode > 599 {
				cleanup()
				return nil, int(frame.Handshake.StatusCode), responseHeaders, &WebSocketError{Code: "invalid_handshake"}
			}
			if frame.Handshake.StatusCode != http.StatusSwitchingProtocols {
				rejectedStatus = int(frame.Handshake.StatusCode)
				continue // The following failure carries the rejected handshake body.
			}
			handshakeMu.Lock()
			handshakeComplete = true
			handshakeMu.Unlock()
			if lifetime.Err() != nil || dialCtx.Err() != nil {
				cleanup()
				return nil, 0, nil, &WebSocketError{Code: "handshake_cancelled", Cause: dialCtx.Err()}
			}
			conn := &WebSocketConn{stream: stream, cancel: cancel, cleanup: cleanup, done: make(chan struct{}), messages: make(chan wsMessage, webSocketQueueFrames), writeAcks: make(map[uint64]chan error), pongs: make(map[string]chan struct{}), sendGate: make(chan struct{}, 1)}
			go conn.receive()
			return conn, int(frame.Handshake.StatusCode), responseHeaders, nil
		case *api.WebSocketEvent_Failure:
			cleanup()
			status := int(frame.Failure.UpstreamStatus)
			if rejectedStatus != 0 {
				status = rejectedStatus
			}
			return nil, status, responseHeaders, &WebSocketError{Code: safeFailureCode(frame.Failure.Code, "handshake_failed"), Cause: &HandshakeError{Status: status, Body: slices.Clone(frame.Failure.RawBody)}}
		default:
			cleanup()
			return nil, 0, nil, &WebSocketError{Code: "invalid_handshake_event"}
		}
	}
}

type HandshakeError struct {
	Status int
	Body   []byte
}

func (*HandshakeError) Error() string { return "managed websocket handshake rejected" }

func (c *WebSocketConn) finish(err error) {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.err = err
		close(c.done)
		c.mu.Unlock()
		c.cleanup()
	})
}
func (c *WebSocketConn) terminalError() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	return io.EOF
}
func (c *WebSocketConn) MayHaveSentBusiness() bool { return c.businessSent.Load() }
func (c *WebSocketConn) UpstreamPingCount() int64  { return c.pings.Load() }

func (c *WebSocketConn) receive() {
	for {
		event, err := c.stream.Recv()
		if err != nil {
			c.finish(&WebSocketError{Code: "connection_interrupted", Sent: c.businessSent.Load(), Cause: err})
			return
		}
		if event.SendState != api.SendState_SEND_STATE_NOT_SENT && event.SendState != api.SendState_SEND_STATE_AMBIGUOUS && event.SendState != api.SendState_SEND_STATE_SENT {
			c.finish(&WebSocketError{Code: "invalid_send_state", Sent: c.businessSent.Load()})
			return
		}
		switch frame := event.Frame.(type) {
		case *api.WebSocketEvent_Message:
			message := frame.Message
			switch message.Kind {
			case api.WebSocketMessageKind_WEB_SOCKET_MESSAGE_KIND_TEXT, api.WebSocketMessageKind_WEB_SOCKET_MESSAGE_KIND_BINARY:
				if len(message.Data) > WebSocketMessageLimit {
					c.finish(&WebSocketError{Code: "message_too_large", Sent: c.businessSent.Load()})
					return
				}
				kind := coderws.MessageText
				if message.Kind == api.WebSocketMessageKind_WEB_SOCKET_MESSAGE_KIND_BINARY {
					kind = coderws.MessageBinary
				}
				if c.queuedBytes.Add(int64(len(message.Data))) > webSocketQueueLimit {
					c.queuedBytes.Add(-int64(len(message.Data)))
					c.finish(&WebSocketError{Code: "reader_backpressure", Sent: c.businessSent.Load()})
					return
				}
				select {
				case c.messages <- wsMessage{kind: kind, data: slices.Clone(message.Data)}:
				case <-c.done:
					c.queuedBytes.Add(-int64(len(message.Data)))
					return
				default:
					// Recv must remain available for ACK/Pong. A stalled consumer
					// gets a bounded, explicit failure instead of blocking controls.
					c.queuedBytes.Add(-int64(len(message.Data)))
					c.finish(&WebSocketError{Code: "reader_backpressure", Sent: c.businessSent.Load()})
					return
				}
			case api.WebSocketMessageKind_WEB_SOCKET_MESSAGE_KIND_PING:
				if len(message.Data) > 125 {
					c.finish(&WebSocketError{Code: "invalid_control_message", Sent: c.businessSent.Load()})
					return
				}
				c.pings.Add(1)
			case api.WebSocketMessageKind_WEB_SOCKET_MESSAGE_KIND_PONG:
				if len(message.Data) > 125 {
					c.finish(&WebSocketError{Code: "invalid_control_message", Sent: c.businessSent.Load()})
					return
				}
				c.mu.Lock()
				pong := c.pongs[string(message.Data)]
				delete(c.pongs, string(message.Data))
				c.mu.Unlock()
				if pong != nil {
					close(pong)
				}
			default:
				c.finish(&WebSocketError{Code: "invalid_message_kind", Sent: c.businessSent.Load()})
				return
			}
		case *api.WebSocketEvent_WriteAck:
			c.mu.Lock()
			ack := c.writeAcks[frame.WriteAck.Sequence]
			delete(c.writeAcks, frame.WriteAck.Sequence)
			c.mu.Unlock()
			if ack == nil {
				c.finish(&WebSocketError{Code: "invalid_write_ack", Sent: c.businessSent.Load()})
				return
			}
			ack <- nil
		case *api.WebSocketEvent_Closed:
			c.finish(&WebSocketError{Code: "peer_closed", Sent: c.businessSent.Load(), Cause: coderws.CloseError{Code: coderws.StatusCode(frame.Closed.Code), Reason: frame.Closed.Reason}})
			return
		case *api.WebSocketEvent_Failure:
			c.finish(&WebSocketError{Code: safeFailureCode(frame.Failure.Code, "connection_failed"), Sent: c.businessSent.Load()})
			return
		default:
			c.finish(&WebSocketError{Code: "invalid_connection_event", Sent: c.businessSent.Load()})
			return
		}
	}
}

func (c *WebSocketConn) Read(ctx context.Context) (coderws.MessageType, []byte, error) {
	select {
	case message := <-c.messages:
		c.queuedBytes.Add(-int64(len(message.data)))
		return message.kind, message.data, nil
	case <-c.done:
		select {
		case message := <-c.messages:
			c.queuedBytes.Add(-int64(len(message.data)))
			return message.kind, message.data, nil
		default:
			return coderws.MessageText, nil, c.terminalError()
		}
	case <-ctx.Done():
		c.finish(&WebSocketError{Code: "read_cancelled", Sent: c.businessSent.Load(), Cause: ctx.Err()})
		return coderws.MessageText, nil, ctx.Err()
	}
}

func (c *WebSocketConn) write(ctx context.Context, kind api.WebSocketMessageKind, data []byte, business bool) error {
	if len(data) > WebSocketMessageLimit || !business && len(data) > 125 {
		return &WebSocketError{Code: "message_too_large"}
	}
	// A business write that has not crossed Send is a new operation, even on
	// a connection whose earlier completed turns were sent. Control failures
	// keep the connection watermark and cannot erase an in-flight turn.
	notSent := func(code string, cause error) error {
		return &WebSocketError{Code: code, Sent: !business && c.businessSent.Load(), Cause: cause}
	}
	select {
	case <-ctx.Done():
		return notSent("write_cancelled", ctx.Err())
	case <-c.done:
		return notSent("write_not_sent", c.terminalError())
	default:
	}
	select {
	case c.sendGate <- struct{}{}:
	case <-ctx.Done():
		return notSent("write_cancelled", ctx.Err())
	case <-c.done:
		return notSent("write_not_sent", c.terminalError())
	}
	defer func() { <-c.sendGate }()
	// Assign sequence only after admission. A cancelled waiter must not create a
	// gap in the driver's strict, connection-local message sequence.
	select {
	case <-ctx.Done():
		return notSent("write_cancelled", ctx.Err())
	case <-c.done:
		return notSent("write_not_sent", c.terminalError())
	default:
	}
	sequence := c.seq.Add(1)
	ack := make(chan error, 1)
	c.mu.Lock()
	c.writeAcks[sequence] = ack
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.writeAcks, sequence); c.mu.Unlock() }()
	if business {
		c.businessSent.Store(true)
	}
	sent := make(chan error, 1)
	go func() {
		sent <- c.stream.Send(&api.WebSocketCommand{Frame: &api.WebSocketCommand_Message{Message: &api.WebSocketMessage{Sequence: sequence, Kind: kind, Data: slices.Clone(data)}}})
	}()
	var err error
	select {
	case err = <-sent:
	case <-ctx.Done():
		c.finish(&WebSocketError{Code: "write_cancelled", Sent: c.businessSent.Load(), Cause: ctx.Err()})
		return c.terminalError()
	case <-c.done:
		return c.terminalError()
	}
	if err != nil {
		c.finish(&WebSocketError{Code: "write_interrupted", Sent: c.businessSent.Load(), Cause: err})
		return c.terminalError()
	}
	select {
	case err = <-ack:
		return err
	case <-c.done:
		return c.terminalError()
	case <-ctx.Done():
		c.finish(&WebSocketError{Code: "write_cancelled", Sent: c.businessSent.Load(), Cause: ctx.Err()})
		return c.terminalError()
	}
}

func (c *WebSocketConn) Write(ctx context.Context, kind coderws.MessageType, data []byte) error {
	switch kind {
	case coderws.MessageText:
		if !utf8.Valid(data) {
			return &WebSocketError{Code: "invalid_text"}
		}
		return c.write(ctx, api.WebSocketMessageKind_WEB_SOCKET_MESSAGE_KIND_TEXT, data, true)
	case coderws.MessageBinary:
		return c.write(ctx, api.WebSocketMessageKind_WEB_SOCKET_MESSAGE_KIND_BINARY, data, true)
	default:
		return &WebSocketError{Code: "unsupported_message_kind"}
	}
}
func (c *WebSocketConn) Ping(ctx context.Context) error {
	data := make([]byte, 8)
	if _, err := rand.Read(data); err != nil {
		return err
	}
	pong := make(chan struct{})
	c.mu.Lock()
	c.pongs[string(data)] = pong
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pongs, string(data)); c.mu.Unlock() }()
	if err := c.write(ctx, api.WebSocketMessageKind_WEB_SOCKET_MESSAGE_KIND_PING, data, false); err != nil {
		return err
	}
	select {
	case <-pong:
		return nil
	case <-c.done:
		return c.terminalError()
	case <-ctx.Done():
		c.finish(&WebSocketError{Code: "ping_cancelled", Sent: c.businessSent.Load(), Cause: ctx.Err()})
		return c.terminalError()
	}
}
func (c *WebSocketConn) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	select {
	case <-c.done:
		return nil
	case c.sendGate <- struct{}{}:
		select {
		case <-c.done:
			<-c.sendGate
			return nil
		default:
		}
		sent := make(chan error, 1)
		go func() {
			sent <- c.stream.Send(&api.WebSocketCommand{Frame: &api.WebSocketCommand_Close{Close: &api.WebSocketClose{Code: 1000}}})
		}()
		select {
		case <-sent:
		case <-ctx.Done():
			c.finish(&WebSocketError{Code: "closed", Sent: c.businessSent.Load()})
		}
		<-c.sendGate
	case <-ctx.Done():
	}
	select {
	case <-c.done:
	case <-ctx.Done():
	}
	c.finish(&WebSocketError{Code: "closed", Sent: c.businessSent.Load()})
	return nil
}
func (c *WebSocketConn) CloseNow() error {
	c.finish(&WebSocketError{Code: "closed", Sent: c.businessSent.Load()})
	return nil
}

var _ interface{ MayHaveBeenSent() bool } = (*WebSocketError)(nil)
