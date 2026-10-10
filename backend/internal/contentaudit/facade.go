// Package contentaudit provides the read-only aggregation boundary used by
// the admin content-audit view. It deliberately composes the existing Ops and
// Prompt Audit stores instead of inventing a second request log or pretending
// that a raw request body exists when it was not persisted.
package contentaudit

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/securityaudit"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type OpsReader interface {
	ListRequestDetails(context.Context, *service.OpsRequestDetailFilter) (*service.OpsRequestDetailList, error)
	GetErrorLogs(context.Context, *service.OpsErrorLogFilter) (*service.OpsErrorLogList, error)
	GetErrorLogByID(context.Context, int64) (*service.OpsErrorLogDetail, error)
}

type PromptReader interface {
	ListEvents(context.Context, securityaudit.EventFilter, int, int) (*securityaudit.EventPage, error)
	GetEvent(context.Context, int64) (*securityaudit.Event, error)
}

// Facade is intentionally read-only. Mutation and retention remain owned by
// the existing Ops and Prompt Audit services, preserving their authorization,
// filtering, and deletion semantics.
type Facade struct {
	ops    OpsReader
	prompt PromptReader
	raw    *service.APIRequestAuditService
}

func NewFacade(ops OpsReader, prompt PromptReader) *Facade {
	return &Facade{ops: ops, prompt: prompt}
}

// SetRawPayloadService attaches the optional raw payload store after the
// existing facade has been constructed by the server wire graph.
func (f *Facade) SetRawPayloadService(raw *service.APIRequestAuditService) {
	if f != nil {
		f.raw = raw
	}
}

// RequestList is the existing request-level view (usage success rows joined
// with client-visible error rows). The underlying service already applies the
// request-id, user, account, model, duration, and time-range filters.
func (f *Facade) ListRequests(ctx context.Context, filter *service.OpsRequestDetailFilter) (*service.OpsRequestDetailList, error) {
	if f == nil || f.ops == nil {
		return nil, infraerrors.ServiceUnavailable("CONTENT_AUDIT_OPS_UNAVAILABLE", "content audit Ops service is unavailable")
	}
	return f.ops.ListRequestDetails(ctx, filter)
}

type RequestDetail struct {
	Request          *service.OpsRequestDetail         `json:"request,omitempty"`
	Errors           []*service.OpsErrorLogDetail      `json:"errors,omitempty"`
	PromptEvents     []*securityaudit.Event            `json:"prompt_events,omitempty"`
	RawPayloads      []*service.APIRequestAuditPayload `json:"raw_payloads,omitempty"`
	ClientRequest    *RawPayloadView                   `json:"client_request,omitempty"`
	ClientResponse   *RawPayloadView                   `json:"client_response,omitempty"`
	UpstreamRequest  *RawPayloadView                   `json:"upstream_request,omitempty"`
	UpstreamResponse *RawPayloadView                   `json:"upstream_response,omitempty"`
	// SanitizedErrorPayloadAvailable indicates that an Ops error row contains
	// its bounded/sanitized error payload. It never implies an original body.
	SanitizedErrorPayloadAvailable bool `json:"sanitized_error_payload_available"`
}

// GetRequest joins the request-level row with persisted error payloads and
// Prompt Audit events sharing the same request_id. It returns only data that
// actually exists in the stores; successful usage rows do not gain a fake raw
// body field.
func (f *Facade) GetRequest(ctx context.Context, requestID string) (*RequestDetail, error) {
	if f == nil || f.ops == nil {
		return nil, infraerrors.ServiceUnavailable("CONTENT_AUDIT_OPS_UNAVAILABLE", "content audit Ops service is unavailable")
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return nil, infraerrors.BadRequest("CONTENT_AUDIT_REQUEST_ID_REQUIRED", "request_id is required")
	}
	start := time.Unix(0, 0).UTC()
	end := time.Now().UTC()
	rows, err := f.ops.ListRequestDetails(ctx, &service.OpsRequestDetailFilter{
		RequestID: requestID, Page: 1, PageSize: 100, StartTime: &start, EndTime: &end,
	})
	if err != nil {
		return nil, err
	}
	if rows == nil || len(rows.Items) == 0 {
		if f.raw == nil {
			return nil, infraerrors.NotFound("CONTENT_AUDIT_REQUEST_NOT_FOUND", "request detail not found")
		}
		payloads, payloadErr := f.raw.List(ctx, requestID)
		if payloadErr != nil { return nil, payloadErr }
		if len(payloads) == 0 { return nil, infraerrors.NotFound("CONTENT_AUDIT_REQUEST_NOT_FOUND", "request detail not found") }
		return &RequestDetail{RawPayloads: payloads, ClientRequest: combineRawPayloads(payloads, service.APIRequestAuditDirectionClientRequest), ClientResponse: combineRawPayloads(payloads, service.APIRequestAuditDirectionClientResponse), UpstreamRequest: combineRawPayloads(payloads, service.APIRequestAuditDirectionUpstreamRequest), UpstreamResponse: combineRawPayloads(payloads, service.APIRequestAuditDirectionUpstreamResponse)}, nil
	}
	detail := &RequestDetail{Request: rows.Items[0]}
	if f.ops != nil {
		errorsList, listErr := f.ops.GetErrorLogs(ctx, &service.OpsErrorLogFilter{
			RequestID: requestID, Page: 1, PageSize: 100, View: "all", IncludeRecoveredUpstream: true,
		})
		if listErr != nil {
			return nil, listErr
		}
		if errorsList != nil {
			for _, item := range errorsList.Errors {
				if item == nil {
					continue
				}
				errorDetail, detailErr := f.ops.GetErrorLogByID(ctx, item.ID)
				if detailErr != nil {
					return nil, detailErr
				}
				detail.Errors = append(detail.Errors, errorDetail)
				if errorDetail != nil && strings.TrimSpace(errorDetail.ErrorBody) != "" {
					detail.SanitizedErrorPayloadAvailable = true
				}
			}
		}
	}
	if f.prompt != nil {
		events, eventErr := f.prompt.ListEvents(ctx, securityaudit.EventFilter{RequestID: requestID}, 1, 100)
		if eventErr != nil {
			return nil, eventErr
		}
		if events != nil {
			detail.PromptEvents = events.Items
		}
	}
	if f.raw != nil {
		payloads, payloadErr := f.raw.List(ctx, requestID)
		if payloadErr != nil {
			return nil, payloadErr
		}
		detail.RawPayloads = payloads
		detail.ClientRequest = combineRawPayloads(payloads, service.APIRequestAuditDirectionClientRequest)
		detail.ClientResponse = combineRawPayloads(payloads, service.APIRequestAuditDirectionClientResponse)
		detail.UpstreamRequest = combineRawPayloads(payloads, service.APIRequestAuditDirectionUpstreamRequest)
		detail.UpstreamResponse = combineRawPayloads(payloads, service.APIRequestAuditDirectionUpstreamResponse)
	}
	return detail, nil
}

type RawPayloadView struct {
	Direction   string              `json:"direction"`
	ContentType string              `json:"content_type,omitempty"`
	Headers     map[string][]string `json:"headers,omitempty"`
	Body        string              `json:"body,omitempty"`
	Raw         string              `json:"raw,omitempty"`
	BodyBase64  string              `json:"body_base64,omitempty"`
	Bytes       int64               `json:"bytes"`
	Truncated   bool                `json:"truncated"`
	ChunkCount  int                 `json:"chunk_count"`
}

func combineRawPayloads(payloads []*service.APIRequestAuditPayload, direction string) *RawPayloadView {
	var view *RawPayloadView
	var body []byte
	for _, payload := range payloads {
		if payload == nil || payload.Direction != direction {
			continue
		}
		if view == nil {
			view = &RawPayloadView{Direction: direction, ContentType: payload.ContentType, Headers: payload.Headers}
		}
		body = append(body, payload.Body...)
		view.ChunkCount++
		view.Truncated = view.Truncated || payload.Truncated
		if payload.OriginalBytes > view.Bytes {
			view.Bytes = payload.OriginalBytes
		}
	}
	if view == nil {
		return nil
	}
	view.Body, view.Raw = string(body), string(body)
	view.BodyBase64 = base64.StdEncoding.EncodeToString(body)
	if view.Bytes == 0 {
		view.Bytes = int64(len(body))
	}
	return view
}

func (f *Facade) ListRawPayloads(ctx context.Context, requestID string) ([]*service.APIRequestAuditPayload, error) {
	if f == nil || f.raw == nil {
		return nil, infraerrors.ServiceUnavailable("CONTENT_AUDIT_RAW_PAYLOAD_UNAVAILABLE", "raw request audit is unavailable")
	}
	if strings.TrimSpace(requestID) == "" {
		return nil, infraerrors.BadRequest("CONTENT_AUDIT_REQUEST_ID_REQUIRED", "request_id is required")
	}
	return f.raw.List(ctx, requestID)
}

type UpstreamAttempt struct {
	ErrorID int64                         `json:"error_id"`
	Event   service.OpsUpstreamErrorEvent `json:"event"`
}

type ErrorPayload struct {
	ErrorID              int64              `json:"error_id"`
	SanitizedErrorBody   string             `json:"sanitized_error_body,omitempty"`
	PayloadAvailable     bool               `json:"payload_available"`
	UpstreamStatusCode   *int               `json:"upstream_status_code,omitempty"`
	UpstreamErrorMessage string             `json:"upstream_error_message,omitempty"`
	UpstreamErrorDetail  string             `json:"upstream_error_detail,omitempty"`
	UpstreamAttempts     []*UpstreamAttempt `json:"upstream_attempts,omitempty"`
}

// GetErrorPayload returns the bounded payload and upstream attempts persisted
// for one Ops error row. It intentionally has no request_body field: replay
// storage was removed from ops_error_logs.
func (f *Facade) GetErrorPayload(ctx context.Context, errorID int64) (*ErrorPayload, error) {
	if f == nil || f.ops == nil {
		return nil, infraerrors.ServiceUnavailable("CONTENT_AUDIT_OPS_UNAVAILABLE", "content audit Ops service is unavailable")
	}
	if errorID <= 0 {
		return nil, infraerrors.BadRequest("CONTENT_AUDIT_ERROR_ID_INVALID", "error id is invalid")
	}
	detail, err := f.ops.GetErrorLogByID(ctx, errorID)
	if err != nil {
		return nil, err
	}
	if detail == nil {
		return nil, infraerrors.NotFound("CONTENT_AUDIT_ERROR_NOT_FOUND", "error detail not found")
	}
	payload := &ErrorPayload{
		ErrorID:              errorID,
		SanitizedErrorBody:   detail.ErrorBody,
		PayloadAvailable:     strings.TrimSpace(detail.ErrorBody) != "",
		UpstreamStatusCode:   detail.UpstreamStatusCode,
		UpstreamErrorMessage: detail.UpstreamErrorMessage,
		UpstreamErrorDetail:  detail.UpstreamErrorDetail,
	}
	for _, event := range decodeUpstreamEvents(detail.UpstreamErrors) {
		copy := event
		payload.UpstreamAttempts = append(payload.UpstreamAttempts, &UpstreamAttempt{ErrorID: errorID, Event: copy})
	}
	return payload, nil
}

// ListUpstreamAttempts expands the existing sanitized upstream_errors JSON
// stored on Ops error rows. No request body is reconstructed or synthesized.
func (f *Facade) ListUpstreamAttempts(ctx context.Context, requestID, clientRequestID string, page, pageSize int) ([]*UpstreamAttempt, int64, error) {
	if f == nil || f.ops == nil {
		return nil, 0, infraerrors.ServiceUnavailable("CONTENT_AUDIT_OPS_UNAVAILABLE", "content audit Ops service is unavailable")
	}
	requestID = strings.TrimSpace(requestID)
	clientRequestID = strings.TrimSpace(clientRequestID)
	if requestID == "" && clientRequestID == "" {
		return []*UpstreamAttempt{}, 0, nil
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 50
	}
	if pageSize > 500 {
		pageSize = 500
	}
	filter := &service.OpsErrorLogFilter{
		RequestID: requestID, ClientRequestID: clientRequestID,
		Page: page, PageSize: pageSize, View: "all", IncludeRecoveredUpstream: true,
		ErrorPhasesAny: []string{"upstream", "account_auth"}, Owner: "provider",
	}
	rows, err := f.ops.GetErrorLogs(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	if rows == nil {
		return []*UpstreamAttempt{}, 0, nil
	}
	items := make([]*UpstreamAttempt, 0)
	for _, row := range rows.Errors {
		if row == nil {
			continue
		}
		detail, detailErr := f.ops.GetErrorLogByID(ctx, row.ID)
		if detailErr != nil {
			return nil, 0, detailErr
		}
		if detail == nil {
			continue
		}
		for _, event := range decodeUpstreamEvents(detail.UpstreamErrors) {
			copy := event
			items = append(items, &UpstreamAttempt{ErrorID: row.ID, Event: copy})
		}
	}
	return items, int64(len(items)), nil
}

func decodeUpstreamEvents(raw string) []service.OpsUpstreamErrorEvent {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return nil
	}
	var events []service.OpsUpstreamErrorEvent
	if err := json.Unmarshal([]byte(raw), &events); err != nil {
		return nil
	}
	return events
}

func (f *Facade) GetPromptEvent(ctx context.Context, id int64) (*securityaudit.Event, error) {
	if f == nil || f.prompt == nil {
		return nil, infraerrors.ServiceUnavailable("CONTENT_AUDIT_PROMPT_UNAVAILABLE", "content audit Prompt Audit service is unavailable")
	}
	if id <= 0 {
		return nil, infraerrors.BadRequest("CONTENT_AUDIT_EVENT_ID_INVALID", "event id is invalid")
	}
	event, err := f.prompt.GetEvent(ctx, id)
	if errors.Is(err, securityaudit.ErrEventNotFound) {
		return nil, infraerrors.NotFound("CONTENT_AUDIT_EVENT_NOT_FOUND", "prompt audit event not found")
	}
	return event, err
}

func (f *Facade) ListPromptEvents(ctx context.Context, filter securityaudit.EventFilter, page, pageSize int) (*securityaudit.EventPage, error) {
	if f == nil || f.prompt == nil {
		return nil, infraerrors.ServiceUnavailable("CONTENT_AUDIT_PROMPT_UNAVAILABLE", "content audit Prompt Audit service is unavailable")
	}
	return f.prompt.ListEvents(ctx, filter, page, pageSize)
}
