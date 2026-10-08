package core

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/security"
)

type settingsFiles struct {
	t   *testing.T
	id  file.ID
	err error
}

func (f *settingsFiles) URL(_ context.Context, actor security.Actor, id file.ID) (string, error) {
	if !actor.IsGuest() {
		f.t.Fatal("file lookup inherited caller privileges")
	}
	f.id = id
	return "/files/logo.svg", f.err
}

type settingsMedia struct {
	t   *testing.T
	err error
}

func (m settingsMedia) Get(_ context.Context, actor security.Actor, id media.ID) (media.Media, error) {
	if !actor.IsGuest() || id != 7 {
		m.t.Fatal("invalid media lookup")
	}
	return media.Media{ID: id, FileID: 12}, m.err
}

func TestPublicSettingsProjection(t *testing.T) {
	defs := []field.Definition{
		{Key: "private"}, {Key: "title", Public: true},
		{Key: "count", Public: true}, {Key: "flag", Public: true},
		{Key: "list", Public: true}, {Key: "object", Public: true},
		{Key: "missing", Public: true}, {Key: "nil", Public: true},
	}
	values := map[string]any{"private": "secret", "unknown": "secret", "title": "A", "count": int64(3), "flag": false, "list": []any{}, "object": map[string]any{"x": 1}, "nil": nil}
	got, err := projectPublicSettings(context.Background(), values, defs, nil, nil)
	want := map[string]any{"title": "A", "count": int64(3), "flag": false, "list": []any{}, "object": map[string]any{"x": 1}, "nil": nil}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, %v", got, err)
	}
	empty, err := projectPublicSettings(context.Background(), values, nil, nil, nil)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty %#v, %v", empty, err)
	}
	cloned := field.CloneDefinitions(defs)
	if !cloned[1].Public || cloned[0].Public {
		t.Fatal("privacy lost when cloning")
	}
}

func TestPublicSettingsReferences(t *testing.T) {
	for _, kind := range []field.TypeCode{field.TypeFile, field.TypeMedia} {
		t.Run(string(kind), func(t *testing.T) {
			for _, tc := range []struct {
				name              string
				fileErr, mediaErr error
				fail, unavailable bool
			}{
				{name: "public"},
				{name: "private disk", fileErr: filesystem.ErrInvalidVisibility, unavailable: true},
				{name: "deleted file", fileErr: file.ErrNotFound, unavailable: true},
				{name: "denied file", fileErr: security.ErrForbidden, unavailable: true},
				{name: "file storage failure", fileErr: errors.New("offline"), fail: true},
				{name: "deleted media", mediaErr: media.ErrNotFound, unavailable: true},
				{name: "denied media", mediaErr: security.ErrForbidden, unavailable: true},
				{name: "media storage failure", mediaErr: errors.New("offline"), fail: true},
			} {
				if kind == field.TypeFile && tc.mediaErr != nil {
					continue
				}
				t.Run(tc.name, func(t *testing.T) {
					files := &settingsFiles{t: t, err: tc.fileErr}
					got, err := projectPublicSettings(context.Background(), map[string]any{"logo": int64(7)}, []field.Definition{{Key: "logo", Type: kind, Public: true}}, files, settingsMedia{t: t, err: tc.mediaErr})
					if tc.fail {
						if err == nil {
							t.Fatal("unexpected error suppressed")
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					if tc.unavailable {
						if value, ok := got["logo"]; !ok || value != nil {
							t.Fatalf("unavailable reference = %#v", got)
						}
						return
					}
					if got["logo"] != (publicFileValue{ID: 7, URL: "/files/logo.svg"}) {
						t.Fatalf("reference = %#v", got)
					}
					wantID := file.ID(7)
					if kind == field.TypeMedia {
						wantID = 12
					}
					if files.id != wantID {
						t.Fatalf("wrong current media file: %d", files.id)
					}
				})
			}
		})
	}
}

func TestPublicSettingsProjectsNestedRepeaterReferences(t *testing.T) {
	publicFields := []field.Definition{
		{Key: "icon", Type: field.TypeFile, Public: true},
		{Key: "documents", Type: field.TypeFile, Public: true, Options: field.FileOptions{Multiple: true}},
		{Key: "logo", Type: field.TypeMedia, Public: true},
		{Key: "gallery", Type: field.TypeMedia, Public: true, Options: field.MediaOptions{Multiple: true}},
		{Key: "secret", Type: field.TypeString, Public: false},
		{Key: "nested", Type: field.TypeRepeater, Public: true, Options: field.RepeaterOptions{Fields: []field.Definition{
			{Key: "attachment", Type: field.TypeFile, Public: true},
			{Key: "private", Type: field.TypeString, Public: false},
		}}},
	}
	definitions := []field.Definition{{
		Key: "portals", Type: field.TypeRepeater, Public: true,
		Options: field.RepeaterOptions{Fields: publicFields},
	}}
	values := map[string]any{"portals": []any{
		map[string]any{
			"icon": int64(1), "documents": []any{int64(2), int64(3)}, "logo": int64(7),
			"gallery": []any{int64(7), int64(7)}, "secret": "must not escape",
			"nested": []any{map[string]any{"attachment": int64(4), "private": "hidden"}},
		},
	}}

	got, err := projectPublicSettings(context.Background(), values, definitions, &settingsFiles{t: t}, settingsMedia{t: t})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"portals": []map[string]any{{
		"icon": publicFileValue{ID: 1, URL: "/files/logo.svg"},
		"documents": []any{
			publicFileValue{ID: 2, URL: "/files/logo.svg"},
			publicFileValue{ID: 3, URL: "/files/logo.svg"},
		},
		"logo": publicFileValue{ID: 7, URL: "/files/logo.svg"},
		"gallery": []any{
			publicFileValue{ID: 7, URL: "/files/logo.svg"},
			publicFileValue{ID: 7, URL: "/files/logo.svg"},
		},
		"nested": []map[string]any{{"attachment": publicFileValue{ID: 4, URL: "/files/logo.svg"}}},
	}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("public settings = %#v, want %#v", got, want)
	}
}
