//go:build integration

package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAPIRequestAuditRetentionAndWholeRequestBudget(t *testing.T) {
	ctx := context.Background()
	repo, ok := NewAPIRequestAuditRepository(integrationDB, &config.Config{APIRequestAudit: config.APIRequestAuditConfig{RetentionHours: 24, MaxStorageMB: 1}}).(*apiRequestAuditRepository)
	require.True(t, ok)
	prefix := "audit-retention-" + time.Now().Format("150405.000000")
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM api_request_audit_payloads WHERE request_id LIKE $1", prefix+"%")
	})
	seed := func(id string, when time.Time, bytes int) {
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO api_request_audit_payloads(request_id,direction,body,body_bytes,created_at)
			VALUES($1,'client_request',$2,$3,$4)`, prefix+id, []byte(strings.Repeat("x", bytes)), bytes, when)
		require.NoError(t, err)
	}
	now := time.Now().UTC()
	seed("expired", now.Add(-25*time.Hour), 200*1024)
	seed("old", now.Add(-2*time.Hour), 200*1024)
	seed("old", now.Add(-2*time.Hour), 200*1024)
	seed("recent", now.Add(-time.Hour), 400*1024)
	require.NoError(t, repo.InsertAPIRequestAuditPayloads(ctx, []*service.APIRequestAuditPayload{{RequestID: prefix + "new", Direction: service.APIRequestAuditDirectionClientRequest, Body: []byte(strings.Repeat("y", 400*1024)), CreatedAt: now}}))
	for _, id := range []string{"expired", "old"} {
		rows, err := repo.ListAPIRequestAuditPayloads(ctx, prefix+id)
		require.NoError(t, err)
		require.Empty(t, rows, "expiry and capacity eviction must remove every chunk of the old request")
	}
	for _, id := range []string{"recent", "new"} {
		rows, err := repo.ListAPIRequestAuditPayloads(ctx, prefix+id)
		require.NoError(t, err)
		require.Len(t, rows, 1)
	}
	var stored int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COALESCE(SUM(`+apiRequestAuditStoredBytesSQL+`),0) FROM api_request_audit_payloads`).Scan(&stored))
	require.LessOrEqual(t, stored, repo.maxStoredBytes)
	_, err := integrationDB.ExecContext(ctx, "UPDATE api_request_audit_payloads SET created_at=$1 WHERE request_id=$2", now.Add(-25*time.Hour), prefix+"recent")
	require.NoError(t, err)
	require.NoError(t, repo.CleanupAPIRequestAuditPayloads(ctx))
	rows, err := repo.ListAPIRequestAuditPayloads(ctx, prefix+"recent")
	require.NoError(t, err)
	require.Empty(t, rows, "idle cleanup must expire records without another captured request")

	// 两个写入者同时进入时也必须遵守同一预算，不能各自按旧容量放行。
	writeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	results := make(chan error, 2)
	for _, id := range []string{"concurrent-a", "concurrent-b"} {
		go func(id string) {
			results <- repo.InsertAPIRequestAuditPayloads(writeCtx, []*service.APIRequestAuditPayload{{RequestID: prefix + id, Direction: service.APIRequestAuditDirectionClientRequest, Body: []byte(strings.Repeat("z", 600*1024)), CreatedAt: now}})
		}(id)
	}
	for range 2 {
		require.NoError(t, <-results)
	}
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COALESCE(SUM(`+apiRequestAuditStoredBytesSQL+`),0) FROM api_request_audit_payloads`).Scan(&stored))
	require.LessOrEqual(t, stored, repo.maxStoredBytes, "concurrent writers must share the budget")
}
