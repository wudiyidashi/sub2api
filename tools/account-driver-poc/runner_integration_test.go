package poc

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
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	pb "example.com/sub2api/account-driver-poc/pocv1"
	"github.com/klauspost/compress/zstd"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type callbacks struct {
	pb.UnimplementedHostCallbacksServer
	token string
	calls atomic.Int32
}

func (s *callbacks) ResolveIdentity(ctx context.Context, req *pb.Start) (*pb.Identity, error) {
	values := metadata.ValueFromIncomingContext(ctx, "authorization")
	if len(values) != 1 || values[0] != s.token {
		return nil, status.Error(codes.Unauthenticated, "callback identity")
	}
	if req.AccountId != 42 || req.CredentialRevision != 9 || req.BindingRevision != 3 {
		return nil, status.Error(codes.PermissionDenied, "selected account or revision changed")
	}
	s.calls.Add(1)
	return &pb.Identity{Authorization: "Bearer fake-access-token", UpstreamAccountId: "fake-account-42"}, nil
}

type harness struct {
	client    pb.DriverClient
	token     string
	callbacks *callbacks
	cmd       *exec.Cmd
	exited    chan error
}

func newHarness(t *testing.T) *harness {
	t.Helper()
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
	cb := &callbacks{token: token}
	pb.RegisterHostCallbacksServer(callbackServer, cb)
	go func() { _ = callbackServer.Serve(listener) }()
	t.Cleanup(callbackServer.Stop)

	binary := os.Getenv("ACCOUNT_DRIVER_POC_BINARY")
	if binary == "" {
		binary = filepath.Join("target", "debug", "account-driver-poc")
		if runtime.GOOS == "windows" {
			binary += ".exe"
		}
	}
	cmd := exec.Command(binary)
	cmd.Env = append(os.Environ(), "ACCOUNT_DRIVER_POC_TOKEN="+token, "ACCOUNT_DRIVER_POC_CALLBACK=http://"+listener.Addr().String())
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
			t.Error("runner did not exit on stdin EOF")
		}
	})
	handshake := make(chan []byte, 1)
	go func() { line, _ := bufio.NewReader(stdout).ReadBytes('\n'); handshake <- line }()
	var hello struct{ Protocol, Address, Transport string }
	select {
	case line := <-handshake:
		if err := json.Unmarshal(line, &hello); err != nil {
			t.Fatalf("handshake: %v; stderr: %s", err, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("handshake timeout")
	}
	if hello.Protocol != "account-driver-poc-v1" || hello.Transport != "grpc" {
		t.Fatalf("unexpected handshake: %+v", hello)
	}
	conn, err := grpc.NewClient(hello.Address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &harness{client: pb.NewDriverClient(conn), token: token, callbacks: cb, cmd: cmd, exited: exited}
}

func (h *harness) stream(t *testing.T, baseURL string, account int64) (grpc.BidiStreamingClient[pb.Command, pb.Event], context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", h.token)
	stream, err := h.client.Execute(ctx)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"model":"fake-model","input":[],"instructions":"fixture","store":false,"vendor_unknown":{"keep":true}}`)
	if err := stream.Send(&pb.Command{Frame: &pb.Command_Start{Start: &pb.Start{AttemptId: "attempt-fixture", AccountId: account, CredentialRevision: 9, BindingRevision: 3, BaseUrl: baseURL, Body: body}}}); err != nil {
		t.Fatal(err)
	}
	return stream, cancel
}

func receive(t *testing.T, stream grpc.BidiStreamingClient[pb.Command, pb.Event], kind string) *pb.Event {
	t.Helper()
	for {
		value, err := stream.Recv()
		if err != nil {
			t.Fatalf("receiving %s: %v", kind, err)
		}
		if value.Kind == kind {
			return value
		}
	}
}

func permit(t *testing.T, stream grpc.BidiStreamingClient[pb.Command, pb.Event], state *string) {
	t.Helper()
	// 发送许可前先在宿主记录 ambiguous，崩溃不能抹掉这条事实
	*state = "ambiguous"
	if err := stream.Send(&pb.Command{Frame: &pb.Command_PermitSend{PermitSend: true}}); err != nil {
		t.Fatal(err)
	}
}

func TestCPRSelectedAccountSSE(t *testing.T) {
	h := newHarness(t)
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/codex/responses" || r.Header.Get("Authorization") != "Bearer fake-access-token" || r.Header.Get("ChatGPT-Account-ID") != "fake-account-42" {
			t.Error("CPR request did not preserve selected account identity")
		}
		if r.Header.Get("Content-Encoding") != "zstd" {
			t.Error("CPR zstd encoder not used")
		}
		compressed, _ := io.ReadAll(r.Body)
		decoder, err := zstd.NewReader(nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer decoder.Close()
		raw, err := decoder.DecodeAll(compressed, nil)
		if err != nil {
			t.Error(err)
			return
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Error(err)
			return
		}
		if body["stream"] != true || body["vendor_unknown"] == nil {
			t.Error("CPR request lost unknown fields or stream enforcement")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": unknown comment\n\nevent: future.vendor\ndata: {\"unknown\":true}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"fixture-response\",\"usage\":{\"input_tokens\":7,\"output_tokens\":3}}}\n\n")
	}))
	defer upstream.Close()
	stream, _ := h.stream(t, upstream.URL, 42)
	state := receive(t, stream, "ready").SendState
	if state != "not_sent" {
		t.Fatal(state)
	}
	permit(t, stream, &state)
	var body bytes.Buffer
	var usage *pb.Event
	for {
		value, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		state = value.SendState
		if value.Kind == "body" {
			body.Write(value.Body)
		}
		if value.Kind == "usage" {
			usage = value
		}
		if value.Kind == "failed" {
			t.Fatalf("attempt failed: %+v", value)
		}
	}
	_ = stream.CloseSend()
	if !bytes.Contains(body.Bytes(), []byte("event: future.vendor\ndata: {\"unknown\":true}")) || !bytes.Contains(body.Bytes(), []byte(": unknown comment")) {
		t.Fatalf("raw SSE changed: %s", body.String())
	}
	if state != "sent" || usage == nil || usage.InputTokens != 7 || usage.OutputTokens != 3 {
		t.Fatalf("state or usage: %s %+v", state, usage)
	}
	if requests.Load() != 1 || h.callbacks.calls.Load() != 1 {
		t.Fatal("runner performed additional attempts or selected another account")
	}
}

func TestCancelBeforePermitNeverSends(t *testing.T) {
	h := newHarness(t)
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer upstream.Close()
	stream, _ := h.stream(t, upstream.URL, 42)
	receive(t, stream, "ready")
	if err := stream.Send(&pb.Command{Frame: &pb.Command_Cancel{Cancel: true}}); err != nil {
		t.Fatal(err)
	}
	value := receive(t, stream, "cancelled")
	_ = stream.CloseSend()
	if value.SendState != "not_sent" || requests.Load() != 0 || h.callbacks.calls.Load() != 1 {
		t.Fatal("cancelled attempt performed upstream work")
	}
}

func TestCallbackScopeRejectsAnotherAccount(t *testing.T) {
	h := newHarness(t)
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer upstream.Close()
	stream, _ := h.stream(t, upstream.URL, 99)
	value := receive(t, stream, "failed")
	_ = stream.CloseSend()
	if value.SendState != "not_sent" || value.ErrorCode != "identity_rejected" || requests.Load() != 0 {
		t.Fatal("host identity scope was bypassed")
	}
}

func TestCrashAfterSendRemainsAmbiguous(t *testing.T) {
	h := newHarness(t)
	arrived := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(arrived)
		<-r.Context().Done()
	}))
	defer upstream.Close()
	stream, _ := h.stream(t, upstream.URL, 42)
	state := receive(t, stream, "ready").SendState
	permit(t, stream, &state)
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("request not received")
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
	if state != "ambiguous" {
		t.Fatal("process loss incorrectly made request replayable")
	}
}

func TestCancelWithResponseBackpressure(t *testing.T) {
	h := newHarness(t)
	disconnected := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		payload := []byte("event: response.output_text.delta\ndata: {\"delta\":\"" + string(bytes.Repeat([]byte("x"), 65536)) + "\"}\n\n")
		for {
			select {
			case <-r.Context().Done():
				close(disconnected)
				return
			default:
			}
			if _, err := w.Write(payload); err != nil {
				close(disconnected)
				return
			}
			w.(http.Flusher).Flush()
		}
	}))
	defer upstream.Close()
	stream, _ := h.stream(t, upstream.URL, 42)
	state := receive(t, stream, "ready").SendState
	permit(t, stream, &state)
	if value := receive(t, stream, "headers"); value.SendState != "sent" {
		t.Fatal(value)
	}
	// 宿主停止读响应，仍能在双向请求流发取消，HTTP连接应立即关闭
	time.Sleep(100 * time.Millisecond)
	if err := stream.Send(&pb.Command{Frame: &pb.Command_Cancel{Cancel: true}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-disconnected:
	case <-time.After(3 * time.Second):
		t.Fatal("backpressure prevented cancellation")
	}
	_ = stream.CloseSend()
}

func TestRunnerRejectsMissingAuthentication(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream, err := h.client.Execute(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = stream.Recv()
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing auth accepted: %v", err)
	}
}

func TestExplicitUpstreamRejectionIsSent(t *testing.T) {
	h := newHarness(t)
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"code":"rate_limit_exceeded","unknown":"preserved"}}`)
	}))
	defer upstream.Close()
	stream, _ := h.stream(t, upstream.URL, 42)
	state := receive(t, stream, "ready").SendState
	permit(t, stream, &state)
	value := receive(t, stream, "failed")
	_ = stream.CloseSend()
	if value.SendState != "sent" || value.Status != 429 || !bytes.Contains(value.Body, []byte(`"unknown":"preserved"`)) || requests.Load() != 1 {
		t.Fatalf("explicit rejection lost sending facts: %+v requests=%d", value, requests.Load())
	}
}
