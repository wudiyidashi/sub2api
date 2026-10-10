package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

type managedWSFakeEvent struct {
	body []byte
	err  error
}
type managedWSFakeConn struct {
	events    chan managedWSFakeEvent
	done      chan struct{}
	closeOnce sync.Once
	writeErr  error
	onWrite   func([]byte)
	writes    int
}

func newManagedWSFakeConn() *managedWSFakeConn {
	return &managedWSFakeConn{events: make(chan managedWSFakeEvent, 8), done: make(chan struct{})}
}
func (c *managedWSFakeConn) Read(ctx context.Context) (coderws.MessageType, []byte, error) {
	select {
	case e := <-c.events:
		return coderws.MessageText, e.body, e.err
	case <-c.done:
		return coderws.MessageText, nil, io.EOF
	case <-ctx.Done():
		return coderws.MessageText, nil, ctx.Err()
	}
}
func (c *managedWSFakeConn) Write(_ context.Context, _ coderws.MessageType, body []byte) error {
	c.writes++
	if c.onWrite != nil {
		c.onWrite(body)
	}
	return c.writeErr
}
func (c *managedWSFakeConn) Ping(context.Context) error { return nil }
func (c *managedWSFakeConn) Close() error               { c.closeOnce.Do(func() { close(c.done) }); return nil }
func (c *managedWSFakeConn) CloseNow() error            { return c.Close() }
func (c *managedWSFakeConn) MayHaveSentBusiness() bool  { return c.writes > 0 }
func (c *managedWSFakeConn) UpstreamPingCount() int64   { return 0 }

func enableManagedWSForTest(t *testing.T) {
	t.Helper()
	require.NoError(t, SetAccountDriverRuntimeCapabilities(AccountDriverCodexHTTP, true, "ws-test", []string{"http", "sse", "websocket"}))
	t.Cleanup(func() {
		_ = SetAccountDriverRuntimeCapabilities(AccountDriverCodexHTTP, false, "unconfigured", []string{"http", "sse"})
	})
}

func TestManagedAccountWSCapabilityAndSavedConfiguration(t *testing.T) {
	enableManagedWSForTest(t)
	account := &Account{AccountDriverID: AccountDriverCodexHTTP, Concurrency: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{"openai_oauth_responses_websockets_v2_mode": "ctx_pool"}}
	cfg := newOpenAIWSExecutionScopeTestConfig()
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	require.Equal(t, OpenAIUpstreamTransportResponsesWebsocketV2, NewOpenAIWSProtocolResolver(cfg).Resolve(account).Transport)
	generation := AccountDriverRuntimeGeneration()
	require.NoError(t, SetAccountDriverRuntimeAvailability(AccountDriverCodexHTTP, false, "stopped"))
	require.Greater(t, AccountDriverRuntimeGeneration(), generation)
	require.Contains(t, AccountDriverFromAccount(account).TransportProtocols, "websocket")
	require.Equal(t, OpenAIUpstreamTransportHTTPSSE, NewOpenAIWSProtocolResolver(cfg).Resolve(account).Transport)
	require.NoError(t, validateAccountDriverWSConfigChange(account, map[string]any{"openai_oauth_responses_websockets_v2_mode": "ctx_pool"}))
	require.Error(t, validateAccountDriverWSConfigChange(account, map[string]any{"openai_oauth_responses_websockets_v2_mode": "passthrough"}))
	require.NoError(t, SetAccountDriverRuntimeCapabilities(AccountDriverCodexHTTP, true, "http-old", []string{"http", "sse"}))
	require.NotContains(t, AccountDriverFromAccount(account).TransportProtocols, "websocket")
}

func TestManagedAccountWSDialBindingCannotComeFromHeaders(t *testing.T) {
	enableManagedWSForTest(t)
	native := &openAIWSCountingDialer{}
	fake := newManagedWSFakeConn()
	defer fake.Close()
	calls := 0
	dialer := &managedOpenAIWSClientDialer{native: native, managed: func(_ context.Context, _ string, _ http.Header, _ string, id, revision int64, scope string) (managedAccountWSConnection, int, http.Header, error) {
		calls++
		require.Equal(t, int64(9), id)
		require.Equal(t, int64(3), revision)
		require.Equal(t, strings.Repeat("a", 64), scope)
		return fake, 101, nil, nil
	}}
	ctx := withManagedAccountWSBinding(context.Background(), &Account{ID: 9, AccountDriverID: AccountDriverCodexHTTP, AccountDriverRevision: 3}, strings.Repeat("a", 64))
	_, _, _, err := dialer.Dial(ctx, "wss://upstream/responses", http.Header{"X-Account-Id": []string{"777"}}, "")
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	require.Zero(t, native.DialCount())
	require.NoError(t, SetAccountDriverRuntimeCapabilities(AccountDriverCodexHTTP, true, "http", []string{"http", "sse"}))
	_, _, _, err = dialer.Dial(ctx, "wss://upstream/responses", nil, "")
	require.Error(t, err)
	require.Zero(t, native.DialCount(), "missing WS capability cannot silently use native dialer")
}

func TestManagedAccountWSPoolIsolatesAuthBindingScopeAndGeneration(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 8
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 8
	pool := newOpenAIWSConnPool(cfg)
	defer pool.Close()
	dialer := &openAIWSCountingDialer{}
	pool.setClientDialerForTest(dialer)
	account := &Account{ID: 71, AccountDriverID: AccountDriverCodexHTTP, AccountDriverRevision: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 8}
	base := openAIWSAcquireRequest{Account: account, WSURL: "wss://upstream/responses", Headers: http.Header{"Authorization": []string{"Bearer first"}}, ManagedScope: strings.Repeat("a", 64), ManagedGeneration: 1}
	first, err := pool.Acquire(context.Background(), base)
	require.NoError(t, err)
	id := first.ConnID()
	first.Release()
	same, err := pool.Acquire(context.Background(), base)
	require.NoError(t, err)
	require.Equal(t, id, same.ConnID())
	same.Release()
	changed := cloneOpenAIWSAcquireRequest(base)
	changed.ManagedScope = strings.Repeat("b", 64)
	changed.PreferredConnID = id
	changed.ForcePreferredConn = true
	_, err = pool.Acquire(context.Background(), changed)
	require.True(t, managedWSReplayForbidden(err))
	require.Equal(t, 1, dialer.DialCount(), "cross-key continuation must not dial a replacement")
	for _, mutate := range []func(*openAIWSAcquireRequest){func(r *openAIWSAcquireRequest) { r.Headers.Set("Authorization", "Bearer rotated") }, func(r *openAIWSAcquireRequest) { r.ManagedScope = strings.Repeat("b", 64) }, func(r *openAIWSAcquireRequest) { a := *r.Account; a.AccountDriverRevision++; r.Account = &a }, func(r *openAIWSAcquireRequest) { r.ManagedGeneration++ }, func(r *openAIWSAcquireRequest) { r.ProxyURL = "http://proxy:8080" }} {
		request := cloneOpenAIWSAcquireRequest(base)
		mutate(&request)
		lease, err := pool.Acquire(context.Background(), request)
		require.NoError(t, err)
		require.NotEqual(t, id, lease.ConnID())
		lease.Release()
	}
}

func TestManagedAccountWSSentFactsSurviveTimeoutAndCompletedTurn(t *testing.T) {
	fake := newManagedWSFakeConn()
	defer fake.Close()
	adapter := &managedOpenAIWSClientConn{conn: fake}
	pooled := newOpenAIWSConn("managed", 1, adapter, nil)
	defer pooled.abort()
	require.NoError(t, adapter.WriteJSON(context.Background(), map[string]any{"type": "response.create"}))
	_, err := pooled.readMessageWithContextTimeout(context.Background(), 5*time.Millisecond)
	require.True(t, managedWSMayHaveBeenSent(err))
	require.False(t, isOpenAIWSIngressTurnRetryable(wrapOpenAIWSIngressTurnError("read_upstream", err, false)))
	_, fallback := wrapOpenAIWSFallback("read_event", err).(*openAIWSFallbackError)
	require.False(t, fallback)
	fake = newManagedWSFakeConn()
	defer fake.Close()
	adapter = &managedOpenAIWSClientConn{conn: fake}
	pooled = newOpenAIWSConn("next-managed", 1, adapter, nil)
	defer pooled.abort()
	require.NoError(t, adapter.WriteJSON(context.Background(), map[string]any{"type": "response.create"}))
	fake.events <- managedWSFakeEvent{body: []byte(`{"type":"response.completed","response":{"id":"resp_owned"}}`)}
	_, err = pooled.readMessageWithContextTimeout(context.Background(), time.Second)
	require.NoError(t, err)
	require.False(t, adapter.MayHaveSentBusiness(), "completed prior turn cannot poison a new unsent attempt")
	unknown := adapter.WriteJSON(context.Background(), map[string]any{"type": "response.create", "previous_response_id": "resp_unknown"})
	require.True(t, managedWSReplayForbidden(unknown))
	require.False(t, managedWSMayHaveBeenSent(unknown))
	require.NoError(t, adapter.WriteJSON(context.Background(), map[string]any{"type": "response.create", "previous_response_id": "resp_owned"}))
	fake.events <- managedWSFakeEvent{err: io.ErrUnexpectedEOF}
	_, err = pooled.readMessageWithContextTimeout(context.Background(), time.Second)
	require.True(t, managedWSMayHaveBeenSent(err))
}
