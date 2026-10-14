package repository

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAPIRequestAuditRejectsOversizedBatchBeforeEviction(t *testing.T) {
	for _, payload := range []*service.APIRequestAuditPayload{
		{RequestID: "large body", Body: make([]byte, 1024*1024)},
		{RequestID: "large headers", Headers: map[string][]string{"X-Large": {strings.Repeat("x", 1024*1024)}}},
	} {
		t.Run(payload.RequestID, func(t *testing.T) {
			db, mock := newSQLMock(t)
			repo := NewAPIRequestAuditRepository(db, &config.Config{APIRequestAudit: config.APIRequestAuditConfig{MaxStorageMB: 1}})
			err := repo.InsertAPIRequestAuditPayloads(context.Background(), []*service.APIRequestAuditPayload{payload})
			require.ErrorContains(t, err, "storage budget")
			require.NoError(t, mock.ExpectationsWereMet(), "oversized capture must not begin eviction")
		})
	}
}

func TestAPIRequestAuditRetentionAndEvictionRollBackOnInsertFailure(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &apiRequestAuditRepository{db: db, retention: 24 * time.Hour, maxStoredBytes: 1000}
	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs(apiRequestAuditStorageLockID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT COALESCE\\(SUM").WillReturnRows(sqlmock.NewRows([]string{"bytes"}).AddRow(950))
	mock.ExpectExec("DELETE FROM api_request_audit_payloads WHERE request_id IN").WithArgs(sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT COALESCE\\(SUM").WillReturnRows(sqlmock.NewRows([]string{"bytes"}).AddRow(950))
	mock.ExpectExec("WITH requests AS").WithArgs(sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectPrepare("INSERT INTO api_request_audit_payloads").ExpectExec().WillReturnError(errors.New("insert failed"))
	mock.ExpectRollback()
	err := repo.InsertAPIRequestAuditPayloads(context.Background(), []*service.APIRequestAuditPayload{{RequestID: "test", Direction: service.APIRequestAuditDirectionClientRequest, Body: []byte("body")}})
	require.ErrorContains(t, err, "insert failed")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAPIRequestAuditBacklogDoesNotTriggerLargeDeleteOnWrite(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &apiRequestAuditRepository{db: db, retention: 24 * time.Hour, maxStoredBytes: 1000}
	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs(apiRequestAuditStorageLockID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT COALESCE\\(SUM").WillReturnRows(sqlmock.NewRows([]string{"bytes"}).AddRow(1001))
	mock.ExpectRollback()
	err := repo.InsertAPIRequestAuditPayloads(context.Background(), []*service.APIRequestAuditPayload{{RequestID: "new", Body: []byte("body")}})
	require.ErrorContains(t, err, "awaiting retention cleanup")
	require.NoError(t, mock.ExpectationsWereMet(), "large historical cleanup must stay off the write path")
}

func TestAPIRequestAuditCleanupUsesDefaultsAndDoesNotTouchUsage(t *testing.T) {
	db, mock := newSQLMock(t)
	repo, ok := NewAPIRequestAuditRepository(db, nil).(*apiRequestAuditRepository)
	require.True(t, ok)
	require.Equal(t, 24*time.Hour, repo.retention)
	require.EqualValues(t, 512*1024*1024, repo.maxStoredBytes)
	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs(apiRequestAuditStorageLockID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("DELETE FROM api_request_audit_payloads WHERE request_id IN").WithArgs(sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT COALESCE\\(SUM").WillReturnRows(sqlmock.NewRows([]string{"bytes"}).AddRow(0))
	mock.ExpectQuery("SELECT EXISTS").WithArgs(sqlmock.AnyArg(), repo.maxStoredBytes).WillReturnRows(sqlmock.NewRows([]string{"more"}).AddRow(false))
	mock.ExpectCommit()
	require.NoError(t, repo.CleanupAPIRequestAuditPayloads(context.Background()))
	require.NoError(t, mock.ExpectationsWereMet())
}
