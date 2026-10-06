package resource

import (
	"context"
	"errors"
	"fmt"
	"strings"

	kernel "github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
	"github.com/vernal96/go-cms-kernel/security"
)

func (s *Service) validateStored(
	ctx context.Context,
	item Resource,
) (Resource, error) {
	if item.ID <= 0 {
		return Resource{}, errors.New("stored resource id is invalid")
	}
	if item.SiteID <= 0 {
		return Resource{}, errors.New("stored resource site id is invalid")
	}

	siteRuntime, exists := s.runtime(ctx, item.SiteID)
	if !exists {
		return Resource{}, fmt.Errorf(
			"stored resource %d references unknown site %d",
			item.ID,
			item.SiteID,
		)
	}

	storedPath := cloneString(item.Path)
	normalized, err := s.normalize(ctx, security.System(), item, siteRuntime, nil, nil)
	if err != nil {
		return Resource{}, err
	}
	if !equalStrings(storedPath, normalized.Path) {
		return Resource{}, fmt.Errorf(
			"stored resource %d path is inconsistent",
			item.ID,
		)
	}
	return normalized, nil
}

func (s *Service) normalize(
	ctx context.Context,
	actor security.Actor,
	item Resource,
	siteRuntime *site.Runtime,
	known map[ID]Resource,
	trustedFileReferences map[string]file.ID,
) (Resource, error) {
	item = Clone(item)
	item.Title = strings.TrimSpace(item.Title)
	item.MenuTitle = strings.TrimSpace(item.MenuTitle)
	if err := s.validateResourceBasics(ctx, item); err != nil {
		return Resource{}, err
	}

	profileRuntime := siteRuntime.Profile()
	resourceType, parent, payload, err := s.normalizeResourcePayload(ctx, item, profileRuntime, known)
	if err != nil {
		return Resource{}, err
	}
	item, err = s.normalizeResourceTemplate(ctx, actor, item, payload, profileRuntime, trustedFileReferences)
	if err != nil {
		return Resource{}, err
	}
	payload.Fields = item.Fields
	item, err = applyResourcePayload(item, resourceType, parent, payload)
	if err != nil {
		return Resource{}, err
	}
	return item, nil
}

func (s *Service) validateResourceBasics(ctx context.Context, item Resource) error {
	if item.Title == "" {
		return errors.New("resource title is empty")
	}
	if !validSlug(item.Slug, item.ParentID) {
		return fmt.Errorf("resource slug %q is invalid", item.Slug)
	}
	if item.PublishedAt != nil && item.UnpublishedAt != nil && !item.UnpublishedAt.After(*item.PublishedAt) {
		return errors.New("resource unpublished_at must be after published_at")
	}
	if item.ImageMediaID != nil {
		return s.validateImageMedia(ctx, *item.ImageMediaID)
	}
	return nil
}

func (s *Service) normalizeResourcePayload(
	ctx context.Context,
	item Resource,
	profileRuntime *kernel.ProfileRuntime,
	known map[ID]Resource,
) (resourcetype.Type, *Resource, resourcetype.Payload, error) {
	resourceType, exists := profileRuntime.Registry().ResourceType(item.Type)
	if !exists {
		return nil, nil, resourcetype.Payload{}, fmt.Errorf("resource references unknown type %q", item.Type)
	}
	parent, err := s.relatedResource(ctx, item.ParentID, item.SiteID, known, "parent")
	if err != nil {
		return nil, nil, resourcetype.Payload{}, err
	}
	if parent != nil && parent.DeletedAt != nil && item.DeletedAt == nil {
		return nil, nil, resourcetype.Payload{}, ErrInvalidTree
	}
	payload := resourcetype.Payload{
		Template: cloneTemplateCode(item.Template), ContentType: cloneString(item.ContentType),
		Content: item.Content, TargetResourceID: resourceTypeID(item.TargetResourceID),
		ExternalURL: cloneString(item.ExternalURL), Fields: cloneMap(item.Fields),
		TypeSettings: cloneMap(item.TypeSettings),
	}
	payload, err = resourceType.Normalize(payload)
	if err != nil {
		return nil, nil, resourcetype.Payload{}, fmt.Errorf("normalize resource type %q: %w", item.Type, err)
	}
	if item.Type == resourcetype.Library {
		if value, ok := payload.TypeSettings["default_item_template"]; ok {
			code, ok := value.(string)
			if !ok || code == "" {
				return nil, nil, resourcetype.Payload{}, errors.New("library default item template is invalid")
			}
			if _, exists := profileRuntime.Template(template.Code(code)); !exists {
				return nil, nil, resourcetype.Payload{}, fmt.Errorf("library references unknown default item template %q", code)
			}
		}
	}
	if payload.TargetResourceID != nil {
		targetID := ID(*payload.TargetResourceID)
		if targetID == item.ID && item.ID != 0 {
			return nil, nil, resourcetype.Payload{}, errors.New("resource cannot target itself")
		}
		if _, err := s.relatedResource(ctx, &targetID, item.SiteID, known, "target"); err != nil {
			return nil, nil, resourcetype.Payload{}, err
		}
	}
	return resourceType, parent, payload, nil
}

func (s *Service) normalizeResourceTemplate(
	ctx context.Context,
	actor security.Actor,
	item Resource,
	payload resourcetype.Payload,
	profileRuntime *kernel.ProfileRuntime,
	trustedFileReferences map[string]file.ID,
) (Resource, error) {
	if payload.Template == nil {
		if len(item.Widgets) != 0 {
			return Resource{}, errors.New("resource without template has widgets")
		}
		if len(payload.Fields) != 0 {
			return Resource{}, errors.New("resource without template has fields")
		}
		payload.Fields = map[string]any{}
		item.Fields = payload.Fields
		return item, nil
	}
	templateRuntime, exists := profileRuntime.Template(*payload.Template)
	if !exists {
		return Resource{}, fmt.Errorf("resource references unknown template %q", *payload.Template)
	}
	widgets, err := normalizeWidgetBindings(profileRuntime, templateRuntime, item.Widgets)
	if err != nil {
		return Resource{}, err
	}
	item.Widgets = widgets
	fields, err := templateRuntime.FieldSchema().Validate(payload.Fields)
	if err != nil {
		return Resource{}, fmt.Errorf("validate resource template %q fields: %w", *payload.Template, err)
	}
	item.FieldValues, err = templateRuntime.FieldSchema().StoredValues(fields)
	if err != nil {
		return Resource{}, fmt.Errorf("encode resource template %q fields: %w", *payload.Template, err)
	}
	if err := s.validateMediaFields(ctx, actor, item.FieldValues); err != nil {
		return Resource{}, err
	}
	fileReferences, err := templateRuntime.FieldSchema().FileReferences(fields)
	if err != nil {
		return Resource{}, fmt.Errorf("collect resource file references: %w", err)
	}
	if err := s.validateFileReferences(ctx, actor, fileReferences, trustedFileReferences); err != nil {
		return Resource{}, err
	}
	item.FileReferences = resourceFileReferenceMap(fileReferences)
	item.Fields = fields
	return item, nil
}

func applyResourcePayload(
	item Resource,
	resourceType resourcetype.Type,
	parent *Resource,
	payload resourcetype.Payload,
) (Resource, error) {
	var err error
	switch resourceType.PathMode() {
	case resourcetype.PathRoute:
		item.Path, err = BuildPath(parent, item.Slug)
		if err != nil {
			return Resource{}, err
		}
	case resourcetype.PathNone:
		item.Path = nil
	default:
		return Resource{}, fmt.Errorf("resource type %q has invalid path mode %q", item.Type, resourceType.PathMode())
	}
	item.Template = cloneTemplateCode(payload.Template)
	item.ContentType = cloneString(payload.ContentType)
	item.Content = payload.Content
	item.TargetResourceID = resourceID(payload.TargetResourceID)
	item.ExternalURL = cloneString(payload.ExternalURL)
	item.Fields = cloneMap(payload.Fields)
	item.TypeSettings = cloneMap(payload.TypeSettings)
	return item, nil
}

func (s *Service) validateFileReferences(ctx context.Context, actor security.Actor, references []field.FileReference, trusted map[string]file.ID) error {
	if len(references) == 0 {
		return nil
	}
	if s.files == nil {
		return errors.New("resource file service is unavailable")
	}
	for _, reference := range references {
		if trusted[reference.Key] == file.ID(reference.ID) {
			continue
		}
		item, err := s.files.GetFile(ctx, actor, file.ID(reference.ID))
		if err != nil {
			return fmt.Errorf("file field %q: %w", reference.Key, err)
		}
		if !field.FileMatches(reference.Options, item.Storage, item.MIMEType) {
			return fmt.Errorf("file field %q rejects selected file", reference.Key)
		}
	}
	return nil
}

func resourceFileReferenceMap(references []field.FileReference) map[string]file.ID {
	if len(references) == 0 {
		return nil
	}
	result := make(map[string]file.ID, len(references))
	for _, reference := range references {
		result[reference.Key] = file.ID(reference.ID)
	}
	return result
}

func normalizeWidgetBindings(
	profileRuntime interface {
		Widget(widget.Code) (*widget.Runtime, bool)
	},
	templateRuntime interface {
		FieldSchema() *field.Schema
	},
	source []widget.Binding,
) ([]widget.Binding, error) {
	if profileRuntime == nil {
		return nil, errors.New("resource widget profile runtime is nil")
	}
	if templateRuntime == nil {
		return nil, errors.New("resource widget template runtime is nil")
	}
	if source == nil {
		return nil, nil
	}

	result := make([]widget.Binding, len(source))
	positions := make(map[widget.AreaCode]int)
	ids := make(map[widget.BindingID]struct{}, len(source))
	for index, binding := range source {
		if binding.ID <= 0 {
			return nil, fmt.Errorf("resource widget at index %d has invalid id %d", index, binding.ID)
		}
		if _, exists := ids[binding.ID]; exists {
			return nil, fmt.Errorf("resource widget id %d is duplicated", binding.ID)
		}
		ids[binding.ID] = struct{}{}
		if !widget.ValidArea(binding.Area) {
			return nil, fmt.Errorf("resource widget %d uses unsupported area %q", binding.ID, binding.Area)
		}
		if binding.Position != positions[binding.Area] {
			return nil, fmt.Errorf("resource widget %d in %q has position %d instead of %d", binding.ID, binding.Area, binding.Position, positions[binding.Area])
		}
		positions[binding.Area]++

		runtime, exists := profileRuntime.Widget(binding.Code)
		if !exists {
			return nil, fmt.Errorf(
				"resource references unknown widget %q",
				binding.Code,
			)
		}
		presentation := binding.Presentation
		presentation.View = widget.NormalizeView(presentation.View)
		if err := runtime.ValidatePresentation(presentation); err != nil {
			return nil, fmt.Errorf("validate resource widget %q presentation: %w", binding.Code, err)
		}
		params, err := runtime.NormalizeConfiguration(binding.Params, binding.ParamBindings, templateRuntime.FieldSchema())
		if err != nil {
			return nil, fmt.Errorf(
				"validate resource widget %q params: %w",
				binding.Code,
				err,
			)
		}

		result[index] = widget.CloneBinding(binding)
		result[index].Presentation = presentation
		result[index].Params = params
	}
	return result, nil
}

func (s *Service) validateImageMedia(
	ctx context.Context,
	id media.ID,
) error {
	if id <= 0 {
		return fmt.Errorf(
			"%w: resource image media id is invalid",
			ErrInvalidReference,
		)
	}

	resolved, err := s.media.Resolve(
		ctx,
		security.System(),
		id,
	)
	if err != nil {
		return fmt.Errorf(
			"%w: resolve resource image media %d: %v",
			ErrInvalidReference,
			id,
			err,
		)
	}
	return ValidateImageMediaFile(
		ctx,
		resolved.File,
		media.Usage{
			Kind: ImageMediaUsage,
		},
	)
}

func ValidateImageMediaFile(
	ctx context.Context,
	linkedFile file.File,
	_ media.Usage,
) error {
	if err := validateContext(ctx, "validate resource image media"); err != nil {
		return err
	}
	if !strings.HasPrefix(
		strings.ToLower(linkedFile.MIMEType),
		"image/",
	) {
		return fmt.Errorf(
			"%w: file %d has MIME type %q instead of image/*",
			ErrInvalidReference,
			linkedFile.ID,
			linkedFile.MIMEType,
		)
	}
	return nil
}

func (s *Service) relatedResource(
	ctx context.Context,
	id *ID,
	siteID site.ID,
	known map[ID]Resource,
	role string,
) (*Resource, error) {
	if id == nil {
		return nil, nil
	}
	if *id <= 0 {
		return nil, fmt.Errorf("resource %s id is invalid", role)
	}

	var (
		item Resource
		err  error
	)
	if known != nil {
		var exists bool
		item, exists = known[*id]
		if !exists {
			return nil, fmt.Errorf(
				"resource %s %d not found",
				role,
				*id,
			)
		}
	} else {
		item, err = s.repository.ByID(ctx, *id)
		if err != nil {
			return nil, fmt.Errorf(
				"%w: get resource %s %d: %w",
				errPersistence,
				role,
				*id,
				err,
			)
		}
	}
	if item.SiteID != siteID {
		return nil, fmt.Errorf(
			"resource %s %d belongs to another site",
			role,
			*id,
		)
	}

	item = Clone(item)
	return &item, nil
}
