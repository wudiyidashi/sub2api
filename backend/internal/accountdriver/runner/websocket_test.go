package runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/Wei-Shaw/sub2api/pkg/accountdriverapi/v1"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
)

func wsFixtureOptions() WebSocketOptions {
	return WebSocketOptions{AccountID: 17, BindingRevision: 3, KeyScopeHash: strings.Repeat("a", 64), HandshakeTimeout: 3 * time.Second}
}

func wsFixtureHeaders() http.Header {
	return http.Header{"Authorization": {"Bearer host-ws-fixture"}, "Chatgpt-Account-Id": {"fixture-account"}, "X-Unknown": {"one", "two"}}
}

func wsEchoFixture(t *testing.T, observed func(*http.Request, *coderws.Conn)) (string, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Add("X-Upstream-Unknown", "first")
		w.Header().Add("X-Upstream-Unknown", "second")
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = conn.CloseNow() }()
		conn.SetReadLimit(WebSocketMessageLimit)
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		if observed != nil {
			observed(r, conn)
		}
		for {
			kind, body, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if err := conn.Write(ctx, kind, body); err != nil {
				return
			}
		}
	}))
	t.Cleanup(upstream.Close)
	return "ws" + strings.TrimPrefix(upstream.URL, "http") + "/codex/responses", calls
}

func TestManagedWebSocketKeepsIdentityRawMessagesAndConnectionLifetime(t *testing.T) {
	client := startFixtureRunner(t)
	require.True(t, client.SupportsWebSocket())
	upstreamPing := make(chan error, 1)
	url, calls := wsEchoFixture(t, func(r *http.Request, conn *coderws.Conn) {
		if r.URL.Path != "/codex/responses" || r.Header.Get("Authorization") != "Bearer host-ws-fixture" || r.Header.Get("ChatGPT-Account-ID") != "fixture-account" || len(r.Header.Values("X-Unknown")) != 2 {
			t.Error("prepared handshake changed")
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			upstreamPing <- conn.Ping(ctx)
		}()
	})
	dialCtx, dialCancel := context.WithCancel(context.Background())
	conn, status, headers, err := client.DialWebSocket(dialCtx, url, wsFixtureHeaders(), "", wsFixtureOptions())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.CloseNow() })
	dialCancel() // Native pool cancels its dial deadline after acquiring the connection.
	require.Equal(t, 101, status)
	require.Equal(t, []string{"first", "second"}, headers.Values("X-Upstream-Unknown"))
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	for _, frame := range []wsMessage{
		{kind: coderws.MessageText, data: []byte(` {"type":"response.create","unknown":{"keep":true}} `)},
		{kind: coderws.MessageText, data: []byte(`{"type":"response.create","previous_response_id":"resp_same_connection"}`)},
		{kind: coderws.MessageBinary, data: bytes.Repeat([]byte{0, 255, 1, 128}, (2<<20)/4)},
	} {
		require.NoError(t, conn.Write(ctx, frame.kind, frame.data))
		kind, body, err := conn.Read(ctx)
		require.NoError(t, err)
		require.Equal(t, frame.kind, kind)
		require.Equal(t, frame.data, body)
	}
	require.NoError(t, conn.Ping(ctx))
	select {
	case err := <-upstreamPing:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("driver did not flush the upstream ping response")
	}
	require.Positive(t, conn.UpstreamPingCount())
	require.True(t, conn.MayHaveSentBusiness())
	require.EqualValues(t, 1, calls.Load())
	require.NoError(t, conn.Close())
	require.Eventually(t, func() bool { client.mu.Lock(); defer client.mu.Unlock(); return len(client.identities) == 0 }, 2*time.Second, 10*time.Millisecond)
}

func TestManagedWebSocketHandshakeRejectionPreservesStatusHeadersAndBody(t *testing.T) {
	client := startFixtureRunner(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("X-Rejection", "first")
		w.Header().Add("X-Rejection", "second")
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":{"code":"usage_limit_reached"}}`))
	}))
	defer upstream.Close()
	conn, status, headers, err := client.DialWebSocket(context.Background(), "ws"+strings.TrimPrefix(upstream.URL, "http")+"/codex/responses", wsFixtureHeaders(), "", wsFixtureOptions())
	require.Nil(t, conn)
	require.Equal(t, 429, status)
	require.Equal(t, []string{"first", "second"}, headers.Values("X-Rejection"))
	var handshake *HandshakeError
	require.ErrorAs(t, err, &handshake)
	require.Equal(t, `{"error":{"code":"usage_limit_reached"}}`, string(handshake.Body))
	var fact interface{ MayHaveBeenSent() bool }
	require.ErrorAs(t, err, &fact)
	require.False(t, fact.MayHaveBeenSent())
}

func TestManagedWebSocketHandshakeCancellationReleasesUpstream(t *testing.T) {
	for _, mode := range []string{"caller_cancel", "handshake_timeout"} {
		t.Run(mode, func(t *testing.T) {
			client := startFixtureRunner(t)
			started, closed := make(chan struct{}), make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(started)
				<-r.Context().Done()
				close(closed)
			}))
			defer upstream.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			options := wsFixtureOptions()
			if mode == "handshake_timeout" {
				options.HandshakeTimeout = 250 * time.Millisecond
			} else {
				go func() { <-started; cancel() }()
			}
			conn, _, _, err := client.DialWebSocket(ctx, "ws"+strings.TrimPrefix(upstream.URL, "http")+"/codex/responses", wsFixtureHeaders(), "", options)
			require.Nil(t, conn)
			require.Error(t, err)
			select {
			case <-closed:
			case <-time.After(3 * time.Second):
				t.Fatal("handshake cancellation leaked upstream")
			}
		})
	}
}

func TestManagedWebSocketCancelledWriteWaiterDoesNotBreakSequence(t *testing.T) {
	client := startFixtureRunner(t)
	url, _ := wsEchoFixture(t, nil)
	conn, _, _, err := client.DialWebSocket(context.Background(), url, wsFixtureHeaders(), "", wsFixtureOptions())
	require.NoError(t, err)
	defer func() { _ = conn.CloseNow() }()
	conn.sendGate <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, conn.Write(ctx, coderws.MessageText, []byte("never sent")), context.DeadlineExceeded)
	<-conn.sendGate
	require.False(t, conn.MayHaveSentBusiness())
	require.Zero(t, conn.seq.Load())
	valid, cancelValid := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelValid()
	require.NoError(t, conn.Write(valid, coderws.MessageText, []byte("first real frame")))
	_, body, err := conn.Read(valid)
	require.NoError(t, err)
	require.Equal(t, "first real frame", string(body))
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := conn.Ping(valid); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}

func TestManagedWebSocketInvalidFramesDoNotPoisonConnection(t *testing.T) {
	client := startFixtureRunner(t)
	url, _ := wsEchoFixture(t, nil)
	conn, _, _, err := client.DialWebSocket(context.Background(), url, wsFixtureHeaders(), "", wsFixtureOptions())
	require.NoError(t, err)
	defer func() { _ = conn.CloseNow() }()
	require.Error(t, conn.Write(context.Background(), coderws.MessageText, []byte{255}))
	require.Error(t, conn.Write(context.Background(), coderws.MessageBinary, make([]byte, WebSocketMessageLimit+1)))
	require.False(t, conn.MayHaveSentBusiness())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte("valid after local rejection")))
	_, body, err := conn.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, "valid after local rejection", string(body))
}

func TestManagedWebSocketProcessExitAfterWriteIsAmbiguousAndNeverReconnects(t *testing.T) {
	client := startFixtureRunner(t)
	url, calls := wsEchoFixture(t, nil)
	conn, _, _, err := client.DialWebSocket(context.Background(), url, wsFixtureHeaders(), "", wsFixtureOptions())
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte("one business frame")))
	_, _, err = conn.Read(ctx)
	require.NoError(t, err)
	client.Close()
	_, _, err = conn.Read(ctx)
	var fact interface{ MayHaveBeenSent() bool }
	require.ErrorAs(t, err, &fact)
	require.True(t, fact.MayHaveBeenSent())
	require.EqualValues(t, 1, calls.Load())
	err = conn.Write(ctx, coderws.MessageText, []byte("new frame after process exit"))
	require.ErrorAs(t, err, &fact)
	require.False(t, fact.MayHaveBeenSent(), "a new write rejected before Send must not inherit a completed turn's watermark")
	require.True(t, conn.MayHaveSentBusiness(), "connection history still records the earlier business frame")
}

func TestManagedWebSocketForgedScopeIsRejectedBeforeConnect(t *testing.T) {
	client := startFixtureRunner(t)
	url, calls := wsEchoFixture(t, nil)
	for _, mutation := range []string{"account", "binding", "key", "credential", "session"} {
		t.Run(mutation, func(t *testing.T) {
			id := strings.Repeat("0", 32) + mutation
			digest := sha256.Sum256([]byte("Bearer host-ws-fixture"))
			fingerprint := hex.EncodeToString(digest[:])
			bound := identity{requestID: "request_" + id, accountID: 17, bindingRevision: 3, headers: headersToWire(wsFixtureHeaders(), true), sessionID: id, keyScopeHash: strings.Repeat("a", 64), credentialFingerprint: fingerprint}
			client.mu.Lock()
			client.identities[id] = bound
			client.mu.Unlock()
			defer func() { client.mu.Lock(); delete(client.identities, id); client.mu.Unlock() }()
			start := &api.WebSocketStart{RequestId: bound.requestID, SessionId: id, AccountId: 17, BindingRevision: 3, KeyScopeHash: bound.keyScopeHash, CredentialFingerprint: fingerprint, Url: url, HandshakeTimeoutMs: 1000}
			switch mutation {
			case "account":
				start.AccountId++
			case "binding":
				start.BindingRevision++
			case "key":
				start.KeyScopeHash = strings.Repeat("b", 64)
			case "credential":
				start.CredentialFingerprint = strings.Repeat("c", 64)
			case "session":
				start.SessionId = strings.Repeat("d", 32)
			}
			ctx, cancel := context.WithTimeout(metadata.AppendToOutgoingContext(context.Background(), "authorization", client.token), 3*time.Second)
			defer cancel()
			stream, err := client.api.OpenWebSocket(ctx)
			require.NoError(t, err)
			require.NoError(t, stream.Send(&api.WebSocketCommand{Frame: &api.WebSocketCommand_Start{Start: start}}))
			event, err := stream.Recv()
			require.NoError(t, err)
			require.Equal(t, api.SendState_SEND_STATE_NOT_SENT, event.SendState)
			require.NotNil(t, event.GetFailure())
			require.NotEmpty(t, event.GetFailure().Code)
		})
	}
	require.Zero(t, calls.Load())
}

func TestManagedWebSocketReadCancellationReleasesConnection(t *testing.T) {
	client := startFixtureRunner(t)
	url, _ := wsEchoFixture(t, nil)
	conn, _, _, err := client.DialWebSocket(context.Background(), url, wsFixtureHeaders(), "", wsFixtureOptions())
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, _, err = conn.Read(ctx)
	require.True(t, errors.Is(err, context.DeadlineExceeded))
	require.Eventually(t, func() bool { client.mu.Lock(); defer client.mu.Unlock(); return len(client.identities) == 0 }, time.Second, 10*time.Millisecond)
}

func TestManagedWebSocketBackpressureDoesNotBlockClose(t *testing.T) {
	client := startFixtureRunner(t)
	started, closed := make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		readDone := make(chan error, 1)
		go func() { _, _, err := conn.Read(ctx); readDone <- err }()
		payload := bytes.Repeat([]byte("x"), 256<<10)
		close(started)
		for range 1000 {
			if err := conn.Write(ctx, coderws.MessageBinary, payload); err != nil {
				break
			}
		}
		select {
		case err := <-readDone:
			if code := coderws.CloseStatus(err); code != coderws.StatusNormalClosure {
				t.Errorf("expected graceful close under backpressure, got %d", code)
			}
		case <-ctx.Done():
			t.Error("backpressure did not release upstream")
		}
		close(closed)
	}))
	defer upstream.Close()
	conn, _, _, err := client.DialWebSocket(context.Background(), "ws"+strings.TrimPrefix(upstream.URL, "http")+"/codex/responses", wsFixtureHeaders(), "", wsFixtureOptions())
	require.NoError(t, err)
	<-started
	require.Eventually(t, func() bool { return len(conn.messages) == cap(conn.messages) }, 3*time.Second, 10*time.Millisecond)
	select {
	case <-conn.done:
	case <-time.After(time.Second):
		t.Fatal("full message queue blocked RPC controls")
	}
	var overflow *WebSocketError
	require.ErrorAs(t, conn.Ping(context.Background()), &overflow)
	var overflowCause *WebSocketError
	require.ErrorAs(t, overflow.Cause, &overflowCause)
	require.Equal(t, "reader_backpressure", overflowCause.Code)
	require.LessOrEqual(t, conn.queuedBytes.Load(), int64(webSocketQueueLimit))
	start := time.Now()
	require.NoError(t, conn.Close())
	require.Less(t, time.Since(start), 2*time.Second)
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("driver input close was blocked by its output queue")
	}
}

func TestManagedWebSocketPingTimeoutPreservesBusinessSendingWatermark(t *testing.T) {
	client := startFixtureRunner(t)
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = conn.CloseNow() }()
		// Deliberately do not read: coder/websocket won't process Ping or send Pong.
		<-release
	}))
	defer upstream.Close()
	defer close(release)
	conn, _, _, err := client.DialWebSocket(context.Background(), "ws"+strings.TrimPrefix(upstream.URL, "http")+"/codex/responses", wsFixtureHeaders(), "", wsFixtureOptions())
	require.NoError(t, err)
	defer func() { _ = conn.CloseNow() }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte("may have been processed")))
	pingCtx, cancelPing := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelPing()
	err = conn.Ping(pingCtx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	var fact interface{ MayHaveBeenSent() bool }
	require.ErrorAs(t, err, &fact)
	require.True(t, fact.MayHaveBeenSent())
	require.True(t, conn.MayHaveSentBusiness())
}
