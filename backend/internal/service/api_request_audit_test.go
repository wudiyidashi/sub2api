package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type recordingAuditRepo struct {
	writes chan []*APIRequestAuditPayload
}

func (r *recordingAuditRepo) InsertAPIRequestAuditPayloads(_ context.Context, payloads []*APIRequestAuditPayload) error {
	r.writes <- payloads
	return nil
}

func (r *recordingAuditRepo) ListAPIRequestAuditPayloads(_ context.Context, id string) ([]*APIRequestAuditPayload, error) {
	return []*APIRequestAuditPayload{{RequestID: id, Body: []byte("historical")}}, nil
}

func TestAPIRequestAuditProviderControlsRecordingAndKeepsHistory(t *testing.T) {
	for _, test := range []struct {
		name string
		cfg  *config.Config
		want bool
	}{
		{name: "missing config"},
		{name: "default config", cfg: &config.Config{}},
		{name: "explicitly enabled", cfg: &config.Config{APIRequestAudit: config.APIRequestAuditConfig{Enabled: true}}, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := &recordingAuditRepo{writes: make(chan []*APIRequestAuditPayload, 4)}
			svc := ProvideAPIRequestAuditService(repo, test.cfg)
			t.Cleanup(svc.Stop)
			require.Equal(t, test.want, svc.Enabled())
			svc.Record(context.Background(), []*APIRequestAuditPayload{{RequestID: "new", Direction: APIRequestAuditDirectionClientRequest, Body: []byte("new body")}})
			if test.want {
				select {
				case batch := <-repo.writes:
					require.Equal(t, "new body", string(batch[0].Body))
				case <-time.After(5 * time.Second):
					t.Fatal("enabled recording did not persist the payload")
				}
			} else {
				svc.Stop()
				require.Empty(t, repo.writes)
			}
			history, err := svc.List(context.Background(), "existing")
			require.NoError(t, err)
			require.Equal(t, "historical", string(history[0].Body))
		})
	}
}

func TestAPIRequestAuditDisabledUpstreamDoesNotCopyOrBuffer(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.test/responses", bytes.NewReader([]byte("raw request")))
	require.NoError(t, err)
	req.GetBody = func() (io.ReadCloser, error) {
		t.Error("disabled audit must not request a body copy")
		return nil, nil
	}
	recordAPIRequestAuditUpstreamRequest(req)
	body := io.NopCloser(bytes.NewReader([]byte("streamed response")))
	wrapped := newAPIRequestAuditReadCloser(body, req.Context(), APIRequestAuditDirectionUpstreamResponse, nil)
	require.True(t, body == wrapped, "disabled audit must return the original response stream")
	raw, err := io.ReadAll(wrapped)
	require.NoError(t, err)
	require.Equal(t, "streamed response", string(raw))
	require.NoError(t, wrapped.Close())

	frame := make([]byte, 64*1024)
	allocations := testing.AllocsPerRun(10, func() {
		captureAPIRequestAuditFrame(req.Context(), APIRequestAuditDirectionClientRequest, frame, nil)
	})
	require.Zero(t, allocations, "disabled WebSocket capture must not copy frames")
}

func TestAPIRequestAuditEnabledUpstreamAndWebSocketPreservePayloads(t *testing.T) {
	capture := NewAPIRequestAuditCapture()
	ctx := WithAPIRequestAuditCapture(context.Background(), capture)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.test/responses", bytes.NewReader([]byte("raw request")))
	require.NoError(t, err)
	req.Header.Add("X-Unknown", "first")
	req.Header.Add("X-Unknown", "second")
	recordAPIRequestAuditUpstreamRequest(req)
	body := newAPIRequestAuditReadCloser(io.NopCloser(bytes.NewReader([]byte("raw response"))), ctx, APIRequestAuditDirectionUpstreamResponse, nil)
	raw, err := io.ReadAll(body)
	require.NoError(t, err)
	require.Equal(t, "raw response", string(raw))
	require.NoError(t, body.Close())
	frame := []byte("raw frame")
	captureAPIRequestAuditFrame(ctx, APIRequestAuditDirectionClientRequest, frame, nil)
	frame[0] = 'X'
	payloads := capture.Payloads()
	require.Len(t, payloads, 3)
	require.Equal(t, "raw request", string(payloads[0].Body))
	require.Equal(t, []string{"first", "second"}, payloads[0].Headers["X-Unknown"])
	require.Equal(t, "raw response", string(payloads[1].Body))
	require.Equal(t, "raw frame", string(payloads[2].Body))
}
