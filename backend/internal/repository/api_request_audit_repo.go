package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type apiRequestAuditRepository struct {
	db             *sql.DB
	retention      time.Duration
	maxStoredBytes int64
}

func NewAPIRequestAuditRepository(db *sql.DB, cfg *config.Config) service.APIRequestAuditRepository {
	hours := config.DefaultAPIRequestAuditRetentionHours
	mb := config.DefaultAPIRequestAuditMaxStorageMB
	if cfg != nil {
		if cfg.APIRequestAudit.RetentionHours > 0 {
			hours = cfg.APIRequestAudit.RetentionHours
		}
		if cfg.APIRequestAudit.MaxStorageMB > 0 {
			mb = cfg.APIRequestAudit.MaxStorageMB
		}
	}
	return &apiRequestAuditRepository{db: db, retention: time.Duration(hours) * time.Hour, maxStoredBytes: mb * 1024 * 1024}
}

func (r *apiRequestAuditRepository) InsertAPIRequestAuditPayloads(ctx context.Context, payloads []*service.APIRequestAuditPayload) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("nil api request audit repository")
	}
	if len(payloads) == 0 {
		return nil
	}
	// 过大的单批原文直接拒绝，避免驱逐全部既有记录；调用方只跳过录制。
	// JSONB 文本会增加分隔空格，双倍 JSON 头长度为元数据留出预算余量。
	var incoming int64
	for _, payload := range payloads {
		if payload == nil || strings.TrimSpace(payload.RequestID) == "" {
			continue
		}
		headers, err := json.Marshal(payload.Headers)
		if err != nil {
			return err
		}
		incoming += int64(len(payload.Body) + 2*len(headers) + len(payload.RequestID) + len(payload.ClientRequestID) + len(payload.Direction) + len(payload.ContentType) + 128)
	}
	if incoming > r.maxStoredBytes {
		return fmt.Errorf("api request audit batch exceeds the storage budget")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := r.prune(ctx, tx, incoming); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO api_request_audit_payloads
		(request_id, client_request_id, direction, content_type, headers, body, body_bytes, original_bytes, chunk_index, is_final, truncated, created_at)
		VALUES ($1,$2,$3,$4,$5::jsonb,$6,$7,$8,$9,$10,$11,$12)`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for _, payload := range payloads {
		if payload == nil || strings.TrimSpace(payload.RequestID) == "" {
			continue
		}
		headers, marshalErr := json.Marshal(payload.Headers)
		if marshalErr != nil {
			return marshalErr
		}
		if _, err := stmt.ExecContext(ctx,
			payload.RequestID, payload.ClientRequestID, payload.Direction, payload.ContentType,
			string(headers), payload.Body, len(payload.Body), payload.OriginalBytes,
			payload.ChunkIndex, payload.Final, payload.Truncated, payload.CreatedAt.UTC()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// 原文预算包含正文与元数据，限制逻辑数据量；删除后 PostgreSQL 可复用原有磁盘页。
// 事务级锁使多实例写入与清理共享同一预算，避免并发同时通过容量检查。
const apiRequestAuditStoredBytesSQL = `body_bytes + octet_length(headers::text) + octet_length(request_id) + octet_length(client_request_id) + octet_length(direction) + octet_length(content_type) + 128`
const apiRequestAuditStorageLockID int64 = 694208311321144028

func (r *apiRequestAuditRepository) prune(ctx context.Context, tx *sql.Tx, incoming int64) error {
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, apiRequestAuditStorageLockID); err != nil {
		return err
	}
	// 存量超预算时交给分批清理，避免每次业务请求都尝试删除整张历史大表。
	if incoming > 0 {
		var current int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(`+apiRequestAuditStoredBytesSQL+`),0) FROM api_request_audit_payloads`).Scan(&current); err != nil {
			return err
		}
		if current > r.maxStoredBytes {
			return fmt.Errorf("api request audit storage is awaiting retention cleanup")
		}
	}
	expiryLimit, capacityLimit := "", ""
	if incoming == 0 {
		expiryLimit = " ORDER BY created_at, id LIMIT 100"
		capacityLimit = " ORDER BY bytes_before, request_id LIMIT 100"
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM api_request_audit_payloads WHERE request_id IN (
		SELECT request_id FROM api_request_audit_payloads WHERE created_at < $1`+expiryLimit+`
	)`, time.Now().UTC().Add(-r.retention)); err != nil {
		return err
	}
	var stored int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(`+apiRequestAuditStoredBytesSQL+`),0) FROM api_request_audit_payloads`).Scan(&stored); err != nil {
		return err
	}
	excess := stored + incoming - r.maxStoredBytes
	if excess <= 0 {
		return nil
	}
	// 按完整请求淘汰，避免只删除开头的分块后仍把残缺正文显示为完整原文。
	_, err := tx.ExecContext(ctx, `WITH requests AS (
		SELECT request_id, MIN(created_at) AS created_at, MIN(id) AS first_id,
			SUM(`+apiRequestAuditStoredBytesSQL+`) AS stored_bytes
		FROM api_request_audit_payloads GROUP BY request_id
	), oldest AS (
		SELECT request_id, COALESCE(SUM(stored_bytes) OVER (
			ORDER BY created_at, first_id ROWS BETWEEN UNBOUNDED PRECEDING AND 1 PRECEDING),0) AS bytes_before
		FROM requests
	) DELETE FROM api_request_audit_payloads WHERE request_id IN (SELECT request_id FROM oldest WHERE bytes_before < $1`+capacityLimit+`)`, excess)
	return err
}

func (r *apiRequestAuditRepository) CleanupAPIRequestAuditPayloads(ctx context.Context) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("nil api request audit repository")
	}
	for ctx.Err() == nil {
		more, err := r.cleanupBatch(ctx)
		if err != nil || !more {
			return err
		}
	}
	return ctx.Err()
}

// 每批提交释放锁和旧版本，取消只回滚当前小批次，已完成的清理无需重做。
func (r *apiRequestAuditRepository) cleanupBatch(ctx context.Context) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := r.prune(ctx, tx, 0); err != nil {
		return false, err
	}
	var more bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM api_request_audit_payloads WHERE created_at < $1)
		OR (SELECT COALESCE(SUM(`+apiRequestAuditStoredBytesSQL+`),0) FROM api_request_audit_payloads) > $2`, time.Now().UTC().Add(-r.retention), r.maxStoredBytes).Scan(&more)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return more, nil
}

func (r *apiRequestAuditRepository) ListAPIRequestAuditPayloads(ctx context.Context, requestID string) ([]*service.APIRequestAuditPayload, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil api request audit repository")
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT request_id, client_request_id, direction, content_type, headers, body,
		       original_bytes, chunk_index, is_final, truncated, created_at
		FROM api_request_audit_payloads
		WHERE request_id=$1
		ORDER BY created_at ASC, direction ASC, chunk_index ASC, id ASC`, requestID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]*service.APIRequestAuditPayload, 0)
	for rows.Next() {
		var payload service.APIRequestAuditPayload
		var headersRaw []byte
		if err := rows.Scan(&payload.RequestID, &payload.ClientRequestID, &payload.Direction, &payload.ContentType, &headersRaw, &payload.Body, &payload.OriginalBytes, &payload.ChunkIndex, &payload.Final, &payload.Truncated, &payload.CreatedAt); err != nil {
			return nil, err
		}
		if len(headersRaw) > 0 && string(headersRaw) != "null" {
			if err := json.Unmarshal(headersRaw, &payload.Headers); err != nil {
				return nil, err
			}
		}
		out = append(out, &payload)
	}
	return out, rows.Err()
}

var _ service.APIRequestAuditRepository = (*apiRequestAuditRepository)(nil)
