package core

import (
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"testing"
)

func TestMediaSettingsDeclarationsAreSnapshots(t *testing.T) {
	options := &field.MediaOptions{SettingsCode: "image"}
	defs := []field.Definition{{Key: "photo", Label: "Photo", Type: field.TypeMedia, Options: options}}
	cloned := field.CloneDefinitions(defs)
	options.SettingsCode = "changed"
	if cloned[0].Options.(*field.MediaOptions).SettingsCode != "image" {
		t.Fatal("mutable media options escaped")
	}
	config := Config{MediaSettings: []media.SettingsDefinition{{Code: "image", Fields: []field.Definition{{Key: "alt", Label: "Alt", Type: field.TypeString}}}}}
	snapshot := config.CloneModuleConfig().(Config)
	registry, err := (Module{}).RegistryForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	config.MediaSettings[0].Fields[0].Key = "changed"
	if snapshot.MediaSettings[0].Fields[0].Key != "alt" {
		t.Fatal("mutable settings config escaped")
	}
	descriptor, err := field.Describe(cloned[0], field.Types(registry.FieldTypes))
	if err != nil {
		t.Fatal(err)
	}
	if string(descriptor.Options) == "" {
		t.Fatal("missing settings metadata")
	}
	if _, err := (Module{}).RegistryForConfig(Config{MediaSettings: []media.SettingsDefinition{{Code: "same"}, {Code: "same"}}}); err == nil {
		t.Fatal("duplicate settings accepted")
	}
}
