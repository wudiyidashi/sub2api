package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/google/uuid"
)

var ErrAccountDriverMaintenance = infraerrors.Conflict("ACCOUNT_DRIVER_MAINTENANCE", "account driver maintenance is in progress")

type AccountDriverSwitchInput struct {
	TargetDriverID          string `json:"target_driver_id"`
	ExpectedBindingRevision int64  `json:"expected_binding_revision"`
	DrainTimeoutSeconds     int    `json:"drain_timeout_seconds"`
	OperationID             string `json:"operation_id"`
	ActorID                 int64  `json:"-"`
}

type AccountDriverMaintenanceStatus struct {
	OperationID             string    `json:"operation_id"`
	AccountID               int64     `json:"account_id"`
	SourceDriverID          string    `json:"source_driver_id"`
	TargetDriverID          string    `json:"target_driver_id"`
	ExpectedBindingRevision int64     `json:"expected_binding_revision"`
	ActorID                 int64     `json:"actor_id"`
	PreviousSchedulable     bool      `json:"-"`
	State                   string    `json:"state"`
	StartedAt               time.Time `json:"started_at"`
	UpdatedAt               time.Time `json:"updated_at"`
	ExpiresAt               time.Time `json:"expires_at"`
	Error                   string    `json:"error,omitempty"`
	ActiveOperations        int64     `json:"active_operations"`
}

type AccountDriverMaintenanceRepository interface {
	BeginAccountDriverMaintenance(context.Context, int64, AccountDriverSwitchInput) (*AccountDriverMaintenanceStatus, error)
	GetAccountDriverMaintenance(context.Context, int64) (*AccountDriverMaintenanceStatus, error)
	GetAccountDriverMaintenanceByOperation(context.Context, string) (*AccountDriverMaintenanceStatus, error)
	SetAccountDriverMaintenanceState(context.Context, int64, string, string, string) error
	FinishAccountDriverMaintenance(context.Context, int64, string, string, string) error
	CommitAccountDriverSwitch(context.Context, *AccountDriverMaintenanceStatus, map[string]any) error
	ListExpiredAccountDriverMaintenance(context.Context) ([]*AccountDriverMaintenanceStatus, error)
	AcquireAccountDriverOperation(context.Context, int64, string, string) error
	AcquireAccountDriverBoundOperation(context.Context, *Account, string, string) error
	RenewAccountDriverOperation(context.Context, string) error
	ReleaseAccountDriverOperation(context.Context, string) error
	CleanExpiredAccountDriverOperationLeases(context.Context) error
}

type AccountDriverMaintenanceAdminService interface {
	GetAccountDriverMaintenance(context.Context, int64) (*AccountDriverMaintenanceStatus, error)
	StartAccountDriverSwitch(context.Context, int64, AccountDriverSwitchInput) (*AccountDriverMaintenanceStatus, error)
	CancelAccountDriverMaintenance(context.Context, int64, string) (*AccountDriverMaintenanceStatus, error)
}

type AccountDriverMaintenanceService struct {
	accounts AccountRepository
	repo     AccountDriverMaintenanceRepository
	mu       sync.Mutex
	cancels  map[string]context.CancelFunc
	drain    func(context.Context, int64) error
	stop     context.CancelFunc
}

var accountDriverMaintenanceGuard atomic.Pointer[AccountDriverMaintenanceService]

func NewAccountDriverMaintenanceService(accounts AccountRepository) *AccountDriverMaintenanceService {
	repo, _ := accounts.(AccountDriverMaintenanceRepository)
	s := &AccountDriverMaintenanceService{accounts: accounts, repo: repo, cancels: make(map[string]context.CancelFunc)}
	accountDriverMaintenanceGuard.Store(s)
	accountDriverRegistryMu.Lock()
	driver, _ := accountDriverRegistry.Lookup(AccountDriverCodexHTTP)
	accountDriverRegistry = newAccountDriverRegistry(driver.Available, driver.Version)
	accountDriverRegistryMu.Unlock()
	return s
}
func (s *AccountDriverMaintenanceService) SetDrainHook(hook func(context.Context, int64) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.drain = hook
}
func (s *AccountDriverMaintenanceService) Start(ctx context.Context) error {
	if err := s.Recover(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	if s.stop != nil {
		s.mu.Unlock()
		return nil
	}
	run, cancel := context.WithCancel(context.Background())
	s.stop = cancel
	s.mu.Unlock()
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-run.Done():
				return
			case <-ticker.C:
				recoverCtx, done := context.WithTimeout(run, 10*time.Second)
				_ = s.Recover(recoverCtx)
				done()
			}
		}
	}()
	return nil
}
func (s *AccountDriverMaintenanceService) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stop != nil {
		s.stop()
		s.stop = nil
	}
	for _, cancel := range s.cancels {
		cancel()
	}
}
func (s *AccountDriverMaintenanceService) GetAccountDriverMaintenance(ctx context.Context, id int64) (*AccountDriverMaintenanceStatus, error) {
	if s == nil || s.repo == nil {
		return nil, infraerrors.BadRequest("ACCOUNT_DRIVER_MAINTENANCE_UNSUPPORTED", "maintenance repository is not configured")
	}
	return s.repo.GetAccountDriverMaintenance(ctx, id)
}

func (s *AccountDriverMaintenanceService) StartAccountDriverSwitch(ctx context.Context, id int64, input AccountDriverSwitchInput) (*AccountDriverMaintenanceStatus, error) {
	input.TargetDriverID = strings.TrimSpace(input.TargetDriverID)
	if s == nil || s.repo == nil {
		return nil, infraerrors.BadRequest("ACCOUNT_DRIVER_MAINTENANCE_UNSUPPORTED", "maintenance repository is not configured")
	}
	if input.ExpectedBindingRevision <= 0 {
		return nil, infraerrors.BadRequest("ACCOUNT_DRIVER_REVISION_REQUIRED", "expected binding revision is required")
	}
	if input.DrainTimeoutSeconds == 0 {
		input.DrainTimeoutSeconds = 120
	}
	if input.DrainTimeoutSeconds < 1 || input.DrainTimeoutSeconds > 3600 {
		return nil, infraerrors.BadRequest("ACCOUNT_DRIVER_DRAIN_TIMEOUT_INVALID", "drain timeout must be between 1 and 3600 seconds")
	}
	if input.OperationID == "" {
		input.OperationID = uuid.NewString()
	}
	if _, err := uuid.Parse(input.OperationID); err != nil {
		return nil, infraerrors.BadRequest("ACCOUNT_DRIVER_OPERATION_INVALID", "operation id must be a UUID")
	}
	input.OperationID = uuid.MustParse(input.OperationID).String()
	prior, err := s.repo.GetAccountDriverMaintenanceByOperation(ctx, input.OperationID)
	if err != nil {
		return nil, err
	}
	if prior.OperationID == input.OperationID {
		if prior.AccountID != id || prior.TargetDriverID != input.TargetDriverID || prior.ExpectedBindingRevision != input.ExpectedBindingRevision {
			return nil, infraerrors.Conflict("ACCOUNT_DRIVER_OPERATION_CONFLICT", "operation id was already used for another switch")
		}
		return prior, nil
	}
	account, err := s.accounts.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if account.EffectiveAccountDriverID() == input.TargetDriverID {
		return nil, infraerrors.BadRequest("ACCOUNT_DRIVER_ALREADY_BOUND", "account already uses this driver")
	}
	if err := ValidateAccountDriverForAccount(input.TargetDriverID, account.Platform, account.Type, account.Credentials); err != nil {
		return nil, err
	}
	if input.TargetDriverID == AccountDriverCodexHTTP {
		shadows, err := s.accounts.ListShadowsByParent(ctx, id)
		if err != nil {
			return nil, err
		}
		if account.ParentAccountID != nil || len(shadows) > 0 {
			return nil, infraerrors.BadRequest("ACCOUNT_DRIVER_AUTH_MODE_UNSUPPORTED", "linked credential shadows must be removed before switching")
		}
	}
	status, err := s.repo.BeginAccountDriverMaintenance(ctx, id, input)
	if err != nil {
		return nil, err
	}
	if status.State == "completed" || status.State == "failed" || status.State == "cancelled" {
		return status, nil
	}
	s.mu.Lock()
	if _, running := s.cancels[status.OperationID]; running {
		s.mu.Unlock()
		return status, nil
	}
	work, cancel := context.WithDeadline(context.Background(), status.ExpiresAt)
	s.cancels[status.OperationID] = cancel
	s.mu.Unlock()
	go s.runSwitch(work, status)
	return status, nil
}

func (s *AccountDriverMaintenanceService) runSwitch(ctx context.Context, status *AccountDriverMaintenanceStatus) {
	defer func() { s.mu.Lock(); delete(s.cancels, status.OperationID); s.mu.Unlock() }()
	fail := func(state, reason string) {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.repo.FinishAccountDriverMaintenance(cleanup, status.AccountID, status.OperationID, state, reason)
	}
	_ = s.repo.SetAccountDriverMaintenanceState(ctx, status.AccountID, status.OperationID, "draining", "")
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		current, err := s.repo.GetAccountDriverMaintenance(ctx, status.AccountID)
		if err != nil {
			fail("failed", "cannot read maintenance state")
			return
		}
		if current.OperationID != status.OperationID || current.State == "cancelled" {
			return
		}
		if current.ActiveOperations == 0 {
			break
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.Canceled) {
				fail("cancelled", "maintenance was cancelled")
			} else {
				fail("failed", "account operations did not drain before the deadline")
			}
			return
		case <-ticker.C:
		}
	}
	s.mu.Lock()
	drain := s.drain
	s.mu.Unlock()
	if drain == nil {
		fail("failed", "account connection drain hook is not configured")
		return
	}
	if err := drain(ctx, status.AccountID); err != nil {
		fail("failed", "account connections could not be drained")
		return
	}
	account, err := s.accounts.GetByID(ctx, status.AccountID)
	if err != nil {
		fail("failed", "account is unavailable")
		return
	}
	if err := ValidateAccountDriverForAccount(status.TargetDriverID, account.Platform, account.Type, account.Credentials); err != nil {
		fail("failed", "target driver is unavailable or incompatible with current credentials")
		return
	}
	candidate := *account
	candidate.AccountDriverID = status.TargetDriverID
	if err := ValidateAccountDriverState(&candidate); err != nil {
		fail("failed", "target driver is incompatible with the current account settings")
		return
	}
	if status.TargetDriverID == AccountDriverCodexHTTP {
		shadows, shadowErr := s.accounts.ListShadowsByParent(ctx, status.AccountID)
		if shadowErr != nil || account.ParentAccountID != nil || len(shadows) > 0 {
			fail("failed", "linked credential shadows must be removed before switching")
			return
		}
	}
	if err := s.repo.SetAccountDriverMaintenanceState(ctx, status.AccountID, status.OperationID, "committing", ""); err != nil {
		fail("failed", "maintenance commit was interrupted")
		return
	}
	if err := s.repo.CommitAccountDriverSwitch(ctx, status, account.Credentials); err != nil {
		fail("failed", "binding or credentials changed before commit")
		return
	}
}

func (s *AccountDriverMaintenanceService) CancelAccountDriverMaintenance(ctx context.Context, id int64, operation string) (*AccountDriverMaintenanceStatus, error) {
	status, err := s.GetAccountDriverMaintenance(ctx, id)
	if err != nil {
		return nil, err
	}
	if status.OperationID != operation {
		return nil, infraerrors.Conflict("ACCOUNT_DRIVER_OPERATION_CHANGED", "maintenance operation changed")
	}
	if err := s.repo.FinishAccountDriverMaintenance(ctx, id, operation, "cancelled", "cancelled by administrator"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	cancel := s.cancels[operation]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return s.GetAccountDriverMaintenance(ctx, id)
}
func (s *AccountDriverMaintenanceService) Recover(ctx context.Context) error {
	if s == nil || s.repo == nil {
		return nil
	}
	operations, err := s.repo.ListExpiredAccountDriverMaintenance(ctx)
	if err != nil {
		return err
	}
	for _, op := range operations {
		if err := s.repo.FinishAccountDriverMaintenance(ctx, op.AccountID, op.OperationID, "failed", "maintenance lease expired after interruption"); err != nil {
			// A worker or an administrator may have finished the same operation
			// after the expiry scan. That terminal race is already recovered.
			if infraerrors.IsConflict(err) && infraerrors.Reason(err) == "ACCOUNT_DRIVER_OPERATION_CHANGED" {
				continue
			}
			return err
		}
	}
	_ = s.repo.CleanExpiredAccountDriverOperationLeases(ctx)
	return nil
}

type accountDriverOperationContextKey struct{}
type accountDriverOperationContext struct {
	accountID int64
	leaseID   string
	driverID  string
	revision  int64
	abort     context.Context
}

func AccountDriverOperationAbortContext(ctx context.Context) context.Context {
	if own, _ := ctx.Value(accountDriverOperationContextKey{}).(accountDriverOperationContext); own.leaseID != "" {
		return own.abort
	}
	return nil
}
func AcquireAccountDriverOperation(ctx context.Context, id int64, kind string) (context.Context, func(), error) {
	return acquireAccountDriverOperation(ctx, id, nil, kind)
}
func AcquireAccountDriverBoundOperation(ctx context.Context, account *Account, kind string) (context.Context, func(), error) {
	if account == nil {
		return ctx, nil, ErrAccountNotFound
	}
	return acquireAccountDriverOperation(ctx, account.ID, account, kind)
}
func acquireAccountDriverOperation(ctx context.Context, id int64, account *Account, kind string) (context.Context, func(), error) {
	if own, _ := ctx.Value(accountDriverOperationContextKey{}).(accountDriverOperationContext); own.accountID == id && own.leaseID != "" {
		if own.abort != nil && own.abort.Err() != nil {
			return ctx, nil, context.Cause(own.abort)
		}
		if account != nil && own.driverID != "" && (own.driverID != account.EffectiveAccountDriverID() || own.revision != account.AccountDriverRevision) {
			return ctx, nil, infraerrors.Conflict("ACCOUNT_DRIVER_BINDING_CHANGED", "operation binding changed")
		}
		return ctx, func() {}, nil
	}
	s := accountDriverMaintenanceGuard.Load()
	if s == nil || s.repo == nil || id <= 0 {
		return ctx, func() {}, nil
	}
	lease := uuid.NewString()
	var err error
	if account != nil {
		err = s.repo.AcquireAccountDriverBoundOperation(ctx, account, lease, kind)
	} else {
		err = s.repo.AcquireAccountDriverOperation(ctx, id, lease, kind)
	}
	if err != nil {
		return ctx, nil, err
	}
	// Keep the lease-abort signal independent from the caller's cancellation.
	// A client disconnect still follows the normal response-body drain path;
	// only a failed lease heartbeat force-closes detached upstream work.
	leaseBase := context.WithoutCancel(ctx)
	abortCtx, cancelAbort := context.WithCancelCause(leaseBase)
	operationCtx, cancelOperation := context.WithCancelCause(ctx)
	fact := accountDriverOperationContext{accountID: id, leaseID: lease, abort: abortCtx}
	if account != nil {
		fact.driverID = account.EffectiveAccountDriverID()
		fact.revision = account.AccountDriverRevision
	}
	ctx = context.WithValue(operationCtx, accountDriverOperationContextKey{}, fact)
	stop := make(chan struct{})
	var once sync.Once
	stopAbortLink := context.AfterFunc(abortCtx, func() {
		cancelOperation(context.Cause(abortCtx))
	})
	release := func() {
		once.Do(func() {
			close(stop)
			stopAbortLink()
			cancelAbort(context.Canceled)
			cancelOperation(context.Canceled)
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = s.repo.ReleaseAccountDriverOperation(cleanup, lease)
		})
	}
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				heartbeat, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				err := s.repo.RenewAccountDriverOperation(heartbeat, lease)
				cancel()
				if err != nil {
					cancelOperation(err)
				}
			}
		}
	}()
	return ctx, release, nil
}

func (s *adminServiceImpl) SetAccountDriverMaintenanceService(m *AccountDriverMaintenanceService) {
	s.accountDriverMaintenance = m
}
func (s *adminServiceImpl) GetAccountDriverMaintenance(ctx context.Context, id int64) (*AccountDriverMaintenanceStatus, error) {
	if s.accountDriverMaintenance == nil {
		return nil, infraerrors.BadRequest("ACCOUNT_DRIVER_MAINTENANCE_UNSUPPORTED", "maintenance service is not configured")
	}
	return s.accountDriverMaintenance.GetAccountDriverMaintenance(ctx, id)
}
func (s *adminServiceImpl) StartAccountDriverSwitch(ctx context.Context, id int64, input AccountDriverSwitchInput) (*AccountDriverMaintenanceStatus, error) {
	if s.accountDriverMaintenance == nil {
		return nil, infraerrors.BadRequest("ACCOUNT_DRIVER_MAINTENANCE_UNSUPPORTED", "maintenance service is not configured")
	}
	return s.accountDriverMaintenance.StartAccountDriverSwitch(ctx, id, input)
}
func (s *adminServiceImpl) CancelAccountDriverMaintenance(ctx context.Context, id int64, operation string) (*AccountDriverMaintenanceStatus, error) {
	if s.accountDriverMaintenance == nil {
		return nil, infraerrors.BadRequest("ACCOUNT_DRIVER_MAINTENANCE_UNSUPPORTED", "maintenance service is not configured")
	}
	return s.accountDriverMaintenance.CancelAccountDriverMaintenance(ctx, id, operation)
}
