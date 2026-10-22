package repository

import (
	"context"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// UpdateCPRPoolBinding 将授权范围、版本和调度事件原子提交，旧快照不能重新扩权。
func (r *accountRepository) UpdateCPRPoolBinding(ctx context.Context, account *service.Account, expectedRevision int64) error {
	if err := service.ValidateAccountDriverState(account); err != nil {
		return err
	}
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	affected, err := tx.Client().Account.Update().Where(
		dbaccount.IDEQ(account.ID), dbaccount.AccountDriverIDEQ("cpr-managed"),
		dbaccount.AccountDriverRevisionEQ(expectedRevision), dbaccount.DeletedAtIsNil(),
	).SetName(account.Name).SetExtra(account.Extra).SetConcurrency(account.Concurrency).
		SetSchedulable(account.Schedulable).SetAccountDriverRevision(expectedRevision + 1).Save(ctx)
	if err != nil {
		return err
	}
	if affected != 1 {
		return infraerrors.Conflict("CPR_POOL_CHANGED", "pool changed; reload before saving")
	}
	if err := enqueueSchedulerOutbox(txCtx, tx.Client(), service.SchedulerOutboxEventAccountChanged, &account.ID, nil, buildSchedulerGroupPayload(account.GroupIDs)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	account.AccountDriverRevision = expectedRevision + 1
	return nil
}
