package middleware

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type middlewareAuditRepo struct {
	writes chan []*service.APIRequestAuditPayload
}

func (r *middlewareAuditRepo) InsertAPIRequestAuditPayloads(_ context.Context, payloads []*service.APIRequestAuditPayload) error {
	r.writes <- payloads
	return nil
}

func (*middlewareAuditRepo) ListAPIRequestAuditPayloads(context.Context, string) ([]*service.APIRequestAuditPayload, error) {
	return nil, nil
}

func TestAPIRequestAuditMiddlewareTogglePreservesHTTPAndStreaming(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, enabled := range []bool{false, true} {
		name := "disabled"
		if enabled {
			name = "enabled"
		}
		t.Run(name, func(t *testing.T) {
			repo := &middlewareAuditRepo{writes: make(chan []*service.APIRequestAuditPayload, 2)}
			cfg := service.DefaultAPIRequestAuditConfig()
			cfg.Enabled = enabled
			audit := service.NewAPIRequestAuditService(repo, cfg)
			t.Cleanup(audit.Stop)
			router := gin.New()
			router.Use(APIRequestAuditMiddleware(audit))
			router.POST("/v1/responses", func(c *gin.Context) {
				if !enabled {
					_, wrapped := c.Writer.(*apiRequestAuditResponseWriter)
					require.False(t, wrapped)
					_, restored := c.Request.Body.(*restoredAuditBody)
					require.False(t, restored)
				}
				body, err := io.ReadAll(c.Request.Body)
				require.NoError(t, err)
				require.Equal(t, "request", string(body))
				c.Header("Content-Type", "text/event-stream")
				_, err = c.Writer.WriteString("data: first\n\n")
				require.NoError(t, err)
				c.Writer.Flush()
				_, err = c.Writer.WriteString("data: second\n\n")
				require.NoError(t, err)
			})
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString("request")))
			require.Equal(t, http.StatusOK, rec.Code)
			require.Equal(t, "data: first\n\ndata: second\n\n", rec.Body.String())
			require.True(t, rec.Flushed)
			if enabled {
				select {
				case payloads := <-repo.writes:
					require.Len(t, payloads, 2)
					require.Equal(t, "request", string(payloads[0].Body))
					require.Equal(t, rec.Body.String(), string(payloads[1].Body))
				case <-time.After(5 * time.Second):
					t.Fatal("enabled middleware did not persist HTTP payloads")
				}
			} else {
				audit.Stop()
				require.Empty(t, repo.writes)
			}
		})
	}
}
