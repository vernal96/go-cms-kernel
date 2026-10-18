package admin_test

import (
	"context"
	"testing"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/modules/admin"
)

func TestModuleBuildsAdminRuntime(t *testing.T) {
	module := admin.New()
	if module.Code() != admin.ModuleCode {
		t.Fatalf("module code = %q", module.Code())
	}

	if _, err := module.Build(
		context.Background(),
		kernel.ModuleContext{},
	); err == nil {
		t.Fatal("build admin module without services succeeded")
	}
}

func TestModuleRegistersAdminPanelPermission(t *testing.T) {
	definitions, err := admin.New().(kernel.DefinitionProvider).NewDefinitions(context.Background(), kernel.DefinitionContext{})
	if err != nil {
		t.Fatal(err)
	}
	catalog := definitions.(kernel.DefinitionCatalogProvider).DefinitionCatalog()
	definition, found := catalog.Permission(admin.AccessPermission)
	if !found || definition.Code != admin.AccessPermission || len(catalog.Permissions()) != 4 {
		t.Fatalf(
			"permission %q is not registered in %#v",
			admin.AccessPermission,
			catalog.Permissions(),
		)
	}
}
