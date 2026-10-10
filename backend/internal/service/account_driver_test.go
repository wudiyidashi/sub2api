//go:build unit

package service

import (
	"context"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestAccountDriverValidationRejectsBeforeAccountMutation(t *testing.T) {
	// A nil repository proves unsupported choices fail before reading or writing
	// an account, including service callers outside the HTTP handlers.
	svc := &adminServiceImpl{}
	for _, tc := range []struct {
		id       string
		settings map[string]any
		reason   string
	}{
		{id: "codex-enhanced", reason: "ACCOUNT_DRIVER_UNKNOWN"},
		{id: "native", settings: map[string]any{"access_token": "must-not-persist"}, reason: "ACCOUNT_DRIVER_SETTINGS_UNSUPPORTED"},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			_, err := svc.CreateAccount(context.Background(), &CreateAccountInput{AccountDriverID: tc.id, DriverSettings: tc.settings})
			require.Equal(t, tc.reason, infraerrors.Reason(err))
			_, err = svc.UpdateAccount(context.Background(), 1, &UpdateAccountInput{AccountDriverID: tc.id, DriverSettings: tc.settings})
			require.Equal(t, tc.reason, infraerrors.Reason(err))
		})
	}
	_, err := svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{Filters: &BulkUpdateAccountFilters{AccountDriverID: "missing"}})
	require.Equal(t, "ACCOUNT_DRIVER_UNKNOWN", infraerrors.Reason(err))
}

func TestAccountDriverNativeCreationPreservesCredentialsAndHostSettings(t *testing.T) {
	for _, id := range []string{"", "native"} {
		t.Run("driver="+id, func(t *testing.T) {
			repo := newDuplicateAccountRepoStub()
			svc := &adminServiceImpl{accountRepo: repo}
			input := &CreateAccountInput{
				AccountDriverID: id, DriverSettings: map[string]any{},
				Name: "existing", Platform: PlatformAnthropic, Type: AccountTypeAPIKey,
				Credentials: map[string]any{"api_key": "secret"}, Extra: map[string]any{"quota_limit": 20.0},
				Concurrency: 5, Priority: 3, SkipDefaultGroupBind: true,
			}
			account, err := svc.CreateAccount(context.Background(), input)
			require.NoError(t, err)
			require.Equal(t, "secret", account.Credentials["api_key"])
			require.Equal(t, 5, account.Concurrency)
			require.Equal(t, 3, account.Priority)
			require.Equal(t, 20.0, account.Extra["quota_limit"])
			require.NotContains(t, account.Credentials, "account_driver_id")
			require.NotContains(t, account.Extra, "driver_settings")
			require.Equal(t, "native", AccountDriverFromAccount(account).ID)
			require.Equal(t, int64(1), AccountDriverFromAccount(account).BindingRevision)
			updated, err := svc.UpdateAccount(context.Background(), account.ID, &UpdateAccountInput{AccountDriverID: id, Name: "edited"})
			require.NoError(t, err)
			require.Equal(t, "edited", updated.Name)
			require.Equal(t, "secret", updated.Credentials["api_key"])
		})
	}
}

func enableCodexHTTPDriverForTest(t *testing.T) {
	t.Helper()
	require.NoError(t, SetAccountDriverRuntimeCapabilities(AccountDriverCodexHTTP, true, "test-runtime", []string{"http", "sse"}))
	t.Cleanup(func() {
		require.NoError(t, SetAccountDriverRuntimeCapabilities(AccountDriverCodexHTTP, false, "unconfigured", []string{"http", "sse"}))
	})
}

func TestAccountDriverEnhancedBindingSurvivesRuntimeFailureAndRejectsSwitch(t *testing.T) {
	enableCodexHTTPDriverForTest(t)
	repo := newDuplicateAccountRepoStub()
	svc := &adminServiceImpl{accountRepo: repo}
	created, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Name: "enhanced", Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		AccountDriverID: AccountDriverCodexHTTP, SkipDefaultGroupBind: true,
		Credentials: map[string]any{"access_token": "host-access", "refresh_token": "host-refresh"},
	})
	require.NoError(t, err)
	require.Equal(t, AccountDriverCodexHTTP, created.AccountDriverID)
	require.Equal(t, int64(1), created.AccountDriverRevision)
	require.NoError(t, SetAccountDriverRuntimeAvailability(AccountDriverCodexHTTP, false, "stopped-runtime"))
	view := AccountDriverFromAccount(created)
	require.Equal(t, AccountDriverCodexHTTP, view.ID)
	require.False(t, view.Available)
	require.Equal(t, "native", view.LifecycleDriverID)
	require.Equal(t, []string{"http", "sse"}, view.TransportProtocols)
	require.Len(t, BuildAccountDriverCatalog().Drivers, 1)
	require.NoError(t, ValidateAccountDriverFilter(AccountDriverCodexHTTP))
	require.Equal(t, "ACCOUNT_DRIVER_UNAVAILABLE", infraerrors.Reason(ValidateAccountDriverSelection(AccountDriverCodexHTTP, nil)))
	for _, id := range []string{"", AccountDriverCodexHTTP} {
		updated, err := svc.UpdateAccount(context.Background(), created.ID, &UpdateAccountInput{Name: "edited", AccountDriverID: id})
		require.NoError(t, err)
		require.Equal(t, AccountDriverCodexHTTP, updated.AccountDriverID)
		require.Equal(t, "host-refresh", updated.GetCredential("refresh_token"))
	}
	_, err = svc.UpdateAccount(context.Background(), created.ID, &UpdateAccountInput{Name: "must-not-write", AccountDriverID: "native"})
	require.Equal(t, "ACCOUNT_DRIVER_SWITCH_UNSUPPORTED", infraerrors.Reason(err))
	stored, err := repo.GetByID(context.Background(), created.ID)
	require.NoError(t, err)
	require.Equal(t, "edited", stored.Name)
}

func TestAccountDriverEnhancedRestrictsPlatformAuthModeAndShadow(t *testing.T) {
	enableCodexHTTPDriverForTest(t)
	svc := &adminServiceImpl{}
	for _, tc := range []struct {
		platform, accountType string
		credentials           map[string]any
		reason                string
	}{
		{PlatformAnthropic, AccountTypeOAuth, nil, "ACCOUNT_DRIVER_ACCOUNT_UNSUPPORTED"},
		{PlatformOpenAI, AccountTypeAPIKey, nil, "ACCOUNT_DRIVER_ACCOUNT_UNSUPPORTED"},
		{PlatformOpenAI, AccountTypeOAuth, map[string]any{"auth_mode": OpenAIAuthModePersonalAccessToken}, "ACCOUNT_DRIVER_AUTH_MODE_UNSUPPORTED"},
		{PlatformOpenAI, AccountTypeOAuth, map[string]any{"auth_mode": OpenAIAuthModeAgentIdentity}, "ACCOUNT_DRIVER_AUTH_MODE_UNSUPPORTED"},
	} {
		_, err := svc.CreateAccount(context.Background(), &CreateAccountInput{AccountDriverID: AccountDriverCodexHTTP, Platform: tc.platform, Type: tc.accountType, Credentials: tc.credentials})
		require.Equal(t, tc.reason, infraerrors.Reason(err))
	}
	repo := newDuplicateAccountRepoStub()
	parent := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, AccountDriverID: AccountDriverCodexHTTP, Credentials: map[string]any{"access_token": "token"}}
	require.NoError(t, repo.Create(context.Background(), parent))
	svc.accountRepo = repo
	_, err := svc.CreateShadow(context.Background(), parent.ID, ShadowOptions{})
	require.Equal(t, "ACCOUNT_DRIVER_AUTH_MODE_UNSUPPORTED", infraerrors.Reason(err))
	_, err = svc.DuplicateAccount(context.Background(), parent.ID, "admin:1", "")
	require.Equal(t, "ACCOUNT_DUPLICATE_CREDENTIAL_TYPE_UNSUPPORTED", infraerrors.Reason(err), "rotating OAuth credentials cannot be duplicated")
}

func TestAccountDriverUnknownSavedBindingIsNotRewritten(t *testing.T) {
	account := &Account{AccountDriverID: "restored-missing-driver", AccountDriverRevision: 9}
	view := AccountDriverFromAccount(account)
	require.Equal(t, account.AccountDriverID, view.ID)
	require.Equal(t, int64(9), view.BindingRevision)
	require.False(t, view.Available)
	require.Nil(t, AccountDriverFromAccount(nil))
}

func TestAccountDriverAccountServiceAndCredentialPersistenceRejectBeforeMutation(t *testing.T) {
	repo := newDuplicateAccountRepoStub()
	account := &Account{Name: "unchanged", Platform: PlatformOpenAI, Type: AccountTypeOAuth, AccountDriverID: AccountDriverCodexHTTP,
		Credentials: map[string]any{"access_token": "original"}, Extra: map[string]any{"existing": true},
	}
	require.NoError(t, repo.Create(context.Background(), account))
	name := "must-not-write"
	for _, credentials := range []map[string]any{{"auth_mode": "agent_identity"}, {"auth_mode": true}, {"openai_auth_mode": 12}} {
		_, err := NewAccountService(repo, nil).Update(context.Background(), account.ID, UpdateAccountRequest{Name: &name, Credentials: &credentials})
		require.Equal(t, "ACCOUNT_DRIVER_AUTH_MODE_UNSUPPORTED", infraerrors.Reason(err))
		require.Equal(t, "unchanged", account.Name)
		require.Equal(t, map[string]any{"access_token": "original"}, account.Credentials)
		err = persistAccountCredentials(context.Background(), repo, account, credentials)
		require.Equal(t, "ACCOUNT_DRIVER_AUTH_MODE_UNSUPPORTED", infraerrors.Reason(err))
		require.Equal(t, map[string]any{"access_token": "original"}, account.Credentials)
	}
	for _, mode := range []any{nil, "", "oauth", " OAUTH "} {
		candidate := *account
		candidate.Credentials = map[string]any{"auth_mode": mode}
		candidate.Extra = map[string]any{"openai_auth_mode": mode}
		require.NoError(t, ValidateAccountDriverState(&candidate))
	}
	// Native rules continue to accept arbitrary provider-specific auth metadata.
	native := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"auth_mode": true}, Extra: map[string]any{"openai_auth_mode": "agent_identity"}}
	require.NoError(t, ValidateAccountDriverState(native))
}

func TestAccountDriverCRSSyncCannotChangeBindingPlatformTypeOrAuthMode(t *testing.T) {
	for _, source := range []crsOpenAILongContextSource{
		{collection: "claudeAccounts", credentials: map[string]any{"access_token": "replacement"}},
		{collection: "claudeConsoleAccounts", credentials: map[string]any{"api_key": "replacement"}},
		{collection: "openaiResponsesAccounts", credentials: map[string]any{"api_key": "replacement"}},
		{collection: "geminiOAuthAccounts", credentials: map[string]any{"access_token": "replacement", "refresh_token": "replacement-refresh"}},
		{collection: "geminiApiKeyAccounts", credentials: map[string]any{"api_key": "replacement"}},
		{collection: "openaiOAuthAccounts", credentials: map[string]any{"access_token": "replacement", "auth_mode": "agent_identity"}},
		{collection: "openaiOAuthAccounts", credentials: map[string]any{"access_token": "replacement"}, extra: map[string]any{"auth_mode": true}},
	} {
		t.Run(source.collection+"/"+infraerrors.Reason(validateAccountDriverAuthModes(source.credentials)), func(t *testing.T) {
			existing := &Account{ID: 41, Name: "unchanged", Platform: PlatformOpenAI, Type: AccountTypeOAuth, AccountDriverID: AccountDriverCodexHTTP,
				Credentials: map[string]any{"access_token": "original"}, Extra: map[string]any{"crs_account_id": "crs-openai-1"},
			}
			repo := newCRSLongContextAccountRepo(existing)
			result := runCRSOpenAILongContextSync(t, repo, source)
			require.Equal(t, 1, result.Failed)
			require.Zero(t, result.Updated)
			require.Contains(t, result.Items[0].Error, "ACCOUNT_DRIVER_")
			require.Equal(t, "unchanged", existing.Name)
			require.Equal(t, PlatformOpenAI, existing.Platform)
			require.Equal(t, AccountTypeOAuth, existing.Type)
			require.Equal(t, map[string]any{"access_token": "original"}, existing.Credentials)
			require.Equal(t, map[string]any{"crs_account_id": "crs-openai-1"}, existing.Extra)
		})
	}
}
