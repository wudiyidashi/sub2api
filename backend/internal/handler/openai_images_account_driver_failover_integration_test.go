//go:build unit

package handler

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
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type imageDriverEmptyPluginRepo struct{ service.PluginRepository }

func (imageDriverEmptyPluginRepo) List(context.Context) ([]*service.PluginInstallation, error) {
	return nil, nil
}

// CONNECT 的实际连接目标始终是本机，不会访问真实 chatgpt.com
func imagesDriverTLSProxy(t *testing.T, handler http.Handler) *service.Proxy {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Images driver fixture CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "chatgpt.com"}, DNSNames: []string{"chatgpt.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, &leafKey.PublicKey, caKey)
	require.NoError(t, err)
	caPath := filepath.Join(t.TempDir(), "fake-ca.pem")
	require.NoError(t, os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0600))
	t.Setenv("SSL_CERT_FILE", caPath)
	upstream := httptest.NewUnstartedServer(handler)
	upstream.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{leafDER, caDER}, PrivateKey: leafKey}}, MinVersion: tls.VersionTLS12}
	upstream.StartTLS()
	t.Cleanup(upstream.Close)
	var sockets sync.Map
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect || r.Host != "chatgpt.com:443" {
			t.Errorf("unexpected CONNECT destination %s %s", r.Method, r.Host)
			http.Error(w, "fixture destination denied", http.StatusBadRequest)
			return
		}
		remote, err := net.DialTimeout("tcp", upstream.Listener.Addr().String(), time.Second)
		if err != nil {
			t.Error(err)
			http.Error(w, "fixture unavailable", http.StatusBadGateway)
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
	proxyURL, err := url.Parse(proxy.URL)
	require.NoError(t, err)
	port, err := strconv.Atoi(proxyURL.Port())
	require.NoError(t, err)
	return &service.Proxy{ID: 911, Name: "image-driver-local-connect", Protocol: "http", Host: proxyURL.Hostname(), Port: port, Status: service.StatusActive}
}

func TestOpenAIGatewayHandlerImages_ManagedDriverStopDoesNotReplayNextAccount(t *testing.T) {
	binary := os.Getenv("SUB2API_CODEX_DRIVER_BINARY")
	if binary == "" {
		t.Skip("set SUB2API_CODEX_DRIVER_BINARY to run the real Rust Images handler regression")
	}
	for _, mode := range []string{"http_503", "sse_server_error"} {
		t.Run(mode, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			var mu sync.Mutex
			var upstreamAccounts []string
			proxy := imagesDriverTLSProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if r.URL.Path != "/backend-api/codex/responses" {
					t.Errorf("Images request bypassed Responses transport: %s", r.URL.Path)
				}
				mu.Lock()
				upstreamAccounts = append(upstreamAccounts, r.Header.Get("ChatGPT-Account-ID"))
				mu.Unlock()
				if mode == "http_503" {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusServiceUnavailable)
					fmt.Fprint(w, `{"error":{"type":"server_error","message":"image backend unavailable"}}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"type\":\"error\",\"error\":{\"type\":\"server_error\",\"code\":\"server_error\",\"message\":\"image backend unavailable\"}}\n\n")
			}))
			binaryBytes, err := os.ReadFile(binary)
			require.NoError(t, err)
			digest := sha256.Sum256(binaryBytes)
			cfg := &config.Config{RunMode: config.RunModeSimple, Plugins: config.PluginConfig{DataDir: t.TempDir()}, AccountDrivers: config.AccountDriversConfig{CodexRSHTTP: config.CodexRSHTTPDriverConfig{Enabled: true, ExecutablePath: binary, SHA256: hex.EncodeToString(digest[:]), StartTimeoutSeconds: 10}}}
			manager := service.NewPluginManager(imageDriverEmptyPluginRepo{}, nil, cfg, service.PluginHostInfo{Version: "0.2.15"}, nil)
			require.NoError(t, manager.Start(context.Background()))
			t.Cleanup(manager.Stop)
			ready := false
			for _, descriptor := range service.BuildAccountDriverCatalog().Drivers {
				if descriptor.ID == service.AccountDriverCodexHTTP {
					ready = descriptor.Available
				}
			}
			require.True(t, ready, "managed runtime must start before the handler regression")
			accounts := []service.Account{
				{ID: 1, Name: "enhanced-image-1", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Priority: 0, AccountDriverID: service.AccountDriverCodexHTTP, AccountDriverRevision: 1, Proxy: proxy, ProxyID: &proxy.ID, Credentials: map[string]any{"access_token": "fake-image-1", "chatgpt_account_id": "fake-image-account-1"}},
				{ID: 2, Name: "enhanced-image-2", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Priority: 1, AccountDriverID: service.AccountDriverCodexHTTP, AccountDriverRevision: 1, Proxy: proxy, ProxyID: &proxy.ID, Credentials: map[string]any{"access_token": "fake-image-2", "chatgpt_account_id": "fake-image-account-2"}},
			}
			native := &openAIImagesFailoverHTTPUpstream{}
			gateway := service.NewOpenAIGatewayService(openAIImagesFailoverAccountRepo{accounts: accounts}, nil, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, native, nil, nil, nil, nil, nil, nil, nil, nil)
			gateway.SetPluginManager(manager)
			billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
			t.Cleanup(billing.Stop)
			h := NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(nil), billing, service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
			h.maxAccountSwitches = 10
			body := []byte(`{"model":"gpt-image-1","prompt":"draw a fixture","quality":"high","size":"1536x1024"}`)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			groupID := int64(3130)
			c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{ID: 99, GroupID: &groupID, Group: &service.Group{ID: groupID, AllowImageGeneration: true}, User: &service.User{ID: 100}})
			c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 100, Concurrency: 0})
			h.Images(c)
			mu.Lock()
			observed := append([]string(nil), upstreamAccounts...)
			mu.Unlock()
			require.Equal(t, []string{"fake-image-account-1"}, observed, "already sent image attempt must not reach the second account")
			require.Empty(t, native.calls(), "enhanced attempts must never fall back to native HTTP")
			require.Zero(t, gateway.SnapshotOpenAIAccountSchedulerMetrics().AccountSwitchTotal)
			require.GreaterOrEqual(t, rec.Code, http.StatusInternalServerError)
		})
	}
}
