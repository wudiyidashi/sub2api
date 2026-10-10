package service

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/accountdriver/runner"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

type AccountDriverUpgradeInput struct {
	ExecutablePath      string `json:"executable_path"`
	SHA256              string `json:"sha256"`
	ExpectedGeneration  uint64 `json:"expected_generation"`
	DrainTimeoutSeconds int    `json:"drain_timeout_seconds"`
}
type AccountDriverRollbackInput struct {
	ExpectedGeneration  uint64 `json:"expected_generation"`
	DrainTimeoutSeconds int    `json:"drain_timeout_seconds"`
}
type AccountDriverRuntimeGenerationStatus struct {
	Generation           uint64     `json:"generation"`
	State                string     `json:"state"`
	Available            bool       `json:"available"`
	ExecutablePath       string     `json:"executable_path"`
	SHA256               string     `json:"sha256"`
	Version              string     `json:"version"`
	UpstreamCommit       string     `json:"upstream_commit"`
	Transports           []string   `json:"transports"`
	StartedAt            time.Time  `json:"started_at"`
	PublishedAt          *time.Time `json:"published_at,omitempty"`
	DrainDeadline        *time.Time `json:"drain_deadline,omitempty"`
	HTTPRequests         int        `json:"http_requests"`
	WebSocketConnections int        `json:"websocket_connections"`
	ExitReason           string     `json:"exit_reason,omitempty"`
}
type AccountDriverRuntimeStatus struct {
	Enabled           bool                                   `json:"enabled"`
	Updating          bool                                   `json:"updating"`
	ActiveGeneration  uint64                                 `json:"active_generation"`
	Active            *AccountDriverRuntimeGenerationStatus  `json:"active,omitempty"`
	Candidate         *AccountDriverRuntimeGenerationStatus  `json:"candidate,omitempty"`
	Retired           []AccountDriverRuntimeGenerationStatus `json:"retired"`
	RollbackAvailable bool                                   `json:"rollback_available"`
	LastError         string                                 `json:"last_error,omitempty"`
	Persisted         bool                                   `json:"persisted"`
}

type accountDriverRuntimeArtifact struct {
	ExecutablePath string   `json:"executable_path"`
	SHA256         string   `json:"sha256"`
	Version        string   `json:"version,omitempty"`
	UpstreamCommit string   `json:"upstream_commit,omitempty"`
	Transports     []string `json:"transports,omitempty"`
}
type accountDriverRuntimeGeneration struct {
	id            uint64
	registryID    uint64
	client        *runner.Client
	artifact      accountDriverRuntimeArtifact
	state         string
	startedAt     time.Time
	publishedAt   *time.Time
	drainDeadline *time.Time
	httpRequests  int
	webSockets    int
	sockets       map[*runner.WebSocketConn]int64
	timer         *time.Timer
	exited        bool
	exitReason    string
}

func (m *AccountDriverManager) Status() AccountDriverRuntimeStatus {
	if m == nil {
		return AccountDriverRuntimeStatus{Retired: []AccountDriverRuntimeGenerationStatus{}}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	status := AccountDriverRuntimeStatus{Enabled: m.config.Enabled, Updating: m.updating, RollbackAvailable: m.previous != nil, LastError: m.lastError, Persisted: m.persisted, Retired: []AccountDriverRuntimeGenerationStatus{}}
	if m.active != nil {
		status.ActiveGeneration = m.active.id
		value := generationStatus(m.active)
		status.Active = &value
	}
	if m.candidate != nil {
		value := generationStatus(m.candidate)
		status.Candidate = &value
	}
	for _, generation := range m.retired {
		status.Retired = append(status.Retired, generationStatus(generation))
	}
	sort.Slice(status.Retired, func(i, j int) bool { return status.Retired[i].Generation > status.Retired[j].Generation })
	return status
}
func generationStatus(g *accountDriverRuntimeGeneration) AccountDriverRuntimeGenerationStatus {
	return AccountDriverRuntimeGenerationStatus{Generation: g.id, State: g.state, Available: g.client != nil && g.client.Available(), ExecutablePath: g.artifact.ExecutablePath, SHA256: g.artifact.SHA256, Version: g.artifact.Version, UpstreamCommit: g.artifact.UpstreamCommit, Transports: slices.Clone(g.artifact.Transports), StartedAt: g.startedAt, PublishedAt: cloneRuntimeTime(g.publishedAt), DrainDeadline: cloneRuntimeTime(g.drainDeadline), HTTPRequests: g.httpRequests, WebSocketConnections: g.webSockets, ExitReason: g.exitReason}
}
func cloneRuntimeTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
func (m *AccountDriverManager) GenerationID() uint64 {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active == nil {
		return 0
	}
	return m.active.id
}
func (m *AccountDriverManager) CanContinueGeneration(id uint64) bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var g *accountDriverRuntimeGeneration
	if m.active != nil && m.active.id == id {
		g = m.active
	} else {
		g = m.retired[id]
	}
	if g == nil && m.active != nil && m.active.registryID == id {
		g = m.active
	}
	if g == nil {
		for _, retired := range m.retired {
			if retired.registryID == id {
				g = retired
				break
			}
		}
	}
	return !m.stopped && g != nil && g.client != nil && g.client.Available() && (g.state == "active" || g.state == "draining" && g.drainDeadline != nil && time.Now().Before(*g.drainDeadline))
}
func (m *AccountDriverManager) GenerationDeadline(id uint64) *time.Time {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active != nil && m.active.id == id {
		return cloneRuntimeTime(m.active.drainDeadline)
	}
	if g := m.retired[id]; g != nil {
		return cloneRuntimeTime(g.drainDeadline)
	}
	return nil
}

func (m *AccountDriverManager) Upgrade(ctx context.Context, input AccountDriverUpgradeInput) (AccountDriverRuntimeStatus, error) {
	if m == nil {
		return AccountDriverRuntimeStatus{}, infraerrors.Conflict("ACCOUNT_DRIVER_UNAVAILABLE", "account driver runtime is unavailable")
	}
	m.opMu.Lock()
	defer m.opMu.Unlock()
	if err := ctx.Err(); err != nil {
		return m.Status(), err
	}
	if len(input.SHA256) != 64 {
		return m.Status(), infraerrors.BadRequest("ACCOUNT_DRIVER_CHECKSUM_REQUIRED", "a SHA256 digest is required")
	}
	if _, err := hex.DecodeString(input.SHA256); err != nil {
		return m.Status(), infraerrors.BadRequest("ACCOUNT_DRIVER_CHECKSUM_INVALID", "SHA256 digest is invalid")
	}
	_, err := m.replaceRuntime(ctx, accountDriverRuntimeArtifact{ExecutablePath: input.ExecutablePath, SHA256: strings.ToLower(input.SHA256)}, input.ExpectedGeneration, input.DrainTimeoutSeconds)
	return m.Status(), err
}
func (m *AccountDriverManager) Rollback(ctx context.Context, input AccountDriverRollbackInput) (AccountDriverRuntimeStatus, error) {
	if m == nil {
		return AccountDriverRuntimeStatus{}, infraerrors.Conflict("ACCOUNT_DRIVER_UNAVAILABLE", "account driver runtime is unavailable")
	}
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.Lock()
	var artifact *accountDriverRuntimeArtifact
	if m.previous != nil {
		copy := *m.previous
		artifact = &copy
	}
	m.mu.Unlock()
	if artifact == nil {
		return m.Status(), infraerrors.Conflict("ACCOUNT_DRIVER_ROLLBACK_UNAVAILABLE", "no previously published driver is available")
	}
	_, err := m.replaceRuntime(ctx, *artifact, input.ExpectedGeneration, input.DrainTimeoutSeconds)
	return m.Status(), err
}
func (m *AccountDriverManager) replaceRuntime(ctx context.Context, artifact accountDriverRuntimeArtifact, expected uint64, drainSeconds int) (AccountDriverRuntimeStatus, error) {
	if drainSeconds == 0 {
		drainSeconds = 30
	}
	if drainSeconds < 1 || drainSeconds > 600 {
		return m.Status(), infraerrors.BadRequest("ACCOUNT_DRIVER_DRAIN_TIMEOUT_INVALID", "drain timeout must be between 1 and 600 seconds")
	}
	m.mu.Lock()
	if m.stopped || !m.config.Enabled {
		m.mu.Unlock()
		return m.Status(), infraerrors.Conflict("ACCOUNT_DRIVER_UNAVAILABLE", "account driver runtime is stopped or disabled")
	}
	if m.active == nil || m.active.id != expected {
		m.mu.Unlock()
		return m.Status(), infraerrors.Conflict("ACCOUNT_DRIVER_GENERATION_CONFLICT", "active runtime generation changed")
	}
	m.updating = true
	m.lastError = ""
	old := m.active
	m.mu.Unlock()
	defer func() { m.mu.Lock(); m.updating = false; m.mu.Unlock() }()
	g, err := m.prepareRuntime(ctx, artifact)
	if err != nil {
		m.setRuntimeError(err)
		return m.Status(), err
	}
	failed := true
	defer func() {
		if failed {
			m.discardRuntime(g)
		}
	}()
	for _, transport := range old.artifact.Transports {
		if !slices.Contains(g.artifact.Transports, transport) {
			err = infraerrors.Conflict("ACCOUNT_DRIVER_CAPABILITY_DOWNGRADE", "candidate must preserve all active transport capabilities")
			m.setRuntimeError(err)
			return m.Status(), err
		}
	}
	if err = ctx.Err(); err != nil {
		m.setRuntimeError(err)
		return m.Status(), err
	}
	m.mu.Lock()
	if m.stopped || m.active != old || g.exited || !g.client.Available() {
		m.mu.Unlock()
		err = infraerrors.Conflict("ACCOUNT_DRIVER_GENERATION_CONFLICT", "runtime changed before publication")
		m.setRuntimeError(err)
		return m.Status(), err
	}
	previous := old.artifact
	record := accountDriverRuntimeState{SchemaVersion: 1, Generation: g.id, Active: g.artifact, Previous: &previous}
	if err = saveAccountDriverRuntimeState(m.statePath, record); err != nil {
		m.mu.Unlock()
		m.setRuntimeError(err)
		return m.Status(), err
	}
	now := time.Now().UTC()
	deadline := now.Add(time.Duration(drainSeconds) * time.Second)
	old.state = "draining"
	old.drainDeadline = &deadline
	m.retired[old.id] = old
	m.pruneRuntimeHistoryLocked()
	g.state = "active"
	g.publishedAt = &now
	m.active = g
	m.candidate = nil
	m.previous = &previous
	m.persisted = true
	m.lastError = ""
	_ = m.publishRegistryLocked()
	old.timer = time.AfterFunc(time.Until(deadline), func() { m.closeGeneration(old, "drain_deadline", true) })
	oldEmpty := old.httpRequests == 0 && old.webSockets == 0
	m.mu.Unlock()
	failed = false
	if oldEmpty {
		m.closeGeneration(old, "drained", false)
	}
	return m.Status(), nil
}

func (m *AccountDriverManager) pruneRuntimeHistoryLocked() {
	if len(m.retired) <= 32 {
		return
	}
	var obsolete []uint64
	for id, generation := range m.retired {
		if generation.state == "closed" || generation.state == "exited" {
			obsolete = append(obsolete, id)
		}
	}
	sort.Slice(obsolete, func(i, j int) bool { return obsolete[i] < obsolete[j] })
	for _, id := range obsolete {
		if len(m.retired) <= 32 {
			break
		}
		delete(m.retired, id)
	}
}

func (m *AccountDriverManager) prepareRuntime(ctx context.Context, artifact accountDriverRuntimeArtifact) (*accountDriverRuntimeGeneration, error) {
	staged, err := stageAccountDriverRuntimeArtifact(m.statePath, artifact)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.nextGeneration++
	g := &accountDriverRuntimeGeneration{id: m.nextGeneration, artifact: staged, state: "candidate", startedAt: time.Now().UTC(), sockets: make(map[*runner.WebSocketConn]int64)}
	m.candidate = g
	m.mu.Unlock()
	timeout := time.Duration(m.config.StartTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	client, err := runner.Start(ctx, runner.Config{ExecutablePath: staged.ExecutablePath, SHA256: staged.SHA256, StartTimeout: timeout, OnExit: func() { m.runtimeExited(g) }})
	if err != nil {
		m.discardRuntime(g)
		return nil, fmt.Errorf("prepare managed account driver: %w", err)
	}
	m.mu.Lock()
	g.client = client
	g.artifact.Version = client.Info().Version
	g.artifact.UpstreamCommit = client.Info().UpstreamCommit
	g.artifact.Transports = slices.Clone(client.Info().Transports)
	if len(g.artifact.Transports) == 0 {
		g.artifact.Transports = []string{"http", "sse"}
	}
	m.mu.Unlock()
	if !slices.Contains(g.artifact.Transports, "http") || !slices.Contains(g.artifact.Transports, "sse") {
		m.discardRuntime(g)
		return nil, infraerrors.Conflict("ACCOUNT_DRIVER_CONTRACT_INCOMPATIBLE", "candidate must support HTTP and SSE")
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	err = client.ValidateContract(probeCtx)
	cancel()
	if err != nil || !client.Available() {
		m.discardRuntime(g)
		if err == nil {
			err = errors.New("candidate exited during preparation")
		}
		return nil, fmt.Errorf("validate managed account driver: %w", err)
	}
	return g, nil
}
func (m *AccountDriverManager) discardRuntime(g *accountDriverRuntimeGeneration) {
	m.mu.Lock()
	if m.candidate == g {
		m.candidate = nil
	}
	g.state = "rejected"
	client := g.client
	m.mu.Unlock()
	if client != nil {
		client.Close()
	}
}
func (m *AccountDriverManager) setRuntimeError(err error) {
	m.mu.Lock()
	m.lastError = err.Error()
	m.mu.Unlock()
}
func (m *AccountDriverManager) startFailed(err error) error {
	m.setRuntimeError(err)
	m.mu.Lock()
	defer m.mu.Unlock()
	_ = SetAccountDriverRuntimeAvailability(AccountDriverCodexHTTP, false, "unavailable")
	return err
}
func (m *AccountDriverManager) publishRegistryLocked() error {
	if m.active == nil || m.active.client == nil {
		return SetAccountDriverRuntimeAvailability(AccountDriverCodexHTTP, false, "unconfigured")
	}
	err := SetAccountDriverRuntimeCapabilities(AccountDriverCodexHTTP, !m.stopped && m.active.client.Available(), m.versionLocked(), m.active.artifact.Transports)
	if err == nil {
		m.active.registryID = AccountDriverRuntimeGeneration()
	}
	return err
}
func (m *AccountDriverManager) runtimeExited(g *accountDriverRuntimeGeneration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g.exited = true
	if g.state != "closed" {
		g.state = "exited"
	}
	if g.exitReason == "" {
		g.exitReason = "process_exit"
	}
	if g.timer != nil {
		g.timer.Stop()
	}
	if m.active == g {
		_ = m.publishRegistryLocked()
	}
}
func (m *AccountDriverManager) acquireRuntime(websocket bool) (*accountDriverRuntimeGeneration, func()) {
	m.mu.Lock()
	g := m.active
	if m.stopped || g == nil || g.state != "active" || g.client == nil || !g.client.Available() || websocket && !g.client.SupportsWebSocket() {
		m.mu.Unlock()
		return nil, func() {}
	}
	if websocket {
		g.webSockets++
	} else {
		g.httpRequests++
	}
	m.mu.Unlock()
	var once sync.Once
	return g, func() {
		once.Do(func() {
			m.mu.Lock()
			if websocket {
				g.webSockets--
			} else {
				g.httpRequests--
			}
			empty := g.state == "draining" && g.httpRequests == 0 && g.webSockets == 0
			m.mu.Unlock()
			if empty {
				m.closeGeneration(g, "drained", false)
			}
		})
	}
}
func (m *AccountDriverManager) closeGeneration(g *accountDriverRuntimeGeneration, reason string, force bool) {
	m.mu.Lock()
	if m.active == g || g.state == "closed" || g.state == "exited" || g.state == "closing" {
		m.mu.Unlock()
		return
	}
	if !force && (g.httpRequests != 0 || g.webSockets != 0) {
		m.mu.Unlock()
		return
	}
	g.state = "closing"
	g.exitReason = reason
	if g.timer != nil {
		g.timer.Stop()
	}
	client := g.client
	m.mu.Unlock()
	if client != nil {
		client.Close()
	}
	m.mu.Lock()
	g.state = "closed"
	m.mu.Unlock()
}
func (m *AccountDriverManager) ForceDrain(ctx context.Context, generation uint64) (AccountDriverRuntimeStatus, error) {
	if m == nil {
		return AccountDriverRuntimeStatus{}, infraerrors.Conflict("ACCOUNT_DRIVER_UNAVAILABLE", "account driver runtime is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return m.Status(), err
	}
	m.mu.Lock()
	g := m.retired[generation]
	m.mu.Unlock()
	if g == nil {
		return m.Status(), infraerrors.Conflict("ACCOUNT_DRIVER_GENERATION_NOT_RETIRED", "only a retired runtime may be force drained")
	}
	m.closeGeneration(g, "force_drained", true)
	return m.Status(), nil
}
func (m *AccountDriverManager) DrainAccountWebSockets(accountID int64) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	var sockets []*runner.WebSocketConn
	generations := make([]*accountDriverRuntimeGeneration, 0, len(m.retired)+1)
	if m.active != nil {
		generations = append(generations, m.active)
	}
	for _, g := range m.retired {
		generations = append(generations, g)
	}
	for _, g := range generations {
		for conn, id := range g.sockets {
			if id == accountID {
				sockets = append(sockets, conn)
			}
		}
	}
	m.mu.Unlock()
	for _, conn := range sockets {
		_ = conn.CloseNow()
	}
	return nil
}
