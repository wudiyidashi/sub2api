package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCPRManagedConfigRequiresIndependentConnectionAndScopeSecret(t *testing.T) {
	require.NoError(t, (CPRManagedConfig{}).Validate())
	valid := CPRManagedConfig{
		Enabled: true, BaseURL: "http://cpr-internal:8080", AdminAPIKey: "admin-private",
		DeploymentID: "production", BridgeSecret: strings.Repeat("x", 32), AdminTimeoutSeconds: 30,
	}
	require.NoError(t, valid.Validate())
	for _, mutate := range []func(*CPRManagedConfig){
		func(c *CPRManagedConfig) { c.BaseURL = "https://u:secret@cpr.test" },
		func(c *CPRManagedConfig) { c.BaseURL = "https://cpr.test/api" },
		func(c *CPRManagedConfig) { c.AdminAPIKey = "" },
		func(c *CPRManagedConfig) { c.BridgeSecret = "too-short" },
		func(c *CPRManagedConfig) { c.DeploymentID = "" },
		func(c *CPRManagedConfig) { c.AdminTimeoutSeconds = 0 },
	} {
		cfg := valid
		mutate(&cfg)
		err := cfg.Validate()
		require.Error(t, err)
		require.NotContains(t, err.Error(), "secret@")
	}
}
