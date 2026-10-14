package accountdriverv1_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/Wei-Shaw/sub2api/pkg/accountdriverapi/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type driverCallbacks struct {
	pb.UnimplementedHostCallbacksServer
	token        string
	proxyURL     string
	calls        atomic.Int32
	wsMu         sync.Mutex
	wsIdentities map[string]wsFixtureIdentity
}

func (s *driverCallbacks) ResolveIdentity(ctx context.Context, req *pb.IdentityRequest) (*pb.IdentityResponse, error) {
	values := metadata.ValueFromIncomingContext(ctx, "authorization")
	if len(values) != 1 || values[0] != s.token {
		return nil, status.Error(codes.Unauthenticated, "callback identity")
	}
	if req.SessionId != "" {
		return s.resolveWebSocketIdentity(req), nil
	}
	if req.AccountId != 42 || req.CredentialRevision != 9 || req.BindingRevision != 3 || req.RequestId != "request-fixture" || req.AttemptId != "attempt-fixture" {
		return &pb.IdentityResponse{Authorized: false}, nil
	}
	s.calls.Add(1)
	return &pb.IdentityResponse{Authorized: true, ProxyUrl: s.proxyURL, ProtectedHeaders: []*pb.Header{
		{Name: "authorization", Value: []byte("Bearer fake-access-token")},
		{Name: "chatgpt-account-id", Value: []byte("fake-account-42")},
	}}, nil
}

type driverHarness struct {
	client    pb.AccountDriverClient
	token     string
	callbacks *driverCallbacks
	cmd       *exec.Cmd
}

func newDriverHarness(t *testing.T, proxyURL string) *driverHarness {
	t.Helper()
	binary := os.Getenv("SUB2API_CODEX_DRIVER_BINARY")
	if binary == "" {
		t.Skip("set SUB2API_CODEX_DRIVER_BINARY to run the real Rust driver integration tests")
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	token := hex.EncodeToString(secret)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	callbackServer := grpc.NewServer()
	cb := &driverCallbacks{token: token, proxyURL: proxyURL}
	pb.RegisterHostCallbacksServer(callbackServer, cb)
	go func() { _ = callbackServer.Serve(listener) }()
	t.Cleanup(callbackServer.Stop)

	cmd := exec.Command(binary) //nolint:gosec // G702: 仅执行测试操作者指定的驱动文件，不经过 shell 或接收网关请求输入。
	cmd.Env = append(os.Environ(), "SUB2API_ACCOUNT_DRIVER_TOKEN="+token, "SUB2API_ACCOUNT_DRIVER_CALLBACK=http://"+listener.Addr().String())
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = stdin.Close()
		select {
		case <-exited:
		case <-time.After(3 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
			t.Error("driver did not exit on stdin EOF")
		}
	})
	handshake := make(chan []byte, 1)
	go func() { line, _ := bufio.NewReader(stdout).ReadBytes('\n'); handshake <- line }()
	var hello struct {
		Protocol, Address string
		DriverID          string `json:"driver_id"`
		DriverVersion     string `json:"driver_version"`
		UpstreamCommit    string `json:"upstream_commit"`
		APIVersion        int    `json:"api_version"`
	}
	select {
	case line := <-handshake:
		if err := json.Unmarshal(line, &hello); err != nil {
			t.Fatalf("handshake: %v; stderr: %s", err, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("driver handshake timeout")
	}
	lockBytes, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "drivers", "codex-rs", "upstream-lock.json"))
	if err != nil {
		t.Fatal("source version lock unavailable for handshake verification")
	}
	var sourceLock struct {
		Commit string `json:"commit"`
	}
	if err := json.Unmarshal(lockBytes, &sourceLock); err != nil {
		t.Fatal("source version lock is invalid")
	}
	if hello.Protocol != "sub2api-account-driver-v1" || hello.APIVersion != 1 || hello.DriverID != "codex-rs-http" || hello.DriverVersion == "" || hello.UpstreamCommit != sourceLock.Commit {
		t.Fatalf("unexpected handshake: %+v", hello)
	}
	conn, err := grpc.NewClient(hello.Address, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithDefaultCallOptions(grpc.MaxCallSendMsgSize(257*1024*1024), grpc.MaxCallRecvMsgSize(1024*1024)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &driverHarness{client: pb.NewAccountDriverClient(conn), token: token, callbacks: cb, cmd: cmd}
}

func fixtureStart(url, proxy string) *pb.ExecuteStart {
	return &pb.ExecuteStart{RequestId: "request-fixture", AttemptId: "attempt-fixture", AccountId: 42, CredentialRevision: 9, BindingRevision: 3, Method: "POST", Url: url, ProxyUrl: proxy, Headers: []*pb.Header{
		{Name: "authorization", Value: []byte("Bearer caller-must-not-select-identity")},
		{Name: "chatgpt-account-id", Value: []byte("caller-other-account")},
		{Name: "content-type", Value: []byte("application/json")},
		{Name: "user-agent", Value: []byte("Sub2API-prepared-fixture")},
		{Name: "x-vendor-unknown", Value: []byte("first")},
		{Name: "x-vendor-unknown", Value: []byte("second")},
	}, Body: []byte(` { "model":"fake","stream":false,"vendor":{"opaque":"keep"} } `)}
}

func fixtureStartWithHeaderTimeout(url, proxy string, timeoutMs int64) *pb.ExecuteStart {
	start := fixtureStart(url, proxy)
	start.HeaderTimeoutMs = timeoutMs
	return start
}

func (h *driverHarness) stream(t *testing.T, start *pb.ExecuteStart) (grpc.BidiStreamingClient[pb.ExecuteCommand, pb.ExecuteEvent], context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", h.token)
	stream, err := h.client.Execute(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&pb.ExecuteCommand{Frame: &pb.ExecuteCommand_Start{Start: start}}); err != nil {
		t.Fatal(err)
	}
	return stream, cancel
}

func receiveDriverEvent(t *testing.T, stream grpc.BidiStreamingClient[pb.ExecuteCommand, pb.ExecuteEvent], kind string) *pb.ExecuteEvent {
	t.Helper()
	for {
		value, err := stream.Recv()
		if err != nil {
			t.Fatalf("receive %s: %v", kind, err)
		}
		if kind == "ready" && value.GetReady() != nil {
			return value
		}
		if kind == "head" && value.GetHead() != nil {
			return value
		}
		if kind == "failure" && value.GetFailure() != nil {
			return value
		}
	}
}
func driverPermit(t *testing.T, stream grpc.BidiStreamingClient[pb.ExecuteCommand, pb.ExecuteEvent], state *pb.SendState) {
	t.Helper()
	*state = pb.SendState_SEND_STATE_AMBIGUOUS
	if err := stream.Send(&pb.ExecuteCommand{Frame: &pb.ExecuteCommand_PermitSend{PermitSend: true}}); err != nil {
		t.Fatal(err)
	}
}

func TestRustDriverPreparedRequestAndCompleteHeaders(t *testing.T) {
	h := newDriverHarness(t, "")
	var requests atomic.Int32
	wantBody := fixtureStart("", "").Body
	wantSSE := []byte(": unknown comment\n\nevent: future.vendor\ndata: {\"unknown\":true}\n\n")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		raw, _ := io.ReadAll(r.Body)
		if !bytes.Equal(raw, wantBody) {
			t.Errorf("driver modified prepared body: %s", raw)
		}
		if r.Header.Get("Authorization") != "Bearer fake-access-token" || r.Header.Get("ChatGPT-Account-ID") != "fake-account-42" {
			t.Error("protected host identity lost")
		}
		if r.Header.Get("User-Agent") != "Sub2API-prepared-fixture" || len(r.Header.Values("X-Vendor-Unknown")) != 2 {
			t.Error("prepared unknown/multiple headers changed")
		}
		w.Header().Add("Set-Cookie", "fixture=a")
		w.Header().Add("Set-Cookie", "fixture=b")
		w.Header().Add("X-Unknown", "alpha")
		w.Header().Add("X-Unknown", "beta")
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(201)
		_, _ = w.Write(wantSSE)
	}))
	defer upstream.Close()
	stream, _ := h.stream(t, fixtureStart(upstream.URL+"/codex/responses", ""))
	state := receiveDriverEvent(t, stream, "ready").SendState
	driverPermit(t, stream, &state)
	var head *pb.ResponseHead
	var body bytes.Buffer
	var ended bool
	for {
		value, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		state = value.SendState
		if state == pb.SendState_SEND_STATE_NOT_SENT {
			t.Fatal("sending facts moved backwards")
		}
		if value.GetFailure() != nil {
			t.Fatal(value.GetFailure())
		}
		if value.GetHead() != nil {
			head = value.GetHead()
		}
		if value.GetBody() != nil {
			_, _ = body.Write(value.GetBody().Data)
		}
		if value.GetEnd() != nil {
			ended = true
		}
	}
	_ = stream.CloseSend()
	counts := map[string]int{}
	if head != nil {
		for _, hdr := range head.Headers {
			counts[hdr.Name]++
		}
	}
	if head == nil || head.StatusCode != 201 || counts["set-cookie"] != 2 || counts["x-unknown"] != 2 || !bytes.Equal(body.Bytes(), wantSSE) || !ended || state != pb.SendState_SEND_STATE_SENT || requests.Load() != 1 {
		t.Fatalf("incomplete transparent response: head=%+v body=%q requests=%d", head, body.Bytes(), requests.Load())
	}
}

func TestRustDriverPreservesHTTP401Body(t *testing.T) {
	h := newDriverHarness(t, "")
	raw := []byte(`{"error":{"code":"credential_expired","unknown":"keep"}}`)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(401)
		_, _ = w.Write(raw)
	}))
	defer upstream.Close()
	stream, _ := h.stream(t, fixtureStart(upstream.URL+"/codex/responses", ""))
	state := receiveDriverEvent(t, stream, "ready").SendState
	driverPermit(t, stream, &state)
	head := receiveDriverEvent(t, stream, "head")
	var body bytes.Buffer
	for {
		value, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if value.GetFailure() != nil {
			t.Fatal("HTTP error turned into opaque RPC failure")
		}
		if value.GetBody() != nil {
			_, _ = body.Write(value.GetBody().Data)
		}
	}
	_ = stream.CloseSend()
	if head.GetHead().StatusCode != 401 || head.SendState != pb.SendState_SEND_STATE_SENT || !bytes.Equal(body.Bytes(), raw) {
		t.Fatal("401 raw response changed")
	}
}

func TestRustDriverRejectsUnsupportedURLAndScope(t *testing.T) {
	h := newDriverHarness(t, "")
	for _, scenario := range []struct {
		name  string
		start *pb.ExecuteStart
		code  string
	}{
		{"wrong host", fixtureStart("https://example.com/codex/responses", ""), "unsupported_endpoint"},
		{"wrong path", fixtureStart("http://127.0.0.1:1/v1/chat/completions", ""), "unsupported_endpoint"},
		{"wrong account", func() *pb.ExecuteStart {
			s := fixtureStart("http://127.0.0.1:1/codex/responses", "")
			s.AccountId = 99
			return s
		}(), "identity_rejected"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			stream, _ := h.stream(t, scenario.start)
			value := receiveDriverEvent(t, stream, "failure")
			_ = stream.CloseSend()
			if value.SendState != pb.SendState_SEND_STATE_NOT_SENT || value.GetFailure().Code != scenario.code {
				t.Fatalf("failure=%+v", value)
			}
		})
	}
}

func TestRustDriverCancelBeforePermit(t *testing.T) {
	h := newDriverHarness(t, "")
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer upstream.Close()
	stream, _ := h.stream(t, fixtureStart(upstream.URL+"/codex/responses", ""))
	receiveDriverEvent(t, stream, "ready")
	if err := stream.Send(&pb.ExecuteCommand{Frame: &pb.ExecuteCommand_Cancel{Cancel: true}}); err != nil {
		t.Fatal(err)
	}
	value := receiveDriverEvent(t, stream, "failure")
	_ = stream.CloseSend()
	if value.SendState != pb.SendState_SEND_STATE_NOT_SENT || requests.Load() != 0 {
		t.Fatal("unpermitted attempt reached upstream")
	}
}

func TestRustDriverProxyIsApplied(t *testing.T) {
	var requests atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		if !r.URL.IsAbs() {
			t.Error("CPR proxy did not receive absolute request URL")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer proxy.Close()
	h := newDriverHarness(t, proxy.URL)
	stream, _ := h.stream(t, fixtureStart("http://127.0.0.1:1/codex/responses", proxy.URL))
	state := receiveDriverEvent(t, stream, "ready").SendState
	driverPermit(t, stream, &state)
	receiveDriverEvent(t, stream, "head")
	for {
		_, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	_ = stream.CloseSend()
	if requests.Load() != 1 {
		t.Fatal("CPR configured proxy was bypassed")
	}
}

func TestRustDriverAcceptsPreparedBodyLargerThanGRPCDefault(t *testing.T) {
	h := newDriverHarness(t, "")
	body := bytes.Repeat([]byte("x"), 5*1024*1024)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if !bytes.Equal(raw, body) {
			t.Error("large body changed or truncated")
		}
		w.WriteHeader(204)
	}))
	defer upstream.Close()
	start := fixtureStart(upstream.URL+"/codex/responses", "")
	start.Body = body
	stream, _ := h.stream(t, start)
	state := receiveDriverEvent(t, stream, "ready").SendState
	driverPermit(t, stream, &state)
	head := receiveDriverEvent(t, stream, "head")
	for {
		_, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	_ = stream.CloseSend()
	if head.GetHead().StatusCode != 204 {
		t.Fatal(head)
	}
}

func TestRustDriverCrashAfterActualSendPreservesAmbiguous(t *testing.T) {
	h := newDriverHarness(t, "")
	arrived := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(arrived)
		<-r.Context().Done()
	}))
	defer upstream.Close()
	stream, _ := h.stream(t, fixtureStart(upstream.URL+"/codex/responses", ""))
	state := receiveDriverEvent(t, stream, "ready").SendState
	driverPermit(t, stream, &state)
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("no upstream send")
	}
	if err := h.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	for {
		_, err := stream.Recv()
		if err != nil {
			break
		}
	}
	if state != pb.SendState_SEND_STATE_AMBIGUOUS {
		t.Fatal("crash made sent attempt replayable")
	}
}

func TestRustDriverBackpressureDoesNotBlockCancel(t *testing.T) {
	h := newDriverHarness(t, "")
	disconnected := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Error(err)
			return
		}
		chunk := bytes.Repeat([]byte("x"), 64*1024)
		for {
			select {
			case <-r.Context().Done():
				close(disconnected)
				return
			default:
			}
			if _, err := w.Write(chunk); err != nil {
				close(disconnected)
				return
			}
			if err := http.NewResponseController(w).Flush(); err != nil {
				close(disconnected)
				return
			}
		}
	}))
	defer upstream.Close()
	stream, _ := h.stream(t, fixtureStart(upstream.URL+"/codex/responses", ""))
	state := receiveDriverEvent(t, stream, "ready").SendState
	driverPermit(t, stream, &state)
	receiveDriverEvent(t, stream, "head")
	time.Sleep(100 * time.Millisecond)
	if err := stream.Send(&pb.ExecuteCommand{Frame: &pb.ExecuteCommand_Cancel{Cancel: true}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-disconnected:
	case <-time.After(3 * time.Second):
		t.Fatal("driver retained upstream connection after cancellation")
	}
	_ = stream.CloseSend()
}

func TestRustDriverDoesNotRetryDisconnectedUpstream(t *testing.T) {
	h := newDriverHarness(t, "")
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		conn, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	}))
	defer upstream.Close()
	stream, _ := h.stream(t, fixtureStart(upstream.URL+"/codex/responses", ""))
	state := receiveDriverEvent(t, stream, "ready").SendState
	driverPermit(t, stream, &state)
	value := receiveDriverEvent(t, stream, "failure")
	_ = stream.CloseSend()
	if value.SendState != pb.SendState_SEND_STATE_AMBIGUOUS || requests.Load() != 1 {
		t.Fatalf("disconnected request replayed or sending facts lost: %+v requests=%d", value, requests.Load())
	}
}

func TestRustDriverResponseHeaderTimeoutIsAmbiguousAndClosesUpstream(t *testing.T) {
	h := newDriverHarness(t, "")
	connected := make(chan struct{})
	closed := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(connected)
		<-r.Context().Done()
		close(closed)
	}))
	defer upstream.Close()
	stream, _ := h.stream(t, fixtureStartWithHeaderTimeout(upstream.URL+"/codex/responses", "", 200))
	state := receiveDriverEvent(t, stream, "ready").SendState
	driverPermit(t, stream, &state)
	value := receiveDriverEvent(t, stream, "failure")
	_ = stream.CloseSend()
	select {
	case <-connected:
	case <-time.After(3 * time.Second):
		t.Fatal("fake upstream did not receive request")
	}
	if value.SendState != pb.SendState_SEND_STATE_AMBIGUOUS || value.GetFailure().Code != "response_header_timeout" {
		t.Fatalf("header timeout lost conservative sending state: %+v", value)
	}
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("header timeout did not cancel the upstream connection")
	}
}

func TestRustDriverHeaderTimeoutEndsAtHeadAndDoesNotInterruptBody(t *testing.T) {
	h := newDriverHarness(t, "")
	bodyReleased := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Error(err)
			return
		}
		select {
		case <-bodyReleased:
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		case <-r.Context().Done():
		}
	}))
	defer upstream.Close()
	stream, _ := h.stream(t, fixtureStartWithHeaderTimeout(upstream.URL+"/codex/responses", "", 100))
	state := receiveDriverEvent(t, stream, "ready").SendState
	driverPermit(t, stream, &state)
	if head := receiveDriverEvent(t, stream, "head"); head.SendState != pb.SendState_SEND_STATE_SENT {
		t.Fatalf("head state=%v", head.SendState)
	}
	time.Sleep(250 * time.Millisecond)
	close(bodyReleased)
	var body bytes.Buffer
	for {
		value, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if value.GetFailure() != nil {
			t.Fatalf("header timeout interrupted body: %+v", value.GetFailure())
		}
		if value.GetBody() != nil {
			_, _ = body.Write(value.GetBody().Data)
		}
	}
	_ = stream.CloseSend()
	if !bytes.Contains(body.Bytes(), []byte("data: [DONE]")) || state != pb.SendState_SEND_STATE_AMBIGUOUS {
		t.Fatalf("body after head was not delivered: %q state=%v", body.Bytes(), state)
	}
}
