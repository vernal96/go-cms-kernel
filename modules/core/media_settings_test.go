package core

import (
	"context"
	"testing"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
)

func TestMediaSettingsDeclarationsAreSnapshots(t *testing.T) {
	options := &field.MediaOptions{Disk: "public", VirtualPath: "images", SettingsCode: "image"}
	defs := []field.Definition{{Key: "photo", Label: "Photo", Type: field.TypeMedia, Options: options}}
	cloned := field.CloneDefinitions(defs)
	options.SettingsCode = "changed"
	if cloned[0].Options.(*field.MediaOptions).SettingsCode != "image" {
		t.Fatal("mutable media options escaped")
	}
	config := Config{MediaSettings: []media.SettingsDefinition{{Code: "image", Fields: []field.Definition{{Key: "alt", Label: "Alt", Type: field.TypeString}}}}}
	snapshot := New(config).(module).config
	target, err := New(config).(kernel.DefinitionProvider).NewDefinitions(context.Background(), kernel.DefinitionContext{})
	if err != nil {
		t.Fatal(err)
	}
	registry := target.(kernel.DefinitionCatalogProvider).DefinitionCatalog()
	config.MediaSettings[0].Fields[0].Key = "changed"
	if snapshot.MediaSettings[0].Fields[0].Key != "alt" {
		t.Fatal("mutable settings config escaped")
	}
	descriptor, err := field.Describe(cloned[0], registry)
	if err != nil {
		t.Fatal(err)
	}
	if string(descriptor.Options) == "" {
		t.Fatal("missing settings metadata")
	}
	if _, err := New(Config{MediaSettings: []media.SettingsDefinition{{Code: "same"}, {Code: "same"}}}).(kernel.DefinitionProvider).NewDefinitions(context.Background(), kernel.DefinitionContext{}); err == nil {
		t.Fatal("duplicate settings accepted")
	}
}
