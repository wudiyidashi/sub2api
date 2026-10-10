package runner

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	api "github.com/Wei-Shaw/sub2api/pkg/accountdriverapi/v1"
	"github.com/stretchr/testify/require"
)

func TestHandshakeRejectsNonLocalAndWrongContracts(t *testing.T) {
	info := Info{Protocol: "sub2api-account-driver-v1", APIVersion: 1, Address: "127.0.0.1:12345", DriverID: DriverID, Version: "0.1.0"}
	require.NoError(t, validateInfo(info))
	for _, address := range []string{"example.com:12345", "0.0.0.0:12345", "127.0.0.1:0", "127.0.0.1:65536"} {
		copyInfo := info
		copyInfo.Address = address
		require.Error(t, validateInfo(copyInfo))
	}
	info.APIVersion = 2
	require.Error(t, validateInfo(info))
}

func startFixtureRunner(t *testing.T) *Client {
	t.Helper()
	path := os.Getenv("SUB2API_CODEX_DRIVER_BINARY")
	if path == "" {
		t.Skip("SUB2API_CODEX_DRIVER_BINARY is required for real process verification")
	}
	client, err := Start(context.Background(), Config{ExecutablePath: path, StartTimeout: 10 * time.Second})
	require.NoError(t, err)
	t.Cleanup(client.Close)
	return client
}

func TestManagedRoundTripKeepsFullResponseAndPreparedRequest(t *testing.T) {
	client := startFixtureRunner(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/codex/responses", r.URL.Path)
		require.Equal(t, "Bearer host-fixture", r.Header.Get("Authorization"))
		require.Equal(t, []string{"one", "two"}, r.Header.Values("X-Unknown"))
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.Equal(t, `{"unknown":true,"stream":true}`, string(body))
		w.Header().Add("X-Upstream-Unknown", "one")
		w.Header().Add("X-Upstream-Unknown", "two")
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, "event: unknown\ndata: {\"unknown\":true}\n\n")
	}))
	defer upstream.Close()
	request, err := http.NewRequest(http.MethodPost, upstream.URL+"/codex/responses", strings.NewReader(`{"unknown":true,"stream":true}`))
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer host-fixture")
	request.Header.Add("X-Unknown", "one")
	request.Header.Add("X-Unknown", "two")
	var observed api.SendState
	response, err := client.RoundTripWithObserver(request, "", 17, 1, func(state api.SendState) { observed = state })
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, api.SendState_SEND_STATE_AMBIGUOUS, observed)
	require.Equal(t, http.StatusCreated, response.StatusCode)
	require.Equal(t, []string{"one", "two"}, response.Header.Values("X-Upstream-Unknown"))
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, "event: unknown\ndata: {\"unknown\":true}\n\n", string(body))
	client.mu.Lock()
	require.Empty(t, client.identities)
	client.mu.Unlock()
}

func TestManagedRoundTripDropAfterSendIsAmbiguous(t *testing.T) {
	client := startFixtureRunner(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		connection, _, err := w.(http.Hijacker).Hijack()
		require.NoError(t, err)
		_ = connection.Close()
	}))
	defer upstream.Close()
	request, err := http.NewRequest(http.MethodPost, upstream.URL+"/codex/responses", strings.NewReader(`{"stream":true}`))
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer host-fixture")
	response, err := client.RoundTrip(request, "", 17, 1)
	require.Nil(t, response)
	var driverError *Error
	require.ErrorAs(t, err, &driverError)
	require.Equal(t, api.SendState_SEND_STATE_AMBIGUOUS, driverError.SendState)
	require.True(t, driverError.RequestSent())
}

func TestManagedCancellationClosesUnreadResponse(t *testing.T) {
	client := startFixtureRunner(t)
	closed := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(closed)
	}))
	defer upstream.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, upstream.URL+"/codex/responses", strings.NewReader(`{"stream":true}`))
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer host-fixture")
	response, err := client.RoundTrip(request, "", 17, 1)
	require.NoError(t, err)
	cancel()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation did not release the unread upstream")
	}
	_ = response.Body.Close()
}

func TestManagedHeaderTimeoutKeepsAmbiguousAndDoesNotTimeOutBody(t *testing.T) {
	client := startFixtureRunner(t)
	closed := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
		close(closed)
	}))
	request, err := http.NewRequest(http.MethodPost, upstream.URL+"/codex/responses", strings.NewReader(`{"stream":true}`))
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer host-fixture")
	response, err := client.RoundTripWithObserverTimeout(request, "", 17, 1, 200*time.Millisecond, nil)
	require.Nil(t, response)
	var failure *Error
	require.ErrorAs(t, err, &failure)
	require.Equal(t, "response_header_timeout", failure.Code)
	require.True(t, failure.RequestSent())
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("header timeout did not cancel upstream")
	}
	upstream.Close()

	streaming := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		time.Sleep(350 * time.Millisecond)
		_, _ = io.WriteString(w, "data: after-header-timeout\n\n")
	}))
	defer streaming.Close()
	request, err = http.NewRequest(http.MethodPost, streaming.URL+"/codex/responses", strings.NewReader(`{"stream":true}`))
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer host-fixture")
	response, err = client.RoundTripWithObserverTimeout(request, "", 17, 1, 200*time.Millisecond, nil)
	require.NoError(t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, "data: after-header-timeout\n\n", string(body))
}
