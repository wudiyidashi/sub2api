// Package accountdriver describes the account management implementations that
// have passed host integration checks. Managed implementations are selectable
// only while their host runtime reports readiness.
package accountdriver

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	NativeID     = "native"
	CodexHTTPID  = "codex-rs-http"
	CPRManagedID = "cpr-managed"
)

// Capabilities describe driver operations, not permission to use every operation
// with every platform or account type. Existing account validation still applies.
type Capabilities struct {
	Authorization bool `json:"authorization"`
	Import        bool `json:"import"`
	Refresh       bool `json:"refresh"`
	Quota         bool `json:"quota"`
	Test          bool `json:"test"`
	Models        bool `json:"models"`
	Settings      bool `json:"settings"`
	Switch        bool `json:"switch"`
}

type Descriptor struct {
	ID                       string       `json:"id"`
	Name                     string       `json:"name"`
	Version                  string       `json:"version"`
	Available                bool         `json:"available"`
	Platforms                []string     `json:"platforms"`
	AccountTypes             []string     `json:"account_types"`
	Capabilities             Capabilities `json:"capabilities"`
	LifecycleDriverID        string       `json:"lifecycle_driver_id"`
	ExecutionDriverID        string       `json:"execution_driver_id"`
	TransportProtocols       []string     `json:"transport_protocols,omitempty"`
	ExecutionEndpoints       []string     `json:"execution_endpoints,omitempty"`
	NativeDelegatedEndpoints []string     `json:"native_delegated_endpoints,omitempty"`
}

type Catalog struct {
	Drivers         []Descriptor `json:"drivers"`
	DefaultDriverID string       `json:"default_driver_id"`
}

// AccountView is resolved management metadata. Persisted bindings start at
// revision one. Zero remains compatible with legacy cached account snapshots.
type AccountView struct {
	ID                       string       `json:"id"`
	Name                     string       `json:"name"`
	Version                  string       `json:"version"`
	Available                bool         `json:"available"`
	BindingRevision          int64        `json:"binding_revision"`
	Capabilities             Capabilities `json:"capabilities"`
	LifecycleDriverID        string       `json:"lifecycle_driver_id"`
	ExecutionDriverID        string       `json:"execution_driver_id"`
	TransportProtocols       []string     `json:"transport_protocols,omitempty"`
	ExecutionEndpoints       []string     `json:"execution_endpoints,omitempty"`
	NativeDelegatedEndpoints []string     `json:"native_delegated_endpoints,omitempty"`
}

// Registry is immutable after construction. It keeps unready descriptors out of
// the selectable catalog and never falls back for an explicit unknown driver.
type Registry struct {
	defaultDriverID string
	drivers         []Descriptor
}

func NewRegistry(defaultDriverID string, drivers ...Descriptor) (*Registry, error) {
	registry := &Registry{defaultDriverID: defaultDriverID, drivers: make([]Descriptor, 0, len(drivers))}
	seen := make(map[string]bool, len(drivers))
	for _, driver := range drivers {
		if driver.ID == "" || strings.TrimSpace(driver.ID) != driver.ID || seen[driver.ID] {
			return nil, fmt.Errorf("invalid or duplicate account driver ID %q", driver.ID)
		}
		seen[driver.ID] = true
		registry.drivers = append(registry.drivers, cloneDescriptor(driver))
	}
	if _, err := registry.Resolve(defaultDriverID); err != nil {
		return nil, fmt.Errorf("invalid default account driver: %w", err)
	}
	return registry, nil
}

func (r *Registry) Resolve(id string) (Descriptor, error) {
	driver, err := r.Lookup(id)
	if err != nil {
		return Descriptor{}, err
	}
	if !driver.Available {
		return Descriptor{}, infraerrors.BadRequest("ACCOUNT_DRIVER_UNAVAILABLE", "account driver is not ready for use")
	}
	return driver, nil
}

// Lookup retains unavailable binding metadata without authorizing creation.
func (r *Registry) Lookup(id string) (Descriptor, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		id = r.defaultDriverID
	}
	for _, driver := range r.drivers {
		if driver.ID != id {
			continue
		}
		return cloneDescriptor(driver), nil
	}
	return Descriptor{}, infraerrors.BadRequest("ACCOUNT_DRIVER_UNKNOWN", "unknown account driver")
}

func (r *Registry) Catalog() Catalog {
	catalog := Catalog{Drivers: make([]Descriptor, 0, len(r.drivers)), DefaultDriverID: r.defaultDriverID}
	for _, driver := range r.drivers {
		if driver.Available {
			catalog.Drivers = append(catalog.Drivers, cloneDescriptor(driver))
		}
	}
	return catalog
}

func cloneDescriptor(driver Descriptor) Descriptor {
	driver.Platforms = slices.Clone(driver.Platforms)
	driver.AccountTypes = slices.Clone(driver.AccountTypes)
	driver.TransportProtocols = slices.Clone(driver.TransportProtocols)
	driver.ExecutionEndpoints = slices.Clone(driver.ExecutionEndpoints)
	driver.NativeDelegatedEndpoints = slices.Clone(driver.NativeDelegatedEndpoints)
	return driver
}

// NativeRegistry follows the host's platform catalog, so upstream platforms are
// inherited without maintaining a second hard-coded list.
func NativeRegistry() *Registry {
	registry, err := NewRegistry(NativeID, Descriptor{
		ID: NativeID, Name: "原生管理", Version: "builtin", Available: true,
		LifecycleDriverID: NativeID, ExecutionDriverID: NativeID,
		Platforms: domain.ConcretePlatformIDs(),
		AccountTypes: []string{domain.AccountTypeOAuth, domain.AccountTypeSetupToken,
			domain.AccountTypeAPIKey, domain.AccountTypeUpstream, domain.AccountTypeBedrock, domain.AccountTypeServiceAccount},
		Capabilities: Capabilities{Authorization: true, Import: true, Refresh: true, Quota: true, Test: true, Models: true},
	})
	if err != nil {
		panic(err) // Built-in metadata is a programming invariant.
	}
	return registry
}
