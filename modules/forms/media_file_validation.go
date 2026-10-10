package forms

import (
	"context"
	"fmt"
	"slices"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
)

func (*Runtime) FileOccurrenceKinds() []string { return []string{"forms.element", "forms.result"} }

func (r *Runtime) ValidateMediaFileOccurrence(ctx context.Context, ref media.FileOccurrence, target file.File) error {
	if r == nil || r.service == nil || int64(r.service.siteID) != ref.SiteID {
		return fmt.Errorf("%w: Forms occurrence runtime unavailable", media.ErrInvalidReference)
	}
	reader, ok := r.service.repository.(media.FileOccurrenceReader)
	if !ok {
		return fmt.Errorf("%w: Forms occurrence reader unavailable", media.ErrInvalidReference)
	}
	values, err := reader.ReadFileOccurrence(ctx, ref)
	if err != nil {
		return err
	}
	if ref.OwnerKind == "forms.result" {
		for _, snapshot := range values.References {
			if snapshot.ID != int64(ref.MediaID) || !slices.Equal(snapshot.Path, ref.Path) {
				continue
			}
			if snapshot.Target == field.ReferenceMedia {
				return resource.ValidateImageMediaFile(ctx, target, media.Usage{})
			}
			ref.Target = snapshot.Target
			return media.ValidateFileOccurrence(ref, []field.Reference{snapshot}, target)
		}
		return fmt.Errorf("%w: Forms result reference snapshot missing", media.ErrInvalidReference)
	}
	schema, err := r.service.elementSchema(ElementTypeCode(values.Code))
	if err != nil {
		return err
	}
	references, err := schema.StoredReferences(values.Values)
	if err != nil {
		return err
	}
	return media.ValidateFileOccurrence(ref, references, target)
}
