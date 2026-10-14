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
			require.Equal(t, 24, cfg.APIRequestAudit.RetentionHours)
			require.EqualValues(t, 512, cfg.APIRequestAudit.MaxStorageMB)
		})
	}
}

func TestLoadAPIRequestAuditStoragePolicy(t *testing.T) {
	resetViperWithJWTSecret(t)
	t.Setenv("API_REQUEST_AUDIT_RETENTION_HOURS", "12")
	t.Setenv("API_REQUEST_AUDIT_MAX_STORAGE_MB", "64")
	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, 12, cfg.APIRequestAudit.RetentionHours)
	require.EqualValues(t, 64, cfg.APIRequestAudit.MaxStorageMB)
	cfg.APIRequestAudit.RetentionHours = -1
	require.ErrorContains(t, cfg.Validate(), "api_request_audit.retention_hours")
	cfg.APIRequestAudit.RetentionHours = 24
	cfg.APIRequestAudit.MaxStorageMB = -1
	require.ErrorContains(t, cfg.Validate(), "api_request_audit.max_storage_mb")
}
