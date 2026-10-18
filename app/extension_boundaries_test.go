package app

import (
	"context"
	"testing"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/permission"
)

type configuredPermissionsModule struct{ config string }

func (configuredPermissionsModule) Code() kernel.ModuleCode { return "custom" }
func (configuredPermissionsModule) Build(context.Context, kernel.ModuleContext) (kernel.ModuleRuntime, error) {
	return nil, nil
}
func (m configuredPermissionsModule) Registry() (kernel.ModuleRegistry, error) {
	return kernel.ModuleRegistry{PermissionEntities: []permission.Entity{{Code: m.config}}}, nil
}
func TestPermissionCatalogUsesConfiguredRegistryAcrossProfiles(t *testing.T) {
	profiles := []kernel.Profile{}
	for _, code := range []string{"first", "second", "first"} {
		profiles = append(profiles, kernel.Profile{Code: kernel.ProfileCode(code), Modules: []kernel.Module{configuredPermissionsModule{config: code}}})
	}
	snapshots := make([]*kernel.ProfileDefinitions, 0, len(profiles))
	for _, profile := range profiles {
		snapshot, err := kernel.CompileProfileDefinitions(context.Background(), profile)
		if err != nil {
			t.Fatal(err)
		}
		snapshots = append(snapshots, snapshot)
	}
	catalog, err := buildPermissionCatalog(snapshots)
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []permission.Code{"custom.first.read", "custom.second.read"} {
		if err := catalog.Require(code); err != nil {
			t.Fatal(err)
		}
	}
	if catalog.Has("custom.obsolete.read") || len(catalog.Codes()) != 8 {
		t.Fatalf("unexpected permissions: %v", catalog.Codes())
	}
}

func (configuredPermissionsModule) Validate(context.Context, kernel.ModuleValidationContext) error {
	return nil
}
