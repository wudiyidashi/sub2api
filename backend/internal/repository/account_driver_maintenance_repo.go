package repository

import (
	"context"
	"database/sql"
	"encoding/json"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

const maintenanceActiveSQL = `state IN ('preparing','draining','committing')`

func (r *accountRepository) BeginAccountDriverMaintenance(ctx context.Context, id int64, input service.AccountDriverSwitchInput) (*service.AccountDriverMaintenanceStatus, error) {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	prior, err := maintenanceStatusByOperation(ctx, tx, input.OperationID)
	if err != nil {
		return nil, err
	}
	if prior.OperationID == input.OperationID {
		if prior.AccountID != id || prior.TargetDriverID != input.TargetDriverID || prior.ExpectedBindingRevision != input.ExpectedBindingRevision {
			return nil, infraerrors.Conflict("ACCOUNT_DRIVER_OPERATION_CONFLICT", "operation id was already used for another switch")
		}
		return prior, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT account_driver_id, account_driver_revision, schedulable FROM accounts WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, id)
	if err != nil {
		return nil, err
	}
	var source string
	var revision int64
	var schedulable bool
	if !rows.Next() {
		_ = rows.Close()
		return nil, service.ErrAccountNotFound
	}
	err = rows.Scan(&source, &revision, &schedulable)
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	// Recheck idempotency after acquiring the account row lock. The initial
	// probe happens before the lock and can race with another retry.
	prior, err = maintenanceStatusByOperation(ctx, tx, input.OperationID)
	if err != nil {
		return nil, err
	}
	if prior.OperationID == input.OperationID {
		if prior.AccountID != id || prior.TargetDriverID != input.TargetDriverID || prior.ExpectedBindingRevision != input.ExpectedBindingRevision {
			return nil, infraerrors.Conflict("ACCOUNT_DRIVER_OPERATION_CONFLICT", "operation id was already used for another switch")
		}
		return prior, nil
	}
	existing, err := maintenanceStatus(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if existing.State == "preparing" || existing.State == "draining" || existing.State == "committing" {
		return nil, service.ErrAccountDriverMaintenance
	}
	if revision != input.ExpectedBindingRevision {
		return nil, infraerrors.Conflict("ACCOUNT_DRIVER_REVISION_CHANGED", "account binding revision changed")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO account_driver_maintenance(account_id,operation_id,source_driver_id,target_driver_id,expected_binding_revision,actor_id,previous_schedulable,state,expires_at)
	VALUES($1,$2,$3,$4,$5,$6,$7,'preparing',NOW()+$8 * INTERVAL '1 second')`, id, input.OperationID, source, input.TargetDriverID, revision, input.ActorID, schedulable, input.DrainTimeoutSeconds)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE accounts SET schedulable=false,updated_at=NOW() WHERE id=$1`, id); err != nil {
		return nil, err
	}
	if err = enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &id, nil, nil); err != nil {
		return nil, err
	}
	status, err := maintenanceStatus(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	r.syncSchedulerAccountSnapshot(ctx, id)
	return status, nil
}

type maintenanceSQLReader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func maintenanceStatus(ctx context.Context, reader maintenanceSQLReader, id int64) (*service.AccountDriverMaintenanceStatus, error) {
	return maintenanceReadStatus(ctx, reader, `m.account_id=$1 ORDER BY m.started_at DESC LIMIT 1`, id, id)
}
func maintenanceStatusByOperation(ctx context.Context, reader maintenanceSQLReader, operation string) (*service.AccountDriverMaintenanceStatus, error) {
	return maintenanceReadStatus(ctx, reader, `m.operation_id=$1`, operation, 0)
}
func maintenanceReadStatus(ctx context.Context, reader maintenanceSQLReader, condition string, argument any, id int64) (*service.AccountDriverMaintenanceStatus, error) {
	rows, err := reader.QueryContext(ctx, `SELECT m.operation_id::text,m.account_id,m.source_driver_id,m.target_driver_id,m.expected_binding_revision,m.actor_id,m.previous_schedulable,m.state,m.started_at,m.updated_at,m.expires_at,m.error,
	(SELECT COUNT(*) FROM account_driver_operation_leases l WHERE l.account_id=m.account_id AND l.expires_at>NOW())
	FROM account_driver_maintenance m WHERE `+condition, argument)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		return &service.AccountDriverMaintenanceStatus{AccountID: id, State: "idle"}, nil
	}
	s := &service.AccountDriverMaintenanceStatus{}
	err = rows.Scan(&s.OperationID, &s.AccountID, &s.SourceDriverID, &s.TargetDriverID, &s.ExpectedBindingRevision, &s.ActorID, &s.PreviousSchedulable, &s.State, &s.StartedAt, &s.UpdatedAt, &s.ExpiresAt, &s.Error, &s.ActiveOperations)
	return s, err
}
func (r *accountRepository) GetAccountDriverMaintenance(ctx context.Context, id int64) (*service.AccountDriverMaintenanceStatus, error) {
	return maintenanceStatus(ctx, r.sql, id)
}
func (r *accountRepository) GetAccountDriverMaintenanceByOperation(ctx context.Context, operation string) (*service.AccountDriverMaintenanceStatus, error) {
	return maintenanceStatusByOperation(ctx, r.sql, operation)
}
func (r *accountRepository) SetAccountDriverMaintenanceState(ctx context.Context, id int64, operation, state, message string) error {
	result, err := r.sql.ExecContext(ctx, `UPDATE account_driver_maintenance SET state=$3,error=$4,updated_at=NOW() WHERE account_id=$1 AND operation_id=$2 AND `+maintenanceActiveSQL, id, operation, state, message)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return infraerrors.Conflict("ACCOUNT_DRIVER_OPERATION_CHANGED", "maintenance operation is no longer active")
	}
	return nil
}

func (r *accountRepository) FinishAccountDriverMaintenance(ctx context.Context, id int64, operation, state, message string) error {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `SELECT id FROM accounts WHERE id=$1 FOR UPDATE`, id); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `WITH finished AS (UPDATE account_driver_maintenance SET state=$3,error=$4,updated_at=NOW() WHERE account_id=$1 AND operation_id=$2 AND `+maintenanceActiveSQL+` RETURNING previous_schedulable)
	UPDATE accounts SET schedulable=finished.previous_schedulable,updated_at=NOW() FROM finished WHERE accounts.id=$1`, id, operation, state, message)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return infraerrors.Conflict("ACCOUNT_DRIVER_OPERATION_CHANGED", "maintenance operation is no longer active")
	}
	if err = enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &id, nil, nil); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	r.syncSchedulerAccountSnapshot(ctx, id)
	return nil
}

func (r *accountRepository) CommitAccountDriverSwitch(ctx context.Context, op *service.AccountDriverMaintenanceStatus, credentials map[string]any) error {
	payload, err := json.Marshal(normalizeJSONMap(credentials))
	if err != nil {
		return err
	}
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `SELECT id FROM accounts WHERE id=$1 FOR UPDATE`, op.AccountID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE accounts a SET account_driver_id=m.target_driver_id,account_driver_revision=a.account_driver_revision+1,schedulable=m.previous_schedulable,updated_at=NOW()
	FROM account_driver_maintenance m WHERE a.id=$1 AND m.account_id=a.id AND m.operation_id=$2 AND m.state='committing' AND m.expires_at>NOW()
	AND a.account_driver_id=m.source_driver_id AND a.account_driver_revision=m.expected_binding_revision AND a.credentials=$3::jsonb
	AND NOT EXISTS(SELECT 1 FROM account_driver_operation_leases l WHERE l.account_id=a.id AND l.expires_at>NOW())`, op.AccountID, op.OperationID, string(payload))
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return infraerrors.Conflict("ACCOUNT_DRIVER_COMMIT_CONFLICT", "binding, credentials or operation changed")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE account_driver_maintenance SET state='completed',updated_at=NOW(),error='' WHERE account_id=$1 AND operation_id=$2`, op.AccountID, op.OperationID); err != nil {
		return err
	}
	if err = enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &op.AccountID, nil, nil); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	r.syncSchedulerAccountSnapshot(ctx, op.AccountID)
	return nil
}
func (r *accountRepository) ListExpiredAccountDriverMaintenance(ctx context.Context) ([]*service.AccountDriverMaintenanceStatus, error) {
	rows, err := r.sql.QueryContext(ctx, `SELECT operation_id::text FROM account_driver_maintenance WHERE `+maintenanceActiveSQL+` AND expires_at<=NOW()`)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	var out []*service.AccountDriverMaintenanceStatus
	for _, id := range ids {
		op, err := r.GetAccountDriverMaintenanceByOperation(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, nil
}

func (r *accountRepository) AcquireAccountDriverOperation(ctx context.Context, id int64, lease, kind string) error {
	return r.acquireAccountDriverOperation(ctx, id, nil, lease, kind)
}
func (r *accountRepository) AcquireAccountDriverBoundOperation(ctx context.Context, account *service.Account, lease, kind string) error {
	return r.acquireAccountDriverOperation(ctx, account.ID, account, lease, kind)
}
func (r *accountRepository) acquireAccountDriverOperation(ctx context.Context, id int64, account *service.Account, lease, kind string) error {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT account_driver_id,account_driver_revision FROM accounts WHERE id=$1 AND deleted_at IS NULL FOR KEY SHARE`, id)
	if err != nil {
		return err
	}
	if !rows.Next() {
		_ = rows.Close()
		return service.ErrAccountNotFound
	}
	var driverID string
	var revision int64
	err = rows.Scan(&driverID, &revision)
	_ = rows.Close()
	if err != nil {
		return err
	}
	if account != nil && (account.EffectiveAccountDriverID() != driverID || (account.AccountDriverRevision != revision && (account.AccountDriverRevision != 0 || driverID != "native"))) {
		return infraerrors.Conflict("ACCOUNT_DRIVER_BINDING_CHANGED", "account binding changed; reload the account before admitting the operation")
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO account_driver_operation_leases(lease_id,account_id,kind,expires_at)
	SELECT $2,$1,$3,NOW()+INTERVAL '120 seconds' WHERE NOT EXISTS(SELECT 1 FROM account_driver_maintenance WHERE account_id=$1 AND `+maintenanceActiveSQL+`)`, id, lease, kind)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return service.ErrAccountDriverMaintenance
	}
	return tx.Commit()
}
func (r *accountRepository) RenewAccountDriverOperation(ctx context.Context, lease string) error {
	result, err := r.sql.ExecContext(ctx, `UPDATE account_driver_operation_leases SET expires_at=NOW()+INTERVAL '120 seconds' WHERE lease_id=$1`, lease)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return service.ErrAccountDriverMaintenance
	}
	return nil
}
func (r *accountRepository) ReleaseAccountDriverOperation(ctx context.Context, lease string) error {
	_, err := r.sql.ExecContext(ctx, `DELETE FROM account_driver_operation_leases WHERE lease_id=$1`, lease)
	return err
}
func (r *accountRepository) CleanExpiredAccountDriverOperationLeases(ctx context.Context) error {
	_, err := r.sql.ExecContext(ctx, `DELETE FROM account_driver_operation_leases WHERE expires_at<=NOW()`)
	return err
}

var _ service.AccountDriverMaintenanceRepository = (*accountRepository)(nil)
