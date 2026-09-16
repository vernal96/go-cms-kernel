package admin_test

import (
	"context"
	"testing"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/modules/admin"
	"github.com/vernal96/go-cms-kernel/permission"
)

func TestModuleBuildsAdminRuntime(t *testing.T) {
	module := admin.Module{}
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
	registry := admin.Module{}.Registry()
	definitions, err := permission.Definitions(
		string(admin.ModuleCode),
		registry.PermissionEntities,
	)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, definition := range definitions {
		if definition.Code == admin.AccessPermission {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf(
			"permission %q is not registered in %#v",
			admin.AccessPermission,
			definitions,
		)
	}
}
