package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type apiRequestAuditRepository struct{ db *sql.DB }

func NewAPIRequestAuditRepository(db *sql.DB) service.APIRequestAuditRepository {
	return &apiRequestAuditRepository{db: db}
}

func (r *apiRequestAuditRepository) InsertAPIRequestAuditPayloads(ctx context.Context, payloads []*service.APIRequestAuditPayload) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("nil api request audit repository")
	}
	if len(payloads) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO api_request_audit_payloads
		(request_id, client_request_id, direction, content_type, headers, body, body_bytes, original_bytes, chunk_index, is_final, truncated, created_at)
		VALUES ($1,$2,$3,$4,$5::jsonb,$6,$7,$8,$9,$10,$11,$12)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
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
	defer rows.Close()
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
