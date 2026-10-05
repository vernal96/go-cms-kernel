package kernel_test

import (
	"context"
	"testing"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/cache"
	"github.com/vernal96/go-cms-kernel/examples/counter"
	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/modules/core"
	"github.com/vernal96/go-cms-kernel/modules/forms"
	"github.com/vernal96/go-cms-kernel/modules/mail"
)

func TestModuleBindingsAreImmutableSnapshots(t *testing.T) {
	caches := []cache.Binding{{Alias: core.HotCacheAlias, Code: "first"}}
	first := core.New(core.Config{Caches: caches})
	caches[0].Code = "second"
	second := core.New(core.Config{Caches: caches})
	for _, test := range []struct {
		module kernel.Module
		code   cache.Code
	}{{first, "first"}, {second, "second"}} {
		provider := test.module.(kernel.CacheBindingsProvider)
		bindings := provider.CacheBindings()
		if bindings[0].Code != test.code {
			t.Fatal("constructor shared cache configuration")
		}
		bindings[0].Code = "changed"
		if provider.CacheBindings()[0].Code != test.code {
			t.Fatal("getter exposed cache configuration")
		}
	}
	for _, constructor := range []func([]filesystem.Binding) kernel.Module{
		func(bindings []filesystem.Binding) kernel.Module { return mail.New(mail.Config{Filesystems: bindings}) },
		func(bindings []filesystem.Binding) kernel.Module {
			return forms.New(forms.Config{Filesystems: bindings})
		},
	} {
		bindings := []filesystem.Binding{{Alias: "spool", Code: "private"}}
		declaration := constructor(bindings)
		bindings[0].Code = "changed"
		provider := declaration.(kernel.FilesystemBindingsProvider)
		if provider.FilesystemBindings()[0].Code != "private" {
			t.Fatal("constructor shared disk configuration")
		}
		provider.FilesystemBindings()[0].Code = "changed again"
		if provider.FilesystemBindings()[0].Code != "private" {
			t.Fatal("getter exposed disk configuration")
		}
	}
}

func TestTypedModuleConfigurationIsIndependentAcrossProfilesAndSites(t *testing.T) {
	factory, err := kernel.NewProfileRuntimeFactory(emptyDatabaseResolver{}, testRuntimeServices())
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		code  kernel.ProfileCode
		limit int
	}{{"small", 3}, {"large", 100}} {
		blueprint, err := factory.Compile(context.Background(), kernel.Profile{Code: test.code, Modules: []kernel.Module{counter.New(test.limit)}})
		if err != nil {
			t.Fatal(err)
		}
		var previous kernel.ModuleRuntime
		for _, id := range []string{"1", "2"} {
			scope := kernel.NewRuntimeScope(id, "example.test", "en", nil)
			runtime, err := blueprint.Build(context.Background(), scope)
			if err != nil {
				t.Fatal(err)
			}
			declaration, ok := runtime.Registry().Module("example.counter")
			if !ok || declaration.(*counter.Runtime).Limit() != test.limit || declaration == previous {
				t.Fatal("profile configuration or site runtime leaked")
			}
			previous = declaration
		}
	}
}
