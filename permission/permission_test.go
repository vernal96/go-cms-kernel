package permission

import (
	"errors"
	"reflect"
	"testing"
)

func TestDefinitionsAndCatalog(t *testing.T) {
	t.Parallel()

	definitions, err := Definitions("core", []Entity{
		{Code: "site"},
		{Code: "resource"},
	})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewCatalog(definitions)
	if err != nil {
		t.Fatal(err)
	}
	want := []Code{
		"core.resource.create",
		"core.resource.delete",
		"core.resource.read",
		"core.resource.update",
		"core.site.create",
		"core.site.delete",
		"core.site.read",
		"core.site.update",
	}
	if got := catalog.Codes(); !reflect.DeepEqual(got, want) {
		t.Fatalf("codes = %#v, want %#v", got, want)
	}
	if err := catalog.Require("core.site.read"); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Require("core.site.publish"); !errors.Is(
		err,
		ErrUnknown,
	) {
		t.Fatalf("unknown permission error = %v", err)
	}
}

func TestCatalogRejectsDuplicatesAndInvalidCodes(t *testing.T) {
	t.Parallel()

	definition := Definition{
		Code:   MustCode("core", "site", Read),
		Module: "core",
		Entity: "site",
		Action: Read,
	}
	if _, err := NewCatalog([]Definition{
		definition,
		definition,
	}); err == nil {
		t.Fatal("expected duplicate permission error")
	}
	invalid := []string{
		"",
		"core.site",
		"Core.site.read",
		"core.site.publish",
		"core..read",
	}
	for _, raw := range invalid {
		raw := raw
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			if _, err := Parse(Code(raw)); err == nil {
				t.Fatalf("Parse(%q) succeeded", raw)
			}
		})
	}
}

func TestDefinitionsAllowRestrictedEntityActions(t *testing.T) {
	definitions, err := Definitions("core", []Entity{{Code: "resource_history", Actions: []Action{Read, Delete}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 2 || definitions[0].Code != "core.resource_history.read" || definitions[1].Code != "core.resource_history.delete" {
		t.Fatalf("definitions = %#v", definitions)
	}
}
