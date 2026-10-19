package publicfields

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

type projectorFiles struct {
	urls  map[file.ID]string
	err   error
	calls int
}

func (f *projectorFiles) URL(_ context.Context, actor security.Actor, id file.ID) (string, error) {
	if !actor.IsSystem() {
		return "", errors.New("file visibility check did not use system actor")
	}
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	return f.urls[id], nil
}

type projectorMedia struct {
	items map[media.ID]media.ResolvedMedia
	err   map[media.ID]error
	calls int
}

func (m *projectorMedia) Resolve(_ context.Context, actor security.Actor, id media.ID) (media.ResolvedMedia, error) {
	if !actor.IsSystem() {
		return media.ResolvedMedia{}, errors.New("internal media resolution did not use system actor")
	}
	m.calls++
	if err := m.err[id]; err != nil {
		return media.ResolvedMedia{}, err
	}
	item, exists := m.items[id]
	if !exists {
		return media.ResolvedMedia{}, media.ErrNotFound
	}
	return item, nil
}

func newProjector(t *testing.T, files *projectorFiles, medias *projectorMedia) *Projector {
	t.Helper()
	projector, err := New(
		[]template.Definition{{Code: "page", Label: "Page", Fields: []field.Definition{
			{Key: "cover", Type: field.TypeMedia, Options: field.MediaOptions{SettingsCode: "image"}},
			{Key: "gallery", Type: field.TypeMedia, Options: field.MediaOptions{SettingsCode: "image", Multiple: true}},
			{Key: "rows", Type: field.TypeRepeater, Options: field.RepeaterOptions{Fields: []field.Definition{
				{Key: "image", Type: field.TypeMedia, Options: field.MediaOptions{SettingsCode: "image"}},
				{Key: "attachments", Type: field.TypeMedia, Options: field.MediaOptions{SettingsCode: "document", Multiple: true}},
			}}},
		}}},
		[]media.SettingsDefinition{
			{Code: "image", Fields: []field.Definition{{Key: "alt", Type: field.TypeString}, {Key: "credit", Type: field.TypeString}}},
			{Code: "document", Fields: []field.Definition{{Key: "caption", Type: field.TypeString}}},
		}, files, medias,
	)
	if err != nil {
		t.Fatal(err)
	}
	return projector
}

func TestTemplateFieldsProjectsMediaValuesAndRepeaterFields(t *testing.T) {
	files := &projectorFiles{urls: map[file.ID]string{101: "/_cms/files/101", 102: "/_cms/files/102"}}
	medias := &projectorMedia{items: map[media.ID]media.ResolvedMedia{
		1: {Media: media.Media{ID: 1, Params: map[string]any{"settings": map[string]any{"alt": "One", "unrelated": "hidden"}}}, File: file.File{ID: 101, MIMEType: "image/jpeg", Size: 2048}},
		2: {Media: media.Media{ID: 2, Params: map[string]any{"settings": map[string]any{"alt": "Two"}}}, File: file.File{ID: 101, MIMEType: "image/jpeg", Size: 2048}},
		3: {Media: media.Media{ID: 3, Params: map[string]any{"settings": map[string]any{"caption": "PDF"}}}, File: file.File{ID: 102, MIMEType: "application/pdf", Size: 4096}},
	}}
	projector := newProjector(t, files, medias)
	code := template.Code("page")
	got, err := projector.TemplateFields(context.Background(), &code, map[string]any{
		"cover": int64(1), "gallery": []any{int64(1), int64(2)},
		"rows": []any{map[string]any{"image": int64(2), "attachments": []any{int64(3)}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantMedia := MediaValue{ID: 1, URL: "/_cms/files/101", MIMEType: "image/jpeg", Size: 2048, Settings: map[string]any{"alt": "One"}}
	want := map[string]any{
		"cover":   wantMedia,
		"gallery": []any{wantMedia, MediaValue{ID: 2, URL: "/_cms/files/101", MIMEType: "image/jpeg", Size: 2048, Settings: map[string]any{"alt": "Two"}}},
		"rows":    []map[string]any{{"image": MediaValue{ID: 2, URL: "/_cms/files/101", MIMEType: "image/jpeg", Size: 2048, Settings: map[string]any{"alt": "Two"}}, "attachments": []any{MediaValue{ID: 3, URL: "/_cms/files/102", MIMEType: "application/pdf", Size: 4096, Settings: map[string]any{"caption": "PDF"}}}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("projected fields = %#v, want %#v", got, want)
	}
	if medias.calls != 3 || files.calls != 3 {
		t.Fatalf("resolved media/file URL calls = %d/%d, want 3/3", medias.calls, files.calls)
	}
}

func TestPublicSettingsProjectsOnlyPublicFieldsAndUnavailableMediaAsNull(t *testing.T) {
	files := &projectorFiles{urls: map[file.ID]string{101: "/_cms/files/101"}}
	medias := &projectorMedia{
		items: map[media.ID]media.ResolvedMedia{1: {Media: media.Media{ID: 1}, File: file.File{ID: 101, MIMEType: "image/jpeg", Size: 100}}},
		err:   map[media.ID]error{2: media.ErrNotFound},
	}
	projector := newProjector(t, files, medias)
	definitions := []field.Definition{
		{Key: "private", Type: field.TypeMedia, Options: field.MediaOptions{SettingsCode: "image"}},
		{Key: "image", Type: field.TypeMedia, Public: true, Options: field.MediaOptions{SettingsCode: "image"}},
		{Key: "images", Type: field.TypeMedia, Public: true, Options: field.MediaOptions{SettingsCode: "image", Multiple: true}},
	}
	got, err := projector.PublicSettings(context.Background(), map[string]any{"private": int64(1), "image": int64(1), "images": []any{int64(1), int64(2)}}, definitions)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"image":  MediaValue{ID: 1, URL: "/_cms/files/101", MIMEType: "image/jpeg", Size: 100, Settings: map[string]any{}},
		"images": []any{MediaValue{ID: 1, URL: "/_cms/files/101", MIMEType: "image/jpeg", Size: 100, Settings: map[string]any{}}, nil},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("public settings = %#v, want %#v", got, want)
	}
}

func TestPublicMediaProjectionReturnsUnexpectedErrors(t *testing.T) {
	files := &projectorFiles{err: errors.New("storage offline")}
	medias := &projectorMedia{items: map[media.ID]media.ResolvedMedia{1: {Media: media.Media{ID: 1}, File: file.File{ID: 101}}}}
	projector := newProjector(t, files, medias)
	code := template.Code("page")
	_, err := projector.TemplateFields(context.Background(), &code, map[string]any{"cover": int64(1)})
	if err == nil {
		t.Fatal("unexpected URL error was suppressed")
	}
	medias = &projectorMedia{err: map[media.ID]error{1: errors.New("file database offline")}}
	projector = newProjector(t, &projectorFiles{}, medias)
	_, err = projector.TemplateFields(context.Background(), &code, map[string]any{"cover": int64(1)})
	if err == nil {
		t.Fatal("unexpected media resolution error was suppressed")
	}
}

type publicProjectionMediaRepository struct{ item media.Media }

func (r publicProjectionMediaRepository) Create(context.Context, *security.UserID, media.Media) (media.Media, error) {
	return media.Media{}, errors.New("unexpected Create call")
}
func (r publicProjectionMediaRepository) ByID(_ context.Context, id media.ID) (media.Media, error) {
	if id != r.item.ID {
		return media.Media{}, media.ErrNotFound
	}
	return r.item, nil
}
func (r publicProjectionMediaRepository) Update(context.Context, *security.UserID, media.Media, media.ValidateUsages) (media.Media, error) {
	return media.Media{}, errors.New("unexpected Update call")
}
func (r publicProjectionMediaRepository) Delete(context.Context, media.ID) error {
	return errors.New("unexpected Delete call")
}

type publicProjectionFileService struct{ file.Service }

func (publicProjectionFileService) GetFile(_ context.Context, actor security.Actor, id file.ID) (file.File, error) {
	if !actor.IsSystem() || id != 6 {
		return file.File{}, errors.New("media service did not resolve its linked file internally")
	}
	return file.File{ID: id, MIMEType: "image/png", Size: 2006}, nil
}
func (publicProjectionFileService) URL(_ context.Context, actor security.Actor, id file.ID) (string, error) {
	if !actor.IsSystem() || id != 6 {
		return "", errors.New("public visibility check did not use system actor")
	}
	return "https://cdn.example/image.png", nil
}

type guestDeniedMediaAuthorizer struct{ security.Authorizer }

func (guestDeniedMediaAuthorizer) Check(_ context.Context, actor security.Actor, _ permission.Code) error {
	if actor.IsGuest() {
		return security.ErrForbidden
	}
	return nil
}

func TestPublicFieldProjectionResolvesMediaWithoutGuestMediaPermission(t *testing.T) {
	mediaService, err := media.NewService(
		publicProjectionMediaRepository{item: media.Media{ID: 8, FileID: 6, Params: map[string]any{"settings": map[string]any{"alt": "News"}}}},
		publicProjectionFileService{}, nil, guestDeniedMediaAuthorizer{},
	)
	if err != nil {
		t.Fatal(err)
	}
	projector, err := New(
		[]template.Definition{{Code: "page", Label: "Page", Fields: []field.Definition{{Key: "gallery", Type: field.TypeMedia, Options: field.MediaOptions{SettingsCode: "image"}}}}},
		[]media.SettingsDefinition{{Code: "image", Fields: []field.Definition{{Key: "alt", Type: field.TypeString}}}},
		publicProjectionFileService{}, mediaService,
	)
	if err != nil {
		t.Fatal(err)
	}
	got, err := projector.TemplateFields(context.Background(), ptrTemplateCode("page"), map[string]any{"gallery": json.Number("8")})
	if err != nil {
		t.Fatal(err)
	}
	want := MediaValue{ID: 8, URL: "/_cms/files/6", MIMEType: "image/png", Size: 2006, Settings: map[string]any{"alt": "News"}}
	if !reflect.DeepEqual(got["gallery"], want) {
		t.Fatalf("projected media = %#v, want %#v", got["gallery"], want)
	}
}

func TestMediaIDValueAcceptsOnlyPositiveIntegralNumbers(t *testing.T) {
	for _, test := range []struct {
		value any
		want  int64
		ok    bool
	}{
		{value: int64(8), want: 8, ok: true},
		{value: json.Number("8"), want: 8, ok: true},
		{value: json.Number("8.0"), want: 8, ok: true},
		{value: json.Number("8e0"), want: 8, ok: true},
		{value: float64(8), want: 8, ok: true},
		{value: json.Number("8.5")},
		{value: float64(8.5)},
		{value: "8"},
		{value: json.Number("9223372036854775808")},
	} {
		got, ok := mediaIDValue(test.value)
		if got != test.want || ok != test.ok {
			t.Errorf("mediaIDValue(%#v) = %d, %v; want %d, %v", test.value, got, ok, test.want, test.ok)
		}
	}
}

func TestProjectorSettingsDefinitionAndMediaOptionsAreSiteScopedSnapshots(t *testing.T) {
	options := field.MediaOptions{SettingsCode: "image"}
	definition := template.Definition{Code: "page", Fields: []field.Definition{{Key: "cover", Type: field.TypeMedia, Options: options}}}
	settings := []media.SettingsDefinition{{Code: "image", Fields: []field.Definition{{Key: "alt", Type: field.TypeString}}}}
	files := &projectorFiles{urls: map[file.ID]string{10: "/_cms/files/10"}}
	medias := &projectorMedia{items: map[media.ID]media.ResolvedMedia{1: {
		Media: media.Media{ID: 1, Params: map[string]any{"settings": map[string]any{"alt": "kept"}}},
		File:  file.File{ID: 10, MIMEType: "image/jpeg", Size: 20},
	}}}
	projector, err := New([]template.Definition{definition}, settings, files, medias)
	if err != nil {
		t.Fatal(err)
	}
	definition.Fields[0].Key = "changed"
	settings[0].Fields[0].Key = "changed"
	got, err := projector.TemplateFields(context.Background(), ptrTemplateCode("page"), map[string]any{"cover": int64(1)})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"cover": MediaValue{ID: 1, URL: "/_cms/files/10", MIMEType: "image/jpeg", Size: 20, Settings: map[string]any{"alt": "kept"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("projected fields after source mutation = %#v, want %#v", got, want)
	}
}

func ptrTemplateCode(code string) *template.Code {
	value := template.Code(code)
	return &value
}
