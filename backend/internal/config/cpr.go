package config

import (
	"fmt"
	"net/url"
	"strings"
)

// CPRManagedConfig 配置完整 CPR 网关，独立于只负责传输的 codex_rs_http。
type CPRManagedConfig struct {
	Enabled             bool   `mapstructure:"enabled"`
	BaseURL             string `mapstructure:"base_url"`
	AdminAPIKey         string `mapstructure:"admin_api_key"`
	DeploymentID        string `mapstructure:"deployment_id"`
	BridgeSecret        string `mapstructure:"bridge_secret"`
	AdminTimeoutSeconds int    `mapstructure:"admin_timeout_seconds"`
}

func (c CPRManagedConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return fmt.Errorf("account_drivers.cpr_managed.base_url must be an HTTP(S) origin")
	}
	if strings.TrimSpace(c.AdminAPIKey) == "" || strings.ContainsAny(c.AdminAPIKey, "\r\n") {
		return fmt.Errorf("account_drivers.cpr_managed.admin_api_key is required")
	}
	if len(c.BridgeSecret) < 32 {
		return fmt.Errorf("account_drivers.cpr_managed.bridge_secret must contain at least 32 bytes")
	}
	if strings.TrimSpace(c.DeploymentID) == "" {
		return fmt.Errorf("account_drivers.cpr_managed.deployment_id is required")
	}
	if c.AdminTimeoutSeconds < 1 || c.AdminTimeoutSeconds > 120 {
		return fmt.Errorf("account_drivers.cpr_managed.admin_timeout_seconds must be between 1 and 120")
	}
	return nil
}
