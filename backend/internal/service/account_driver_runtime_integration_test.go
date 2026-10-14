//go:build unit

package service

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"
)

// 原生桩只负责识别是否发生了错误回落，不模拟 Rust 上游请求
type accountDriverNativeTransportSpy struct{ calls atomic.Int32 }

func (s *accountDriverNativeTransportSpy) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	s.calls.Add(1)
	return &http.Response{StatusCode: 200, Header: http.Header{"X-Native-Transport": []string{"selected"}}, Body: io.NopCloser(strings.NewReader("native")), Request: req}, nil
}
func (s *accountDriverNativeTransportSpy) DoWithTLS(req *http.Request, proxy string, id int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return s.Do(req, proxy, id, concurrency)
}

func newAccountDriverRuntimeIntegration(t *testing.T) (*AccountDriverManager, *config.Config) {
	t.Helper()
	binary := os.Getenv("SUB2API_CODEX_DRIVER_BINARY")
	if binary == "" {
		t.Skip("set SUB2API_CODEX_DRIVER_BINARY to run the actual managed Rust service integration")
	}
	raw, err := os.ReadFile(binary)
	require.NoError(t, err)
	digest := sha256.Sum256(raw)
	cfg := &config.Config{AccountDrivers: config.AccountDriversConfig{CodexRSHTTP: config.CodexRSHTTPDriverConfig{
		Enabled: true, ExecutablePath: binary, SHA256: hex.EncodeToString(digest[:]), StartTimeoutSeconds: 10,
	}}, Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
	manager := NewAccountDriverManager(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, manager.Start(ctx))
	t.Cleanup(func() {
		manager.Stop()
		require.NoError(t, SetAccountDriverRuntimeCapabilities(AccountDriverCodexHTTP, false, "unconfigured", []string{"http", "sse"}))
	})
	return manager, cfg
}

func accountDriverIntegrationAccount() *Account {
	return &Account{ID: 42, Name: "fake-driver-integration", Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true, Concurrency: 1, AccountDriverID: AccountDriverCodexHTTP, AccountDriverRevision: 3,
		Credentials: map[string]any{"access_token": "fake-access-token", "chatgpt_account_id": "fake-account-42"},
	}
}

func accountDriverIntegrationRequest(t *testing.T, ctx context.Context, target string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(` {"model":"fake","vendor_unknown":{"keep":true}} `))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer fake-access-token")
	req.Header.Set("ChatGPT-Account-ID", "fake-account-42")
	req.Header.Add("X-Unknown-Request", "first")
	req.Header.Add("X-Unknown-Request", "second")
	return req
}

func TestManagedCodexDriverServiceDispatchPreservesWireAndStopsWithoutFallback(t *testing.T) {
	manager, _ := newAccountDriverRuntimeIntegration(t)
	var requests atomic.Int32
	rawSSE := ": preserved comment\n\nevent: future.vendor\ndata: {\"unknown\":true}\n\n"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		raw, _ := io.ReadAll(r.Body)
		if string(raw) != ` {"model":"fake","vendor_unknown":{"keep":true}} ` || r.Header.Get("Authorization") != "Bearer fake-access-token" || len(r.Header.Values("X-Unknown-Request")) != 2 {
			t.Error("prepared service request changed")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Add("Set-Cookie", "fixture=a")
		w.Header().Add("Set-Cookie", "fixture=b")
		w.Header().Add("X-Unknown", "one")
		w.Header().Add("X-Unknown", "two")
		w.WriteHeader(201)
		fmt.Fprint(w, rawSSE)
	}))
	defer upstream.Close()
	native := &accountDriverNativeTransportSpy{}
	plugins := &PluginManager{accountDriverManager: manager}
	// 已启用但不可用的 v1 插件会把错误叠加立即暴露给测试
	plugins.route.Store(&pluginRoute{rolloutPercent: 100, unavailable: "v1 must not wrap the explicit account driver"})
	svc := &OpenAIGatewayService{httpUpstream: native, pluginManager: plugins}
	account := accountDriverIntegrationAccount()
	require.True(t, usesCodexHTTPDriver(account))
	found := false
	for _, driver := range BuildAccountDriverCatalog().Drivers {
		if driver.ID == AccountDriverCodexHTTP {
			found = true
			require.True(t, driver.Available)
		}
	}
	require.True(t, found)
	trackedCtx, tracker := withAccountDriverAttemptTracking(context.Background())
	response, err := svc.doOpenAIUpstream(accountDriverIntegrationRequest(t, trackedCtx, upstream.URL+"/codex/responses"), "", account)
	require.NoError(t, err)
	require.Equal(t, 201, response.StatusCode)
	require.Len(t, response.Header.Values("Set-Cookie"), 2)
	require.Len(t, response.Header.Values("X-Unknown"), 2)
	raw, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, rawSSE, string(raw))
	require.True(t, tracker.sent.Load())
	require.EqualValues(t, 1, requests.Load())
	require.Zero(t, native.calls.Load())
	manager.Stop()
	ctx, stoppedTracker := withAccountDriverAttemptTracking(context.Background())
	_, err = svc.doOpenAIUpstream(accountDriverIntegrationRequest(t, ctx, upstream.URL+"/codex/responses"), "", account)
	var transportError *PluginTransportError
	require.ErrorAs(t, err, &transportError)
	require.Equal(t, "account_driver_unavailable", transportError.Code)
	require.False(t, transportError.RequestSent)
	require.False(t, stoppedTracker.sent.Load())
	require.Zero(t, native.calls.Load())
	require.EqualValues(t, 1, requests.Load())
	require.False(t, AccountDriverFromAccount(account).Available)
}

func TestManagedCodexDriverDelegatedEndpointAndNativeModeKeepHostTransport(t *testing.T) {
	manager, _ := newAccountDriverRuntimeIntegration(t)
	native := &accountDriverNativeTransportSpy{}
	plugins := &PluginManager{accountDriverManager: manager}
	plugins.route.Store(&pluginRoute{rolloutPercent: 100, unavailable: "v1 must not wrap explicitly delegated endpoints"})
	svc := &OpenAIGatewayService{httpUpstream: native, pluginManager: plugins}
	enhanced := accountDriverIntegrationAccount()
	response, err := svc.doOpenAIUpstream(accountDriverIntegrationRequest(t, context.Background(), "https://chatgpt.com/backend-api/codex/images/generations"), "", enhanced)
	require.NoError(t, err)
	require.Equal(t, "selected", response.Header.Get("X-Native-Transport"))
	_ = response.Body.Close()
	plugins.route.Store(nil)
	nativeAccount := accountDriverIntegrationAccount()
	nativeAccount.AccountDriverID = ""
	nativeAccount.AccountDriverRevision = 0
	response, err = svc.doOpenAIUpstream(accountDriverIntegrationRequest(t, context.Background(), "https://chatgpt.com/backend-api/codex/responses"), "", nativeAccount)
	require.NoError(t, err)
	require.Equal(t, "selected", response.Header.Get("X-Native-Transport"))
	_ = response.Body.Close()
	require.EqualValues(t, 2, native.calls.Load())
}

// TLS fixture 的连接目标固定为本机 listener，即使 URL 是 chatgpt.com 也不会访问外网
func accountDriverTLSProxyFixture(t *testing.T, handler http.Handler) (*Proxy, *atomic.Int32) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Sub2API fake driver CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "chatgpt.com"}, DNSNames: []string{"chatgpt.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, &leafKey.PublicKey, caKey)
	require.NoError(t, err)
	certPath := filepath.Join(t.TempDir(), "driver-fake-ca.pem")
	require.NoError(t, os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0600))
	t.Setenv("SSL_CERT_FILE", certPath)
	upstream := httptest.NewUnstartedServer(handler)
	upstream.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{leafDER, caDER}, PrivateKey: leafKey}}, MinVersion: tls.VersionTLS12}
	upstream.StartTLS()
	t.Cleanup(upstream.Close)
	var sockets sync.Map
	connects := &atomic.Int32{}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect || r.Host != "chatgpt.com:443" {
			t.Errorf("unexpected proxy destination %s %s", r.Method, r.Host)
			http.Error(w, "fixture destination denied", 400)
			return
		}
		connects.Add(1)
		remote, err := net.DialTimeout("tcp", upstream.Listener.Addr().String(), time.Second)
		if err != nil {
			t.Error(err)
			http.Error(w, "fixture dial failed", 502)
			return
		}
		client, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			_ = remote.Close()
			t.Error(err)
			return
		}
		sockets.Store(client, remote)
		defer sockets.Delete(client)
		defer client.Close()
		defer remote.Close()
		if _, err := rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			return
		}
		if err := rw.Flush(); err != nil {
			return
		}
		copied := make(chan struct{})
		go func() { _, _ = io.Copy(remote, rw); _ = remote.Close(); close(copied) }()
		_, _ = io.Copy(client, remote)
		_ = client.Close()
		<-copied
	}))
	t.Cleanup(func() {
		sockets.Range(func(client, remote any) bool {
			_ = client.(net.Conn).Close()
			_ = remote.(net.Conn).Close()
			return true
		})
		proxy.Close()
	})
	parsed, err := url.Parse(proxy.URL)
	require.NoError(t, err)
	port, err := strconv.Atoi(parsed.Port())
	require.NoError(t, err)
	return &Proxy{ID: 91, Name: "local-connect-fixture", Protocol: "http", Host: parsed.Hostname(), Port: port, Status: StatusActive}, connects
}

func accountDriverForwardFixture(t *testing.T, handler http.Handler) (*OpenAIGatewayService, *Account, *accountDriverNativeTransportSpy, *atomic.Int32) {
	t.Helper()
	if os.Getenv("SUB2API_CODEX_DRIVER_BINARY") == "" {
		t.Skip("set SUB2API_CODEX_DRIVER_BINARY to run the actual managed Rust service integration")
	}
	proxy, connects := accountDriverTLSProxyFixture(t, handler)
	manager, cfg := newAccountDriverRuntimeIntegration(t)
	native := &accountDriverNativeTransportSpy{}
	svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: native, pluginManager: &PluginManager{accountDriverManager: manager}, cache: &stubGatewayCache{}, toolCorrector: NewCodexToolCorrector(), openaiWSResolver: NewOpenAIWSProtocolResolver(cfg)}
	account := accountDriverIntegrationAccount()
	account.Proxy = proxy
	account.ProxyID = &proxy.ID
	return svc, account, native, connects
}

func accountDriverForwardGinContext(body []byte) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.144.1")
	return c, rec
}

func TestManagedCodexDriverForwardRunsNativeUsageParsing(t *testing.T) {
	var requests atomic.Int32
	svc, account, native, connects := accountDriverForwardFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		raw, _ := io.ReadAll(r.Body)
		if r.Header.Get("Content-Encoding") != "zstd" {
			t.Error("managed Codex request must use zstd encoding")
			return
		}
		decoder, err := zstd.NewReader(nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer decoder.Close()
		raw, err = decoder.DecodeAll(raw, nil)
		if err != nil {
			t.Error(err)
			return
		}
		if r.URL.Path != "/backend-api/codex/responses" || r.Header.Get("Authorization") != "Bearer fake-access-token" || !bytes.Contains(raw, []byte("vendor_unknown")) {
			t.Error("native prepared request identity or body lost")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Request-ID", "driver-native-usage")
		w.Header().Add("X-Driver-Unknown", "first")
		w.Header().Add("X-Driver-Unknown", "second")
		fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_driver\",\"model\":\"gpt-5.5\",\"status\":\"in_progress\"}}\n\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hello from driver\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_driver\",\"model\":\"gpt-5.5\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello from driver\"}]}],\"usage\":{\"input_tokens\":17,\"output_tokens\":5,\"total_tokens\":22,\"input_tokens_details\":{\"cached_tokens\":4}}}}\n\n")
	}))
	body := []byte(`{"model":"gpt-5.5","stream":true,"instructions":"fixture","input":"hello","vendor_unknown":{"keep":true}}`)
	c, rec := accountDriverForwardGinContext(body)
	trackedCtx, tracker := withAccountDriverAttemptTracking(context.Background())
	result, err := svc.Forward(trackedCtx, c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 17, result.Usage.InputTokens)
	require.Equal(t, 5, result.Usage.OutputTokens)
	require.Equal(t, 4, result.Usage.CacheReadInputTokens)
	require.Equal(t, "driver-native-usage", result.RequestID)
	require.Len(t, result.UpstreamHeaders.Values("X-Driver-Unknown"), 2)
	require.Contains(t, rec.Body.String(), "hello from driver")
	require.True(t, tracker.sent.Load(), "detached native contexts must retain the same attempt tracker")
	require.EqualValues(t, 1, requests.Load())
	require.EqualValues(t, 1, connects.Load())
	require.Zero(t, native.calls.Load())
}

func TestManagedCodexDriverForward503StopsAccountFailover(t *testing.T) {
	var requests atomic.Int32
	svc, account, native, _ := accountDriverForwardFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(503)
		fmt.Fprint(w, `{"error":{"type":"server_error","message":"fixture upstream unavailable"}}`)
	}))
	body := []byte(`{"model":"gpt-5.5","stream":true,"instructions":"fixture","input":"hello"}`)
	c, _ := accountDriverForwardGinContext(body)
	ctx, tracker := withAccountDriverAttemptTracking(context.Background())
	_, err := svc.Forward(ctx, c, account, body)
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover)
	require.Equal(t, 503, failover.StatusCode)
	require.Equal(t, NextAccountStop, failover.NextAccountAction)
	require.False(t, failover.ShouldRetryNextAccount())
	require.False(t, failover.RetryableOnSameAccount)
	require.False(t, failover.SafeToFailoverAfterWrite)
	require.True(t, tracker.sent.Load())
	require.EqualValues(t, 1, requests.Load())
	require.Zero(t, native.calls.Load())
}

func TestManagedCodexDriverFirstOutputTimeoutKeepsSendingWatermark(t *testing.T) {
	var requests atomic.Int32
	canceled := make(chan struct{})
	svc, account, native, _ := accountDriverForwardFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
		close(canceled)
	}))
	svc.cfg.Gateway.OpenAIFirstOutputTimeoutSeconds = 1
	body := []byte(`{"model":"gpt-5.5","stream":true,"reasoning":{"effort":"low"},"instructions":"fixture","input":"hello"}`)
	c, rec := accountDriverForwardGinContext(body)
	ctx, tracker := withAccountDriverAttemptTracking(context.Background())
	_, err := svc.Forward(ctx, c, account, body)
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover)
	require.Equal(t, http.StatusGatewayTimeout, failover.StatusCode)
	require.Contains(t, string(failover.ResponseBody), "first_output_timeout")
	require.Equal(t, NextAccountStop, failover.NextAccountAction)
	require.False(t, failover.ShouldRetryNextAccount())
	require.False(t, failover.SafeToFailoverAfterWrite)
	require.True(t, tracker.sent.Load())
	require.Empty(t, rec.Body.String())
	require.EqualValues(t, 1, requests.Load())
	require.Zero(t, native.calls.Load())
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("first-output timeout did not cancel the actual Rust upstream")
	}
}
