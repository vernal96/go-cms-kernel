package core

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
)

func (s *Services) validateMediaFileOccurrence(ctx context.Context, target file.File, usage media.Usage) error {
	if usage.Occurrence == nil || s.Sites == nil {
		return fmt.Errorf("%w: file occurrence runtime unavailable", media.ErrInvalidReference)
	}
	ref := *usage.Occurrence
	runtime, ok := s.Sites.RuntimeByID(site.ID(ref.SiteID))
	if !ok {
		return fmt.Errorf("%w: occurrence site %d is unavailable", media.ErrInvalidReference, ref.SiteID)
	}
	if ref.OwnerKind != "site" && ref.OwnerKind != "resource" {
		for _, module := range runtime.Profile().Modules() {
			if validator, ok := module.(media.FileOccurrenceValidator); ok && slices.Contains(validator.FileOccurrenceKinds(), ref.OwnerKind) {
				return validator.ValidateMediaFileOccurrence(ctx, ref, target)
			}
		}
		return fmt.Errorf("%w: unsupported occurrence owner %q", media.ErrInvalidReference, ref.OwnerKind)
	}
	reader := s.fileOccurrenceReaders[ref.OwnerKind]
	if reader == nil {
		return fmt.Errorf("%w: occurrence reader %q unavailable", media.ErrInvalidReference, ref.OwnerKind)
	}
	values, err := reader.ReadFileOccurrence(ctx, ref)
	if err != nil {
		return err
	}
	var schema *field.Schema
	if ref.OwnerKind == "site" {
		if values.Code != string(runtime.Site().ProfileCode) {
			return fmt.Errorf("%w: occurrence profile changed", media.ErrInvalidReference)
		}
		schema = runtime.Profile().ParamSchema()
	} else if strings.HasPrefix(ref.Container, "fields:") {
		definition, ok := runtime.Profile().Template(template.Code(values.Code))
		if !ok {
			return fmt.Errorf("%w: occurrence template %q unavailable", media.ErrInvalidReference, values.Code)
		}
		schema = definition.FieldSchema()
	} else if strings.HasPrefix(ref.Container, "widget:") {
		definition, ok := runtime.Profile().Widget(widget.Code(values.Code))
		if !ok {
			return fmt.Errorf("%w: occurrence widget %q unavailable", media.ErrInvalidReference, values.Code)
		}
		schema = definition.FieldSchema()
	} else {
		return fmt.Errorf("%w: invalid occurrence container %q", media.ErrInvalidReference, ref.Container)
	}
	references, err := schema.StoredReferences(values.Values)
	if err != nil {
		return err
	}
	if ref.Target == field.ReferenceMedia {
		for _, reference := range references {
			if reference.Target == ref.Target && reference.ID == int64(ref.MediaID) && slices.Equal(reference.Path, ref.Path) {
				return resource.ValidateImageMediaFile(ctx, target, usage)
			}
		}
		return fmt.Errorf("%w: saved Media occurrence no longer matches its schema", media.ErrInvalidReference)
	}
	return media.ValidateFileOccurrence(ref, references, target)
}
