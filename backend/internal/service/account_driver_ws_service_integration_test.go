//go:build unit

package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func enableManagedForwardWS(svc *OpenAIGatewayService, account *Account) {
	svc.cfg.Gateway.OpenAIWS.Enabled = true
	svc.cfg.Gateway.OpenAIWS.OAuthEnabled = true
	svc.cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	svc.cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	svc.cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 2
	svc.cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 2
	account.Extra = map[string]any{"openai_oauth_responses_websockets_v2_mode": "ctx_pool"}
}

func TestManagedCodexWSServiceWSSConnectAndContinuation(t *testing.T) {
	var creates atomic.Int32
	svc, account, native, connects := accountDriverForwardFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer fake-access-token", r.Header.Get("Authorization"))
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		for {
			_, body, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			if gjson.GetBytes(body, "type").String() != "response.create" {
				continue
			}
			turn := creates.Add(1)
			if turn == 2 {
				require.Equal(t, "resp_managed_ws_1", gjson.GetBytes(body, "previous_response_id").String())
			}
			message := fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_managed_ws_%d","status":"completed","model":"gpt-5.5","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"managed turn"}]}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}}`, turn)
			if err := conn.Write(r.Context(), coderws.MessageText, []byte(message)); err != nil {
				return
			}
		}
	}))
	enableManagedForwardWS(svc, account)
	t.Cleanup(func() { svc.getOpenAIWSConnPool().Close() })
	for turn, body := range [][]byte{[]byte(`{"model":"gpt-5.5","input":"one","stream":false,"store":false}`), []byte(`{"model":"gpt-5.5","input":"two","stream":false,"store":false,"previous_response_id":"resp_managed_ws_1"}`)} {
		c, _ := accountDriverForwardGinContext(body)
		c.Set("api_key", &APIKey{ID: 7})
		c.Request.Header.Set("session_id", "managed-ws-session")
		c.Request.Header.Set("thread-id", "managed-ws-thread")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		result, err := svc.Forward(ctx, c, account, body)
		cancel()
		require.NoError(t, err, "turn %d", turn+1)
		require.Equal(t, 2, result.Usage.InputTokens)
		require.Equal(t, 3, result.Usage.OutputTokens)
	}
	require.EqualValues(t, 2, creates.Load())
	require.EqualValues(t, 1, connects.Load(), "native host pool must reuse the same CPR socket")
	require.Zero(t, native.calls.Load())
	for _, policy := range []string{"capability_lost", "force_http"} {
		if policy == "capability_lost" {
			require.NoError(t, SetAccountDriverRuntimeCapabilities(AccountDriverCodexHTTP, true, "http-only", []string{"http", "sse"}))
		} else {
			require.NoError(t, SetAccountDriverRuntimeCapabilities(AccountDriverCodexHTTP, true, "ws", []string{"http", "sse", "websocket"}))
			svc.cfg.Gateway.OpenAIWS.ForceHTTP = true
		}
		body := []byte(`{"model":"gpt-5.5","input":"must not replay","stream":false,"previous_response_id":"resp_managed_ws_2"}`)
		c, rec := accountDriverForwardGinContext(body)
		c.Set("api_key", &APIKey{ID: 7})
		c.Request.Header.Set("session_id", "managed-ws-session")
		c.Request.Header.Set("thread-id", "managed-ws-thread")
		_, err := svc.Forward(context.Background(), c, account, body)
		require.True(t, managedWSReplayForbidden(err), policy)
		require.Equal(t, http.StatusBadRequest, rec.Code, policy)
		require.EqualValues(t, 2, creates.Load())
		require.EqualValues(t, 1, connects.Load())
		require.Zero(t, native.calls.Load())
	}
}

func TestManagedCodexWSServiceSentDisconnectHasNoReplay(t *testing.T) {
	var creates atomic.Int32
	svc, account, native, connects := accountDriverForwardFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		_, _, err = conn.Read(r.Context())
		if err == nil {
			creates.Add(1)
		}
	}))
	enableManagedForwardWS(svc, account)
	t.Cleanup(func() { svc.getOpenAIWSConnPool().Close() })
	body := []byte(`{"model":"gpt-5.5","input":"sent once","stream":false,"store":false}`)
	c, _ := accountDriverForwardGinContext(body)
	c.Set("api_key", &APIKey{ID: 7})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := svc.Forward(ctx, c, account, body)
	require.Error(t, err)
	require.True(t, managedWSReplayForbidden(err))
	require.EqualValues(t, 1, creates.Load())
	require.EqualValues(t, 1, connects.Load())
	require.Zero(t, native.calls.Load(), "sent managed WS must not fall back to native HTTP")
}

func TestManagedCodexWSServicePassthroughTwoTurnsPreservesHostHooksAndUsage(t *testing.T) {
	var creates atomic.Int32
	svc, account, native, connects := accountDriverForwardFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		for {
			_, body, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			turn := creates.Add(1)
			if turn == 2 {
				require.Equal(t, "resp_passthrough_1", gjson.GetBytes(body, "previous_response_id").String())
			}
			message := fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_passthrough_%d","status":"completed","model":"gpt-5.5","usage":{"input_tokens":%d,"output_tokens":3,"total_tokens":5}}}`, turn, turn+1)
			if err := conn.Write(r.Context(), coderws.MessageText, []byte(message)); err != nil {
				return
			}
		}
	}))
	enableManagedForwardWS(svc, account)
	account.Extra["openai_oauth_responses_websockets_v2_mode"] = "passthrough"
	var mu sync.Mutex
	before := []int{}
	after := []int{}
	inputs := []int{}
	results := make(chan error, 1)
	hooks := &OpenAIWSIngressHooks{BeforeRequest: func(int, []byte, string) error { return nil }, BeforeTurn: func(turn int) error { mu.Lock(); before = append(before, turn); mu.Unlock(); return nil }, AfterTurn: func(turn int, result *OpenAIForwardResult, err error) {
		mu.Lock()
		defer mu.Unlock()
		after = append(after, turn)
		if result != nil {
			inputs = append(inputs, result.Usage.InputTokens)
		}
	}}
	control, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			results <- err
			return
		}
		defer conn.CloseNow()
		_, first, err := conn.Read(control)
		if err != nil {
			results <- err
			return
		}
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = r.Clone(control)
		c.Set("api_key", &APIKey{ID: 7})
		// Real ingress handler owns first-turn admission before handing the
		// accepted socket to this service; subsequent admission stays in relay.
		_ = hooks.BeforeRequest(1, first, "gpt-5.5")
		_ = hooks.BeforeTurn(1)
		results <- svc.ProxyResponsesWebSocketFromClient(control, c, conn, account, "fake-access-token", first, hooks)
	}))
	defer downstream.Close()
	client, _, err := coderws.Dial(control, "ws"+strings.TrimPrefix(downstream.URL, "http"), nil)
	require.NoError(t, err)
	defer client.CloseNow()
	for _, body := range []string{`{"type":"response.create","model":"gpt-5.5","input":"one"}`, `{"type":"response.create","model":"gpt-5.5","input":"two","previous_response_id":"resp_passthrough_1"}`} {
		require.NoError(t, client.Write(control, coderws.MessageText, []byte(body)))
		_, event, err := client.Read(control)
		require.NoError(t, err)
		require.Equal(t, "response.completed", gjson.GetBytes(event, "type").String())
	}
	require.Eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(after) == 2 }, time.Second, 5*time.Millisecond)
	mu.Lock()
	require.Equal(t, []int{1, 2}, before)
	require.Equal(t, []int{1, 2}, after)
	require.Equal(t, []int{2, 3}, inputs)
	mu.Unlock()
	require.EqualValues(t, 2, creates.Load())
	require.EqualValues(t, 1, connects.Load())
	require.Zero(t, native.calls.Load())
	_ = client.CloseNow()
	select {
	case <-results:
	case <-control.Done():
		t.Fatal("managed passthrough did not release after client close")
	}
}
