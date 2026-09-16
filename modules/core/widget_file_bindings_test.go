package core

import (
	"context"
	"errors"
	"testing"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
	"github.com/vernal96/go-cms-kernel/security"
)

type bindingFields map[field.TypeCode]field.Type

func (r bindingFields) FieldType(code field.TypeCode) (field.Type, bool) {
	v, ok := r[code]
	return v, ok
}

type bindingFiles struct {
	file.Service
	mime string
}

func (f bindingFiles) GetFile(context.Context, security.Actor, file.ID) (file.File, error) {
	return file.File{Storage: "public", MIMEType: f.mime}, nil
}

func TestBoundFileChecksTargetRestrictions(t *testing.T) {
	types := bindingFields{}
	for _, typ := range field.StandardTypes() {
		types[typ.Code()] = typ
	}
	schema, err := field.CompilePersistent([]field.Definition{{Key: "attachment", Label: "Attachment", Type: field.TypeFile}}, types)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := widget.Compile([]widget.Source{{Module: widget.ModuleDescriptor{Code: "test", Label: "Test"}, Widgets: []widget.Widget{widget.Functional{
		Description: widget.Definition{Reference: widget.NewRef("file"), Label: "File", Description: "File", Fields: []field.Definition{{Key: "image", Label: "Image", Type: field.TypeFile, Options: field.FileOptions{MIMETypes: []string{"image/*"}}}}},
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
		handler := pageResourceHandler{files: bindingFiles{mime: mime}}
		_, err := handler.newWidgetInstance(context.Background(), runtime, placement, schema, widget.ResourceValues{Fields: map[string]any{"attachment": int64(1)}})
		if mime == "image/png" && err != nil {
			t.Fatal(err)
		}
		if mime == "application/pdf" && !errors.Is(err, widget.ErrInvalidParams) {
			t.Fatalf("MIME constraint bypassed: %v", err)
		}
	}
}
