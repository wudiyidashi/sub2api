package accountdriverv1_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/Wei-Shaw/sub2api/pkg/accountdriverapi/v1"
	"github.com/gorilla/websocket"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

type wsFixtureIdentity struct{ keyHash, fingerprint, authorization string }

func (s *driverCallbacks) resolveWebSocketIdentity(req *pb.IdentityRequest) *pb.IdentityResponse {
	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	identity, ok := s.wsIdentities[req.SessionId]
	if !ok || req.AttemptId != req.SessionId || req.AccountId != 42 || req.BindingRevision != 3 || req.CredentialRevision != 0 || req.RequestId != "request-fixture" || req.KeyScopeHash != identity.keyHash || req.CredentialFingerprint != identity.fingerprint {
		return &pb.IdentityResponse{}
	}
	s.calls.Add(1)
	return &pb.IdentityResponse{Authorized: true, ProxyUrl: s.proxyURL, ProtectedHeaders: []*pb.Header{{Name: "authorization", Value: []byte(identity.authorization)}, {Name: "chatgpt-account-id", Value: []byte("fake-account-42")}}}
}
func wsFingerprint(authorization string) string {
	digest := sha256.Sum256([]byte(authorization))
	return hex.EncodeToString(digest[:])
}
func (h *driverHarness) wsStart(t *testing.T, url, authorization string) *pb.WebSocketStart {
	t.Helper()
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	session := hex.EncodeToString(random)
	keyHash := wsFingerprint("fake-key-private-session")
	start := &pb.WebSocketStart{RequestId: "request-fixture", SessionId: session, AccountId: 42, BindingRevision: 3, KeyScopeHash: keyHash, CredentialFingerprint: wsFingerprint(authorization), Url: url, ProxyUrl: h.callbacks.proxyURL, HandshakeTimeoutMs: 2000, Headers: []*pb.Header{{Name: "user-agent", Value: []byte("prepared-ws-fixture")}, {Name: "x-vendor", Value: []byte("first")}, {Name: "x-vendor", Value: []byte("second")}}}
	h.callbacks.wsMu.Lock()
	if h.callbacks.wsIdentities == nil {
		h.callbacks.wsIdentities = map[string]wsFixtureIdentity{}
	}
	h.callbacks.wsIdentities[session] = wsFixtureIdentity{keyHash, start.CredentialFingerprint, authorization}
	h.callbacks.wsMu.Unlock()
	return start
}
func (h *driverHarness) wsStream(t *testing.T, start *pb.WebSocketStart) (grpc.BidiStreamingClient[pb.WebSocketCommand, pb.WebSocketEvent], context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", h.token)
	stream, err := h.client.OpenWebSocket(ctx, grpc.MaxCallRecvMsgSize(17*1024*1024))
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&pb.WebSocketCommand{Frame: &pb.WebSocketCommand_Start{Start: start}}); err != nil {
		t.Fatal(err)
	}
	return stream, cancel
}
func receiveWS(t *testing.T, stream grpc.BidiStreamingClient[pb.WebSocketCommand, pb.WebSocketEvent], kind string) *pb.WebSocketEvent {
	t.Helper()
	for {
		v, err := stream.Recv()
		if err != nil {
			t.Fatalf("receive WS %s: %v", kind, err)
		}
		if v.GetFailure() != nil && kind != "failure" {
			t.Fatalf("unexpected WS failure: %+v", v.GetFailure())
		}
		if kind == "ready" && v.GetReady() != nil || kind == "handshake" && v.GetHandshake() != nil || kind == "failure" && v.GetFailure() != nil || kind == "closed" && v.GetClosed() != nil {
			return v
		}
	}
}
func openWS(t *testing.T, h *driverHarness, start *pb.WebSocketStart) (grpc.BidiStreamingClient[pb.WebSocketCommand, pb.WebSocketEvent], context.CancelFunc, *pb.ResponseHead) {
	t.Helper()
	stream, cancel := h.wsStream(t, start)
	v := receiveWS(t, stream, "ready")
	if v.SendState != pb.SendState_SEND_STATE_NOT_SENT {
		t.Fatal("opening not permitted yet")
	}
	if err := stream.Send(&pb.WebSocketCommand{Frame: &pb.WebSocketCommand_PermitConnect{PermitConnect: true}}); err != nil {
		t.Fatal(err)
	}
	v = receiveWS(t, stream, "handshake")
	if v.SendState != pb.SendState_SEND_STATE_NOT_SENT {
		t.Fatal("handshake incorrectly marked business sent")
	}
	return stream, cancel, v.GetHandshake()
}
func wsURL(server *httptest.Server) string {
	return strings.Replace(server.URL, "http://", "ws://", 1) + "/codex/responses"
}
func sendWSMessage(t *testing.T, stream grpc.BidiStreamingClient[pb.WebSocketCommand, pb.WebSocketEvent], seq uint64, kind pb.WebSocketMessageKind, data []byte) {
	t.Helper()
	if err := stream.Send(&pb.WebSocketCommand{Frame: &pb.WebSocketCommand_Message{Message: &pb.WebSocketMessage{Sequence: seq, Kind: kind, Data: data}}}); err != nil {
		t.Fatal(err)
	}
}
func collectWSTurn(t *testing.T, stream grpc.BidiStreamingClient[pb.WebSocketCommand, pb.WebSocketEvent], seq uint64) *pb.WebSocketMessage {
	t.Helper()
	var ack bool
	var message *pb.WebSocketMessage
	for !ack || message == nil {
		v, err := stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if v.GetFailure() != nil {
			t.Fatal(v.GetFailure())
		}
		if v.GetWriteAck() != nil && v.GetWriteAck().Sequence == seq {
			ack = true
		}
		if m := v.GetMessage(); m != nil && (m.Kind == pb.WebSocketMessageKind_WEB_SOCKET_MESSAGE_KIND_TEXT || m.Kind == pb.WebSocketMessageKind_WEB_SOCKET_MESSAGE_KIND_BINARY) {
			message = m
		}
	}
	return message
}

func TestRustWebSocketTwoTurnsKeepRawBytesAndConnectionLocalResponse(t *testing.T) {
	h := newDriverHarness(t, "")
	var connections atomic.Int32
	var business atomic.Int32
	want := [][]byte{[]byte(` {"type":"response.create","store":false,"vendor_unknown":{"keep":"一"}} `), []byte(` {"type":"response.create","previous_response_id":"socket-local-1","input":[],"vendor_unknown":{"keep":"二"}} `)}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fake-access-token" || r.Header.Get("User-Agent") != "prepared-ws-fixture" || len(r.Header.Values("X-Vendor")) != 2 {
			t.Error("prepared WS headers lost")
		}
		conn, err := upgrader.Upgrade(w, r, http.Header{"X-Unknown": []string{"one", "two"}})
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		connections.Add(1)
		for index := range want {
			kind, raw, err := conn.ReadMessage()
			if err != nil {
				t.Error(err)
				return
			}
			business.Add(1)
			if kind != websocket.TextMessage || !bytes.Equal(raw, want[index]) {
				t.Error("WS JSON bytes modified or continuation moved")
			}
			payload := []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":"socket-local-%d","unknown":true}}`, index+1))
			if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
				t.Error(err)
				return
			}
		}
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()
	stream, cancel, head := openWS(t, h, h.wsStart(t, wsURL(server), "Bearer fake-access-token"))
	defer cancel()
	if head.StatusCode != 101 {
		t.Fatal(head)
	}
	var headers int
	for _, v := range head.Headers {
		if v.Name == "x-unknown" {
			headers++
		}
	}
	if headers != 2 {
		t.Fatal("handshake multivalue header lost")
	}
	for index, payload := range want {
		sendWSMessage(t, stream, uint64(index+1), pb.WebSocketMessageKind_WEB_SOCKET_MESSAGE_KIND_TEXT, payload)
		message := collectWSTurn(t, stream, uint64(index+1))
		if !bytes.Contains(message.Data, []byte(fmt.Sprintf("socket-local-%d", index+1))) {
			t.Fatal(message)
		}
	}
	if connections.Load() != 1 || business.Load() != 2 {
		t.Fatalf("connections=%d business=%d", connections.Load(), business.Load())
	}
	_ = stream.Send(&pb.WebSocketCommand{Frame: &pb.WebSocketCommand_Close{Close: &pb.WebSocketClose{Code: 1000}}})
}

func TestRustWebSocketScopeAndCredentialDigestRejectBeforeConnect(t *testing.T) {
	h := newDriverHarness(t, "")
	var connections atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connections.Add(1)
		http.Error(w, "must not connect", 500)
	}))
	defer server.Close()
	for _, mode := range []string{"key_scope", "credential_digest"} {
		t.Run(mode, func(t *testing.T) {
			start := h.wsStart(t, wsURL(server), "Bearer fake-access-token")
			if mode == "key_scope" {
				start.KeyScopeHash = wsFingerprint("another-key")
			} else {
				h.callbacks.wsMu.Lock()
				v := h.callbacks.wsIdentities[start.SessionId]
				v.authorization = "Bearer rotated-access-token"
				h.callbacks.wsIdentities[start.SessionId] = v
				h.callbacks.wsMu.Unlock()
			}
			stream, _ := h.wsStream(t, start)
			failure := receiveWS(t, stream, "failure")
			if failure.SendState != pb.SendState_SEND_STATE_NOT_SENT {
				t.Fatal("rejected scope incorrectly sent")
			}
			_ = stream.CloseSend()
		})
	}
	if connections.Load() != 0 {
		t.Fatal("scope rejection reached upstream")
	}
}

func TestRustWebSocketCredentialRotationCreatesIndependentIdentity(t *testing.T) {
	h := newDriverHarness(t, "")
	var connections atomic.Int32
	seen := make(chan string, 2)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		connections.Add(1)
		seen <- r.Header.Get("Authorization")
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()
	first, cancelFirst, _ := openWS(t, h, h.wsStart(t, wsURL(server), "Bearer fake-access-token"))
	second, cancelSecond, _ := openWS(t, h, h.wsStart(t, wsURL(server), "Bearer rotated-access-token"))
	_ = first
	_ = second
	cancelFirst()
	cancelSecond()
	got := []string{<-seen, <-seen}
	if got[0] == got[1] || connections.Load() != 2 {
		t.Fatalf("identity was shared after AT rotation: %v %d", got, connections.Load())
	}
}

func TestRustWebSocketIdlePingWorksWithoutHostRead(t *testing.T) {
	h := newDriverHarness(t, "")
	pong := make(chan struct{}, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		conn.SetPongHandler(func(string) error {
			select {
			case pong <- struct{}{}:
			default:
			}
			return nil
		})
		_ = conn.WriteControl(websocket.PingMessage, []byte("idle-probe"), time.Now().Add(time.Second))
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	_, cancel, _ := openWS(t, h, h.wsStart(t, wsURL(server), "Bearer fake-access-token"))
	defer cancel()
	select {
	case <-pong:
	case <-time.After(2 * time.Second):
		t.Fatal("native CPR socket did not pong while host was idle")
	}
}

func TestRustWebSocketBinaryPeerCloseAndNoResendAfterDrop(t *testing.T) {
	h := newDriverHarness(t, "")
	var connections atomic.Int32
	var sends atomic.Int32
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		connections.Add(1)
		kind, data, err := conn.ReadMessage()
		if err != nil {
			t.Error(err)
			return
		}
		sends.Add(1)
		_ = conn.WriteMessage(kind, data)
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(1008, "fixture close"), time.Now().Add(time.Second))
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()
	stream, cancel, _ := openWS(t, h, h.wsStart(t, wsURL(server), "Bearer fake-access-token"))
	defer cancel()
	payload := []byte{0, 255, 128, 1}
	sendWSMessage(t, stream, 1, pb.WebSocketMessageKind_WEB_SOCKET_MESSAGE_KIND_BINARY, payload)
	message := collectWSTurn(t, stream, 1)
	if message.Kind != pb.WebSocketMessageKind_WEB_SOCKET_MESSAGE_KIND_BINARY || !bytes.Equal(payload, message.Data) {
		t.Fatal("binary frame changed")
	}
	closed := receiveWS(t, stream, "closed")
	if closed.GetClosed().Code != 1008 || closed.GetClosed().Reason != "fixture close" {
		t.Fatal(closed)
	}
	if connections.Load() != 1 || sends.Load() != 1 {
		t.Fatal("WS driver reconnected or resent")
	}
}

func TestRustWebSocketBackpressure200MiBDoesNotBlockClose(t *testing.T) {
	h := newDriverHarness(t, "")
	closed := make(chan struct{})
	var completed atomic.Int32
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		defer close(closed)
		_ = conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
		payload := bytes.Repeat([]byte("x"), 1024*1024)
		for i := 0; i < 200; i++ {
			if err := conn.WriteMessage(websocket.BinaryMessage, payload); err != nil {
				return
			}
			completed.Add(1)
		}
	}))
	defer server.Close()
	stream, cancel, _ := openWS(t, h, h.wsStart(t, wsURL(server), "Bearer fake-access-token"))
	defer cancel()
	time.Sleep(100 * time.Millisecond)
	if completed.Load() == 200 {
		t.Fatal("all 200MiB were buffered before close despite host not reading")
	}
	if err := stream.Send(&pb.WebSocketCommand{Frame: &pb.WebSocketCommand_Close{Close: &pb.WebSocketClose{Code: 1000, Reason: "backpressure stop"}}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("bounded output queue blocked control close")
	}
	cancel()
}

func TestRustWebSocketHandshakeFailurePreservesRawHeadersAndBody(t *testing.T) {
	h := newDriverHarness(t, "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("X-Unknown", "one")
		w.Header().Add("X-Unknown", "two")
		w.WriteHeader(401)
		_, _ = io.WriteString(w, `{"error":"fixture auth denied"}`)
	}))
	defer server.Close()
	stream, _ := h.wsStream(t, h.wsStart(t, wsURL(server), "Bearer fake-access-token"))
	receiveWS(t, stream, "ready")
	_ = stream.Send(&pb.WebSocketCommand{Frame: &pb.WebSocketCommand_PermitConnect{PermitConnect: true}})
	head := receiveWS(t, stream, "handshake")
	failure := receiveWS(t, stream, "failure")
	if head.GetHandshake().StatusCode != 401 || failure.SendState != pb.SendState_SEND_STATE_NOT_SENT || !bytes.Contains(failure.GetFailure().RawBody, []byte("fixture auth denied")) {
		t.Fatalf("handshake rejection facts lost: %v %v", head, failure)
	}
	var values int
	for _, v := range head.GetHandshake().Headers {
		if v.Name == "x-unknown" {
			values++
		}
	}
	if values != 2 {
		t.Fatal("unknown handshake error header values lost")
	}
	_ = stream.CloseSend()
}
