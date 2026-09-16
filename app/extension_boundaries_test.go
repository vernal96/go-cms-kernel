package app

import (
	"context"
	"testing"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/permission"
)

type configuredPermissionsModule struct{}

func (configuredPermissionsModule) Code() kernel.ModuleCode { return "custom" }
func (configuredPermissionsModule) Build(context.Context, kernel.ModuleContext) (kernel.ModuleRuntime, error) {
	return nil, nil
}
func (configuredPermissionsModule) Registry() kernel.ModuleRegistry {
	return kernel.ModuleRegistry{PermissionEntities: []permission.Entity{{Code: "obsolete"}}}
}
func (configuredPermissionsModule) RegistryForConfig(config any) (kernel.ModuleRegistry, error) {
	return kernel.ModuleRegistry{PermissionEntities: []permission.Entity{{Code: config.(string)}}}, nil
}
func TestPermissionCatalogUsesConfiguredRegistryAcrossProfiles(t *testing.T) {
	profiles := []kernel.Profile{}
	for _, code := range []string{"first", "second", "first"} {
		profiles = append(profiles, kernel.Profile{Code: kernel.ProfileCode(code), Modules: []kernel.ProfileModule{{Module: configuredPermissionsModule{}, Config: code}}})
	}
	catalog, err := buildPermissionCatalog(profiles)
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
