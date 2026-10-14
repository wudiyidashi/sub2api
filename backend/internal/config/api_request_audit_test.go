package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadAPIRequestAuditEnabled(t *testing.T) {
	for _, test := range []struct {
		name, file, env string
		want            bool
	}{
		{name: "disabled by default"},
		{name: "enabled by yaml", file: "api_request_audit:\n  enabled: true\n", want: true},
		{name: "enabled by environment", env: "true", want: true},
		{name: "environment disables yaml", file: "api_request_audit:\n  enabled: true\n", env: "false"},
	} {
		t.Run(test.name, func(t *testing.T) {
			resetViperWithJWTSecret(t)
			t.Setenv("API_REQUEST_AUDIT_ENABLED", test.env)
			if test.file != "" {
				path := filepath.Join(t.TempDir(), "config.yaml")
				require.NoError(t, os.WriteFile(path, []byte(test.file), 0600))
				t.Setenv("CONFIG_FILE", path)
			}
			cfg, err := Load()
			require.NoError(t, err)
			require.Equal(t, test.want, cfg.APIRequestAudit.Enabled)
		})
	}
}
