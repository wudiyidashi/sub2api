package middleware

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

var apiRequestAuditService atomic.Pointer[service.APIRequestAuditService]

// SetAPIRequestAuditService installs the process-wide capture service before
// router construction. Keeping this setter avoids expanding the long-standing
// SetupRouter dependency signature while still making capture global.
func SetAPIRequestAuditService(audit *service.APIRequestAuditService) {
	apiRequestAuditService.Store(audit)
}

func APIRequestAuditService() *service.APIRequestAuditService {
	return apiRequestAuditService.Load()
}

// APIRequestAuditMiddleware captures bounded raw client request/response bytes.
// The request body is restored before downstream handlers run and the response
// writer forwards every operation unchanged, including streaming writes.
func APIRequestAuditMiddleware(audit *service.APIRequestAuditService) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c == nil || c.Request == nil || audit == nil || !audit.Enabled() {
			c.Next()
			return
		}
		// The content-audit surface concerns API traffic. Avoid persisting
		// embedded frontend assets and health probes from the global router.
		// Gateway traffic is mounted on /v1, /v1beta, /v3, /responses and
		// /backend-api in addition to the panel's /api tree. The old /api-only
		// check caused successful WebSocket requests to appear in Ops without
		// any raw payloads because the upgrade route never entered this capture.
		path := c.Request.URL.Path
		isGatewayPath := strings.HasPrefix(path, "/v1") ||
			strings.HasPrefix(path, "/v3") ||
			strings.HasPrefix(path, "/responses") ||
			strings.HasPrefix(path, "/backend-api/")
		if (!strings.HasPrefix(path, "/api/") && !isGatewayPath) || strings.HasPrefix(path, "/api/v1/admin/content-audit") {
			c.Next()
			return
		}
		capture := service.NewAPIRequestAuditCapture()
		requestCtx := service.WithAPIRequestAuditCapture(c.Request.Context(), capture)
		c.Request = c.Request.WithContext(requestCtx)

		requestBody, requestOriginalBytes, requestTruncated := readAndRestoreAuditRequestBody(c.Request, audit.MaxBodyBytes())
		requestID := contextString(requestCtx, ctxkey.RequestID)
		clientRequestID := contextString(requestCtx, ctxkey.ClientRequestID)
		if requestID == "" {
			requestID = clientRequestID
		}
		requestHeaders := snapshotHTTPHeaders(c.Request.Header)
		responseWriter := &apiRequestAuditResponseWriter{ResponseWriter: c.Writer, maxBytes: audit.MaxBodyBytes()}
		c.Writer = responseWriter
		c.Next()

		responseBody, responseOriginalBytes, responseTruncated := responseWriter.snapshot()
		capturePayloads := capture.Payloads()
		capturePayloads = append(capturePayloads,
			&service.APIRequestAuditPayload{RequestID: requestID, ClientRequestID: clientRequestID, Direction: service.APIRequestAuditDirectionClientRequest, ContentType: c.GetHeader("Content-Type"), Headers: requestHeaders, Body: requestBody, OriginalBytes: requestOriginalBytes, Truncated: requestTruncated},
			&service.APIRequestAuditPayload{RequestID: requestID, ClientRequestID: clientRequestID, Direction: service.APIRequestAuditDirectionClientResponse, ContentType: responseWriter.Header().Get("Content-Type"), Headers: snapshotHTTPHeaders(responseWriter.Header()), Body: responseBody, OriginalBytes: responseOriginalBytes, Truncated: responseTruncated},
		)
		audit.Record(context.Background(), capturePayloads)
	}
}

func contextString(ctx context.Context, key ctxkey.Key) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(key).(string)
	return strings.TrimSpace(value)
}

func snapshotHTTPHeaders(headers http.Header) map[string][]string {
	if headers == nil {
		return nil
	}
	out := make(map[string][]string, len(headers))
	for key, values := range headers {
		out[key] = append([]string(nil), values...)
	}
	return out
}

func readAndRestoreAuditRequestBody(request *http.Request, maxBytes int) ([]byte, int64, bool) {
	if request == nil || request.Body == nil || maxBytes <= 0 {
		return nil, 0, false
	}
	original := request.Body
	raw, _ := io.ReadAll(io.LimitReader(original, int64(maxBytes)+1))
	truncated := len(raw) > maxBytes
	body := raw
	if truncated {
		body = raw[:maxBytes]
	}
	request.Body = &restoredAuditBody{Reader: io.MultiReader(bytes.NewReader(raw), original), closer: original}
	return body, int64(len(raw)), truncated
}

type restoredAuditBody struct {
	io.Reader
	closer io.Closer
}

func (b *restoredAuditBody) Close() error { return b.closer.Close() }

type apiRequestAuditResponseWriter struct {
	gin.ResponseWriter
	mu        sync.Mutex
	maxBytes  int
	body      bytes.Buffer
	total     int64
	truncated bool
}

func (w *apiRequestAuditResponseWriter) Write(data []byte) (int, error) {
	w.capture(data)
	return w.ResponseWriter.Write(data)
}

func (w *apiRequestAuditResponseWriter) WriteString(data string) (int, error) {
	w.capture([]byte(data))
	return w.ResponseWriter.WriteString(data)
}

func (w *apiRequestAuditResponseWriter) capture(data []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.total += int64(len(data))
	if w.maxBytes <= w.body.Len() {
		w.truncated = w.truncated || len(data) > 0
		return
	}
	remaining := w.maxBytes - w.body.Len()
	if len(data) > remaining {
		w.body.Write(data[:remaining])
		w.truncated = true
	} else {
		w.body.Write(data)
	}
}

func (w *apiRequestAuditResponseWriter) snapshot() ([]byte, int64, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.body.Bytes()...), w.total, w.truncated
}
