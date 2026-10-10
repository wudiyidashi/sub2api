package service

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/Wei-Shaw/sub2api/internal/accountdriver"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

var (
	accountDriverRegistryMu sync.RWMutex
	accountDriverRegistry   = newAccountDriverRegistry(false, "unconfigured")
	accountDriverWSKnown    bool
	accountDriverGeneration atomic.Uint64
)

const AccountDriverCodexHTTP = accountdriver.CodexHTTPID

func newAccountDriverRegistry(available bool, version string) *accountdriver.Registry {
	native := accountdriver.NativeRegistry().Catalog().Drivers[0]
	if guard := accountDriverMaintenanceGuard.Load(); guard != nil && guard.repo != nil {
		native.Capabilities.Switch = true
	}
	protocols := []string{"http", "sse"}
	if accountDriverWSKnown {
		protocols = append(protocols, "websocket")
	}
	registry, err := accountdriver.NewRegistry(accountdriver.NativeID, native, accountdriver.Descriptor{
		ID: AccountDriverCodexHTTP, Name: "Codex 增强", Version: version, Available: available,
		Platforms: []string{PlatformOpenAI}, AccountTypes: []string{AccountTypeOAuth},
		Capabilities: native.Capabilities, LifecycleDriverID: accountdriver.NativeID,
		ExecutionDriverID: AccountDriverCodexHTTP, TransportProtocols: protocols,
		ExecutionEndpoints: []string{"responses"}, NativeDelegatedEndpoints: []string{"images", "search", "compact"},
	})
	if err != nil {
		panic(err)
	}
	return registry
}

func accountDriverRegistrySnapshot() *accountdriver.Registry {
	accountDriverRegistryMu.RLock()
	defer accountDriverRegistryMu.RUnlock()
	return accountDriverRegistry
}

// SetAccountDriverRuntimeAvailability is called by the managed runtime on
// startup, health failure, or exit; account identities never move on failure.
func SetAccountDriverRuntimeAvailability(id string, available bool, version string) error {
	return SetAccountDriverRuntimeCapabilities(id, available, version, nil)
}

// A non-nil transport list is a new runtime's negotiated capabilities. Nil
// preserves the last known capabilities while marking an instance unavailable.
func SetAccountDriverRuntimeCapabilities(id string, available bool, version string, transports []string) error {
	if id != AccountDriverCodexHTTP {
		return infraerrors.BadRequest("ACCOUNT_DRIVER_UNKNOWN", "unknown managed account driver")
	}
	if strings.TrimSpace(version) == "" {
		version = "unconfigured"
	}
	accountDriverRegistryMu.Lock()
	defer accountDriverRegistryMu.Unlock()
	if transports != nil {
		accountDriverWSKnown = slices.Contains(transports, "websocket")
	}
	accountDriverGeneration.Add(1)
	accountDriverRegistry = newAccountDriverRegistry(available, version)
	return nil
}

func AccountDriverRuntimeGeneration() uint64 { return accountDriverGeneration.Load() }

func validateAccountDriverWSConfigChange(existing *Account, extra map[string]any) error {
	if !usesCodexHTTPDriver(existing) || extra == nil || accountDriverWebSocketAvailable(existing) {
		return nil
	}
	keys := []string{"openai_oauth_responses_websockets_v2_mode", "openai_oauth_responses_websockets_v2_enabled", "responses_websockets_v2_enabled", "openai_ws_enabled"}
	for _, key := range keys {
		value, exists := extra[key]
		if !exists || reflect.DeepEqual(value, existing.Extra[key]) {
			continue
		}
		if value == nil || value == false || value == "off" || value == "http_bridge" {
			continue
		}
		return infraerrors.BadRequest("ACCOUNT_DRIVER_WEBSOCKET_UNAVAILABLE", "this account driver runtime does not have a ready websocket capability")
	}
	return nil
}

func accountDriverWebSocketAvailable(account *Account) bool {
	if !usesCodexHTTPDriver(account) {
		return false
	}
	driver, err := accountDriverRegistrySnapshot().Lookup(account.EffectiveAccountDriverID())
	return err == nil && driver.Available && slices.Contains(driver.TransportProtocols, "websocket")
}

func BuildAccountDriverCatalog() accountdriver.Catalog {
	return accountDriverRegistrySnapshot().Catalog()
}

// ValidateAccountDriverSelection covers new account choices and imports.
// Managed implementations must be ready for new bindings. Settings have no
// validated schema yet and must not be persisted in credentials or extra.
func ValidateAccountDriverSelection(id string, settings map[string]any) error {
	if _, err := accountDriverRegistrySnapshot().Resolve(id); err != nil {
		return err
	}
	if len(settings) > 0 {
		return infraerrors.BadRequest("ACCOUNT_DRIVER_SETTINGS_UNSUPPORTED", "this account driver does not support driver settings")
	}
	return nil
}

func ValidateAccountDriverFilter(id string) error {
	_, err := accountDriverRegistrySnapshot().Lookup(id)
	return err
}

func ValidateAccountDriverForAccount(id, platform, accountType string, credentials map[string]any) error {
	if err := ValidateAccountDriverSelection(id, nil); err != nil {
		return err
	}
	return validateAccountDriverCompatibility(id, platform, accountType, credentials)
}

func validateAccountDriverCompatibility(id, platform, accountType string, credentials map[string]any) error {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(id) == accountdriver.NativeID {
		return nil
	}
	driver, err := accountDriverRegistrySnapshot().Lookup(id)
	if err != nil {
		return err
	}
	if !slices.Contains(driver.Platforms, platform) || !slices.Contains(driver.AccountTypes, accountType) {
		return infraerrors.BadRequest("ACCOUNT_DRIVER_ACCOUNT_UNSUPPORTED", "account driver does not support this platform and account type")
	}
	return validateAccountDriverAuthModes(credentials)
}

func validateAccountDriverAuthModes(values map[string]any) error {
	for _, key := range []string{"auth_mode", "openai_auth_mode"} {
		value, supplied := values[key]
		if !supplied || value == nil {
			continue
		}
		mode, ok := value.(string)
		// Match PostgreSQL btrim's default ASCII-space behavior exactly; accepting
		// other whitespace here would defer an otherwise valid-looking edit to a
		// database constraint error instead of returning this stable API reason.
		mode = strings.Trim(mode, " ")
		if !ok || (mode != "" && !strings.EqualFold(mode, AccountTypeOAuth)) {
			return infraerrors.BadRequest("ACCOUNT_DRIVER_AUTH_MODE_UNSUPPORTED", "account driver currently supports standard OpenAI OAuth credentials only")
		}
	}
	return nil
}

// ValidateAccountDriverState enforces an existing binding's compatibility
// without requiring execution readiness. Native account behavior is unchanged.
func ValidateAccountDriverState(account *Account) error {
	if account == nil || account.EffectiveAccountDriverID() == accountdriver.NativeID {
		return nil
	}
	if err := validateAccountDriverCompatibility(account.EffectiveAccountDriverID(), account.Platform, account.Type, account.Credentials); err != nil {
		return err
	}
	if account.ParentAccountID != nil {
		return infraerrors.BadRequest("ACCOUNT_DRIVER_AUTH_MODE_UNSUPPORTED", "this account driver does not support credential shadows")
	}
	// Extra is not an authentication authority. Refuse incompatible auth-mode
	// pseudo-settings rather than accepting a configuration the preset cannot use.
	return validateAccountDriverAuthModes(account.Extra)
}

func ValidateAccountDriverUpdate(account *Account, id string, settings map[string]any) error {
	if len(settings) > 0 {
		return infraerrors.BadRequest("ACCOUNT_DRIVER_SETTINGS_UNSUPPORTED", "this account driver does not support driver settings")
	}
	if id == "" {
		return nil
	}
	if _, err := accountDriverRegistrySnapshot().Lookup(id); err != nil {
		return err
	}
	if strings.TrimSpace(id) != account.EffectiveAccountDriverID() {
		return infraerrors.BadRequest("ACCOUNT_DRIVER_SWITCH_UNSUPPORTED", "changing an account driver requires a maintenance operation and is not supported yet")
	}
	return nil
}

// AccountDriverFromAccount preserves saved identities even when their runtime
// is stopped or their implementation is absent from this host.
func AccountDriverFromAccount(account *Account) *accountdriver.AccountView {
	if account == nil {
		return nil
	}
	id := account.EffectiveAccountDriverID()
	driver, err := accountDriverRegistrySnapshot().Lookup(id)
	if err != nil {
		return &accountdriver.AccountView{ID: id, Name: id, Version: "unknown", Available: false, BindingRevision: account.AccountDriverRevision}
	}
	capabilities := driver.Capabilities
	// Driver-level switch support is only meaningful for the compatible
	// OpenAI OAuth account shape. Keep metadata for Anthropic/API-key/shadow
	// accounts explicit so the panel cannot offer an invalid switch action.
	capabilities.Switch = false
	if account.Platform == PlatformOpenAI && account.Type == AccountTypeOAuth && account.ParentAccountID == nil {
		if guard := accountDriverMaintenanceGuard.Load(); guard != nil && guard.repo != nil {
			capabilities.Switch = true
		}
	}
	return &accountdriver.AccountView{
		ID: driver.ID, Name: driver.Name, Version: driver.Version, Available: driver.Available,
		BindingRevision: account.AccountDriverRevision, Capabilities: capabilities,
		LifecycleDriverID: driver.LifecycleDriverID, ExecutionDriverID: driver.ExecutionDriverID,
		TransportProtocols: driver.TransportProtocols,
		ExecutionEndpoints: driver.ExecutionEndpoints, NativeDelegatedEndpoints: driver.NativeDelegatedEndpoints,
	}
}

// Additive listing interfaces preserve gateway-facing contracts while applying
// account-driver conditions in the same SQL query as count and pagination.
type AccountDriverListingService interface {
	ListAccountsWithDriver(ctx context.Context, page, pageSize int, platform, accountType, status, search string, groupID int64, privacyMode, sortBy, sortOrder, driverID string) ([]Account, int64, error)
	ListAccountsForSchedulerScoreFilterWithDriver(ctx context.Context, platform, accountType, status, search string, groupID int64, privacyMode, driverID string) ([]Account, error)
}

type AccountDriverFilterRepository interface {
	ListWithDriverFilters(ctx context.Context, params pagination.PaginationParams, platform, accountType, status, search string, groupID int64, privacyMode, driverID string) ([]Account, *pagination.PaginationResult, error)
	ListAllWithDriverFilters(ctx context.Context, platform, accountType, status, search string, groupID int64, privacyMode, driverID string) ([]Account, error)
}

func (s *adminServiceImpl) ListAccountsWithDriver(ctx context.Context, page, pageSize int, platform, accountType, status, search string, groupID int64, privacyMode, sortBy, sortOrder, driverID string) ([]Account, int64, error) {
	if err := ValidateAccountDriverFilter(driverID); err != nil {
		return nil, 0, err
	}
	if strings.TrimSpace(driverID) == "" {
		return s.ListAccounts(ctx, page, pageSize, platform, accountType, status, search, groupID, privacyMode, sortBy, sortOrder)
	}
	if groupID > 0 {
		if err := s.ValidateAccountGroupBindings(ctx, []int64{groupID}); err != nil {
			return nil, 0, err
		}
	}
	repo, ok := s.accountRepo.(AccountDriverFilterRepository)
	if !ok {
		return nil, 0, infraerrors.BadRequest("ACCOUNT_DRIVER_FILTER_UNSUPPORTED", "account repository does not support account driver filtering")
	}
	accounts, result, err := repo.ListWithDriverFilters(ctx, pagination.PaginationParams{Page: page, PageSize: pageSize, SortBy: sortBy, SortOrder: sortOrder}, platform, accountType, status, search, groupID, privacyMode, strings.TrimSpace(driverID))
	if err != nil {
		return nil, 0, err
	}
	return accounts, result.Total, nil
}

func (s *adminServiceImpl) ListAccountsForSchedulerScoreFilterWithDriver(ctx context.Context, platform, accountType, status, search string, groupID int64, privacyMode, driverID string) ([]Account, error) {
	if strings.TrimSpace(driverID) == "" {
		return s.ListAccountsForSchedulerScoreFilter(ctx, platform, accountType, status, search, groupID, privacyMode)
	}
	if err := ValidateAccountDriverFilter(driverID); err != nil {
		return nil, err
	}
	repo, ok := s.accountRepo.(AccountDriverFilterRepository)
	if !ok {
		return nil, infraerrors.BadRequest("ACCOUNT_DRIVER_FILTER_UNSUPPORTED", "account repository does not support account driver filtering")
	}
	return repo.ListAllWithDriverFilters(ctx, platform, accountType, status, search, groupID, privacyMode, strings.TrimSpace(driverID))
}
