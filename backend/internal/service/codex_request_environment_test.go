//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type environmentProbeStub struct {
	mu    sync.Mutex
	calls int
	fail  bool
	block <-chan struct{}
}

func (p *environmentProbeStub) ProbeRequestEnvironment(ctx context.Context, _ string) (*CodexRequestEnvironment, error) {
	p.mu.Lock()
	p.calls++
	fail := p.fail
	p.mu.Unlock()
	if p.block != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-p.block:
		}
	}
	if fail {
		return nil, errors.New("sensitive proxy error")
	}
	return &CodexRequestEnvironment{IP: "1.2.3.4", Country: "United States", CountryCode: "US", Region: "California", City: "Los Angeles", Timezone: "America/Los_Angeles"}, nil
}

type environmentRepoStub struct {
	SettingRepository
	mu     sync.Mutex
	values map[string]string
}

func (r *environmentRepoStub) GetValue(_ context.Context, k string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.values[k]
	if !ok {
		return "", ErrSettingNotFound
	}
	return v, nil
}
func (r *environmentRepoStub) Set(_ context.Context, k, v string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.values == nil {
		r.values = map[string]string{}
	}
	r.values[k] = v
	return nil
}
func environmentAccount() *Account {
	id := int64(4)
	return &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ProxyID: &id, Proxy: &Proxy{ID: id, Protocol: "socks5", Host: "proxy.test", Port: 1080, Username: "user", Password: "secret"}}
}

func TestRequestEnvironmentCacheIdentityAndPersistence(t *testing.T) {
	p := &environmentProbeStub{}
	repo := &environmentRepoStub{}
	s := NewAccountRequestEnvironmentService(repo, p)
	a := environmentAccount()
	ctx := context.Background()
	e, err := s.Resolve(ctx, a)
	require.NoError(t, err)
	require.Equal(t, "America/Los_Angeles", e.Timezone)
	_, err = s.Resolve(ctx, a)
	require.NoError(t, err)
	require.Equal(t, 1, p.calls)
	s = NewAccountRequestEnvironmentService(repo, p)
	_, err = s.Resolve(ctx, a)
	require.NoError(t, err)
	require.Equal(t, 1, p.calls)
	a.Proxy.Password = "changed"
	_, err = s.Resolve(ctx, a)
	require.NoError(t, err)
	require.Equal(t, 2, p.calls)
	require.NotContains(t, repo.values["codex_request_environment_proxy_4"], "changed")
	_, err = s.Refresh(ctx, a)
	require.NoError(t, err)
	require.Equal(t, 3, p.calls)
	a.Proxy = nil
	_, err = s.Resolve(ctx, a)
	require.ErrorContains(t, err, "禁止按直连探测")
	require.Equal(t, 3, p.calls)
}
func TestRequestEnvironmentExpiresWithoutStaleFallback(t *testing.T) {
	p := &environmentProbeStub{}
	s := NewAccountRequestEnvironmentService(nil, p)
	clock := time.Date(2026, 10, 22, 1, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }
	_, err := s.Resolve(context.Background(), environmentAccount())
	require.NoError(t, err)
	clock = clock.Add(16 * time.Minute)
	p.fail = true
	_, err = s.Resolve(context.Background(), environmentAccount())
	require.ErrorIs(t, err, errRequestEnvironmentUnavailable)
	require.NotContains(t, err.Error(), "sensitive")
	_, err = s.Resolve(context.Background(), environmentAccount())
	require.Error(t, err)
	require.Equal(t, 2, p.calls)
}

func TestRequestEnvironmentFailedRefreshPersistsInvalidation(t *testing.T) {
	p := &environmentProbeStub{}
	repo := &environmentRepoStub{}
	s := NewAccountRequestEnvironmentService(repo, p)
	a := environmentAccount()
	_, err := s.Resolve(context.Background(), a)
	require.NoError(t, err)
	p.fail = true
	_, err = s.Refresh(context.Background(), a)
	require.ErrorIs(t, err, errRequestEnvironmentUnavailable)
	// 重启后也不能读回强制刷新之前的有效快照。
	s = NewAccountRequestEnvironmentService(repo, p)
	_, err = s.Resolve(context.Background(), a)
	require.ErrorIs(t, err, errRequestEnvironmentUnavailable)
	require.Equal(t, 2, p.calls)
	require.NotContains(t, repo.values["codex_request_environment_proxy_4"], "1.2.3.4")
}
func TestRequestEnvironmentCoalescesConcurrentProbes(t *testing.T) {
	block := make(chan struct{})
	p := &environmentProbeStub{block: block}
	s := NewAccountRequestEnvironmentService(nil, p)
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Resolve(context.Background(), environmentAccount())
			require.NoError(t, err)
		}()
	}
	require.Eventually(t, func() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.calls == 1 }, time.Second, time.Millisecond)
	close(block)
	wg.Wait()
	require.Equal(t, 1, p.calls)
}
func TestRequestEnvironmentStructuredAlignmentPreservesBusinessBytes(t *testing.T) {
	e, _ := (&environmentProbeStub{}).ProbeRequestEnvironment(context.Background(), "")
	now := time.Date(2026, 10, 22, 1, 30, 0, 0, time.UTC)
	raw := []byte(`{"z": 1.0000,"input":[{"role":"user","content":[{"type":"input_text","text":"<environment_context><timezone>Asia/Shanghai</timezone><current_date>2026-10-22</current_date><cwd>D:/Work</cwd><city>Shanghai</city></environment_context>"}]},{"role":"developer","internal_chat_message_metadata_passthrough":{"content_item_kinds":["additional_content.codex_apps_client_time_context"]},"content":[{"type":"input_text","text":"<codex_apps_client_time_context><timezone>Asia/Shanghai</timezone></codex_apps_client_time_context>"}]},{"role":"user","content":[{"type":"input_text","text":"Schedule in Asia/Shanghai on 2026-10-22"}]}],"tools":[{"type":"web_search","filters":{"allowed_domains":["example.com"]}},{"type":"function","parameters":{"timezone":"Asia/Shanghai"}}]}`)
	aligned, err := alignCodexRequestEnvironment(raw, e, now)
	require.NoError(t, err)
	require.Contains(t, string(aligned), `"z": 1.0000`)
	require.Contains(t, gjson.GetBytes(aligned, "input.0.content.0.text").String(), "<current_date>2026-10-21</current_date>")
	require.Contains(t, gjson.GetBytes(aligned, "input.0.content.0.text").String(), "<cwd>D:/Work</cwd>")
	require.Contains(t, gjson.GetBytes(aligned, "input.1.content.0.text").String(), "America/Los_Angeles")
	require.Equal(t, gjson.GetBytes(raw, "input.2").Raw, gjson.GetBytes(aligned, "input.2").Raw)
	require.Equal(t, gjson.GetBytes(raw, "tools.1").Raw, gjson.GetBytes(aligned, "tools.1").Raw)
	require.Equal(t, "US", gjson.GetBytes(aligned, "tools.0.user_location.country").String())
	require.Equal(t, "example.com", gjson.GetBytes(aligned, "tools.0.filters.allowed_domains.0").String())
}
func TestRequestEnvironmentClassificationAndDST(t *testing.T) {
	e := &CodexRequestEnvironment{Timezone: "America/Los_Angeles", CountryCode: "US"}
	raw := []byte(`{"input":[{"role":"user","internal_chat_message_metadata_passthrough":{"content_item_kinds":["text"]},"content":[{"type":"input_text","text":"<environment_context><timezone>Asia/Shanghai</timezone></environment_context>"}]}]}`)
	aligned, err := alignCodexRequestEnvironment(raw, e, time.Now())
	require.NoError(t, err)
	require.Equal(t, raw, aligned)
	for _, tc := range []struct{ at, offset string }{{"2026-07-01T12:00:00Z", "-07:00"}, {"2026-12-01T12:00:00Z", "-08:00"}} {
		instant, err := time.Parse(time.RFC3339, tc.at)
		require.NoError(t, err)
		loc, err := time.LoadLocation(e.Timezone)
		require.NoError(t, err)
		text, ok := alignCodexEnvironmentContext("<environment_context><utc_offset>+08:00</utc_offset><filesystem><timezone>keep</timezone></filesystem></environment_context>", "environment_context", e, instant.In(loc))
		require.True(t, ok)
		require.Contains(t, text, "<utc_offset>"+tc.offset+"</utc_offset>")
		require.Contains(t, text, "<filesystem><timezone>keep</timezone></filesystem>")
	}
}
func TestRequestEnvironmentHTTPPreparedRequest(t *testing.T) {
	p := &environmentProbeStub{}
	resolver := NewAccountRequestEnvironmentService(nil, p)
	a := environmentAccount()
	raw := []byte(`{"input":[],"tools":[{"type":"web_search"}],"x":1.0000}`)
	req, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", bytes.NewReader(raw))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Accept-Language", "fr-CA")
	req.Header.Set("Content-Length", "999")
	require.ErrorContains(t, alignCodexHTTPRequestEnvironment(req, a, resolver, ""), "does not match")
	require.Equal(t, 0, p.calls)
	require.NoError(t, alignCodexHTTPRequestEnvironment(req, a, resolver, a.Proxy.URL()))
	body, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"x":1.0000`)
	require.Equal(t, "US", gjson.GetBytes(body, "tools.0.user_location.country").String())
	require.Equal(t, int64(len(body)), req.ContentLength)
	require.Empty(t, req.Header.Get("Content-Length"))
	require.Equal(t, "fr-CA", req.Header.Get("Accept-Language"))
	p.fail = true
	resolver.entries = map[string]requestEnvironmentEntry{}
	req, err = http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", strings.NewReader(string(raw)))
	require.NoError(t, err)
	require.Error(t, alignCodexHTTPRequestEnvironment(req, a, resolver, a.Proxy.URL()))
}

func TestRequestEnvironmentWSSnapshotAndPoolIsolation(t *testing.T) {
	prober := &environmentProbeStub{}
	resolver := NewAccountRequestEnvironmentService(nil, prober)
	service := &OpenAIGatewayService{settingService: &SettingService{requestEnvironments: resolver}}
	account := environmentAccount()
	ctx, err := service.pinCodexEnvironment(context.Background(), account)
	require.NoError(t, err)
	oldKey := codexEnvironmentPoolKey(ctx)
	require.NotEmpty(t, oldKey)
	resolver.mu.Lock()
	entry := resolver.entries["proxy_4"]
	replacement := *entry.Environment
	replacement.IP = "5.6.7.8"
	replacement.Timezone = "Asia/Tokyo"
	entry.Environment = &replacement
	resolver.entries["proxy_4"] = entry
	resolver.mu.Unlock()
	pinned, err := service.codexEnvironment(ctx, account)
	require.NoError(t, err)
	require.Equal(t, "America/Los_Angeles", pinned.Timezone)
	nextCtx, err := service.pinCodexEnvironment(context.Background(), account)
	require.NoError(t, err)
	require.NotEqual(t, oldKey, codexEnvironmentPoolKey(nextCtx))
	for _, driver := range []string{"native", AccountDriverCodexHTTP} {
		account.AccountDriverID = driver
		before := normalizeManagedWSCompatibility(openAIWSAcquireRequest{Account: account, RequestEnvironment: oldKey})
		after := normalizeManagedWSCompatibility(openAIWSAcquireRequest{Account: account, RequestEnvironment: codexEnvironmentPoolKey(nextCtx)})
		require.NotEqual(t, before, after)
	}
	raw := []byte(`{"type":"session.update","session":{"tools":[{"type":"web_search"}]},"unknown":1.0000}`)
	aligned, err := alignCodexRequestEnvironment(raw, pinned, time.Now())
	require.NoError(t, err)
	require.Equal(t, "US", gjson.GetBytes(aligned, "session.tools.0.user_location.country").String())
	require.Contains(t, string(aligned), `"unknown":1.0000`)
}
