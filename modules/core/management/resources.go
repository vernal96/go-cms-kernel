package management

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
	"github.com/vernal96/go-cms-kernel/modules/resourceextension"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

type ResourceTreeItem struct {
	ID              resource.ID    `json:"id"`
	Version         int64          `json:"version"`
	ParentID        *resource.ID   `json:"parent_id"`
	TemplateCode    *template.Code `json:"template_code"`
	Icon            string         `json:"icon"`
	Title           string         `json:"title"`
	MenuTitle       string         `json:"menu_title"`
	DisplayTitle    string         `json:"display_title"`
	Sort            int            `json:"sort"`
	Deleted         bool           `json:"deleted"`
	Published       bool           `json:"published"`
	InMenu          bool           `json:"in_menu"`
	DeletedAt       *time.Time     `json:"deleted_at"`
	HasChildren     bool           `json:"has_children"`
	CanCreateChild  bool           `json:"can_create_child"`
	CanTransferSite bool           `json:"can_transfer_site"`
}

type ResourceChildren struct {
	Items       []ResourceTreeItem `json:"items"`
	Permissions struct {
		CreateRoot bool `json:"create_root"`
	} `json:"permissions"`
}

func (m *Resources) ResourceChildren(
	ctx context.Context,
	actor security.Actor,
	siteID site.ID,
	parentID *resource.ID,
) (ResourceChildren, error) {
	if err := m.requireSite(ctx, actor, siteID, ResourceReadPermission, SiteAccessEdit); err != nil {
		return ResourceChildren{}, err
	}
	runtime, exists := m.sites.RuntimeByID(siteID)
	if !exists {
		return ResourceChildren{}, site.ErrNotFound
	}
	children, err := m.resourceRepo.ListChildren(ctx, siteID, parentID)
	if err != nil {
		return ResourceChildren{}, fmt.Errorf("list CMS resource children: %w", err)
	}
	canCreate, err := m.allowed(ctx, actor, ResourceCreatePermission)
	if err != nil {
		return ResourceChildren{}, err
	}
	result := ResourceChildren{Items: make([]ResourceTreeItem, len(children))}
	result.Permissions.CreateRoot = canCreate
	for index, child := range children {
		result.Items[index] = treeItem(runtime, child, canCreate)
	}
	return result, nil
}

type ResourceTemplate struct {
	Code                    template.Code             `json:"code"`
	Label                   string                    `json:"label"`
	Icon                    string                    `json:"icon"`
	Fields                  []field.Descriptor        `json:"fields"`
	EditorTabs              []FieldEditorTab          `json:"editor_tabs"`
	SupportsResourceWidgets bool                      `json:"supports_resource_widgets"`
	WidgetAreas             []template.AreaDescriptor `json:"widget_areas"`
	WidgetValueSources      []widget.ValueSource      `json:"widget_value_sources"`
}

type FieldEditorTab struct {
	Code   string   `json:"code"`
	Label  string   `json:"label"`
	Fields []string `json:"fields"`
}

func editorTabs(source []field.EditorTab) []FieldEditorTab {
	result := make([]FieldEditorTab, len(source))
	for index, tab := range source {
		result[index] = FieldEditorTab{
			Code:   tab.Code,
			Label:  tab.Label,
			Fields: append([]string(nil), tab.Fields...),
		}
	}
	return result
}

type WidgetView struct {
	Code  widget.ViewCode `json:"code"`
	Label string          `json:"label"`
}

type WidgetDefinition struct {
	Code              widget.Code                 `json:"code"`
	ModuleCode        string                      `json:"module_code"`
	ModuleLabel       string                      `json:"module_label"`
	ModuleDescription string                      `json:"module_description"`
	Label             string                      `json:"label"`
	Description       string                      `json:"description"`
	Fields            []field.Descriptor          `json:"fields"`
	EditorTabs        []FieldEditorTab            `json:"editor_tabs"`
	SummaryFields     []string                    `json:"summary_fields"`
	Views             []WidgetView                `json:"views"`
	ParamTypes        map[string]field.ValueShape `json:"param_types"`
}

type ResourceType struct {
	Code             resourcetype.Code           `json:"code"`
	Label            string                      `json:"label"`
	Capabilities     ResourceTypeCapabilities    `json:"capabilities"`
	SettingsFields   []field.Descriptor          `json:"settings_fields"`
	SettingsDefaults map[string]any              `json:"settings_defaults"`
	ContentTypes     []ResourceContentTypeOption `json:"content_types"`
}

type ResourceContentTypeOption struct {
	Code   string           `json:"code"`
	Label  string           `json:"label"`
	Editor field.EditorCode `json:"editor"`
}

type ResourceTypeCapabilities struct {
	SupportsTemplate       bool   `json:"supports_template"`
	SupportsContent        bool   `json:"supports_content"`
	SupportsWidgets        bool   `json:"supports_widgets"`
	SupportsFields         bool   `json:"supports_fields"`
	SupportsExternalURL    bool   `json:"supports_external_url"`
	SupportsTargetResource bool   `json:"supports_target_resource"`
	MutableType            bool   `json:"mutable_type"`
	OwnsLibraryItems       bool   `json:"owns_library_items"`
	DefaultIcon            string `json:"default_icon"`
}

type ResourceMetadata struct {
	Types      []ResourceType               `json:"types"`
	Templates  []ResourceTemplate           `json:"templates"`
	Widgets    []WidgetDefinition           `json:"widgets"`
	Extensions []resourceextension.Metadata `json:"extensions"`
}

func (m *Resources) ResourceMetadata(
	ctx context.Context,
	actor security.Actor,
	siteID site.ID,
) (ResourceMetadata, error) {
	if err := m.requireSite(ctx, actor, siteID, ResourceReadPermission, SiteAccessEdit); err != nil {
		return ResourceMetadata{}, err
	}
	runtime, exists := m.sites.RuntimeByID(siteID)
	if !exists {
		return ResourceMetadata{}, site.ErrNotFound
	}
	definitions := runtime.Profile().Templates()
	templates := make([]ResourceTemplate, len(definitions))
	for index, definition := range definitions {
		fields, err := field.DescribeDefinitions(definition.Fields, runtime.Profile().Registry())
		if err != nil {
			return ResourceMetadata{}, err
		}
		templateRuntime, _ := runtime.Profile().Template(definition.Code)
		templates[index] = ResourceTemplate{
			Code:                    definition.Code,
			Label:                   definition.Label,
			Icon:                    iconOrDefault(definition.Icon),
			Fields:                  fields,
			EditorTabs:              editorTabs(definition.EditorTabs),
			SupportsResourceWidgets: templateRuntime.SupportsResourceWidgets(),
			WidgetAreas:             templateRuntime.Areas(),
			WidgetValueSources:      widget.ValueSources(templateRuntime.FieldSchema()),
		}
	}
	widgetDefinitions := runtime.Profile().Widgets()
	widgets := make([]WidgetDefinition, len(widgetDefinitions))
	for index, definition := range widgetDefinitions {
		fields, err := field.DescribeDefinitions(definition.Fields, runtime.Profile().Registry())
		if err != nil {
			return ResourceMetadata{}, err
		}
		views := make([]WidgetView, len(definition.Views))
		for viewIndex, view := range definition.Views {
			views[viewIndex] = WidgetView{Code: view.Code(), Label: view.Label()}
		}
		widgetRuntime, _ := runtime.Profile().Widget(definition.Code)
		paramTypes := make(map[string]field.ValueShape, len(definition.Fields))
		for _, def := range definition.Fields {
			paramTypes[def.Key], _ = widgetRuntime.FieldSchema().Shape(def.Key)
		}
		widgets[index] = WidgetDefinition{
			ParamTypes: paramTypes,
			Code:       definition.Code, ModuleCode: definition.Module.Code,
			ModuleLabel: definition.Module.Label, ModuleDescription: definition.Module.Description,
			Label: definition.Label, Description: definition.Description, Fields: fields,
			EditorTabs: editorTabs(definition.EditorTabs), SummaryFields: append([]string{}, definition.SummaryFields...), Views: views,
		}
	}
	typeCodes := runtime.Profile().Registry().ResourceTypes()
	types := make([]ResourceType, 0, len(typeCodes))
	for _, code := range typeCodes {
		resourceType, exists := runtime.Profile().Registry().ResourceType(code)
		if !exists {
			continue
		}
		metadata := resourceType.Metadata()
		settingsFields, err := field.DescribeDefinitions(metadata.SettingsFields, runtime.Profile().Registry())
		if err != nil {
			return ResourceMetadata{}, fmt.Errorf("resource type %q settings metadata: %w", code, err)
		}
		contentTypes := make([]ResourceContentTypeOption, len(metadata.ContentTypes))
		for index, option := range metadata.ContentTypes {
			contentTypes[index] = ResourceContentTypeOption{Code: option.Code, Label: option.Label, Editor: option.Editor}
		}
		capabilities := metadata.Capabilities
		types = append(types, ResourceType{
			Code: code, Label: metadata.Label,
			Capabilities:   ResourceTypeCapabilities{SupportsTemplate: capabilities.SupportsTemplate, SupportsContent: capabilities.SupportsContent, SupportsWidgets: capabilities.SupportsWidgets, SupportsFields: capabilities.SupportsFields, SupportsExternalURL: capabilities.SupportsExternalURL, SupportsTargetResource: capabilities.SupportsTargetResource, MutableType: capabilities.MutableType, OwnsLibraryItems: capabilities.OwnsLibraryItems, DefaultIcon: capabilities.DefaultIcon},
			SettingsFields: settingsFields, SettingsDefaults: cloneAnyMap(metadata.SettingsDefaults), ContentTypes: contentTypes,
		})
	}
	extensions := make([]resourceextension.Metadata, 0)
	for _, moduleRuntime := range runtime.Profile().Modules() {
		provider, ok := moduleRuntime.(resourceextension.EditorProvider)
		if !ok {
			continue
		}
		editor := provider.ResourceEditorExtension()
		if editor == nil {
			return ResourceMetadata{}, errors.New(
				"resource editor extension is nil",
			)
		}
		extensions = append(extensions, editor.Metadata())
	}
	return ResourceMetadata{
		Types: types, Templates: templates, Widgets: widgets, Extensions: extensions,
	}, nil
}

func (m *Resources) ResourceExtension(
	ctx context.Context,
	actor security.Actor,
	siteID site.ID,
	resourceID resource.ID,
	code resourceextension.Code,
) (any, error) {
	editor, request, err := m.resourceEditorExtension(
		ctx, actor, siteID, resourceID, code, ResourceReadPermission,
	)
	if err != nil {
		return nil, err
	}
	result, err := editor.Read(ctx, request)
	return result, resourceExtensionError(err)
}

func (m *Resources) SaveResourceExtension(
	ctx context.Context,
	actor security.Actor,
	siteID site.ID,
	resourceID resource.ID,
	code resourceextension.Code,
	payload json.RawMessage,
) (any, error) {
	editor, request, err := m.resourceEditorExtension(
		ctx, actor, siteID, resourceID, code, ResourceUpdatePermission,
	)
	if err != nil {
		return nil, err
	}
	result, err := editor.Save(ctx, request, payload)
	return result, resourceExtensionError(err)
}

func (m *Resources) PreviewResourceExtension(
	ctx context.Context,
	actor security.Actor,
	siteID site.ID,
	resourceID resource.ID,
	code resourceextension.Code,
	payload json.RawMessage,
) (any, error) {
	editor, request, err := m.resourceEditorExtension(
		ctx, actor, siteID, resourceID, code, ResourceReadPermission,
	)
	if err != nil {
		return nil, err
	}
	result, err := editor.Preview(ctx, request, payload)
	return result, resourceExtensionError(err)
}

func (m *Resources) resourceEditorExtension(
	ctx context.Context,
	actor security.Actor,
	siteID site.ID,
	resourceID resource.ID,
	code resourceextension.Code,
	permissionCode permission.Code,
) (resourceextension.Editor, resourceextension.Request, error) {
	if code == "" {
		return nil, resourceextension.Request{}, resource.ErrNotFound
	}
	if err := m.requireSite(ctx, actor, siteID, permissionCode, SiteAccessEdit); err != nil {
		return nil, resourceextension.Request{}, err
	}
	item, err := m.resourceEntity(ctx, actor, resourceID)
	if err != nil {
		return nil, resourceextension.Request{}, err
	}
	if item.SiteID != siteID {
		return nil, resourceextension.Request{}, resource.ErrNotFound
	}
	siteRuntime, exists := m.sites.RuntimeByID(siteID)
	if !exists {
		return nil, resourceextension.Request{}, site.ErrNotFound
	}
	for _, moduleRuntime := range siteRuntime.Profile().Modules() {
		provider, ok := moduleRuntime.(resourceextension.EditorProvider)
		if !ok {
			continue
		}
		editor := provider.ResourceEditorExtension()
		if editor == nil || editor.Metadata().Code != code {
			continue
		}
		if !editor.AppliesTo(item.Type) {
			return nil, resourceextension.Request{}, resource.ErrNotFound
		}
		return editor, resourceextension.Request{
			Actor: actor, Site: siteRuntime.Site(), Resource: item,
		}, nil
	}
	return nil, resourceextension.Request{}, resource.ErrNotFound
}

func resourceExtensionError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, resourceextension.ErrNotApplicable) {
		return resource.ErrNotFound
	}
	var validation resourceextension.ValidationError
	if !errors.As(err, &validation) {
		return err
	}
	fields := make([]FieldValidationError, len(validation.Fields))
	for index, field := range validation.Fields {
		fields[index] = FieldValidationError{
			Key: field.Key, Code: "extension", Params: map[string]any{"message": field.Message},
		}
	}
	return ValidationError{Message: validation.Error(), Fields: fields}
}

type ResourceCreateInput struct {
	ParentID         *resource.ID
	Type             resourcetype.Code
	Template         *template.Code
	ContentType      *string
	Content          string
	TargetResourceID *resource.ID
	Title            string
	MenuTitle        string
	Slug             string
	ExternalURL      *string
	Fields           map[string]any
	TypeSettings     map[string]any
}

func (m *Resources) CreateResource(
	ctx context.Context,
	actor security.Actor,
	siteID site.ID,
	input ResourceCreateInput,
) (ResourceTreeItem, error) {
	if err := m.requireSite(ctx, actor, siteID, ResourceCreatePermission, SiteAccessEdit); err != nil {
		return ResourceTreeItem{}, err
	}
	runtime, exists := m.sites.RuntimeByID(siteID)
	if !exists {
		return ResourceTreeItem{}, site.ErrNotFound
	}
	if _, exists := runtime.Profile().Registry().ResourceType(input.Type); !exists {
		return ResourceTreeItem{}, fmt.Errorf("%w: unsupported resource type", ErrValidation)
	}
	if input.ParentID != nil {
		exists, err := m.resourceRepo.ExistsInSite(ctx, siteID, *input.ParentID)
		if err != nil {
			return ResourceTreeItem{}, err
		}
		if !exists {
			return ResourceTreeItem{}, resource.ErrNotFound
		}
	}
	created, err := m.resources.Create(ctx, actor, resource.CreateInput{
		SiteID:           siteID,
		ParentID:         input.ParentID,
		Type:             input.Type,
		Template:         input.Template,
		ContentType:      input.ContentType,
		Content:          input.Content,
		TargetResourceID: input.TargetResourceID,
		Title:            input.Title,
		MenuTitle:        input.MenuTitle,
		Slug:             input.Slug,
		ExternalURL:      input.ExternalURL,
		Fields:           input.Fields,
		TypeSettings:     input.TypeSettings,
	})
	if err != nil {
		if errors.Is(err, resource.ErrNotFound) {
			return ResourceTreeItem{}, err
		}
		return ResourceTreeItem{}, validationError(err)
	}
	return treeItem(runtime, resource.Child{
		ID:            created.ID,
		Version:       created.Version,
		SiteID:        created.SiteID,
		ParentID:      created.ParentID,
		Type:          created.Type,
		Template:      created.Template,
		Title:         created.Title,
		MenuTitle:     created.MenuTitle,
		Sort:          created.Sort,
		IsPublic:      created.IsPublic,
		PublishedAt:   created.PublishedAt,
		UnpublishedAt: created.UnpublishedAt,
		DeletedAt:     created.DeletedAt,
	}, true), nil
}

type ResourceDTO struct {
	ImageMediaID     *media.ID         `json:"image_media_id"`
	ID               resource.ID       `json:"id"`
	SiteID           site.ID           `json:"site_id"`
	Version          int64             `json:"version"`
	ParentID         *resource.ID      `json:"parent_id"`
	Type             resourcetype.Code `json:"type"`
	TemplateCode     *template.Code    `json:"template_code"`
	Title            string            `json:"title"`
	MenuTitle        string            `json:"menu_title"`
	Slug             string            `json:"slug"`
	Path             *string           `json:"path"`
	Annotation       string            `json:"annotation"`
	ContentType      *string           `json:"content_type"`
	Content          string            `json:"content"`
	TargetResourceID *resource.ID      `json:"target_resource_id"`
	ExternalURL      *string           `json:"external_url"`
	IsPublic         bool              `json:"is_public"`
	IsSearchable     bool              `json:"is_searchable"`
	InMenu           bool              `json:"in_menu"`
	InSitemap        bool              `json:"in_sitemap"`
	Sort             int               `json:"sort"`
	PublishedAt      *time.Time        `json:"published_at"`
	UnpublishedAt    *time.Time        `json:"unpublished_at"`
	Deleted          bool              `json:"deleted"`
	DeletedAt        *time.Time        `json:"deleted_at"`
	Fields           map[string]any    `json:"fields"`
	TypeSettings     map[string]any    `json:"type_settings"`
	Widgets          []ResourceWidget  `json:"widgets"`
}

type ResourceWidget struct {
	ID              widget.BindingID     `json:"id"`
	Code            widget.Code          `json:"code"`
	Area            widget.AreaCode      `json:"area"`
	Position        int                  `json:"position"`
	View            widget.ViewCode      `json:"view"`
	Columns         int                  `json:"columns"`
	MarginTop       int                  `json:"margin_top"`
	MarginBottom    int                  `json:"margin_bottom"`
	Enabled         bool                 `json:"enabled"`
	Params          map[string]any       `json:"params"`
	ParamBindings   widget.ParamBindings `json:"param_bindings"`
	ResourceVersion int64                `json:"resource_version,omitempty"`
}

type ResourceDetails struct {
	Resource    ResourceDTO `json:"resource"`
	Permissions struct {
		Update        bool `json:"update"`
		Delete        bool `json:"delete"`
		Restore       bool `json:"restore"`
		HistoryRead   bool `json:"history_read"`
		HistoryDelete bool `json:"history_delete"`
	} `json:"permissions"`
}

type ResourceUpdateInput struct {
	ImageMediaID     *media.ID
	ExpectedVersion  int64
	ParentID         *resource.ID
	Type             resourcetype.Code
	Template         *template.Code
	Title            string
	MenuTitle        string
	Slug             string
	Annotation       string
	ContentType      *string
	Content          string
	TargetResourceID *resource.ID
	ExternalURL      *string
	IsPublic         bool
	IsSearchable     bool
	InMenu           bool
	InSitemap        bool
	Sort             int
	PublishedAt      *time.Time
	UnpublishedAt    *time.Time
	Fields           map[string]any
	TypeSettings     map[string]any
}

func (m *Resources) Resource(
	ctx context.Context,
	actor security.Actor,
	siteID site.ID,
	resourceID resource.ID,
) (ResourceDetails, error) {
	if err := m.requireSite(ctx, actor, siteID, ResourceReadPermission, SiteAccessEdit); err != nil {
		return ResourceDetails{}, err
	}
	item, err := m.resources.Get(ctx, actor, resourceID)
	if err != nil {
		return ResourceDetails{}, err
	}
	if item.SiteID != siteID {
		return ResourceDetails{}, resource.ErrNotFound
	}
	allowed, err := m.allowedPermissions(ctx, actor, []permission.Code{ResourceUpdatePermission, ResourceDeletePermission, resource.HistoryReadPermission, resource.HistoryDeletePermission})
	if err != nil {
		return ResourceDetails{}, err
	}
	canDelete := allowed[ResourceDeletePermission]
	result := ResourceDetails{Resource: resourceDTO(item)}
	result.Permissions.Update = allowed[ResourceUpdatePermission]
	result.Permissions.Delete = canDelete
	result.Permissions.Restore = canDelete
	result.Permissions.HistoryRead = allowed[resource.HistoryReadPermission]
	result.Permissions.HistoryDelete = allowed[resource.HistoryDeletePermission]
	if item.ParentID != nil {
		parent, parentErr := m.resources.Get(ctx, actor, *item.ParentID)
		if parentErr != nil {
			return ResourceDetails{}, parentErr
		}
		result.Permissions.Restore = canDelete && parent.DeletedAt == nil
	}
	return result, nil
}

func (m *Resources) Revisions(ctx context.Context, actor security.Actor, siteID site.ID, resourceID resource.ID, page, perPage int) (resource.RevisionPage, error) {
	if err := m.requireSite(ctx, actor, siteID, resource.HistoryReadPermission, SiteAccessView); err != nil {
		return resource.RevisionPage{}, err
	}
	if err := m.requireResourceSite(ctx, actor, siteID, resourceID); err != nil {
		return resource.RevisionPage{}, err
	}
	return m.revisions.List(ctx, actor, siteID, resourceID, page, perPage)
}

func (m *Resources) Revision(ctx context.Context, actor security.Actor, siteID site.ID, resourceID resource.ID, version int64) (resource.Revision, error) {
	if err := m.requireSite(ctx, actor, siteID, resource.HistoryReadPermission, SiteAccessView); err != nil {
		return resource.Revision{}, err
	}
	if err := m.requireResourceSite(ctx, actor, siteID, resourceID); err != nil {
		return resource.Revision{}, err
	}
	return m.revisions.Get(ctx, actor, siteID, resourceID, version)
}

func (m *Resources) RestoreRevision(ctx context.Context, actor security.Actor, siteID site.ID, resourceID resource.ID, version, expectedVersion int64) (ResourceDetails, error) {
	if err := m.requireSite(ctx, actor, siteID, resource.HistoryReadPermission, SiteAccessEdit); err != nil {
		return ResourceDetails{}, err
	}
	if err := m.requireSite(ctx, actor, siteID, ResourceUpdatePermission, SiteAccessEdit); err != nil {
		return ResourceDetails{}, err
	}
	updated, err := m.revisions.Restore(ctx, actor, siteID, resourceID, version, expectedVersion)
	if err != nil {
		return ResourceDetails{}, validationError(err)
	}
	result := ResourceDetails{Resource: resourceDTO(updated)}
	result.Permissions.Update = true
	result.Permissions.HistoryRead = true
	result.Permissions.HistoryDelete, err = m.allowed(ctx, actor, resource.HistoryDeletePermission)
	return result, err
}

func (m *Resources) PurgeRevisions(ctx context.Context, actor security.Actor, siteID site.ID, resourceID resource.ID) (int64, error) {
	if err := m.requireSite(ctx, actor, siteID, resource.HistoryDeletePermission, SiteAccessEdit); err != nil {
		return 0, err
	}
	if err := m.requireResourceSite(ctx, actor, siteID, resourceID); err != nil {
		return 0, err
	}
	return m.revisions.Purge(ctx, actor, siteID, resourceID)
}

func (m *Resources) UpdateResource(
	ctx context.Context,
	actor security.Actor,
	siteID site.ID,
	resourceID resource.ID,
	input ResourceUpdateInput,
) (ResourceDetails, error) {
	if err := m.requireSite(ctx, actor, siteID, ResourceUpdatePermission, SiteAccessEdit); err != nil {
		return ResourceDetails{}, err
	}
	current, err := m.resources.Get(ctx, actor, resourceID)
	if err != nil {
		return ResourceDetails{}, err
	}
	if current.SiteID != siteID {
		return ResourceDetails{}, resource.ErrNotFound
	}
	runtime, exists := m.sites.RuntimeByID(siteID)
	if !exists {
		return ResourceDetails{}, site.ErrNotFound
	}
	if _, exists := runtime.Profile().Registry().ResourceType(current.Type); !exists {
		return ResourceDetails{}, fmt.Errorf("%w: unsupported current resource type", ErrValidation)
	}
	if _, exists := runtime.Profile().Registry().ResourceType(input.Type); !exists {
		return ResourceDetails{}, fmt.Errorf("%w: unsupported resource type", ErrValidation)
	}
	updated, err := m.resources.Update(ctx, actor, resource.UpdateInput{
		ID:               resourceID,
		ExpectedVersion:  input.ExpectedVersion,
		ParentID:         input.ParentID,
		Type:             input.Type,
		Template:         input.Template,
		ContentType:      input.ContentType,
		Title:            input.Title,
		MenuTitle:        input.MenuTitle,
		Slug:             input.Slug,
		Annotation:       input.Annotation,
		Content:          input.Content,
		ImageMediaID:     input.ImageMediaID,
		TargetResourceID: input.TargetResourceID,
		ExternalURL:      input.ExternalURL,
		IsPublic:         input.IsPublic,
		IsSearchable:     input.IsSearchable,
		InMenu:           input.InMenu,
		InSitemap:        input.InSitemap,
		Sort:             input.Sort,
		PublishedAt:      input.PublishedAt,
		UnpublishedAt:    input.UnpublishedAt,
		Fields:           input.Fields,
		TypeSettings:     input.TypeSettings,
	})
	if err != nil {
		return ResourceDetails{}, validationError(err)
	}
	result := ResourceDetails{Resource: resourceDTO(updated)}
	result.Permissions.Update = true
	canDelete, permissionErr := m.allowed(ctx, actor, ResourceDeletePermission)
	if permissionErr != nil {
		return ResourceDetails{}, permissionErr
	}
	result.Permissions.Delete = canDelete
	result.Permissions.Restore = canDelete
	return result, nil
}
