package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEnabledAccountDriverRejectsInvalidConfigurationBeforeStartup(t *testing.T) {
	for _, test := range []struct {
		config   CodexRSHTTPDriverConfig
		expected string
	}{
		{CodexRSHTTPDriverConfig{Enabled: true, StartTimeoutSeconds: 10}, "executable_path"},
		{CodexRSHTTPDriverConfig{Enabled: true, ExecutablePath: "driver", StartTimeoutSeconds: 0}, "start_timeout_seconds"},
		{CodexRSHTTPDriverConfig{Enabled: true, ExecutablePath: "driver", StartTimeoutSeconds: 10, SHA256: strings.Repeat("z", 64)}, "sha256"},
	} {
		cfg := &Config{AccountDrivers: AccountDriversConfig{CodexRSHTTP: test.config}}
		err := cfg.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), test.expected)
	}
}
