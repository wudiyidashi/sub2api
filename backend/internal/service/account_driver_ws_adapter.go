package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/Wei-Shaw/sub2api/internal/accountdriver"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

type managedAccountWSBindingKey struct{}
type managedAccountWSBinding struct {
	accountID int64
	driverID  string
	revision  int64
	scope     string
}

func withManagedAccountWSBinding(ctx context.Context, account *Account, scope string) context.Context {
	if account == nil {
		return ctx
	}
	return context.WithValue(ctx, managedAccountWSBindingKey{}, managedAccountWSBinding{account.ID, account.EffectiveAccountDriverID(), account.AccountDriverRevision, scope})
}

// The API key ID is trusted middleware state. Client thread/session identifiers
// only select a lane inside that key; headers never provide an account identity.
func managedAccountWSScope(c *gin.Context, body []byte, session string) string {
	keyID := getAPIKeyIDFromContext(c)
	if execution, _ := resolveOpenAIWSExecutionScope(c, body, keyID); execution != "" {
		session = execution
	}
	if session == "" {
		session = uuid.NewString()
	}
	seed := fmt.Sprintf("key=%d|session=%s", keyID, session)
	digest := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(digest[:])
}

type managedAccountWSConnection interface {
	Read(context.Context) (coderws.MessageType, []byte, error)
	Write(context.Context, coderws.MessageType, []byte) error
	Ping(context.Context) error
	Close() error
	CloseNow() error
	MayHaveSentBusiness() bool
	UpstreamPingCount() int64
}

type managedAccountWSDialFunc func(context.Context, string, http.Header, string, int64, int64, string) (managedAccountWSConnection, int, http.Header, error)

type managedOpenAIWSClientDialer struct {
	native  openAIWSClientDialer
	managed managedAccountWSDialFunc
}

func (d *managedOpenAIWSClientDialer) Dial(ctx context.Context, url string, headers http.Header, proxy string) (openAIWSClientConn, int, http.Header, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	binding, exists := ctx.Value(managedAccountWSBindingKey{}).(managedAccountWSBinding)
	if !exists || binding.driverID == accountdriver.NativeID {
		return d.native.Dial(ctx, url, headers, proxy)
	}
	if binding.driverID != AccountDriverCodexHTTP || binding.accountID <= 0 || binding.scope == "" || !accountDriverWebSocketAvailable(&Account{AccountDriverID: binding.driverID}) {
		return nil, 0, nil, errors.New("managed account websocket capability or trusted binding is unavailable")
	}
	if d.managed == nil {
		return nil, 0, nil, errors.New("managed account websocket runtime is unavailable")
	}
	conn, status, responseHeaders, err := d.managed(ctx, url, headers, proxy, binding.accountID, binding.revision, binding.scope)
	if err != nil {
		return nil, status, responseHeaders, err
	}
	if conn == nil {
		return nil, status, responseHeaders, errors.New("managed websocket returned no connection")
	}
	tracker, _ := ctx.Value(accountDriverAttemptTrackerKey{}).(*accountDriverAttemptTracker)
	return &managedOpenAIWSClientConn{conn: conn, tracker: tracker}, status, responseHeaders, nil
}

func (s *OpenAIGatewayService) managedWSDialer(native openAIWSClientDialer) openAIWSClientDialer {
	return &managedOpenAIWSClientDialer{native: native, managed: func(ctx context.Context, url string, h http.Header, proxy string, id, revision int64, scope string) (managedAccountWSConnection, int, http.Header, error) {
		if s.pluginManager == nil || s.pluginManager.accountDriverManager == nil {
			return nil, 0, nil, errors.New("managed websocket runtime is unavailable")
		}
		conn, status, headers, err := s.pluginManager.accountDriverManager.DialManagedWebSocket(ctx, url, h, proxy, id, revision, scope)
		if conn == nil {
			return nil, status, headers, err
		}
		return conn, status, headers, err
	}}
}

// A typed transport fact survives all native wrappers and prevents generic
// reconnection/failover from replaying a turn after an ambiguous WS send.
type managedWSMaySentError struct{ err error }

func (e *managedWSMaySentError) Error() string         { return e.err.Error() }
func (e *managedWSMaySentError) Unwrap() error         { return e.err }
func (e *managedWSMaySentError) MayHaveBeenSent() bool { return true }

type managedWSIdleError struct{ err error }

func (e *managedWSIdleError) Error() string         { return e.err.Error() }
func (e *managedWSIdleError) Unwrap() error         { return e.err }
func (e *managedWSIdleError) MayHaveBeenSent() bool { return false }
func managedWSMayHaveBeenSent(err error) bool {
	var fact interface{ MayHaveBeenSent() bool }
	return errors.As(err, &fact) && fact.MayHaveBeenSent()
}

type managedWSContinuationError struct{ err error }

func (e *managedWSContinuationError) Error() string {
	return "managed websocket continuation requires its original connection; replay full input without previous_response_id: " + e.err.Error()
}
func (e *managedWSContinuationError) Unwrap() error      { return e.err }
func (e *managedWSContinuationError) ForbidReplay() bool { return true }
func managedWSReplayForbidden(err error) bool {
	var constraint interface{ ForbidReplay() bool }
	return managedWSMayHaveBeenSent(err) || (errors.As(err, &constraint) && constraint.ForbidReplay())
}

func (s *OpenAIGatewayService) managedWSHTTPContinuationError(account *Account, previous string) error {
	if !usesCodexHTTPDriver(account) || previous == "" {
		return nil
	}
	known := false
	if store := s.getOpenAIWSStateStore(); store != nil {
		_, known = store.GetResponseConn(previous)
	}
	mode := account.ResolveOpenAIResponsesWebSocketV2Mode(OpenAIWSIngressModeOff)
	if known || (mode != OpenAIWSIngressModeOff && mode != OpenAIWSIngressModeHTTPBridge) {
		return &managedWSContinuationError{errors.New("websocket capability or policy no longer permits the original transport")}
	}
	return nil
}

type managedOpenAIWSClientConn struct {
	conn          managedAccountWSConnection
	turnSent      atomic.Bool
	tracker       *accountDriverAttemptTracker
	responsesMu   sync.Mutex
	responses     map[string]struct{}
	responseOrder []string
}

func (c *managedOpenAIWSClientConn) WriteJSON(ctx context.Context, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return c.WriteFrame(ctx, coderws.MessageText, body)
}
func (c *managedOpenAIWSClientConn) WriteFrame(ctx context.Context, kind coderws.MessageType, body []byte) error {
	if previous := gjson.GetBytes(body, "previous_response_id").String(); previous != "" {
		c.responsesMu.Lock()
		_, known := c.responses[previous]
		c.responsesMu.Unlock()
		if !known {
			return &managedWSContinuationError{errors.New("managed websocket continuation requires its original live connection; replay full input without previous_response_id")}
		}
	}
	tracker := c.tracker
	if ctx != nil {
		if current, _ := ctx.Value(accountDriverAttemptTrackerKey{}).(*accountDriverAttemptTracker); current != nil {
			tracker = current
		}
	}
	control := gjson.GetBytes(body, "type").String()
	business := control != "response.cancel" && control != "session.update"
	previous := c.turnSent.Load()
	if business {
		c.turnSent.Store(true)
	}
	err := c.conn.Write(ctx, kind, body)
	if err != nil {
		var fact interface{ MayHaveBeenSent() bool }
		if business && errors.As(err, &fact) && !fact.MayHaveBeenSent() && !previous {
			c.turnSent.Store(false)
		}
		if c.turnSent.Load() {
			if tracker != nil {
				tracker.sent.Store(true)
			}
			return &managedWSMaySentError{err}
		}
		return err
	}
	if business && tracker != nil {
		tracker.sent.Store(true)
	}
	return nil
}
func (c *managedOpenAIWSClientConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	kind, body, err := c.conn.Read(ctx)
	if err == nil {
		id := gjson.GetBytes(body, "response.id").String()
		if id == "" {
			id = gjson.GetBytes(body, "response_id").String()
		}
		if id != "" {
			c.responsesMu.Lock()
			if c.responses == nil {
				c.responses = make(map[string]struct{})
			}
			if _, known := c.responses[id]; !known {
				c.responses[id] = struct{}{}
				c.responseOrder = append(c.responseOrder, id)
				if len(c.responseOrder) > 1024 {
					delete(c.responses, c.responseOrder[0])
					c.responseOrder = c.responseOrder[1:]
				}
			}
			c.responsesMu.Unlock()
		}
	}
	if err != nil && c.turnSent.Load() {
		return kind, body, &managedWSMaySentError{err}
	}
	if err != nil {
		return kind, body, &managedWSIdleError{err}
	}
	if err == nil && isOpenAIWSTerminalEvent(gjson.GetBytes(body, "type").String()) {
		c.turnSent.Store(false)
	}
	return kind, body, err
}
func (c *managedOpenAIWSClientConn) ReadMessage(ctx context.Context) ([]byte, error) {
	_, body, err := c.ReadFrame(ctx)
	return body, err
}
func (c *managedOpenAIWSClientConn) Ping(ctx context.Context) error      { return c.conn.Ping(ctx) }
func (c *managedOpenAIWSClientConn) Close() error                        { return c.conn.Close() }
func (c *managedOpenAIWSClientConn) CloseNow() error                     { return c.conn.CloseNow() }
func (c *managedOpenAIWSClientConn) RequiresReaderLoop() bool            { return true }
func (c *managedOpenAIWSClientConn) SupportsIdlePingWithoutReader() bool { return true }
func (c *managedOpenAIWSClientConn) UpstreamPingCount() int64            { return c.conn.UpstreamPingCount() }
func (c *managedOpenAIWSClientConn) MayHaveSentBusiness() bool           { return c.turnSent.Load() }

func normalizeManagedWSCompatibility(req openAIWSAcquireRequest) openAIWSHandshakeCompatibilityKey {
	key := normalizeOpenAIWSHandshakeCompatibility(req.Account, req.Headers)
	if usesCodexHTTPDriver(req.Account) {
		key.managedScope = req.ManagedScope
		key.managedDriver = req.Account.EffectiveAccountDriverID()
		key.managedRevision = req.Account.AccountDriverRevision
		key.managedGeneration = req.ManagedGeneration
		key.managedURL = req.WSURL
		proxyHash := sha256.Sum256([]byte(req.ProxyURL))
		key.managedProxy = hex.EncodeToString(proxyHash[:])
		digest := sha256.Sum256([]byte(req.Headers.Get("Authorization")))
		key.managedAuth = hex.EncodeToString(digest[:])
	}
	return key
}

// Keep transport metrics from the unchanged native implementation.
func (d *managedOpenAIWSClientDialer) SnapshotTransportMetrics() OpenAIWSTransportMetricsSnapshot {
	if m, ok := d.native.(openAIWSTransportMetricsDialer); ok {
		return m.SnapshotTransportMetrics()
	}
	return OpenAIWSTransportMetricsSnapshot{}
}
