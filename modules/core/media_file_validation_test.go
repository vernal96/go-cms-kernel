package core

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	kernel "github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
)

type mediaValidationModule struct {
	deletionFieldsModule
	fields []field.Definition
}

func (m mediaValidationModule) Build(context.Context, kernel.ModuleContext) (kernel.ModuleRuntime, error) {
	return m, nil
}
func (m mediaValidationModule) Widgets() []widget.Widget {
	return []widget.Widget{widget.Functional{
		Description: widget.Definition{Reference: widget.NewRef("tile"), Label: "Tile", Description: "Tile", Fields: m.fields},
		Render:      func(context.Context, widget.RenderInput, map[string]any) (map[string]any, error) { return nil, nil },
	}}
}

type mediaValidationProfiles struct{ blueprint *kernel.ProfileBlueprint }

func (p *mediaValidationProfiles) ProfileBlueprint(kernel.ProfileCode) (*kernel.ProfileBlueprint, bool) {
	return p.blueprint, true
}

type mediaValidationReader struct{}

func (mediaValidationReader) ReadFileOccurrence(_ context.Context, ref media.FileOccurrence) (media.FileOccurrenceValues, error) {
	switch ref.Container {
	case "settings":
		return media.FileOccurrenceValues{Code: "guard", Values: map[string]any{"rows": []any{map[string]any{"icon": int64(1)}}}}, nil
	case "fields:documents:1":
		return media.FileOccurrenceValues{Code: "page", Values: map[string]any{"documents": []any{int64(2), int64(1)}}}, nil
	case "widget:3":
		return media.FileOccurrenceValues{Code: "fields_tile", Values: map[string]any{"rows": []any{map[string]any{"icon": int64(1)}}}}, nil
	}
	return media.FileOccurrenceValues{}, media.ErrInvalidReference
}

func TestMediaFileValidationUsesCurrentSiteResourceAndWidgetSchemas(t *testing.T) {
	ctx := context.Background()
	factory, err := kernel.NewProfileRuntimeFactory(deletionResolver{}, kernel.RuntimeServices{EventBus: deletionEventBus{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	compile := func(mime string) *kernel.ProfileBlueprint {
		t.Helper()
		options := field.FileOptions{Disk: "public", VirtualPath: "files", SettingsCode: "document", MIMETypes: []string{mime}}
		rows := []field.Definition{{Key: "rows", Label: "Rows", Type: field.TypeRepeater, Options: field.RepeaterOptions{Fields: []field.Definition{{Key: "icon", Label: "Icon", Type: field.TypeFile, Options: options}}}}}
		options.Multiple = true
		blueprint, err := factory.Compile(ctx, kernel.Profile{Code: "guard", Modules: []kernel.Module{mediaValidationModule{fields: rows}}, Params: rows, Templates: []template.Definition{{Code: "page", Label: "Page", Fields: []field.Definition{{Key: "documents", Label: "Documents", Type: field.TypeFile, Options: options}}}}})
		if err != nil {
			t.Fatal(err)
		}
		return blueprint
	}
	profiles := &mediaValidationProfiles{blueprint: compile("image/png")}
	catalog, err := site.NewCatalog(&siteRepositoryStub{items: []site.Site{{ID: 1, ProfileCode: "guard", Domain: "guard.test", Name: "Guard", Locale: "en", Settings: map[string]any{"rows": []any{map[string]any{"icon": int64(1)}}}}}}, profiles, &deletionSiteAccess{})
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.SetMediaService(deletionSiteMedia{}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	services := &Services{Sites: catalog, fileOccurrenceReaders: map[string]media.FileOccurrenceReader{"site": mediaValidationReader{}, "resource": mediaValidationReader{}}}
	refs := []media.FileOccurrence{
		{OwnerKind: "site", OwnerID: 1, SiteID: 1, Container: "settings", Path: []string{"rows", "0", "icon"}, MediaID: 1, Target: field.ReferenceFile},
		{OwnerKind: "resource", OwnerID: 2, SiteID: 1, Container: "fields:documents:1", Path: []string{"documents", "1"}, MediaID: 1, Target: field.ReferenceFile},
		{OwnerKind: "resource", OwnerID: 2, SiteID: 1, Container: "widget:3", Path: []string{"rows", "0", "icon"}, MediaID: 1, Target: field.ReferenceFile},
	}
	for _, ref := range refs {
		usage := media.Usage{Kind: media.FileFieldUsage, OwnerID: ref.OwnerID, Occurrence: &ref}
		if err := services.validateMediaFileOccurrence(ctx, file.File{Storage: "public", MIMEType: "image/png"}, usage); err != nil {
			t.Fatalf("valid %s: %v", ref.Container, err)
		}
		for _, invalid := range []file.File{{Storage: "private", MIMEType: "image/png"}, {Storage: "public", MIMEType: "application/pdf"}} {
			var validation field.ValidationErrors
			if err := services.validateMediaFileOccurrence(ctx, invalid, usage); !errors.As(err, &validation) {
				t.Fatalf("invalid %s: %v", ref.Container, err)
			}
		}
	}
	// Rebuilding the runtime changes the authoritative MIME policy immediately;
	// no occurrence/index snapshot or owner save is needed.
	profiles.blueprint = compile("image/*")
	if err := catalog.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	for _, ref := range refs {
		if err := services.validateMediaFileOccurrence(ctx, file.File{Storage: "public", MIMEType: "image/jpeg"}, media.Usage{Kind: media.FileFieldUsage, Occurrence: &ref}); err != nil {
			t.Fatalf("reloaded %s constraints are stale: %v", ref.Container, err)
		}
	}
}
