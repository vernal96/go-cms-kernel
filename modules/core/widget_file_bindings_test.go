package core

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
	"github.com/vernal96/go-cms-kernel/security"
)

type bindingFields map[field.TypeCode]field.Type

func (r bindingFields) FieldType(code field.TypeCode) (field.Type, bool) {
	v, ok := r[code]
	return v, ok
}

type bindingFiles struct {
	mime string
}

func (f bindingFiles) Resolve(context.Context, security.Actor, media.ID) (media.ResolvedMedia, error) {
	return media.ResolvedMedia{Media: media.Media{ID: 1, FileID: 1}, File: file.File{ID: 1, Storage: "public", MIMEType: f.mime}}, nil
}

func TestBoundFileChecksTargetRestrictions(t *testing.T) {
	types := bindingFields{}
	for _, typ := range field.StandardTypes() {
		types[typ.Code()] = typ
	}
	schema, err := field.CompilePersistent([]field.Definition{{Key: "attachment", Label: "Attachment", Type: field.TypeFile, Options: field.FileOptions{Disk: "public", VirtualPath: "assets", SettingsCode: "image"}}}, types)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := widget.Compile([]widget.Source{{Module: widget.ModuleDescriptor{Code: "test", Label: "Test"}, Widgets: []widget.Widget{widget.Functional{
		Description: widget.Definition{Reference: widget.NewRef("file"), Label: "File", Description: "File", Fields: []field.Definition{{Key: "image", Label: "Image", Type: field.TypeFile, Options: field.FileOptions{Disk: "public", VirtualPath: "assets", SettingsCode: "image", MIMETypes: []string{"image/*"}}}}},
		Render: func(_ context.Context, _ widget.RenderInput, params map[string]any) (map[string]any, error) {
			return params, nil
		},
	}}}}, nil, types)
	if err != nil {
		t.Fatal(err)
	}
	runtime, _ := catalog.Widget("test_file")
	placement := widget.Placement{ParamBindings: widget.ParamBindings{"image": widget.ResourceField("attachment")}}
	for _, mime := range []string{"image/png", "application/pdf"} {
		handler := pageResourceHandler{media: bindingFiles{mime: mime}}
		_, err := handler.newWidgetInstance(context.Background(), runtime, placement, schema, widget.ResourceValues{Fields: map[string]any{"attachment": int64(1)}})
		if mime == "image/png" && err != nil {
			t.Fatal(err)
		}
		if mime == "application/pdf" && (!errors.Is(err, widget.ErrInvalidParams) || !strings.Contains(err.Error(), mime) || !strings.Contains(err.Error(), "image/*")) {
			t.Fatalf("MIME mismatch error is not explicit: %v", err)
		}
	}
}
