package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestAccountDriverFailoverRequiresProofAfterSending(t *testing.T) {
	account := &Account{ID: 17, AccountDriverID: AccountDriverCodexHTTP, AccountDriverRevision: 1}
	ctx, tracker := withAccountDriverAttemptTracking(context.Background())
	_, same := withAccountDriverAttemptTracking(ctx)
	require.Same(t, tracker, same)
	original := &UpstreamFailoverError{StatusCode: http.StatusServiceUnavailable, RetryableOnSameAccount: true, SafeToFailoverAfterWrite: true}
	require.Same(t, original, restrictAccountDriverFailover(account, tracker, original))
	tracker.sent.Store(true)
	err := restrictAccountDriverFailover(account, tracker, original)
	var stopped *UpstreamFailoverError
	require.ErrorAs(t, err, &stopped)
	require.False(t, stopped.ShouldRetryNextAccount())
	require.False(t, stopped.RetryableOnSameAccount)
	require.False(t, stopped.SafeToFailoverAfterWrite)
	require.True(t, original.ShouldRetryNextAccount(), "must not mutate an existing shared error")
	native := &Account{ID: 18}
	require.Same(t, original, restrictAccountDriverFailover(native, tracker, original))
}

func TestAccountDriverExplicitQuotaRefusalCanRotateOnlyNewChains(t *testing.T) {
	account := &Account{ID: 17, AccountDriverID: AccountDriverCodexHTTP, AccountDriverRevision: 1}
	_, tracker := withAccountDriverAttemptTracking(context.Background())
	tracker.sent.Store(true)
	quota := &UpstreamFailoverError{StatusCode: http.StatusTooManyRequests, ResponseBody: []byte(`{"error":{"code":"usage_limit_reached"}}`), RetryableOnSameAccount: true}
	var failover *UpstreamFailoverError
	require.ErrorAs(t, restrictAccountDriverFailover(account, tracker, quota), &failover)
	require.True(t, failover.ShouldRetryNextAccount())
	require.False(t, failover.RetryableOnSameAccount)
	_, continuation := withAccountDriverAttemptTracking(context.Background(), []byte(`{"previous_response_id":"connection-private"}`))
	continuation.sent.Store(true)
	require.ErrorAs(t, restrictAccountDriverFailover(account, continuation, quota), &failover)
	require.False(t, failover.ShouldRetryNextAccount())
	quota.ResponseBody = []byte(`{"error":{"message":"unknown 429"}}`)
	require.ErrorAs(t, restrictAccountDriverFailover(account, tracker, quota), &failover)
	require.False(t, failover.ShouldRetryNextAccount())
}

func TestAccountDriverEndpointDelegationIsExplicit(t *testing.T) {
	for _, test := range []struct {
		method, path string
		enhanced     bool
	}{
		{"POST", "/backend-api/codex/responses", true},
		{"POST", "/codex/responses", true},
		{"POST", "/backend-api/codex/responses/compact", false},
		{"POST", "/backend-api/codex/images/generations", false},
		{"GET", "/backend-api/codex/responses", false},
	} {
		request, err := http.NewRequest(test.method, "https://chatgpt.com"+test.path, nil)
		require.NoError(t, err)
		require.Equal(t, test.enhanced, codexDriverHandlesRequest(request))
	}
}

func TestAccountDriverBindingFailureDoesNotFallBackToNative(t *testing.T) {
	account := &Account{ID: 17, Platform: PlatformOpenAI, Type: AccountTypeOAuth, AccountDriverID: AccountDriverCodexHTTP, AccountDriverRevision: 1}
	request, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", nil)
	require.NoError(t, err)
	svc := &OpenAIGatewayService{}
	response, err := svc.doOpenAIUpstream(request, "", account)
	require.Nil(t, response)
	var transport *PluginTransportError
	require.ErrorAs(t, err, &transport)
	require.Equal(t, "account_driver_unavailable", transport.Code)
	require.False(t, transport.RequestSent)
	account.AccountDriverID = "unknown-saved-driver"
	_, err = svc.doOpenAIUpstream(request, "", account)
	require.ErrorAs(t, err, &transport)
	require.Equal(t, "account_driver_unknown", transport.Code)
}

func TestAccountDriverManagerPreservesResponseHeaderTimeout(t *testing.T) {
	cfg := &config.Config{Gateway: config.GatewayConfig{OpenAIResponseHeaderTimeout: 7}}
	manager := NewAccountDriverManager(cfg)
	require.Equal(t, 7*time.Second, manager.headerTimeout)
}
