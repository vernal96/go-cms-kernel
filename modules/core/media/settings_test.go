package media

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

type settingsMemoryRepository struct{ *memoryRepository }

func (r settingsMemoryRepository) UpdateSettings(ctx context.Context, _ *security.UserID, id ID, values map[string]any, expected time.Time) (Media, error) {
	item, err := r.ByID(ctx, id)
	if err != nil {
		return Media{}, err
	}
	if !item.UpdatedAt.Equal(expected) {
		return Media{}, ErrSettingsConflict
	}
	item.Params["settings"] = cloneMap(values)
	item.UpdatedAt = time.Now().UTC()
	r.items[id] = item
	return Clone(item), nil
}

type settingsAuthorizer struct{ denied permission.Code }

func (a settingsAuthorizer) Check(_ context.Context, _ security.Actor, code permission.Code) error {
	if code == a.denied {
		return security.ErrForbidden
	}
	return nil
}

func TestSettingsValidationPermissionsAndIsolation(t *testing.T) {
	optional := false
	definitions := []SettingsDefinition{{Code: "image", Fields: []field.Definition{
		{Key: "alt", Type: field.TypeString, Label: "Alt", Rules: []string{"max=10"}},
		{Key: "count", Type: field.TypeInteger, Label: "Count"},
		{Key: "decorative", Type: field.TypeCheckbox, Label: "Decorative", Required: &optional},
		{Key: "attachment", Type: field.TypeFile, Label: "Attachment", Required: &optional, Options: field.FileOptions{MIMETypes: []string{"image/*"}}},
	}}}
	catalog, err := CompileSettings(definitions, field.StandardTypes())
	if err != nil {
		t.Fatal(err)
	}
	repo := settingsMemoryRepository{newMemoryRepository()}
	ctx := context.Background()
	actor := security.System()
	first, _ := repo.Create(ctx, nil, Media{FileID: 1, Params: map[string]any{"image": map[string]any{"version": 1}}})
	second, _ := repo.Create(ctx, nil, Media{FileID: 1, Params: map[string]any{}})
	files := memoryFiles{items: map[file.ID]file.File{1: {ID: 1, MIMEType: "image/png"}, 2: {ID: 2, MIMEType: "application/pdf"}}}
	service, err := NewSettingsService(catalog, repo, files, settingsAuthorizer{})
	if err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"alt": "Example", "count": float64(3), "decorative": false, "attachment": float64(1)}
	result, err := service.Save(ctx, actor, first.ID, "image", input, first.UpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if result.Values["count"] != int64(3) {
		t.Fatalf("not normalized: %#v", result.Values)
	}
	current, _ := repo.ByID(ctx, first.ID)
	untouched, _ := repo.ByID(ctx, second.ID)
	if current.Params["image"] == nil || untouched.Params["settings"] != nil {
		t.Fatal("settings overwrote other metadata or Media")
	}
	result.Values["alt"] = "mutated"
	reopened, _ := service.Get(ctx, actor, first.ID, "image")
	if reopened.Values["alt"] != "Example" {
		t.Fatal("mutable state escaped")
	}
	if _, err = service.Save(ctx, actor, first.ID, "image", input, first.UpdatedAt); !errors.Is(err, ErrSettingsConflict) {
		t.Fatalf("stale save: %v", err)
	}
	for _, invalid := range []map[string]any{{"alt": "too long value", "count": 3}, {"alt": "ok", "count": "oops"}, {"alt": "ok", "count": 3, "attachment": 2}} {
		_, err := service.Save(ctx, actor, first.ID, "image", invalid, current.UpdatedAt)
		var validation field.ValidationErrors
		if !errors.As(err, &validation) {
			t.Fatalf("expected validation: %v", err)
		}
	}
	if _, err := service.Get(ctx, actor, first.ID, "missing"); !errors.Is(err, ErrSettings) {
		t.Fatal(err)
	}
	for _, denied := range []permission.Code{readPermission, updatePermission} {
		restricted, _ := NewSettingsService(catalog, repo, files, settingsAuthorizer{denied})
		if denied == readPermission {
			_, err = restricted.Get(ctx, actor, first.ID, "image")
		} else {
			_, err = restricted.Save(ctx, actor, first.ID, "image", input, current.UpdatedAt)
		}
		if !errors.Is(err, security.ErrForbidden) {
			t.Fatal("permissions ignored", err)
		}
	}
}

func TestSettingsCompilationAndMediaMetadata(t *testing.T) {
	optional := false
	defs := []field.Definition{{Key: "alt", Type: field.TypeString, Label: "Alt", Required: &optional}}
	for _, multiple := range []bool{false, true} {
		types := append(field.Types{}, field.StandardTypes()...)
		for i, typ := range types {
			if typ.Code() == field.TypeMedia {
				types[i] = field.MediaType(map[string][]field.Definition{"image": defs})
			}
		}
		def := field.Definition{Key: "image", Type: field.TypeMedia, Label: "Image", Options: field.MediaOptions{SettingsCode: "image", Multiple: multiple}}
		desc, err := field.Describe(def, types)
		if err != nil {
			t.Fatal(err)
		}
		var options struct {
			SettingsCode   string             `json:"settings_code"`
			SettingsFields []field.Descriptor `json:"settings_fields"`
			Multiple       bool               `json:"multiple"`
		}
		if err := json.Unmarshal(desc.Options, &options); err != nil {
			t.Fatal(err)
		}
		if options.SettingsCode != "image" || len(options.SettingsFields) != 1 || options.Multiple != multiple {
			t.Fatalf("missing metadata: %s", desc.Options)
		}
		nested := field.Definition{Key: "slides", Type: field.TypeRepeater, Label: "Slides", Options: field.RepeaterOptions{Fields: []field.Definition{def}}}
		if _, err := field.Describe(nested, types); err != nil {
			t.Fatal(err)
		}
		def.Options = field.MediaOptions{SettingsCode: "missing"}
		if _, err := field.Compile([]field.Definition{def}, types); err == nil {
			t.Fatal("accepted missing settings")
		}
	}
	for _, definitions := range [][]SettingsDefinition{
		{{Code: "a"}, {Code: "a"}},
		{{Code: "a", Fields: []field.Definition{{Key: "x", Label: "X", Type: "missing"}}}},
		{{Code: "a", Fields: []field.Definition{{Key: "x", Label: "X", Type: field.TypeMedia}}}},
		{{Code: "a", Fields: []field.Definition{{Key: "x", Label: "X", Type: field.TypeRepeater, Options: field.RepeaterOptions{Fields: []field.Definition{{Key: "m", Label: "M", Type: field.TypeMedia}}}}}}},
	} {
		if _, err := CompileSettings(definitions, field.StandardTypes()); err == nil {
			t.Fatal("invalid settings accepted")
		}
	}
}
