package forms

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	corefile "github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

type FileFieldTarget struct {
	Owner       string          `json:"owner"`
	ElementType ElementTypeCode `json:"element_type,omitempty"`
	FieldPath   []string        `json:"field_path"`
}

// UploadFieldFile resolves permanent builder selections from saved form fields
// or registered element configuration. Public forms.upload remains transient.
func (s *Service) UploadFieldFile(ctx context.Context, actor security.Actor, formID FormID, target FileFieldTarget, name string, content io.Reader) (corefile.File, error) {
	if err := s.authorizer.Check(ctx, actor, FormUpdatePermission); err != nil {
		return corefile.File{}, err
	}
	if err := s.authorizer.Check(ctx, actor, permission.MustCode("core", "file", permission.Create)); err != nil {
		return corefile.File{}, err
	}
	detail, err := s.repository.FormDetail(ctx, s.siteID, formID)
	if err != nil {
		return corefile.File{}, err
	}
	var definitions []field.Definition
	switch target.Owner {
	case "element":
		item, exists := s.elements.Type(target.ElementType)
		if !exists {
			return corefile.File{}, fmt.Errorf("%w: unknown element type", ErrInvalid)
		}
		definitions, err = elementConfigDefinitions(item.Metadata().Fields)
	case "field":
		if target.ElementType != "" {
			return corefile.File{}, fmt.Errorf("%w: invalid file field target", ErrInvalid)
		}
		for _, item := range detail.Fields {
			definitions = append(definitions, item.Definition())
		}
	default:
		return corefile.File{}, fmt.Errorf("%w: unknown file field owner", ErrInvalid)
	}
	if err != nil {
		return corefile.File{}, err
	}
	options, err := field.FileUploadOptions(definitions, target.FieldPath)
	if err != nil {
		return corefile.File{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	item, err := field.UploadFile(ctx, actor, s.files, options, name, content)
	var validation field.ValidationErrors
	if errors.As(err, &validation) {
		return corefile.File{}, fmt.Errorf("%w: %w", ErrValidation, FieldValidationErrors{field.ReferenceKey(target.FieldPath): {"mime_type"}})
	}
	if errors.Is(err, corefile.ErrInvalidInput) {
		return corefile.File{}, fmt.Errorf("%w: invalid uploaded file", ErrInvalid)
	}
	return item, err
}
