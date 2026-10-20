package kernel_test

import (
	"context"
	"testing"

	kernel "github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
)

func TestProfileResourceImageConfigurationAndSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name  string
		image *field.MediaOptions
		valid bool
	}{
		{"disabled", nil, true},
		{"valid", &field.MediaOptions{Disk: "public", VirtualPath: "resources/images", SettingsCode: "image", MIMETypes: []string{"image/*"}}, true},
		{"unknown disk", &field.MediaOptions{Disk: "unknown", VirtualPath: "images", SettingsCode: "image"}, false},
		{"missing path", &field.MediaOptions{Disk: "public", SettingsCode: "image"}, false},
		{"missing settings", &field.MediaOptions{Disk: "public", VirtualPath: "images"}, false},
		{"multiple", &field.MediaOptions{Disk: "public", VirtualPath: "images", SettingsCode: "image", Multiple: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			services := testRuntimeServices()
			services.Filesystems = widgetFileDisks{"public": true}
			factory, err := kernel.NewProfileRuntimeFactory(emptyDatabaseResolver{}, services)
			if err != nil {
				t.Fatal(err)
			}
			blueprint, err := factory.Compile(context.Background(), kernel.Profile{
				Code: "image",
				Settings: kernel.ProfileSettings{FileSystem: kernel.ProfileFileSystem{
					ResourceImage: tc.image,
				}},
				Modules: []kernel.Module{
					widgetFileModule{widgetProviderModule{code: "fields"}},
				},
			})
			if (err == nil) != tc.valid {
				t.Fatalf("error=%v valid=%t", err, tc.valid)
			}
			if tc.valid && tc.image != nil {
				tc.image.VirtualPath = "mutated"
				tc.image.MIMETypes[0] = "text/plain"
				got := blueprint.Profile().Settings.FileSystem.ResourceImage
				if got.VirtualPath != "resources/images" || got.MIMETypes[0] != "image/*" {
					t.Fatalf("mutable declaration escaped: %+v", got)
				}
				got.MIMETypes[0] = "text/plain"
				if blueprint.Profile().Settings.FileSystem.ResourceImage.MIMETypes[0] != "image/*" {
					t.Fatal("profile snapshot escaped")
				}
			}
		})
	}
}
