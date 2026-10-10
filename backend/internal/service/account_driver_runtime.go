package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/accountdriver/runner"
	"github.com/Wei-Shaw/sub2api/internal/config"
	api "github.com/Wei-Shaw/sub2api/pkg/accountdriverapi/v1"
	"github.com/tidwall/gjson"
)

// AccountDriverManager owns the optional Rust process, not accounts or billing.
type AccountDriverManager struct {
	opMu           sync.Mutex
	mu             sync.Mutex
	config         config.CodexRSHTTPDriverConfig
	active         *accountDriverRuntimeGeneration
	candidate      *accountDriverRuntimeGeneration
	retired        map[uint64]*accountDriverRuntimeGeneration
	nextGeneration uint64
	previous       *accountDriverRuntimeArtifact
	statePath      string
	updating       bool
	persisted      bool
	lastError      string
	stopped        bool
	headerTimeout  time.Duration
}

func NewAccountDriverManager(cfg *config.Config) *AccountDriverManager {
	m := &AccountDriverManager{retired: make(map[uint64]*accountDriverRuntimeGeneration), nextGeneration: uint64(time.Now().UnixMilli())}
	if cfg != nil {
		m.config = cfg.AccountDrivers.CodexRSHTTP
		if cfg.Gateway.OpenAIResponseHeaderTimeout > 0 {
			m.headerTimeout = time.Duration(cfg.Gateway.OpenAIResponseHeaderTimeout) * time.Second
		}
	}
	m.statePath = accountDriverStatePath(cfg)
	return m
}

func (m *AccountDriverManager) Start(ctx context.Context) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.Lock()
	if !m.config.Enabled {
		m.mu.Unlock()
		return SetAccountDriverRuntimeAvailability(AccountDriverCodexHTTP, false, "unconfigured")
	}
	if m.active != nil && m.active.client != nil && m.active.client.Available() {
		m.mu.Unlock()
		return nil
	}
	m.stopped = false
	artifact := accountDriverRuntimeArtifact{ExecutablePath: m.config.ExecutablePath, SHA256: m.config.SHA256}
	m.mu.Unlock()
	record, err := loadAccountDriverRuntimeState(m.statePath)
	if err != nil {
		return m.startFailed(err)
	}
	if record != nil {
		artifact = record.Active
		m.mu.Lock()
		if record.Generation > m.nextGeneration {
			m.nextGeneration = record.Generation
		}
		m.previous = record.Previous
		m.persisted = true
		m.mu.Unlock()
	}
	generation, err := m.prepareRuntime(ctx, artifact)
	if err != nil {
		return m.startFailed(err)
	}
	m.mu.Lock()
	if m.stopped || generation.exited || !generation.client.Available() {
		m.mu.Unlock()
		m.discardRuntime(generation)
		return m.startFailed(errors.New("account driver startup was interrupted"))
	}
	m.active = generation
	m.candidate = nil
	generation.state = "active"
	now := time.Now().UTC()
	generation.publishedAt = &now
	m.lastError = ""
	err = m.publishRegistryLocked()
	m.mu.Unlock()
	return err
}

func (m *AccountDriverManager) versionLocked() string {
	if m.active != nil && m.active.client != nil {
		info := m.active.client.Info()
		if len(info.UpstreamCommit) == 40 {
			return info.Version + "+" + info.UpstreamCommit[:8]
		}
		return info.Version
	}
	return "unconfigured"
}

func (m *AccountDriverManager) Stop() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.stopped = true
	var clients []*runner.Client
	if m.active != nil && m.active.client != nil {
		clients = append(clients, m.active.client)
	}
	if m.candidate != nil && m.candidate.client != nil {
		clients = append(clients, m.candidate.client)
	}
	for _, generation := range m.retired {
		if generation.client != nil {
			clients = append(clients, generation.client)
		}
		if generation.timer != nil {
			generation.timer.Stop()
		}
		generation.state = "closed"
	}
	_ = SetAccountDriverRuntimeAvailability(AccountDriverCodexHTTP, false, m.versionLocked())
	m.mu.Unlock()
	for _, client := range clients {
		client.Close()
	}
}

func usesCodexHTTPDriver(account *Account) bool {
	return account != nil && account.EffectiveAccountDriverID() == AccountDriverCodexHTTP
}

// The preset delegates non-Responses endpoints to the existing host transport.
// This decision is based on the endpoint, never on a failed enhanced attempt.
func codexDriverHandlesRequest(request *http.Request) bool {
	if request == nil || request.URL == nil || request.Method != http.MethodPost {
		return false
	}
	path := strings.TrimRight(request.URL.Path, "/")
	return path == "/backend-api/codex/responses" || path == "/codex/responses"
}

func (m *AccountDriverManager) RoundTrip(request *http.Request, proxyURL string, account *Account) (*http.Response, error) {
	if !usesCodexHTTPDriver(account) || account.Platform != PlatformOpenAI || account.Type != AccountTypeOAuth || account.AccountDriverRevision <= 0 {
		return nil, &PluginTransportError{Code: "account_driver_binding_invalid", Message: "Account driver binding is not valid"}
	}
	if m == nil {
		return nil, &PluginTransportError{Code: "account_driver_unavailable", Message: "Account driver is unavailable"}
	}
	generation, release := m.acquireRuntime(false)
	if generation == nil {
		return nil, &PluginTransportError{Code: "account_driver_unavailable", Message: "Account driver is unavailable"}
	}
	client := generation.client
	observer := func(state api.SendState) {
		if state != api.SendState_SEND_STATE_NOT_SENT {
			if tracker, _ := request.Context().Value(accountDriverAttemptTrackerKey{}).(*accountDriverAttemptTracker); tracker != nil {
				tracker.sent.Store(true)
			}
		}
	}
	response, err := client.RoundTripWithObserverTimeout(request, proxyURL, account.ID, account.AccountDriverRevision, m.headerTimeout, observer)
	if err != nil {
		release()
		var driverError *runner.Error
		if errors.As(err, &driverError) {
			return response, &PluginTransportError{Code: "account_driver_" + driverError.Code, Message: driverError.Error(), RequestSent: driverError.RequestSent()}
		}
		return response, &PluginTransportError{Code: "account_driver_failed", Message: "Account driver request failed", RequestSent: true}
	}
	if response == nil || response.Body == nil {
		release()
	} else {
		response.Body = &accountDriverRuntimeBody{ReadCloser: response.Body, release: release}
	}
	return response, nil
}

// DialManagedWebSocket receives only the identity already selected by the host.
// Pool reuse and continuation remain in the existing WS engine.
func (m *AccountDriverManager) DialManagedWebSocket(ctx context.Context, url string, headers http.Header, proxyURL string, accountID, bindingRevision int64, scopeKey string) (*runner.WebSocketConn, int, http.Header, error) {
	if m == nil {
		return nil, 0, nil, &runner.WebSocketError{Code: "unavailable"}
	}
	generation, release := m.acquireRuntime(true)
	if generation == nil {
		return nil, 0, nil, &runner.WebSocketError{Code: "unavailable"}
	}
	client := generation.client
	timeout := 15 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			release()
			return nil, 0, nil, ctx.Err()
		}
		if remaining < timeout {
			timeout = remaining
		}
	}
	conn, status, respHeaders, err := client.DialWebSocket(ctx, url, headers, proxyURL, runner.WebSocketOptions{AccountID: accountID, BindingRevision: bindingRevision, KeyScopeHash: scopeKey, HandshakeTimeout: timeout})
	if err != nil || conn == nil {
		release()
		return conn, status, respHeaders, err
	}
	conn.SetRuntimeGeneration(generation.id)
	m.mu.Lock()
	generation.sockets[conn] = accountID
	m.mu.Unlock()
	go func() { <-conn.Done(); m.mu.Lock(); delete(generation.sockets, conn); m.mu.Unlock(); release() }()
	return conn, status, respHeaders, nil
}

type accountDriverRuntimeBody struct {
	io.ReadCloser
	release func()
	once    sync.Once
}

func (b *accountDriverRuntimeBody) Read(buffer []byte) (int, error) {
	n, err := b.ReadCloser.Read(buffer)
	if err != nil {
		b.once.Do(b.release)
	}
	return n, err
}
func (b *accountDriverRuntimeBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.release)
	return err
}

type accountDriverAttemptTrackerKey struct{}
type accountDriverAttemptTracker struct {
	sent         atomic.Bool
	continuation atomic.Bool
}

func withAccountDriverAttemptTracking(ctx context.Context, bodies ...[]byte) (context.Context, *accountDriverAttemptTracker) {
	if tracker, _ := ctx.Value(accountDriverAttemptTrackerKey{}).(*accountDriverAttemptTracker); tracker != nil {
		for _, body := range bodies {
			if strings.TrimSpace(gjson.GetBytes(body, "previous_response_id").String()) != "" {
				tracker.continuation.Store(true)
			}
		}
		return ctx, tracker
	}
	tracker := &accountDriverAttemptTracker{}
	for _, body := range bodies {
		if strings.TrimSpace(gjson.GetBytes(body, "previous_response_id").String()) != "" {
			tracker.continuation.Store(true)
		}
	}
	return context.WithValue(ctx, accountDriverAttemptTrackerKey{}, tracker), tracker
}

// Once the managed process may have sent, a generic 5xx or first-output timeout
// is not evidence that replaying against another account is safe.
func restrictAccountDriverFailover(account *Account, tracker *accountDriverAttemptTracker, err error) error {
	if !usesCodexHTTPDriver(account) || tracker == nil || !tracker.sent.Load() || err == nil {
		return err
	}
	var failover *UpstreamFailoverError
	if !errors.As(err, &failover) {
		return err
	}
	// An explicit quota rejection proves that this request did not execute. Keep
	// host account rotation for a new chain; incremental continuation stays pinned.
	if !tracker.continuation.Load() && failover.StatusCode == http.StatusTooManyRequests {
		switch extractUpstreamErrorCode(failover.ResponseBody) {
		case "usage_limit_reached", "rate_limit_exceeded", "insufficient_quota":
			copyError := *failover
			copyError.RetryableOnSameAccount = false
			return &copyError
		}
	}
	copyError := *failover
	copyError.NextAccountAction = NextAccountStop
	copyError.RetryableOnSameAccount = false
	copyError.SafeToFailoverAfterWrite = false
	return &copyError
}
