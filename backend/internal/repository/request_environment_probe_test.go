package repository

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type requestEnvironmentRoundTripper func(*http.Request) (*http.Response, error)

func (f requestEnvironmentRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
func TestRequestEnvironmentProbeUsesSelectedEgressAndValidatesLocation(t *testing.T) {
	for _, tc := range []struct {
		name, first, second string
		wantCalls           int
		wantError           bool
	}{
		{"geojs", `{"ip":"1.2.3.4","country_code":"US","city":"LA","timezone":"America/Los_Angeles"}`, "", 1, false},
		{"fallback", `{"ip":"1.2.3.4","country_code":"US"}`, `{"success":true,"ip":"1.2.3.4","country_code":"US","timezone":{"id":"America/New_York"}}`, 2, false},
		{"invalid", `{"ip":"invalid","country_code":"US","timezone":"America/New_York"}`, `{"success":false}`, 2, true},
		{"oversize", strings.Repeat("x", 65*1024), `{"success":false}`, 2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			prober := &requestEnvironmentProber{clientFactory: func(proxy string) (*http.Client, error) {
				require.Equal(t, "socks5://test:secret@proxy.test:1080", proxy)
				return &http.Client{Transport: requestEnvironmentRoundTripper(func(r *http.Request) (*http.Response, error) {
					calls++
					require.Equal(t, "https", r.URL.Scheme)
					require.Empty(t, r.Header.Get("Authorization"))
					require.Empty(t, r.URL.RawQuery)
					body := tc.first
					if calls == 2 {
						body = tc.second
					}
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
				})}, nil
			}}
			e, err := prober.ProbeRequestEnvironment(context.Background(), "socks5://test:secret@proxy.test:1080")
			require.Equal(t, tc.wantCalls, calls)
			if tc.wantError {
				require.Error(t, err)
				require.Nil(t, e)
			} else {
				require.NoError(t, err)
				require.NotEmpty(t, e.Timezone)
				require.Equal(t, "1.2.3.4", e.IP)
			}
		})
	}
}
