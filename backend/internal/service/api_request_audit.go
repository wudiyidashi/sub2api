package service

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

const (
	APIRequestAuditDirectionClientRequest    = "client_request"
	APIRequestAuditDirectionClientResponse   = "client_response"
	APIRequestAuditDirectionUpstreamRequest  = "upstream_request"
	APIRequestAuditDirectionUpstreamResponse = "upstream_response"

	MaxAPIRequestAuditBodyBytes         = 64 * 1024 * 1024
	DefaultAPIRequestAuditMaxBodyBytes  = MaxAPIRequestAuditBodyBytes
	DefaultAPIRequestAuditChunkBytes    = 1024 * 1024
	DefaultAPIRequestAuditQueueCapacity = 1024
)

type APIRequestAuditPayload struct {
	RequestID       string              `json:"request_id"`
	ClientRequestID string              `json:"client_request_id"`
	Direction       string              `json:"direction"`
	ContentType     string              `json:"content_type"`
	Headers         map[string][]string `json:"headers,omitempty"`
	Body            []byte              `json:"body"`
	OriginalBytes   int64               `json:"original_bytes"`
	ChunkIndex      int                 `json:"chunk_index"`
	Final           bool                `json:"final"`
	Truncated       bool                `json:"truncated"`
	CreatedAt       time.Time           `json:"created_at"`
}

type APIRequestAuditRepository interface {
	InsertAPIRequestAuditPayloads(context.Context, []*APIRequestAuditPayload) error
	ListAPIRequestAuditPayloads(context.Context, string) ([]*APIRequestAuditPayload, error)
}

type APIRequestAuditConfig struct {
	Enabled       bool
	MaxBodyBytes  int
	ChunkBytes    int
	QueueCapacity int
}

func DefaultAPIRequestAuditConfig() APIRequestAuditConfig {
	return APIRequestAuditConfig{Enabled: false, MaxBodyBytes: DefaultAPIRequestAuditMaxBodyBytes, ChunkBytes: DefaultAPIRequestAuditChunkBytes, QueueCapacity: DefaultAPIRequestAuditQueueCapacity}
}

type APIRequestAuditService struct {
	repo      APIRequestAuditRepository
	cfg       APIRequestAuditConfig
	queue     chan []*APIRequestAuditPayload
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	startOnce sync.Once
	stopOnce  sync.Once
}

func NewAPIRequestAuditService(repo APIRequestAuditRepository, cfg APIRequestAuditConfig) *APIRequestAuditService {
	defaults := DefaultAPIRequestAuditConfig()
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = defaults.MaxBodyBytes
	}
	if cfg.MaxBodyBytes > MaxAPIRequestAuditBodyBytes {
		cfg.MaxBodyBytes = MaxAPIRequestAuditBodyBytes
	}
	if cfg.ChunkBytes <= 0 {
		cfg.ChunkBytes = defaults.ChunkBytes
	}
	if cfg.QueueCapacity <= 0 {
		cfg.QueueCapacity = defaults.QueueCapacity
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &APIRequestAuditService{repo: repo, cfg: cfg, queue: make(chan []*APIRequestAuditPayload, cfg.QueueCapacity), ctx: ctx, cancel: cancel}
}

func (s *APIRequestAuditService) Enabled() bool { return s != nil && s.repo != nil && s.cfg.Enabled }
func (s *APIRequestAuditService) MaxBodyBytes() int {
	if s == nil || s.cfg.MaxBodyBytes <= 0 {
		return DefaultAPIRequestAuditMaxBodyBytes
	}
	return s.cfg.MaxBodyBytes
}

func (s *APIRequestAuditService) Start() {
	if s == nil || s.repo == nil {
		return
	}
	_, canCleanup := s.repo.(interface {
		CleanupAPIRequestAuditPayloads(context.Context) error
	})
	// 关闭录制只停止新增，已有排障原文仍应遵守保留期。
	if !s.cfg.Enabled && !canCleanup {
		return
	}
	s.startOnce.Do(func() {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			ticker := time.NewTicker(5 * time.Minute)
			defer ticker.Stop()
			s.cleanup()
			for {
				select {
				case <-s.ctx.Done():
					s.drain()
					return
				case batch := <-s.queue:
					if len(batch) == 0 {
						continue
					}
					s.write(s.ctx, batch)
				case <-ticker.C:
					s.cleanup()
				}
			}
		}()
	})
}

func (s *APIRequestAuditService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() { s.cancel(); s.wg.Wait() })
}

func (s *APIRequestAuditService) cleanup() {
	cleaner, ok := s.repo.(interface {
		CleanupAPIRequestAuditPayloads(context.Context) error
	})
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
	defer cancel()
	if err := cleaner.CleanupAPIRequestAuditPayloads(ctx); err != nil && s.ctx.Err() == nil {
		logger.LegacyPrintf("service.content_audit", "raw content cleanup failed: %v", err)
	}
}

func (s *APIRequestAuditService) write(parent context.Context, batch []*APIRequestAuditPayload) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	if err := s.repo.InsertAPIRequestAuditPayloads(ctx, batch); err != nil && parent.Err() == nil {
		logger.LegacyPrintf("service.content_audit", "raw content recording skipped: %v", err)
	}
}

func (s *APIRequestAuditService) drain() {
	// 关闭时给整个录制队列一个共享期限，避免逐批超时拖延网关停机。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case batch := <-s.queue:
			if len(batch) > 0 {
				s.write(ctx, batch)
			}
		default:
			return
		}
	}
}

// Record queues a bounded copy and falls back to a short synchronous write
// when the queue is full, keeping the audit path usable under burst load.
func (s *APIRequestAuditService) Record(ctx context.Context, payloads []*APIRequestAuditPayload) {
	if !s.Enabled() || len(payloads) == 0 {
		return
	}
	batch := s.normalize(payloads)
	if len(batch) == 0 {
		return
	}
	s.Start()
	select {
	case s.queue <- batch:
		return
	default:
	}
	writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.repo.InsertAPIRequestAuditPayloads(writeCtx, batch); err != nil {
		logger.LegacyPrintf("service.content_audit", "raw content recording skipped: %v", err)
	}
}

func (s *APIRequestAuditService) normalize(payloads []*APIRequestAuditPayload) []*APIRequestAuditPayload {
	chunkBytes := s.cfg.ChunkBytes
	maxBytes := s.MaxBodyBytes()
	out := make([]*APIRequestAuditPayload, 0, len(payloads))
	for _, original := range payloads {
		if original == nil || original.Direction == "" {
			continue
		}
		item := *original
		item.RequestID = trimAuditID(item.RequestID)
		item.ClientRequestID = trimAuditID(item.ClientRequestID)
		item.Body = append([]byte(nil), item.Body...)
		item.Headers = cloneHeaderValues(item.Headers)
		if item.CreatedAt.IsZero() {
			item.CreatedAt = time.Now().UTC()
		}
		if len(item.Body) > maxBytes {
			item.Body = item.Body[:maxBytes]
			item.Truncated = true
		}
		if item.OriginalBytes <= 0 {
			item.OriginalBytes = int64(len(original.Body))
		}
		if len(item.Body) == 0 {
			item.ChunkIndex, item.Final = 0, true
			out = append(out, &item)
			continue
		}
		for offset, index := 0, 0; offset < len(item.Body); index++ {
			end := offset + chunkBytes
			if end > len(item.Body) {
				end = len(item.Body)
			}
			chunk := item
			chunk.Body = append([]byte(nil), item.Body[offset:end]...)
			chunk.ChunkIndex = index
			chunk.Final = end == len(item.Body)
			out = append(out, &chunk)
			offset = end
		}
	}
	return out
}

func (s *APIRequestAuditService) List(ctx context.Context, requestID string) ([]*APIRequestAuditPayload, error) {
	if s == nil || s.repo == nil {
		return nil, context.Canceled
	}
	return s.repo.ListAPIRequestAuditPayloads(ctx, trimAuditID(requestID))
}

type apiRequestAuditCaptureKey struct{}

type APIRequestAuditCapture struct {
	mu              sync.Mutex
	payloads        []*APIRequestAuditPayload
	activeRequestID string
}

// MarkAPIRequestAuditTurn starts a new logical WebSocket turn. The payloads
// captured after the returned index can later be attached to the response ID
// emitted by the upstream response.created event.
func MarkAPIRequestAuditTurn(ctx context.Context) int {
	capture := auditCaptureFromContext(ctx)
	if capture == nil {
		return 0
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	capture.activeRequestID = ""
	return len(capture.payloads)
}

// AliasAPIRequestAuditTurn binds a buffered WebSocket turn to the response ID
// used by usage/Ops records. This keeps the content-audit detail lookup on the
// same identifier as the request list while preserving later turns separately.
func AliasAPIRequestAuditTurn(ctx context.Context, start int, requestID string) {
	capture := auditCaptureFromContext(ctx)
	requestID = trimAuditID(requestID)
	if capture == nil || requestID == "" {
		return
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if start < 0 || start > len(capture.payloads) {
		start = 0
	}
	for _, payload := range capture.payloads[start:] {
		if payload != nil {
			payload.RequestID = requestID
		}
	}
	capture.activeRequestID = requestID
}

func NewAPIRequestAuditCapture() *APIRequestAuditCapture { return &APIRequestAuditCapture{} }

func WithAPIRequestAuditCapture(ctx context.Context, capture *APIRequestAuditCapture) context.Context {
	return context.WithValue(ctx, apiRequestAuditCaptureKey{}, capture)
}

func CaptureAPIRequestAuditPayload(ctx context.Context, payload *APIRequestAuditPayload) {
	if ctx == nil || payload == nil {
		return
	}
	capture := auditCaptureFromContext(ctx)
	if capture == nil {
		return
	}
	capture.mu.Lock()
	if capture.activeRequestID != "" {
		payload.RequestID = capture.activeRequestID
	}
	capture.payloads = append(capture.payloads, payload)
	capture.mu.Unlock()
}

func auditCaptureFromContext(ctx context.Context) *APIRequestAuditCapture {
	if ctx == nil {
		return nil
	}
	capture, _ := ctx.Value(apiRequestAuditCaptureKey{}).(*APIRequestAuditCapture)
	return capture
}

// captureAPIRequestAuditFrame persists one WebSocket message in the same raw
// payload store used by HTTP request/response capture. WebSocket upgrades have
// no useful HTTP body or response writer to observe, so each frame must be
// recorded at the relay boundary instead. The helper keeps frame capture
// bounded by the configured global audit limit and records the original byte
// count so the UI can show truncation accurately.
func captureAPIRequestAuditFrame(ctx context.Context, direction string, payload []byte, headers http.Header) {
	if auditCaptureFromContext(ctx) == nil || direction == "" {
		return
	}
	originalBytes := len(payload)
	body := payload
	truncated := false
	if len(body) > MaxAPIRequestAuditBodyBytes {
		body = body[:MaxAPIRequestAuditBodyBytes]
		truncated = true
	}
	contentType := "application/websocket+json"
	if headers != nil {
		if value := strings.TrimSpace(headers.Get("Content-Type")); value != "" {
			contentType = value
		}
	}
	CaptureAPIRequestAuditPayload(ctx, &APIRequestAuditPayload{
		RequestID: contextAuditRequestID(ctx), ClientRequestID: contextAuditClientRequestID(ctx),
		Direction: direction, ContentType: contentType, Headers: auditHeaderValues(headers),
		Body: append([]byte(nil), body...), OriginalBytes: int64(originalBytes), Truncated: truncated,
	})
}

func (c *APIRequestAuditCapture) Payloads() []*APIRequestAuditPayload {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*APIRequestAuditPayload, 0, len(c.payloads))
	for _, payload := range c.payloads {
		if payload == nil {
			continue
		}
		copy := *payload
		copy.Body = append([]byte(nil), payload.Body...)
		copy.Headers = cloneHeaderValues(payload.Headers)
		out = append(out, &copy)
	}
	return out
}

func cloneHeaderValues(input map[string][]string) map[string][]string {
	if input == nil {
		return nil
	}
	out := make(map[string][]string, len(input))
	for key, values := range input {
		out[key] = append([]string(nil), values...)
	}
	return out
}

func trimAuditID(value string) string {
	value = string([]byte(value))
	if len(value) > 128 {
		return value[:128]
	}
	return value
}
