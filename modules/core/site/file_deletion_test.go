package site

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	kernel "github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/security"
)

type fileDeletionSites struct{ *memoryRepository }

func (r fileDeletionSites) ApplyFileDeletionSettings(ctx context.Context, actor *security.UserID, item Site) (Site, error) {
	item.Version++
	return r.Update(ctx, actor, item)
}

func TestPreparedFileRemovalPublishesOnlyAfterPersistenceAndKeepsRequiredFieldReloadable(t *testing.T) {
	ctx := context.Background()
	factory, err := kernel.NewProfileRuntimeFactory(testResolver{}, kernel.RuntimeServices{EventBus: testEventBus{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	blueprint, err := factory.Compile(ctx, kernel.Profile{Code: "delete", Modules: []kernel.Module{settingsModule{transitionModule{code: "fields", recorder: &transitionRecorder{}}}}, Params: []field.Definition{{Key: "rows", Type: field.TypeRepeater, Label: "Rows", Options: field.RepeaterOptions{Fields: []field.Definition{{Key: "icon", Type: field.TypeFile, Label: "Icon", Required: true, Options: field.FileOptions{Disk: "public", VirtualPath: "icons", SettingsCode: "icon"}}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	repo := fileDeletionSites{&memoryRepository{items: []Site{{ID: 1, Version: 1, ProfileCode: "delete", Name: "Site", Domain: "delete.test", Locale: "ru-RU", Settings: map[string]any{"rows": []any{map[string]any{"icon": int64(1)}}}}}}}
	catalog, err := NewCatalog(repo, testProfiles{"delete": blueprint}, testAccess{allow: true})
	if err != nil {
		t.Fatal(err)
	}
	catalog.SetMediaService(settingsMedia{})
	if err := catalog.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	before, _ := catalog.RuntimeByID(1)
	ref := media.FileOccurrence{OwnerKind: "site", OwnerID: 1, SiteID: 1, Container: "settings", Path: []string{"rows", "0", "icon"}, MediaID: 1, Target: field.ReferenceFile}
	prepared, err := catalog.PrepareFileOccurrence(ctx, security.System(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if current, _ := catalog.RuntimeByID(1); current != before {
		t.Fatal("preparation published")
	}
	repo.updateErr = errors.New("SQL unavailable")
	if _, err := prepared.Apply(ctx); err == nil {
		t.Fatal("expected persistence failure")
	}
	prepared.Abort()
	if current, _ := catalog.RuntimeByID(1); current != before {
		t.Fatal("failed mutation changed runtime")
	}
	repo.updateErr = nil
	prepared, err = catalog.PrepareFileOccurrence(ctx, security.System(), ref)
	if err != nil {
		t.Fatal(err)
	}
	cleared, err := prepared.Apply(ctx)
	if err != nil {
		prepared.Abort()
		t.Fatal(err)
	}
	if current, _ := catalog.RuntimeByID(1); current != before {
		t.Fatal("SQL private mutation published early")
	}
	prepared.Publish()
	after, _ := catalog.RuntimeByID(1)
	if after == before || cleared.OwnerVersion != 2 || len(after.Site().FileReferences) != 0 {
		t.Fatalf("invalid publication: %v", after.Site())
	}
	if err := catalog.Reload(ctx); err != nil {
		t.Fatalf("required file removal failed reload: %v", err)
	}
}
