package mail

import (
	"context"
	"testing"

	kernel "github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/permission"
)

func TestModuleDefinesMailPermissions(t *testing.T) {
	target, err := New(Config{}).(kernel.DefinitionProvider).NewDefinitions(context.Background(), kernel.DefinitionContext{})
	if err != nil {
		t.Fatal(err)
	}
	catalog := target.(kernel.DefinitionCatalogProvider).DefinitionCatalog()
	for _, code := range []permission.Code{
		"mail.template.read", "mail.template.create", "mail.template.update", "mail.template.delete",
		"mail.message.read", "mail.message.create", "mail.message.delete",
	} {
		if _, ok := catalog.Permission(code); !ok {
			t.Errorf("mail permission %q is missing", code)
		}
	}
	if _, ok := catalog.Permission("mail.message.update"); ok {
		t.Fatal("mail message update permission should remain unavailable")
	}
}
