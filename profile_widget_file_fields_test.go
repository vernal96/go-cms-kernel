package kernel_test

import (
	"context"
	"strings"
	"testing"

	kernel "github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
)

type widgetFileModule struct{ widgetProviderModule }

func (widgetFileModule) Registry() (kernel.ModuleRegistry, error) {
	return kernel.ModuleRegistry{FieldTypes: field.StandardTypes()}, nil
}

type widgetFileDisks map[filesystem.Code]bool

func (d widgetFileDisks) Disk(code filesystem.Code) (filesystem.Disk, bool) {
	return nil, d[code]
}

func TestProfileWidgetFieldsValidateDiskIncludingRepeater(t *testing.T) {
	for _, nested := range []bool{false, true} {
		definition := field.Definition{Key: "asset", Type: field.TypeFile, Label: "Asset", Options: field.FileOptions{Disk: "widget-assets", VirtualPath: "widgets/images", SettingsCode: "image"}}
		if nested {
			definition = field.Definition{Key: "rows", Type: field.TypeRepeater, Label: "Rows", Options: field.RepeaterOptions{Fields: []field.Definition{definition}}}
		}
		for _, configured := range []bool{false, true} {
			services := testRuntimeServices()
			services.Filesystems = widgetFileDisks{"widget-assets": configured}
			factory, err := kernel.NewProfileRuntimeFactory(emptyDatabaseResolver{}, services)
			if err != nil {
				t.Fatal(err)
			}
			_, err = buildProfileRuntime(factory, context.Background(), kernel.Profile{
				Code: "widget-files", Modules: []kernel.Module{widgetFileModule{widgetProviderModule{
					code: "feature", widgets: []widget.Widget{runtimeWidget{definition: widget.Definition{Reference: widget.NewRef("assets"), Label: "Assets", Description: "Uploaded assets", Fields: []field.Definition{definition}}}},
				}}},
			})
			if configured && err != nil {
				t.Fatalf("configured widget disk rejected, nested=%v: %v", nested, err)
			}
			if !configured && (err == nil || !strings.Contains(err.Error(), `widget "feature_assets"`) || !strings.Contains(err.Error(), `unknown disk "widget-assets"`)) {
				t.Fatalf("unknown widget disk accepted, nested=%v: %v", nested, err)
			}
		}
	}
}
