// Package runner manages the authenticated local Rust transport process.
package runner

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	api "github.com/Wei-Shaw/sub2api/pkg/accountdriverapi/v1"
	"golang.org/x/net/http/httpguts"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const DriverID = "codex-rs-http"

type Config struct {
	ExecutablePath string
	SHA256         string
	StartTimeout   time.Duration
	OnExit         func()
}

type Info struct {
	Protocol       string   `json:"protocol"`
	APIVersion     int      `json:"api_version"`
	Address        string   `json:"address"`
	DriverID       string   `json:"driver_id"`
	Version        string   `json:"driver_version"`
	UpstreamCommit string   `json:"upstream_commit"`
	Transports     []string `json:"transports"`
}

// Error carries the host's conservative sending watermark, not a network guess.
type Error struct {
	Code      string
	SendState api.SendState
	Cause     error
}

func (e *Error) Error() string     { return "account driver: " + e.Code }
func (e *Error) Unwrap() error     { return e.Cause }
func (e *Error) RequestSent() bool { return e.SendState != api.SendState_SEND_STATE_NOT_SENT }

type identity struct {
	requestID             string
	accountID             int64
	bindingRevision       int64
	headers               []*api.Header
	proxyURL              string
	sessionID             string
	keyScopeHash          string
	credentialFingerprint string
}

type Client struct {
	api.UnimplementedHostCallbacksServer
	mu         sync.Mutex
	identities map[string]identity
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	server     *grpc.Server
	connection *grpc.ClientConn
	api        api.AccountDriverClient
	info       Info
	token      string
	done       chan struct{}
	stopOnce   sync.Once
	closed     bool
}

func Start(ctx context.Context, cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.ExecutablePath) == "" {
		return nil, errors.New("account driver executable is required")
	}
	if cfg.StartTimeout <= 0 {
		cfg.StartTimeout = 10 * time.Second
	}
	if cfg.SHA256 != "" {
		file, err := os.Open(cfg.ExecutablePath)
		if err != nil {
			return nil, errors.New("account driver executable is unavailable")
		}
		digest := sha256.New()
		_, err = io.Copy(digest, file)
		_ = file.Close()
		if err != nil || !strings.EqualFold(hex.EncodeToString(digest.Sum(nil)), cfg.SHA256) {
			return nil, errors.New("account driver executable checksum does not match")
		}
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	client := &Client{identities: make(map[string]identity), token: hex.EncodeToString(key), done: make(chan struct{})}
	client.server = grpc.NewServer()
	api.RegisterHostCallbacksServer(client.server, client)
	go func() { _ = client.server.Serve(listener) }()
	cmd := exec.Command(cfg.ExecutablePath)
	hideProcessWindow(cmd)
	// The child receives only runtime essentials and its ephemeral RPC identity.
	for _, name := range []string{"PATH", "SystemRoot", "TEMP", "TMP", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
		if value, ok := os.LookupEnv(name); ok {
			cmd.Env = append(cmd.Env, name+"="+value)
		}
	}
	cmd.Env = append(cmd.Env, "SUB2API_ACCOUNT_DRIVER_TOKEN="+client.token, "SUB2API_ACCOUNT_DRIVER_CALLBACK=http://"+listener.Addr().String())
	cmd.Stderr = io.Discard
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		client.server.Stop()
		return nil, err
	}
	client.stdin, err = cmd.StdinPipe()
	if err != nil {
		client.server.Stop()
		return nil, err
	}
	client.cmd = cmd
	if err = cmd.Start(); err != nil {
		client.server.Stop()
		_ = client.stdin.Close()
		return nil, errors.New("account driver process could not start")
	}
	go func() {
		_ = cmd.Wait()
		client.mu.Lock()
		client.closed = true
		connection := client.connection
		client.mu.Unlock()
		if connection != nil {
			_ = connection.Close()
		}
		client.server.Stop()
		close(client.done)
		if cfg.OnExit != nil {
			cfg.OnExit()
		}
	}()
	handshake := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 4096), 4096)
		if !scanner.Scan() {
			handshake <- errors.New("account driver did not return a handshake")
			return
		}
		var info Info
		if err := json.Unmarshal(scanner.Bytes(), &info); err != nil {
			handshake <- errors.New("account driver handshake is invalid")
			return
		}
		if err := validateInfo(info); err != nil {
			handshake <- err
			return
		}
		client.mu.Lock()
		client.info = info
		client.mu.Unlock()
		handshake <- nil
		// Any additional stdout is discarded rather than copied to ordinary logs.
		_, _ = io.Copy(io.Discard, stdout)
	}()
	startCtx, cancel := context.WithTimeout(ctx, cfg.StartTimeout)
	defer cancel()
	select {
	case err := <-handshake:
		if err != nil {
			client.Close()
			return nil, err
		}
	case <-startCtx.Done():
		client.Close()
		return nil, errors.New("account driver startup timed out")
	case <-client.done:
		return nil, errors.New("account driver exited during startup")
	}
	connection, err := grpc.NewClient(client.info.Address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(1<<20), grpc.MaxCallSendMsgSize(257<<20)),
	)
	if err != nil {
		client.Close()
		return nil, errors.New("account driver connection is unavailable")
	}
	client.mu.Lock()
	if client.closed {
		client.mu.Unlock()
		_ = connection.Close()
		return nil, errors.New("account driver exited during startup")
	}
	client.connection = connection
	client.api = api.NewAccountDriverClient(connection)
	client.mu.Unlock()
	return client, nil
}

func validateInfo(info Info) error {
	host, port, err := net.SplitHostPort(strings.TrimPrefix(info.Address, "http://"))
	if err != nil {
		return errors.New("account driver handshake address is invalid")
	}
	ip := net.ParseIP(host)
	number, err := strconv.Atoi(port)
	if ip == nil || !ip.IsLoopback() || err != nil || number <= 0 || number > 65535 ||
		info.Protocol != "sub2api-account-driver-v1" || info.APIVersion != 1 || info.DriverID != DriverID || info.Version == "" {
		return errors.New("account driver handshake is incompatible")
	}
	return nil
}

func (c *Client) Info() Info {
	c.mu.Lock()
	defer c.mu.Unlock()
	info := c.info
	info.Transports = slices.Clone(info.Transports)
	return info
}
func (c *Client) Available() bool { c.mu.Lock(); defer c.mu.Unlock(); return !c.closed && c.api != nil }
func (c *Client) Close() {
	c.stopOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		connection := c.connection
		c.mu.Unlock()
		if connection != nil {
			_ = connection.Close()
		}
		if c.stdin != nil {
			_ = c.stdin.Close()
		}
		if c.cmd != nil && c.cmd.Process != nil {
			select {
			case <-c.done:
			case <-time.After(2 * time.Second):
				_ = c.cmd.Process.Kill()
				<-c.done
			}
		}
		c.server.Stop()
	})
}

func (c *Client) ResolveIdentity(ctx context.Context, request *api.IdentityRequest) (*api.IdentityResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	auth := md.Get("authorization")
	if len(auth) != 1 || subtle.ConstantTimeCompare([]byte(auth[0]), []byte(c.token)) != 1 {
		return nil, status.Error(codes.Unauthenticated, "driver identity required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	identity, ok := c.identities[request.AttemptId]
	if c.closed || !ok || identity.accountID != request.AccountId || identity.requestID != request.RequestId || identity.bindingRevision != request.BindingRevision || request.CredentialRevision != 0 || identity.sessionID != request.SessionId || identity.keyScopeHash != request.KeyScopeHash || identity.credentialFingerprint != request.CredentialFingerprint {
		return nil, status.Error(codes.PermissionDenied, "attempt identity is not active")
	}
	return &api.IdentityResponse{Authorized: true, ProtectedHeaders: identity.headers, ProxyUrl: identity.proxyURL}, nil
}

func headersToWire(headers http.Header, protectedOnly bool) []*api.Header {
	var result []*api.Header
	hopHeaders := make(map[string]bool)
	for _, value := range headers.Values("Connection") {
		for _, name := range strings.Split(value, ",") {
			hopHeaders[strings.ToLower(strings.TrimSpace(name))] = true
		}
	}
	for name, values := range headers {
		lower := strings.ToLower(name)
		if hopHeaders[lower] {
			continue
		}
		protected := lower == "authorization" || lower == "chatgpt-account-id"
		if protectedOnly != protected {
			continue
		}
		switch lower {
		case "host", "connection", "proxy-connection", "transfer-encoding", "upgrade", "content-length", "proxy-authorization", "keep-alive", "te", "trailer":
			continue
		}
		for _, value := range values {
			result = append(result, &api.Header{Name: name, Value: []byte(value)})
		}
	}
	return result
}

func (c *Client) RoundTrip(request *http.Request, proxyURL string, accountID, bindingRevision int64) (*http.Response, error) {
	return c.RoundTripWithObserverTimeout(request, proxyURL, accountID, bindingRevision, 0, nil)
}

func (c *Client) RoundTripWithObserver(request *http.Request, proxyURL string, accountID, bindingRevision int64, observe func(api.SendState)) (*http.Response, error) {
	return c.RoundTripWithObserverTimeout(request, proxyURL, accountID, bindingRevision, 0, observe)
}

func (c *Client) RoundTripWithObserverTimeout(request *http.Request, proxyURL string, accountID, bindingRevision int64, headerTimeout time.Duration, observe func(api.SendState)) (*http.Response, error) {
	if !c.Available() {
		return nil, &Error{Code: "unavailable", SendState: api.SendState_SEND_STATE_NOT_SENT}
	}
	var body []byte
	var err error
	if request.Body != nil {
		body, err = io.ReadAll(request.Body)
		_ = request.Body.Close()
		if err != nil {
			return nil, &Error{Code: "request_body", SendState: api.SendState_SEND_STATE_NOT_SENT, Cause: err}
		}
	}
	key := make([]byte, 16)
	if _, err = rand.Read(key); err != nil {
		return nil, err
	}
	attemptID := hex.EncodeToString(key)
	requestID := request.Header.Get("X-Request-ID")
	if requestID == "" {
		requestID = attemptID
	}
	ctx, cancel := context.WithCancel(request.Context())
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", c.token)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		cancel()
		return nil, &Error{Code: "unavailable", SendState: api.SendState_SEND_STATE_NOT_SENT}
	}
	c.identities[attemptID] = identity{requestID: requestID, accountID: accountID, bindingRevision: bindingRevision, headers: headersToWire(request.Header, true), proxyURL: proxyURL}
	client := c.api
	c.mu.Unlock()
	cleanup := func() { cancel(); c.mu.Lock(); delete(c.identities, attemptID); c.mu.Unlock() }
	stream, err := client.Execute(ctx)
	if err != nil {
		cleanup()
		return nil, &Error{Code: "connect", SendState: api.SendState_SEND_STATE_NOT_SENT, Cause: err}
	}
	state := &atomic.Int32{}
	state.Store(int32(api.SendState_SEND_STATE_NOT_SENT))
	headerTimeoutMs := int64(0)
	if headerTimeout > 0 {
		headerTimeoutMs = headerTimeout.Milliseconds()
	}
	start := &api.ExecuteStart{RequestId: requestID, AttemptId: attemptID, AccountId: accountID, BindingRevision: bindingRevision, Method: request.Method, Url: request.URL.String(), Headers: headersToWire(request.Header, false), Body: body, ProxyUrl: proxyURL, HeaderTimeoutMs: headerTimeoutMs}
	if err = stream.Send(&api.ExecuteCommand{Frame: &api.ExecuteCommand_Start{Start: start}}); err != nil {
		cleanup()
		return nil, &Error{Code: "start", SendState: api.SendState_SEND_STATE_NOT_SENT, Cause: err}
	}
	permitted := false
	for {
		event, receiveErr := stream.Recv()
		if receiveErr != nil {
			cleanup()
			return nil, &Error{Code: "disconnected", SendState: api.SendState(state.Load()), Cause: receiveErr}
		}
		if event.SendState == api.SendState_SEND_STATE_UNSPECIFIED || int32(event.SendState) < state.Load() {
			cleanup()
			return nil, &Error{Code: "invalid_send_state", SendState: api.SendState(state.Load())}
		}
		state.Store(int32(event.SendState))
		switch frame := event.Frame.(type) {
		case *api.ExecuteEvent_Ready:
			if permitted || event.SendState != api.SendState_SEND_STATE_NOT_SENT {
				cleanup()
				return nil, &Error{Code: "invalid_ready", SendState: api.SendState(state.Load())}
			}
			// Freeze the watermark before allowing the process to send upstream.
			state.Store(int32(api.SendState_SEND_STATE_AMBIGUOUS))
			if observe != nil {
				observe(api.SendState_SEND_STATE_AMBIGUOUS)
			}
			permitted = true
			if err = stream.Send(&api.ExecuteCommand{Frame: &api.ExecuteCommand_PermitSend{PermitSend: true}}); err != nil {
				cleanup()
				return nil, &Error{Code: "permission_send", SendState: api.SendState_SEND_STATE_AMBIGUOUS, Cause: err}
			}
		case *api.ExecuteEvent_StateChanged:
			if !permitted {
				cleanup()
				return nil, &Error{Code: "invalid_state_change", SendState: api.SendState(state.Load())}
			}
		case *api.ExecuteEvent_Head:
			if !permitted || event.SendState != api.SendState_SEND_STATE_SENT || frame.Head.StatusCode < 100 || frame.Head.StatusCode > 599 {
				cleanup()
				return nil, &Error{Code: "invalid_response_head", SendState: api.SendState(state.Load())}
			}
			headers := make(http.Header)
			for _, header := range frame.Head.Headers {
				if !httpguts.ValidHeaderFieldName(header.Name) || !httpguts.ValidHeaderFieldValue(string(header.Value)) {
					cleanup()
					return nil, &Error{Code: "invalid_response_headers", SendState: api.SendState_SEND_STATE_SENT}
				}
				headers.Add(header.Name, string(header.Value))
			}
			protocol := frame.Head.Protocol
			if protocol == "" {
				protocol = "HTTP/1.1"
			}
			major, minor, validProtocol := http.ParseHTTPVersion(protocol)
			if !validProtocol {
				cleanup()
				return nil, &Error{Code: "invalid_response_protocol", SendState: api.SendState_SEND_STATE_SENT}
			}
			reader, writer := io.Pipe()
			responseBody := &responseBody{reader: reader, cleanup: cleanup}
			bodyDone := make(chan struct{})
			go func() {
				select {
				case <-ctx.Done():
					_ = writer.CloseWithError(&Error{Code: "response_cancelled", SendState: api.SendState(state.Load()), Cause: ctx.Err()})
				case <-bodyDone:
				}
			}()
			go func() { defer close(bodyDone); receiveBody(stream, writer, state, cleanup) }()
			statusCode := int(frame.Head.StatusCode)
			return &http.Response{StatusCode: statusCode, Status: fmt.Sprintf("%d %s", statusCode, http.StatusText(statusCode)), Header: headers, Body: responseBody, ContentLength: -1, Proto: protocol, ProtoMajor: major, ProtoMinor: minor, Request: request}, nil
		case *api.ExecuteEvent_Failure:
			cleanup()
			return nil, &Error{Code: safeFailureCode(frame.Failure.Code, "attempt_failed"), SendState: api.SendState(state.Load())}
		default:
			cleanup()
			return nil, &Error{Code: "invalid_event_before_headers", SendState: api.SendState(state.Load())}
		}
	}
}

type responseBody struct {
	reader  *io.PipeReader
	cleanup func()
	once    sync.Once
}

func safeFailureCode(code, fallback string) string {
	if len(code) == 0 || len(code) > 64 {
		return fallback
	}
	for _, value := range code {
		if value < 'a' || value > 'z' {
			if value != '_' && value != '-' && (value < '0' || value > '9') {
				return fallback
			}
		}
	}
	return code
}

func (b *responseBody) Read(p []byte) (int, error) { return b.reader.Read(p) }
func (b *responseBody) Close() error               { b.once.Do(b.cleanup); return b.reader.Close() }

func receiveBody(stream grpc.BidiStreamingClient[api.ExecuteCommand, api.ExecuteEvent], writer *io.PipeWriter, state *atomic.Int32, cleanup func()) {
	defer cleanup()
	defer writer.Close()
	for {
		event, err := stream.Recv()
		if err != nil {
			_ = writer.CloseWithError(&Error{Code: "response_interrupted", SendState: api.SendState(state.Load()), Cause: err})
			return
		}
		if event.SendState != api.SendState_SEND_STATE_SENT {
			_ = writer.CloseWithError(&Error{Code: "invalid_body_send_state", SendState: api.SendState_SEND_STATE_SENT})
			return
		}
		switch frame := event.Frame.(type) {
		case *api.ExecuteEvent_Body:
			if _, err = writer.Write(frame.Body.Data); err != nil {
				return
			}
		case *api.ExecuteEvent_End:
			return
		case *api.ExecuteEvent_Failure:
			_ = writer.CloseWithError(&Error{Code: "upstream_stream_failed", SendState: api.SendState_SEND_STATE_SENT})
			return
		default:
			_ = writer.CloseWithError(&Error{Code: "invalid_response_event", SendState: api.SendState_SEND_STATE_SENT})
			return
		}
	}
}
