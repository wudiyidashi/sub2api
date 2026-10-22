package repository

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// 通过实际代理访问 HTTPS 地域端点，禁止向查询方发送账号认证或请求正文
// 不采用代理主机地理位置，也不通过国家代码猜测多时区国家的时区
type requestEnvironmentProber struct {
	clientFactory func(string) (*http.Client, error)
}

func NewRequestEnvironmentProber() service.RequestEnvironmentProber {
	return &requestEnvironmentProber{}
}
func (p *requestEnvironmentProber) ProbeRequestEnvironment(ctx context.Context, proxy string) (*service.CodexRequestEnvironment, error) {
	factory := p.clientFactory
	if factory == nil {
		factory = func(proxy string) (*http.Client, error) {
			return httpclient.GetClient(httpclient.Options{ProxyURL: proxy, Timeout: 5 * time.Second})
		}
	}
	client, err := factory(proxy)
	if err != nil {
		return nil, errors.New("invalid egress proxy")
	}
	for _, endpoint := range []string{"https://get.geojs.io/v1/ip/geo.json", "https://ipwho.is/"} {
		request, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if requestErr != nil {
			continue
		}
		response, requestErr := client.Do(request)
		if requestErr != nil {
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 64*1024+1))
		_ = response.Body.Close()
		if readErr != nil || response.StatusCode != http.StatusOK || len(body) > 64*1024 {
			continue
		}
		var raw struct {
			IP          string          `json:"ip"`
			Country     string          `json:"country"`
			CountryCode string          `json:"country_code"`
			Region      string          `json:"region"`
			City        string          `json:"city"`
			Timezone    json.RawMessage `json:"timezone"`
			Success     *bool           `json:"success"`
		}
		if json.Unmarshal(body, &raw) != nil || (raw.Success != nil && !*raw.Success) {
			continue
		}
		var zone string
		if json.Unmarshal(raw.Timezone, &zone) != nil {
			var nested struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(raw.Timezone, &nested) == nil {
				zone = nested.ID
			}
		}
		if zone == "" || net.ParseIP(raw.IP) == nil || len(raw.CountryCode) != 2 || !validCountryCode(raw.CountryCode) {
			continue
		}
		if _, err := time.LoadLocation(zone); err != nil {
			continue
		}
		return &service.CodexRequestEnvironment{IP: raw.IP, Country: raw.Country, CountryCode: raw.CountryCode, Region: raw.Region, City: raw.City, Timezone: zone}, nil
	}
	return nil, errors.New("egress location unavailable")
}

func validCountryCode(code string) bool {
	code = strings.ToUpper(code)
	return len(code) == 2 && code[0] >= 'A' && code[0] <= 'Z' && code[1] >= 'A' && code[1] <= 'Z'
}
