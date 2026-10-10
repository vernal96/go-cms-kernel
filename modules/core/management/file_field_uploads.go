package management

import (
	"context"
	"errors"
	"fmt"
	"io"

	kernel "github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
	"github.com/vernal96/go-cms-kernel/security"
)

// FileFieldTarget identifies a trusted owner definition, including an editor
// draft. Storage configuration is never accepted from the caller.
type FileFieldTarget struct {
	Owner        string             `json:"owner"`
	SiteID       site.ID            `json:"site_id,omitempty"`
	ProfileCode  kernel.ProfileCode `json:"profile_code,omitempty"`
	ResourceID   resource.ID        `json:"resource_id,omitempty"`
	TemplateCode template.Code      `json:"template_code,omitempty"`
	WidgetCode   widget.Code        `json:"widget_code,omitempty"`
	FieldPath    []string           `json:"field_path"`
}

func (m *Sites) siteFileFields(ctx context.Context, actor security.Actor, target FileFieldTarget) ([]field.Definition, error) {
	if target.ResourceID != 0 || target.TemplateCode != "" || target.WidgetCode != "" {
		return nil, fmt.Errorf("%w: invalid site file field target", ErrValidation)
	}
	if target.SiteID == 0 {
		if err := m.authorizer.Check(ctx, actor, SiteCreatePermission); err != nil {
			return nil, err
		}
		blueprint, exists := m.profileSource.ProfileBlueprint(target.ProfileCode)
		if !exists {
			return nil, fmt.Errorf("%w: unknown profile", ErrValidation)
		}
		return blueprint.Profile().Params, nil
	}
	if err := m.requireSite(ctx, actor, target.SiteID, SiteUpdatePermission, SiteAccessEdit); err != nil {
		return nil, err
	}
	runtime, exists := m.sites.RuntimeByID(target.SiteID)
	if !exists {
		return nil, site.ErrNotFound
	}
	if target.ProfileCode != "" {
		blueprint, exists := m.profileSource.ProfileBlueprint(target.ProfileCode)
		if !exists {
			return nil, fmt.Errorf("%w: unknown profile", ErrValidation)
		}
		return blueprint.Profile().Params, nil
	}
	return runtime.Profile().Profile().Params, nil
}

func (m *Resources) resourceFileFields(ctx context.Context, actor security.Actor, target FileFieldTarget) ([]field.Definition, error) {
	if target.ProfileCode != "" || target.ResourceID < 0 {
		return nil, fmt.Errorf("%w: invalid resource file field target", ErrValidation)
	}
	code := ResourceCreatePermission
	if target.ResourceID > 0 {
		code = ResourceUpdatePermission
	}
	if err := m.requireSite(ctx, actor, target.SiteID, code, SiteAccessEdit); err != nil {
		return nil, err
	}
	runtime, exists := m.sites.RuntimeByID(target.SiteID)
	if !exists {
		return nil, site.ErrNotFound
	}
	if target.ResourceID > 0 {
		current, err := m.resourceEntity(ctx, actor, target.ResourceID)
		if err != nil {
			return nil, err
		}
		if current.SiteID != target.SiteID {
			return nil, resource.ErrNotFound
		}
		if target.TemplateCode == "" && current.Template != nil {
			target.TemplateCode = *current.Template
		}
	}
	if target.Owner == "widget" {
		if target.TemplateCode != "" && target.ResourceID == 0 {
			return nil, fmt.Errorf("%w: invalid widget file field target", ErrValidation)
		}
		item, exists := runtime.Profile().Widget(target.WidgetCode)
		if !exists {
			return nil, fmt.Errorf("%w: unknown widget", ErrValidation)
		}
		return item.Definition().Fields, nil
	}
	if target.WidgetCode != "" {
		return nil, fmt.Errorf("%w: invalid resource file field target", ErrValidation)
	}
	item, exists := runtime.Profile().Template(target.TemplateCode)
	if !exists {
		return nil, fmt.Errorf("%w: unknown template", ErrValidation)
	}
	return item.Definition().Fields, nil
}

func (m *Files) UploadFieldFile(ctx context.Context, actor security.Actor, sites *Sites, resources *Resources, target FileFieldTarget, name string, content io.Reader) (FilesystemItemDTO, error) {
	if err := m.authorizer.Check(ctx, actor, FileCreatePermission); err != nil {
		return FilesystemItemDTO{}, err
	}
	var definitions []field.Definition
	var err error
	switch target.Owner {
	case "site":
		definitions, err = sites.siteFileFields(ctx, actor, target)
	case "resource", "widget":
		definitions, err = resources.resourceFileFields(ctx, actor, target)
	default:
		err = fmt.Errorf("%w: invalid file field owner", ErrValidation)
	}
	if err != nil {
		return FilesystemItemDTO{}, err
	}
	options, err := field.FileUploadOptions(definitions, target.FieldPath)
	if err != nil {
		return FilesystemItemDTO{}, fmt.Errorf("%w: %v", ErrValidation, err)
	}
	item, err := field.UploadFile(ctx, actor, m.files, options, name, content)
	if err != nil {
		var validation field.ValidationErrors
		if errors.As(err, &validation) {
			for index := range validation {
				validation[index].Key = field.ReferenceKey(target.FieldPath)
			}
			return FilesystemItemDTO{}, validationError(validation)
		}
		return FilesystemItemDTO{}, fileValidationError(err)
	}
	return fileItemDTO(item), nil
}
