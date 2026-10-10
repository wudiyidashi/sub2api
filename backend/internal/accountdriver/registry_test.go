package accountdriver

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestNativeRegistryDefaultsAndInheritsHostPlatforms(t *testing.T) {
	registry := NativeRegistry()
	for _, id := range []string{"", NativeID, " native "} {
		driver, err := registry.Resolve(id)
		require.NoError(t, err)
		require.Equal(t, NativeID, driver.ID)
		require.Equal(t, domain.ConcretePlatformIDs(), driver.Platforms)
		require.False(t, driver.Capabilities.Switch)
		require.False(t, driver.Capabilities.Settings)
	}
	_, err := registry.Resolve("codex-enhanced")
	require.Equal(t, "ACCOUNT_DRIVER_UNKNOWN", infraerrors.Reason(err))
}

func TestRegistryRejectsUnavailableWithoutFallback(t *testing.T) {
	registry, err := NewRegistry(NativeID,
		Descriptor{ID: NativeID, Available: true},
		Descriptor{ID: "unready", Available: false},
	)
	require.NoError(t, err)
	require.Len(t, registry.Catalog().Drivers, 1)
	_, err = registry.Resolve("unready")
	require.Equal(t, "ACCOUNT_DRIVER_UNAVAILABLE", infraerrors.Reason(err))
	_, err = NewRegistry("unready", Descriptor{ID: "unready", Available: false})
	require.Error(t, err)
}

func TestRegistryDoesNotExposeMutableMetadata(t *testing.T) {
	driver := Descriptor{ID: NativeID, Available: true, Platforms: []string{"openai"}, AccountTypes: []string{"oauth"}}
	registry, err := NewRegistry(NativeID, driver)
	require.NoError(t, err)
	driver.Platforms[0] = "changed-at-construction"
	catalog := registry.Catalog()
	catalog.Drivers[0].Platforms[0] = "changed-in-catalog"
	catalog.Drivers[0].AccountTypes[0] = "changed-in-catalog"
	resolved, err := registry.Resolve(NativeID)
	require.NoError(t, err)
	require.Equal(t, []string{"openai"}, resolved.Platforms)
	require.Equal(t, []string{"oauth"}, resolved.AccountTypes)
}
