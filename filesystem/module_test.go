package filesystem

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestModuleManagerExposesOnlyBoundAliases(t *testing.T) {
	public := &testDisk{code: "public", visibility: VisibilityPublic}
	private := &testDisk{code: "private", visibility: VisibilityPrivate}
	manager, err := NewManager(context.Background(), []Factory{
		testFactory{code: "public", disk: public},
		testFactory{code: "private", disk: private},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })

	scoped, err := NewModuleManager(manager, []Binding{{
		Alias: "assets",
		Code:  "public",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if disk, exists := scoped.Disk("assets"); !exists || disk != public {
		t.Fatalf("assets disk = %#v, %t", disk, exists)
	}
	if _, exists := scoped.Disk("private"); exists {
		t.Fatal("module resolved an unbound disk")
	}
	if binding, exists := scoped.Binding("assets"); !exists ||
		binding.Code != "public" {
		t.Fatalf("assets binding = %#v, %t", binding, exists)
	}
}

func TestModuleManagerRejectsInvalidBindings(t *testing.T) {
	resolver := resolverMap{
		"public": &testDisk{
			code:       "public",
			visibility: VisibilityPublic,
		},
	}
	testCases := []struct {
		name     string
		resolver Resolver
		bindings []Binding
		match    string
	}{
		{
			name:     "empty alias",
			resolver: resolver,
			bindings: []Binding{{Code: "public"}},
			match:    "empty alias",
		},
		{
			name:     "duplicate alias",
			resolver: resolver,
			bindings: []Binding{
				{Alias: "assets", Code: "public"},
				{Alias: "assets", Code: "public"},
			},
			match: "more than once",
		},
		{
			name:     "missing disk",
			resolver: resolver,
			bindings: []Binding{{Alias: "assets", Code: "missing"}},
			match:    ErrDiskNotFound.Error(),
		},
	}

	for _, test := range testCases {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewModuleManager(test.resolver, test.bindings)
			if err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("error = %v", err)
			}
		})
	}

	_, err := NewModuleManager(nil, []Binding{{
		Alias: "assets",
		Code:  "public",
	}})
	if !errors.Is(err, ErrDiskNotFound) {
		t.Fatalf("nil resolver error = %v", err)
	}
}

type resolverMap map[Code]Disk

func (r resolverMap) Disk(code Code) (Disk, bool) {
	disk, exists := r[code]
	return disk, exists
}
