package site

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	kernel "github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/security"
)

type settingsModule struct{ transitionModule }

func (settingsModule) Registry() (kernel.ModuleRegistry, error) {
	return kernel.ModuleRegistry{FieldTypes: field.StandardTypes(), ValidatorTypes: field.StandardValidatorTypes()}, nil
}

type settingsFiles struct{ file.Service }

func (settingsFiles) GetFile(_ context.Context, _ security.Actor, id file.ID) (file.File, error) {
	mimeTypes := map[file.ID]string{1: "image/png", 2: "image/svg+xml", 3: "image/jpeg"}
	return file.File{ID: id, MIMEType: mimeTypes[id]}, nil
}

type settingsMedia struct{ media.Service }

func (settingsMedia) Resolve(_ context.Context, _ security.Actor, id media.ID) (media.ResolvedMedia, error) {
	mimeTypes := map[media.ID]string{1: "image/png", 2: "image/svg+xml", 3: "image/jpeg"}
	return media.ResolvedMedia{Media: media.Media{ID: id}, File: file.File{ID: file.ID(id), Storage: "public", MIMEType: mimeTypes[id]}}, nil
}

func TestSiteSettingsRequiredOnlyOnUpdate(t *testing.T) {
	ctx := context.Background()
	factory, err := kernel.NewProfileRuntimeFactory(testResolver{}, kernel.RuntimeServices{EventBus: testEventBus{}, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	blueprint, err := factory.Compile(ctx, kernel.Profile{Code: "settings", Modules: []kernel.Module{settingsModule{transitionModule{code: "fields", recorder: &transitionRecorder{}}}}, Params: []field.Definition{
		{Key: "logo", Type: field.TypeFile, Label: "Logo", Required: true, Options: field.FileOptions{Disk: "public", VirtualPath: "site", SettingsCode: "logo", MIMETypes: []string{"image/png", "image/svg+xml"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, values := range []map[string]any{nil, {}, {"logo": nil}, {"logo": ""}} {
		repo := &memoryRepository{items: []Site{{ID: 1, ProfileCode: "settings", Name: "Existing", Domain: "existing.test", Locale: "ru-RU", Settings: values}}}
		catalog, err := NewCatalog(repo, testProfiles{"settings": blueprint}, testAccess{allow: true})
		if err != nil {
			t.Fatal(err)
		}
		if err := catalog.SetMediaService(settingsMedia{}); err != nil {
			t.Fatal(err)
		}
		if err := catalog.Reload(ctx); err != nil {
			t.Fatalf("boot/reload with %v: %v", values, err)
		}
		created, err := catalog.Create(ctx, security.System(), CreateInput{ProfileCode: "settings", Name: "New", Domain: "new.test", Locale: "ru-RU", Settings: values})
		if err != nil {
			t.Fatalf("create with %v: %v", values, err)
		}
		if err := catalog.Reload(ctx); err != nil {
			t.Fatal(err)
		}
		previous, _ := catalog.RuntimeByID(created.site.ID)
		input := UpdateInput{ID: created.site.ID, ProfileCode: "settings", Name: "Edited", Domain: "new.test", Locale: "ru-RU", Settings: values}
		_, err = catalog.Update(ctx, security.System(), input)
		var failures field.ValidationErrors
		if !errors.As(err, &failures) || len(failures) != 1 || failures[0].Key != "logo" || failures[0].Code != "required" {
			t.Fatalf("update without logo: %v", err)
		}
		current, _ := catalog.RuntimeByID(created.site.ID)
		if current != previous || repo.items[1].Name != "New" {
			t.Fatal("failed update changed runtime or stored site")
		}
		input.Settings = map[string]any{"logo": 3}
		if _, err := catalog.Update(ctx, security.System(), input); err == nil || !strings.Contains(err.Error(), "image/jpeg") || !strings.Contains(err.Error(), "image/png") {
			t.Fatalf("update MIME mismatch error = %v", err)
		}
		if _, err := catalog.Create(ctx, security.System(), CreateInput{ProfileCode: "settings", Name: "Invalid", Domain: "invalid.test", Locale: "ru-RU", Settings: input.Settings}); err == nil || !strings.Contains(err.Error(), "image/jpeg") || !strings.Contains(err.Error(), "image/png") {
			t.Fatalf("create MIME mismatch error = %v", err)
		}
		for _, id := range []int{1, 2} {
			input.Settings = map[string]any{"logo": id}
			updated, err := catalog.Update(ctx, security.System(), input)
			if err != nil {
				t.Fatalf("update logo %d: %v", id, err)
			}
			if updated.Site().Settings["logo"] != int64(id) || repo.items[1].Settings["logo"] != int64(id) {
				t.Fatal("valid logo not persisted")
			}
		}
	}
}
