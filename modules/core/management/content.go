package management

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/group"
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

var ErrValidation = errors.New("CMS management validation failed")

const (
	SiteReadPermission       permission.Code = "core.site.read"
	SiteCreatePermission     permission.Code = "core.site.create"
	SiteUpdatePermission     permission.Code = "core.site.update"
	SiteDeletePermission     permission.Code = "core.site.delete"
	ResourceReadPermission   permission.Code = "core.resource.read"
	ResourceCreatePermission permission.Code = "core.resource.create"
	ResourceUpdatePermission permission.Code = "core.resource.update"
	ResourceDeletePermission permission.Code = "core.resource.delete"
)

type SiteAccessScope struct {
	All     bool
	SiteIDs []site.ID
}

type SiteAccessAction = group.SiteAccessAction

const (
	SiteAccessView   = group.SiteAccessView
	SiteAccessEdit   = group.SiteAccessEdit
	SiteAccessDelete = group.SiteAccessDelete
)

type SiteAccessPolicy interface {
	Scope(context.Context, security.Actor, SiteAccessAction) (SiteAccessScope, error)
	Check(context.Context, security.Actor, site.ID, SiteAccessAction) error
}

type AllowAllSitesPolicy struct{}

func (AllowAllSitesPolicy) Scope(context.Context, security.Actor, SiteAccessAction) (SiteAccessScope, error) {
	return SiteAccessScope{All: true}, nil
}

func (AllowAllSitesPolicy) Check(context.Context, security.Actor, site.ID, SiteAccessAction) error {
	return nil
}

type ProfileResolver interface {
	ProfileBlueprint(kernel.ProfileCode) (*kernel.ProfileBlueprint, bool)
}

type SiteCatalog interface {
	RuntimeByID(site.ID) (*site.Runtime, bool)
	Create(context.Context, security.Actor, site.CreateInput) (*site.Runtime, error)
	Update(context.Context, security.Actor, site.UpdateInput) (*site.Runtime, error)
	Delete(context.Context, security.Actor, site.ID) error
}

type authorization struct {
	sites      SiteCatalog
	authorizer security.Authorizer
	policy     SiteAccessPolicy
}

// Sites owns site listing, lifecycle orchestration, capabilities, and profile
// metadata. Runtime construction/publication remains owned by SiteCatalog.
type Sites struct {
	authorization
	profiles      []kernel.Profile
	profileSource ProfileResolver
	repository    site.ManagementRepository
	resources     *resource.Service
}

type SiteDependencies struct {
	Profiles         []kernel.Profile
	ProfileSource    ProfileResolver
	SiteRepository   site.ManagementRepository
	Sites            SiteCatalog
	Resources        *resource.Service
	Authorizer       security.Authorizer
	SiteAccessPolicy SiteAccessPolicy
}

func NewSites(dependencies SiteDependencies) (*Sites, error) {
	if dependencies.ProfileSource == nil {
		return nil, errors.New("CMS profile source is nil")
	}
	if dependencies.SiteRepository == nil || dependencies.Sites == nil {
		return nil, errors.New("CMS site dependencies are nil")
	}
	if dependencies.Resources == nil {
		return nil, errors.New("CMS resource service is nil")
	}
	if dependencies.Authorizer == nil {
		return nil, errors.New("CMS authorizer is nil")
	}
	if dependencies.SiteAccessPolicy == nil {
		return nil, errors.New("CMS site access policy is nil")
	}
	profiles := append([]kernel.Profile(nil), dependencies.Profiles...)
	return &Sites{
		authorization: authorization{
			sites: dependencies.Sites, authorizer: dependencies.Authorizer,
			policy: dependencies.SiteAccessPolicy,
		},
		profiles:      profiles,
		profileSource: dependencies.ProfileSource,
		repository:    dependencies.SiteRepository,
		resources:     dependencies.Resources,
	}, nil
}

// Resources owns resource, widget, extension, and menu orchestration.
type Resources struct {
	authorization
	resources     *resource.Service
	libraryItems  *resource.LibraryService
	revisions     *resource.RevisionService
	administrator interface {
		IsAdministrator(context.Context, security.Actor) (bool, error)
	}
	resourceRepo resource.ManagementRepository
}

type ResourceDependencies struct {
	Sites         SiteCatalog
	Resources     *resource.Service
	LibraryItems  *resource.LibraryService
	Revisions     *resource.RevisionService
	Administrator interface {
		IsAdministrator(context.Context, security.Actor) (bool, error)
	}
	ResourceRepository resource.ManagementRepository
	Authorizer         security.Authorizer
	SiteAccessPolicy   SiteAccessPolicy
}

func NewResources(dependencies ResourceDependencies) (*Resources, error) {
	if dependencies.Sites == nil || dependencies.Resources == nil || dependencies.Revisions == nil || dependencies.ResourceRepository == nil {
		return nil, errors.New("CMS resource dependencies are nil")
	}
	if dependencies.Authorizer == nil || dependencies.SiteAccessPolicy == nil || dependencies.Administrator == nil {
		return nil, errors.New("CMS resource authorization dependencies are nil")
	}
	return &Resources{
		authorization: authorization{
			sites: dependencies.Sites, authorizer: dependencies.Authorizer,
			policy: dependencies.SiteAccessPolicy,
		},
		resources: dependencies.Resources, resourceRepo: dependencies.ResourceRepository,
		libraryItems: dependencies.LibraryItems, revisions: dependencies.Revisions, administrator: dependencies.Administrator,
	}, nil
}

func (m *Resources) AdministrationRevisionCount(ctx context.Context, actor security.Actor) (int64, error) {
	allowed, err := m.administrator.IsAdministrator(ctx, actor)
	if err != nil {
		return 0, err
	}
	if !allowed {
		return 0, security.ErrForbidden
	}
	return m.revisions.CountAll(ctx)
}

func (m *Resources) AdministrationPurgeRevisions(ctx context.Context, actor security.Actor) (int64, error) {
	allowed, err := m.administrator.IsAdministrator(ctx, actor)
	if err != nil {
		return 0, err
	}
	if !allowed {
		return 0, security.ErrForbidden
	}
	return m.revisions.PurgeAll(ctx)
}

type Pagination struct {
	Page    int `json:"page"`
	PerPage int `json:"per_page"`
	Total   int `json:"total"`
}

type PermissionSet struct {
	Read   bool `json:"read"`
	Create bool `json:"create"`
	Update bool `json:"update"`
	Delete bool `json:"delete"`
}

type SiteDTO struct {
	ID           site.ID            `json:"id"`
	ProfileCode  kernel.ProfileCode `json:"profile_code"`
	Domain       string             `json:"domain"`
	Locale       string             `json:"locale"`
	Settings     map[string]any     `json:"settings"`
	IsPublic     bool               `json:"is_public"`
	Capabilities SiteCapabilities   `json:"capabilities"`
}

type SiteCapabilities struct {
	View   bool `json:"view"`
	Edit   bool `json:"edit"`
	Delete bool `json:"delete"`
}

type SiteOption struct {
	ID     site.ID `json:"id"`
	Domain string  `json:"domain"`
}

type SiteList struct {
	Items       []SiteDTO     `json:"items"`
	Pagination  Pagination    `json:"pagination"`
	Permissions PermissionSet `json:"permissions"`
}

type SiteOptions struct {
	Items      []SiteOption `json:"items"`
	Pagination Pagination   `json:"pagination"`
}

type SiteDetails struct {
	Site        SiteDTO       `json:"site"`
	Permissions PermissionSet `json:"permissions"`
}

type SiteProfile struct {
	Code       kernel.ProfileCode `json:"code"`
	Name       string             `json:"name"`
	Fields     []field.Descriptor `json:"fields"`
	EditorTabs []FieldEditorTab   `json:"editor_tabs"`
}

type SiteProfiles struct {
	Items []SiteProfile `json:"items"`
}

type SiteCreateInput struct {
	ProfileCode kernel.ProfileCode
	Domain      string
	Locale      string
	Settings    map[string]any
	IsPublic    bool
}

type SiteUpdateInput struct {
	ProfileCode kernel.ProfileCode
	Domain      string
	Locale      string
	Settings    map[string]any
	IsPublic    bool
}

func (m *Sites) ListSites(
	ctx context.Context,
	actor security.Actor,
	search string,
	page int,
	perPage int,
) (SiteList, error) {
	if err := m.authorizer.Check(ctx, actor, SiteReadPermission); err != nil {
		return SiteList{}, err
	}
	page, perPage, err := normalizePagination(page, perPage)
	if err != nil {
		return SiteList{}, err
	}
	scope, err := m.policy.Scope(ctx, actor, SiteAccessView)
	if err != nil {
		return SiteList{}, err
	}
	result, err := m.repository.ListPage(ctx, site.ListQuery{
		Search:  search,
		Page:    page,
		PerPage: perPage,
		Scope:   site.Scope{All: scope.All, SiteIDs: append([]site.ID(nil), scope.SiteIDs...)},
	})
	if err != nil {
		return SiteList{}, fmt.Errorf("list CMS sites: %w", err)
	}
	capabilities, err := m.siteCapabilities(ctx, actor, result.Items, &scope)
	if err != nil {
		return SiteList{}, err
	}
	items := make([]SiteDTO, len(result.Items))
	for index, item := range result.Items {
		items[index] = siteDTO(item, capabilities[item.ID])
	}
	permissions, err := m.sitePermissions(ctx, actor)
	if err != nil {
		return SiteList{}, err
	}
	return SiteList{
		Items:       items,
		Pagination:  Pagination{Page: page, PerPage: perPage, Total: result.Total},
		Permissions: permissions,
	}, nil
}

func (m *Sites) ListSiteOptions(
	ctx context.Context,
	actor security.Actor,
	search string,
	page int,
	perPage int,
	excludeIDs ...site.ID,
) (SiteOptions, error) {
	if err := m.authorizer.Check(ctx, actor, SiteReadPermission); err != nil {
		return SiteOptions{}, err
	}
	page, perPage, err := normalizePagination(page, perPage)
	if err != nil {
		return SiteOptions{}, err
	}
	scope, err := m.policy.Scope(ctx, actor, SiteAccessEdit)
	if err != nil {
		return SiteOptions{}, err
	}
	var excludeID *site.ID
	if len(excludeIDs) > 0 && excludeIDs[0] > 0 {
		value := excludeIDs[0]
		excludeID = &value
	}
	result, err := m.repository.ListPage(ctx, site.ListQuery{
		Search: search, Page: page, PerPage: perPage,
		Scope:     site.Scope{All: scope.All, SiteIDs: append([]site.ID(nil), scope.SiteIDs...)},
		ExcludeID: excludeID,
	})
	if err != nil {
		return SiteOptions{}, fmt.Errorf("list CMS site options: %w", err)
	}
	items := make([]SiteOption, len(result.Items))
	for index, item := range result.Items {
		items[index] = SiteOption{ID: item.ID, Domain: item.Domain}
	}
	return SiteOptions{Items: items, Pagination: Pagination{Page: page, PerPage: perPage, Total: result.Total}}, nil
}

func (m *Sites) Site(
	ctx context.Context,
	actor security.Actor,
	id site.ID,
) (SiteDetails, error) {
	if err := m.requireSite(ctx, actor, id, SiteReadPermission, SiteAccessEdit); err != nil {
		return SiteDetails{}, err
	}
	runtime, exists := m.sites.RuntimeByID(id)
	if !exists {
		return SiteDetails{}, site.ErrNotFound
	}
	permissions, err := m.sitePermissions(ctx, actor)
	if err != nil {
		return SiteDetails{}, err
	}
	capabilities, err := m.siteCapabilities(ctx, actor, []site.Site{runtime.Site()}, nil)
	if err != nil {
		return SiteDetails{}, err
	}
	return SiteDetails{Site: siteDTO(runtime.Site(), capabilities[id]), Permissions: permissions}, nil
}

func (m *Sites) CreateSite(
	ctx context.Context,
	actor security.Actor,
	input SiteCreateInput,
) (SiteDetails, error) {
	if err := m.authorizer.Check(ctx, actor, SiteCreatePermission); err != nil {
		return SiteDetails{}, err
	}
	runtime, err := m.sites.Create(ctx, actor, site.CreateInput{
		ProfileCode: input.ProfileCode,
		Domain:      input.Domain,
		Locale:      input.Locale,
		Settings:    input.Settings,
		IsPublic:    input.IsPublic,
	})
	if err != nil {
		return SiteDetails{}, validationError(err)
	}
	_, err = m.resources.Create(ctx, security.System(), resource.CreateInput{
		SiteID: runtime.Site().ID,
		Type:   resourcetype.Page,
		Title:  "Первая страница",
		Slug:   "",
		Fields: map[string]any{},
	})
	if err != nil {
		rollbackErr := m.sites.Delete(ctx, security.System(), runtime.Site().ID)
		if rollbackErr != nil {
			return SiteDetails{}, fmt.Errorf(
				"create initial resource: %v; rollback site: %w",
				err,
				rollbackErr,
			)
		}
		return SiteDetails{}, validationError(err)
	}
	permissions, err := m.sitePermissions(ctx, actor)
	if err != nil {
		return SiteDetails{}, err
	}
	capabilities, err := m.siteCapabilities(ctx, actor, []site.Site{runtime.Site()}, nil)
	if err != nil {
		return SiteDetails{}, err
	}
	return SiteDetails{Site: siteDTO(runtime.Site(), capabilities[runtime.Site().ID]), Permissions: permissions}, nil
}

func (m *Sites) UpdateSite(
	ctx context.Context,
	actor security.Actor,
	id site.ID,
	input SiteUpdateInput,
) (SiteDetails, error) {
	if err := m.requireSite(ctx, actor, id, SiteUpdatePermission, SiteAccessEdit); err != nil {
		return SiteDetails{}, err
	}
	if _, exists := m.sites.RuntimeByID(id); !exists {
		return SiteDetails{}, site.ErrNotFound
	}
	runtime, err := m.sites.Update(ctx, actor, site.UpdateInput{
		ID:          id,
		ProfileCode: input.ProfileCode,
		Domain:      input.Domain,
		Locale:      input.Locale,
		Settings:    input.Settings,
		IsPublic:    input.IsPublic,
	})
	if err != nil {
		return SiteDetails{}, validationError(err)
	}
	permissions, err := m.sitePermissions(ctx, actor)
	if err != nil {
		return SiteDetails{}, err
	}
	capabilities, err := m.siteCapabilities(ctx, actor, []site.Site{runtime.Site()}, nil)
	if err != nil {
		return SiteDetails{}, err
	}
	return SiteDetails{Site: siteDTO(runtime.Site(), capabilities[runtime.Site().ID]), Permissions: permissions}, nil
}

func (m *Sites) DeleteSite(
	ctx context.Context,
	actor security.Actor,
	id site.ID,
) error {
	if err := m.requireSite(ctx, actor, id, SiteDeletePermission, SiteAccessDelete); err != nil {
		return err
	}
	return m.sites.Delete(ctx, actor, id)
}

func (m *Sites) Profiles(
	ctx context.Context,
	actor security.Actor,
) (SiteProfiles, error) {
	if err := m.authorizer.Check(ctx, actor, SiteReadPermission); err != nil {
		return SiteProfiles{}, err
	}
	items := make([]SiteProfile, 0, len(m.profiles))
	for _, profile := range m.profiles {
		blueprint, exists := m.profileSource.ProfileBlueprint(profile.Code)
		if !exists {
			continue
		}
		fields, err := field.DescribeDefinitions(blueprint.ParamSchema().Definitions(), blueprint.Registry())
		if err != nil {
			return SiteProfiles{}, err
		}
		for i, definition := range blueprint.ParamSchema().Definitions() {
			public := definition.Public
			fields[i].Public = &public
		}
		items = append(items, SiteProfile{
			Code:       profile.Code,
			Name:       profile.Name,
			Fields:     fields,
			EditorTabs: editorTabs(blueprint.Profile().EditorTabs),
		})
	}
	return SiteProfiles{Items: items}, nil
}

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
	Code                    template.Code        `json:"code"`
	Label                   string               `json:"label"`
	Icon                    string               `json:"icon"`
	Fields                  []field.Descriptor   `json:"fields"`
	EditorTabs              []FieldEditorTab     `json:"editor_tabs"`
	SupportsResourceWidgets bool                 `json:"supports_resource_widgets"`
	WidgetAreas             []widget.AreaCode    `json:"widget_areas"`
	WidgetValueSources      []widget.ValueSource `json:"widget_value_sources"`
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
			WidgetAreas:             templateRuntime.ResourceAreas(),
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
			Key: field.Key, Rule: "extension", Param: field.Message,
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
	canUpdate, err := m.allowed(ctx, actor, ResourceUpdatePermission)
	if err != nil {
		return ResourceDetails{}, err
	}
	result := ResourceDetails{Resource: resourceDTO(item)}
	result.Permissions.Update = canUpdate
	canDelete, err := m.allowed(ctx, actor, ResourceDeletePermission)
	if err != nil {
		return ResourceDetails{}, err
	}
	result.Permissions.Delete = canDelete
	result.Permissions.Restore = canDelete
	result.Permissions.HistoryRead, err = m.allowed(ctx, actor, resource.HistoryReadPermission)
	if err != nil {
		return ResourceDetails{}, err
	}
	result.Permissions.HistoryDelete, err = m.allowed(ctx, actor, resource.HistoryDeletePermission)
	if err != nil {
		return ResourceDetails{}, err
	}
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

type LibraryItemDTO struct {
	ImageMediaID  *media.ID        `json:"image_media_id"`
	ID            resource.ID      `json:"id"`
	Version       int64            `json:"version"`
	SiteID        site.ID          `json:"site_id"`
	LibraryID     resource.ID      `json:"library_id"`
	TemplateCode  *template.Code   `json:"template_code"`
	Title         string           `json:"title"`
	Slug          string           `json:"slug"`
	Annotation    string           `json:"annotation"`
	ContentType   *string          `json:"content_type"`
	Content       string           `json:"content"`
	IsPublic      bool             `json:"is_public"`
	IsSearchable  bool             `json:"is_searchable"`
	PublishedAt   *time.Time       `json:"published_at"`
	UnpublishedAt *time.Time       `json:"unpublished_at"`
	Deleted       bool             `json:"deleted"`
	DeletedAt     *time.Time       `json:"deleted_at"`
	Fields        map[string]any   `json:"fields"`
	Widgets       []ResourceWidget `json:"widgets"`
	EffectiveURL  string           `json:"effective_url"`
}

type LibraryItemDetails struct {
	Item        LibraryItemDTO `json:"item"`
	Permissions struct {
		Update        bool `json:"update"`
		Delete        bool `json:"delete"`
		Restore       bool `json:"restore"`
		HistoryRead   bool `json:"history_read"`
		HistoryDelete bool `json:"history_delete"`
	} `json:"permissions"`
}

type LibraryItemsPage struct {
	Items      []LibraryItemDTO `json:"items"`
	NextCursor string           `json:"next_cursor"`
}

type LibraryItemsInput struct {
	Cursor  string
	Limit   int
	Search  string
	Filters []resource.FilterCondition
	Sort    []resource.Sort
}

type LibraryItemCreateInput struct {
	ImageMediaID  *media.ID
	Template      *template.Code
	Title         string
	Slug          string
	Annotation    string
	Content       string
	IsPublic      *bool
	IsSearchable  *bool
	PublishedAt   *time.Time
	UnpublishedAt *time.Time
	Fields        map[string]any
}

type LibraryItemUpdateInput struct {
	LibraryItemCreateInput
	ExpectedVersion int64
	IsPublic        *bool
	IsSearchable    *bool
}

func (m *Resources) LibraryItems(ctx context.Context, actor security.Actor, siteID site.ID, libraryID resource.ID, input LibraryItemsInput) (LibraryItemsPage, error) {
	if m.libraryItems == nil {
		return LibraryItemsPage{}, errors.New("library item service is unavailable")
	}
	if err := m.requireSite(ctx, actor, siteID, ResourceReadPermission, SiteAccessEdit); err != nil {
		return LibraryItemsPage{}, err
	}
	page, err := m.libraryItems.Query(ctx, actor, resource.LibraryItemQuery{
		SiteID: siteID, LibraryID: libraryID, Cursor: input.Cursor, Limit: input.Limit,
		Search: input.Search, Filters: input.Filters, Sort: input.Sort,
	})
	if err != nil {
		return LibraryItemsPage{}, validationError(err)
	}
	library, err := m.resources.Get(ctx, actor, libraryID)
	if err != nil {
		return LibraryItemsPage{}, err
	}
	result := LibraryItemsPage{Items: make([]LibraryItemDTO, len(page.Items)), NextCursor: page.NextCursor}
	for index, item := range page.Items {
		result.Items[index] = libraryItemDTO(library, item)
	}
	return result, nil
}

func (m *Resources) CreateLibraryItem(ctx context.Context, actor security.Actor, siteID site.ID, libraryID resource.ID, input LibraryItemCreateInput) (LibraryItemDetails, error) {
	if m.libraryItems == nil {
		return LibraryItemDetails{}, errors.New("library item service is unavailable")
	}
	if err := m.requireSite(ctx, actor, siteID, ResourceCreatePermission, SiteAccessEdit); err != nil {
		return LibraryItemDetails{}, err
	}
	item, err := m.libraryItems.Create(ctx, actor, resource.CreateLibraryItemInput{ImageMediaID: input.ImageMediaID, SiteID: siteID, LibraryID: libraryID, Template: input.Template, Title: input.Title, Slug: input.Slug, Annotation: input.Annotation, Content: input.Content, IsPublic: input.IsPublic, IsSearchable: input.IsSearchable, PublishedAt: input.PublishedAt, UnpublishedAt: input.UnpublishedAt, Fields: input.Fields})
	if err != nil {
		return LibraryItemDetails{}, validationError(err)
	}
	return m.libraryItemDetails(ctx, actor, item)
}

func (m *Resources) LibraryItem(ctx context.Context, actor security.Actor, siteID site.ID, itemID resource.ID) (LibraryItemDetails, error) {
	if m.libraryItems == nil {
		return LibraryItemDetails{}, errors.New("library item service is unavailable")
	}
	if err := m.requireSite(ctx, actor, siteID, ResourceReadPermission, SiteAccessEdit); err != nil {
		return LibraryItemDetails{}, err
	}
	item, err := m.libraryItems.Get(ctx, actor, itemID)
	if err != nil {
		return LibraryItemDetails{}, err
	}
	if item.SiteID != siteID {
		return LibraryItemDetails{}, resource.ErrNotFound
	}
	return m.libraryItemDetails(ctx, actor, item)
}

func (m *Resources) UpdateLibraryItem(ctx context.Context, actor security.Actor, siteID site.ID, itemID resource.ID, input LibraryItemUpdateInput) (LibraryItemDetails, error) {
	if m.libraryItems == nil {
		return LibraryItemDetails{}, errors.New("library item service is unavailable")
	}
	if err := m.requireSite(ctx, actor, siteID, ResourceUpdatePermission, SiteAccessEdit); err != nil {
		return LibraryItemDetails{}, err
	}
	current, err := m.libraryItems.Get(ctx, actor, itemID)
	if err != nil {
		return LibraryItemDetails{}, err
	}
	if current.SiteID != siteID {
		return LibraryItemDetails{}, resource.ErrNotFound
	}
	if input.IsPublic == nil || input.IsSearchable == nil {
		return LibraryItemDetails{}, ErrValidation
	}
	item, err := m.libraryItems.Update(ctx, actor, resource.UpdateLibraryItemInput{ImageMediaID: input.ImageMediaID, ID: itemID, ExpectedVersion: input.ExpectedVersion, Template: input.Template, Title: input.Title, Slug: input.Slug, Annotation: input.Annotation, Content: input.Content, IsPublic: *input.IsPublic, IsSearchable: *input.IsSearchable, PublishedAt: input.PublishedAt, UnpublishedAt: input.UnpublishedAt, Fields: input.Fields})
	if err != nil {
		return LibraryItemDetails{}, validationError(err)
	}
	return m.libraryItemDetails(ctx, actor, item)
}

func (m *Resources) MoveLibraryItem(ctx context.Context, actor security.Actor, siteID site.ID, itemID, targetLibraryID resource.ID, expectedVersion int64) (LibraryItemDetails, error) {
	if m.libraryItems == nil {
		return LibraryItemDetails{}, errors.New("library item service is unavailable")
	}
	if err := m.requireSite(ctx, actor, siteID, ResourceUpdatePermission, SiteAccessEdit); err != nil {
		return LibraryItemDetails{}, err
	}
	current, err := m.libraryItems.Get(ctx, actor, itemID)
	if err != nil {
		return LibraryItemDetails{}, err
	}
	if current.SiteID != siteID {
		return LibraryItemDetails{}, resource.ErrNotFound
	}
	item, err := m.libraryItems.Move(ctx, actor, itemID, targetLibraryID, expectedVersion)
	if err != nil {
		return LibraryItemDetails{}, validationError(err)
	}
	return m.libraryItemDetails(ctx, actor, item)
}

func (m *Resources) DeleteLibraryItem(ctx context.Context, actor security.Actor, siteID site.ID, itemID resource.ID, permanent bool) error {
	if m.libraryItems == nil {
		return errors.New("library item service is unavailable")
	}
	if err := m.requireSite(ctx, actor, siteID, ResourceDeletePermission, SiteAccessEdit); err != nil {
		return err
	}
	item, err := m.libraryItems.Get(ctx, actor, itemID)
	if err != nil {
		return err
	}
	if item.SiteID != siteID {
		return resource.ErrNotFound
	}
	return m.libraryItems.Delete(ctx, actor, itemID, permanent)
}
func (m *Resources) RestoreLibraryItem(ctx context.Context, actor security.Actor, siteID site.ID, itemID resource.ID) error {
	if m.libraryItems == nil {
		return errors.New("library item service is unavailable")
	}
	if err := m.requireSite(ctx, actor, siteID, ResourceDeletePermission, SiteAccessEdit); err != nil {
		return err
	}
	item, err := m.libraryItems.Get(ctx, actor, itemID)
	if err != nil {
		return err
	}
	if item.SiteID != siteID {
		return resource.ErrNotFound
	}
	return m.libraryItems.Restore(ctx, actor, itemID)
}

func (m *Resources) libraryItemDetails(ctx context.Context, actor security.Actor, item resource.LibraryItem) (LibraryItemDetails, error) {
	library, err := m.resources.Get(ctx, actor, item.LibraryID)
	if err != nil {
		return LibraryItemDetails{}, err
	}
	result := LibraryItemDetails{Item: libraryItemDTO(library, item)}
	result.Permissions.Update, err = m.allowed(ctx, actor, ResourceUpdatePermission)
	if err != nil {
		return LibraryItemDetails{}, err
	}
	result.Permissions.Delete, err = m.allowed(ctx, actor, ResourceDeletePermission)
	result.Permissions.Restore = result.Permissions.Delete && library.DeletedAt == nil
	if err == nil && m.revisions.LibraryHistoryEnabled(item.SiteID) {
		result.Permissions.HistoryRead, err = m.allowed(ctx, actor, resource.HistoryReadPermission)
		if err == nil {
			result.Permissions.HistoryDelete, err = m.allowed(ctx, actor, resource.HistoryDeletePermission)
		}
	}
	return result, err
}

func libraryItemDTO(library resource.Resource, item resource.LibraryItem) LibraryItemDTO {
	url, _ := resource.EffectiveLibraryItemURL(library, item)
	fields := make(map[string]any, len(item.Fields))
	for key, value := range item.Fields {
		fields[key] = value
	}
	return LibraryItemDTO{ImageMediaID: item.ImageMediaID, ID: item.ID, Version: item.Version, SiteID: item.SiteID, LibraryID: item.LibraryID, TemplateCode: item.Template, Title: item.Title, Slug: item.Slug, Annotation: item.Annotation, ContentType: item.ContentType, Content: item.Content, IsPublic: item.IsPublic, IsSearchable: item.IsSearchable, PublishedAt: item.PublishedAt, UnpublishedAt: item.UnpublishedAt, Deleted: item.DeletedAt != nil, DeletedAt: item.DeletedAt, Fields: fields, Widgets: resourceWidgets(item.Widgets), EffectiveURL: url}
}

func (m *Resources) CreateResourceWidget(
	ctx context.Context,
	actor security.Actor,
	siteID site.ID,
	resourceID resource.ID,
	input resource.CreateWidgetInput,
) (ResourceWidget, error) {
	if err := m.requireSite(ctx, actor, siteID, ResourceUpdatePermission, SiteAccessEdit); err != nil {
		return ResourceWidget{}, err
	}
	if err := m.requireResourceSite(ctx, actor, siteID, resourceID); err != nil {
		return ResourceWidget{}, err
	}
	created, err := m.resources.CreateWidget(ctx, actor, resourceID, input)
	if err != nil {
		return ResourceWidget{}, validationError(err)
	}
	result := resourceWidgets([]widget.Binding{created})[0]
	current, loadErr := m.resourceEntity(ctx, actor, resourceID)
	if loadErr != nil {
		return ResourceWidget{}, loadErr
	}
	result.ResourceVersion = current.Version
	return result, nil
}

func (m *Resources) UpdateResourceWidget(
	ctx context.Context,
	actor security.Actor,
	siteID site.ID,
	resourceID resource.ID,
	bindingID widget.BindingID,
	input resource.UpdateWidgetInput,
) (ResourceWidget, error) {
	if err := m.requireSite(ctx, actor, siteID, ResourceUpdatePermission, SiteAccessEdit); err != nil {
		return ResourceWidget{}, err
	}
	if err := m.requireResourceSite(ctx, actor, siteID, resourceID); err != nil {
		return ResourceWidget{}, err
	}
	updated, err := m.resources.UpdateWidget(ctx, actor, resourceID, bindingID, input)
	if err != nil {
		if errors.Is(err, resource.ErrNotFound) {
			return ResourceWidget{}, err
		}
		return ResourceWidget{}, validationError(err)
	}
	result := resourceWidgets([]widget.Binding{updated})[0]
	current, loadErr := m.resourceEntity(ctx, actor, resourceID)
	if loadErr != nil {
		return ResourceWidget{}, loadErr
	}
	result.ResourceVersion = current.Version
	return result, nil
}

func (m *Resources) DeleteResourceWidget(
	ctx context.Context,
	actor security.Actor,
	siteID site.ID,
	resourceID resource.ID,
	bindingID widget.BindingID,
	expectedVersion int64,
) error {
	if err := m.requireSite(ctx, actor, siteID, ResourceUpdatePermission, SiteAccessEdit); err != nil {
		return err
	}
	if err := m.requireResourceSite(ctx, actor, siteID, resourceID); err != nil {
		return err
	}
	return m.resources.DeleteWidget(ctx, actor, resourceID, bindingID, expectedVersion)
}

func (m *Resources) ReorderResourceWidgets(
	ctx context.Context,
	actor security.Actor,
	siteID site.ID,
	resourceID resource.ID,
	expectedVersion int64,
	order []widget.Order,
) ([]ResourceWidget, error) {
	if err := m.requireSite(ctx, actor, siteID, ResourceUpdatePermission, SiteAccessEdit); err != nil {
		return nil, err
	}
	if err := m.requireResourceSite(ctx, actor, siteID, resourceID); err != nil {
		return nil, err
	}
	updated, err := m.resources.ReorderWidgets(ctx, actor, resourceID, expectedVersion, order)
	if err != nil {
		return nil, validationError(err)
	}
	return resourceWidgets(updated), nil
}

func (m *Resources) requireResourceSite(
	ctx context.Context,
	actor security.Actor,
	siteID site.ID,
	resourceID resource.ID,
) error {
	current, err := m.resourceEntity(ctx, actor, resourceID)
	if err != nil {
		return err
	}
	if current.SiteID != siteID {
		return resource.ErrNotFound
	}
	return nil
}

func (m *Resources) resourceEntity(
	ctx context.Context,
	actor security.Actor,
	resourceID resource.ID,
) (resource.Resource, error) {
	current, err := m.resourceRepo.ByID(ctx, resourceID)
	if !errors.Is(err, resource.ErrNotFound) || m.libraryItems == nil {
		return current, err
	}
	item, err := m.libraryItems.Get(ctx, actor, resourceID)
	if err != nil {
		return resource.Resource{}, err
	}
	return resource.Resource{
		ID: item.ID, SiteID: item.SiteID, Version: item.Version, Type: resourcetype.Page,
		Template: item.Template, ContentType: item.ContentType,
		Title: item.Title, Slug: item.Slug, Annotation: item.Annotation,
		Content: item.Content, ImageMediaID: item.ImageMediaID,
		IsPublic: item.IsPublic, IsSearchable: item.IsSearchable,
		PublishedAt: item.PublishedAt, UnpublishedAt: item.UnpublishedAt,
		Fields: item.Fields, FieldValues: item.FieldValues, Widgets: item.Widgets,
		CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
		CreatedBy: item.CreatedBy, UpdatedBy: item.UpdatedBy,
		DeletedAt: item.DeletedAt, DeletedBy: item.DeletedBy,
	}, nil
}

func (m *Resources) MoveResource(
	ctx context.Context,
	actor security.Actor,
	siteID site.ID,
	resourceID resource.ID,
	parentID *resource.ID,
	position int,
	expectedVersion int64,
) (ResourceDTO, error) {
	if err := m.requireSite(ctx, actor, siteID, ResourceUpdatePermission, SiteAccessEdit); err != nil {
		return ResourceDTO{}, err
	}
	current, err := m.resources.Get(ctx, actor, resourceID)
	if err != nil {
		return ResourceDTO{}, err
	}
	if current.SiteID != siteID {
		return ResourceDTO{}, resource.ErrNotFound
	}
	if parentID != nil {
		parent, parentErr := m.resources.Get(ctx, actor, *parentID)
		if parentErr != nil {
			return ResourceDTO{}, parentErr
		}
		if parent.SiteID != siteID {
			return ResourceDTO{}, resource.ErrInvalidTree
		}
	}
	updated, err := m.resources.Move(ctx, actor, resourceID, parentID, position, expectedVersion)
	if err != nil {
		return ResourceDTO{}, err
	}
	return resourceDTO(updated), nil
}

func (m *Resources) TransferResourceToSite(
	ctx context.Context,
	actor security.Actor,
	sourceSiteID site.ID,
	resourceID resource.ID,
	targetSiteID site.ID,
	expectedVersion int64,
) (ResourceDTO, error) {
	if err := m.requireSite(ctx, actor, sourceSiteID, ResourceUpdatePermission, SiteAccessEdit); err != nil {
		return ResourceDTO{}, err
	}
	if err := m.requireSite(ctx, actor, targetSiteID, ResourceUpdatePermission, SiteAccessEdit); err != nil {
		return ResourceDTO{}, err
	}
	current, err := m.resources.Get(ctx, actor, resourceID)
	if err != nil {
		return ResourceDTO{}, err
	}
	if current.SiteID != sourceSiteID {
		return ResourceDTO{}, resource.ErrNotFound
	}
	result, err := m.resources.TransferToSite(
		ctx, actor, resourceID, targetSiteID, expectedVersion, validateTransferExtensions,
	)
	if err != nil {
		return ResourceDTO{}, err
	}
	return resourceDTO(result.Resource), nil
}

func validateTransferExtensions(
	ctx context.Context,
	items []resource.Resource,
	sourceRuntime *site.Runtime,
	targetRuntime *site.Runtime,
) error {
	targetEditors := make(map[resourceextension.Code]resourceextension.Editor)
	for _, moduleRuntime := range targetRuntime.Profile().Modules() {
		provider, ok := moduleRuntime.(resourceextension.EditorProvider)
		if !ok {
			continue
		}
		editor := provider.ResourceEditorExtension()
		if editor != nil {
			targetEditors[editor.Metadata().Code] = editor
		}
	}
	for _, moduleRuntime := range sourceRuntime.Profile().Modules() {
		provider, ok := moduleRuntime.(resourceextension.EditorProvider)
		if !ok {
			continue
		}
		editor := provider.ResourceEditorExtension()
		if editor == nil {
			continue
		}
		applicableIDs := make([]resource.ID, 0, len(items))
		for _, item := range items {
			if editor.AppliesTo(item.Type) {
				applicableIDs = append(applicableIDs, item.ID)
			}
		}
		if len(applicableIDs) == 0 {
			continue
		}
		if usage, supportsUsage := editor.(resourceextension.TransferUsage); supportsUsage {
			used, err := usage.UsedByResources(ctx, sourceRuntime.Site().ID, applicableIDs)
			if err != nil {
				return fmt.Errorf("query resource extension %q usage: %w", editor.Metadata().Code, err)
			}
			if !used {
				continue
			}
		}
		target, exists := targetEditors[editor.Metadata().Code]
		if !exists || !reflect.DeepEqual(editor.Metadata(), target.Metadata()) {
			return fmt.Errorf("%w: resource extension %q is unavailable or incompatible", resource.ErrIncompatibleTargetSite, editor.Metadata().Code)
		}
		for _, item := range items {
			if editor.AppliesTo(item.Type) && !target.AppliesTo(item.Type) {
				return fmt.Errorf("%w: resource extension %q does not support type %q", resource.ErrIncompatibleTargetSite, editor.Metadata().Code, item.Type)
			}
		}
	}
	return nil
}

func (m *Resources) DeleteResource(
	ctx context.Context,
	actor security.Actor,
	siteID site.ID,
	resourceID resource.ID,
	permanent bool,
) error {
	if err := m.requireSite(ctx, actor, siteID, ResourceDeletePermission, SiteAccessEdit); err != nil {
		return err
	}
	current, err := m.resources.Get(ctx, actor, resourceID)
	if err != nil {
		return err
	}
	if current.SiteID != siteID {
		return resource.ErrNotFound
	}
	if permanent {
		return m.resources.DeletePermanent(ctx, actor, resourceID)
	}
	return m.resources.Delete(ctx, actor, resourceID)
}

func (m *Resources) RestoreResource(
	ctx context.Context,
	actor security.Actor,
	siteID site.ID,
	resourceID resource.ID,
	withDescendants bool,
) error {
	if err := m.requireSite(ctx, actor, siteID, ResourceDeletePermission, SiteAccessEdit); err != nil {
		return err
	}
	current, err := m.resources.Get(ctx, actor, resourceID)
	if err != nil {
		return err
	}
	if current.SiteID != siteID {
		return resource.ErrNotFound
	}
	return m.resources.Restore(ctx, actor, resourceID, withDescendants)
}

type ResourceOption struct {
	ID           resource.ID       `json:"id"`
	ParentID     *resource.ID      `json:"parent_id"`
	Type         resourcetype.Code `json:"type"`
	DisplayTitle string            `json:"display_title"`
	Path         *string           `json:"path"`
}

type ResourceOptions struct {
	Items []ResourceOption `json:"items"`
}

type ResourceLookup struct {
	Items      []ResourceOption `json:"items"`
	Pagination Pagination       `json:"pagination"`
}

// ResourceLookup is a bounded, site-scoped picker source. It intentionally
// differs from the legacy tree options endpoint: callers never receive an
// entire site as static field choices.
func (m *Resources) ResourceLookup(ctx context.Context, actor security.Actor, siteID site.ID, search string, page, perPage int) (ResourceLookup, error) {
	if err := m.requireSite(ctx, actor, siteID, ResourceReadPermission, SiteAccessEdit); err != nil {
		return ResourceLookup{}, err
	}
	page, perPage, err := normalizePagination(page, perPage)
	if err != nil {
		return ResourceLookup{}, err
	}
	items, err := m.resourceRepo.ListBySite(ctx, siteID)
	if err != nil {
		return ResourceLookup{}, fmt.Errorf("list resource lookup: %w", err)
	}
	query := strings.ToLower(strings.TrimSpace(search))
	options := make([]ResourceOption, 0, len(items))
	for _, item := range items {
		if query != "" && !strings.Contains(strings.ToLower(item.Title), query) && !strings.Contains(strings.ToLower(item.MenuTitle), query) && (item.Path == nil || !strings.Contains(strings.ToLower(*item.Path), query)) {
			continue
		}
		title := strings.TrimSpace(item.MenuTitle)
		if title == "" {
			title = item.Title
		}
		options = append(options, ResourceOption{ID: item.ID, ParentID: item.ParentID, Type: item.Type, DisplayTitle: title, Path: item.Path})
	}
	total := len(options)
	start := (page - 1) * perPage
	if start > total {
		start = total
	}
	end := start + perPage
	if end > total {
		end = total
	}
	return ResourceLookup{Items: options[start:end], Pagination: Pagination{Page: page, PerPage: perPage, Total: total}}, nil
}

func (m *Resources) ResourceOptions(
	ctx context.Context,
	actor security.Actor,
	siteID site.ID,
) (ResourceOptions, error) {
	if err := m.requireSite(ctx, actor, siteID, ResourceReadPermission, SiteAccessEdit); err != nil {
		return ResourceOptions{}, err
	}
	tree, err := m.resources.Tree(ctx, actor, siteID)
	if err != nil {
		return ResourceOptions{}, err
	}
	items := make([]ResourceOption, 0)
	appendResourceOptions(&items, tree)
	return ResourceOptions{Items: items}, nil
}

type Menu struct {
	Items []MenuItem `json:"items"`
}

type MenuItem struct {
	ID       resource.ID `json:"id"`
	Title    string      `json:"title"`
	URL      string      `json:"url"`
	Children []MenuItem  `json:"children"`
}

// Menu projects the published resource hierarchy. Resources remain the only
// source of truth for visibility, order, hierarchy, and route targets.
func (m *Resources) Menu(
	ctx context.Context,
	actor security.Actor,
	siteID site.ID,
) (Menu, error) {
	if err := m.requireSite(ctx, actor, siteID, ResourceReadPermission, SiteAccessView); err != nil {
		return Menu{}, err
	}
	if _, exists := m.sites.RuntimeByID(siteID); !exists {
		return Menu{}, site.ErrNotFound
	}
	items, err := m.resourceRepo.ListBySite(ctx, siteID)
	if err != nil {
		return Menu{}, fmt.Errorf("list menu resources: %w", err)
	}
	now := time.Now().UTC()
	byID := make(map[resource.ID]resource.Resource, len(items))
	for _, item := range items {
		byID[item.ID] = item
	}
	visible := make(map[resource.ID]resource.Resource, len(items))
	urls := make(map[resource.ID]string, len(items))
	for _, item := range items {
		if !item.InMenu || !resourcePublishedAt(item, now) {
			continue
		}
		url, ok := menuURL(item, byID, now)
		if !ok {
			continue
		}
		visible[item.ID] = item
		urls[item.ID] = url
	}
	children := make(map[resource.ID][]resource.Resource)
	roots := make([]resource.Resource, 0)
	for _, item := range visible {
		if item.ParentID == nil {
			roots = append(roots, item)
			continue
		}
		if _, parentVisible := visible[*item.ParentID]; parentVisible {
			children[*item.ParentID] = append(children[*item.ParentID], item)
		}
	}
	sortResources := func(items []resource.Resource) {
		sort.Slice(items, func(i, j int) bool {
			if items[i].Sort != items[j].Sort {
				return items[i].Sort < items[j].Sort
			}
			return items[i].ID < items[j].ID
		})
	}
	sortResources(roots)
	for id := range children {
		sortResources(children[id])
	}
	var project func([]resource.Resource) []MenuItem
	project = func(source []resource.Resource) []MenuItem {
		result := make([]MenuItem, len(source))
		for index, item := range source {
			title := strings.TrimSpace(item.MenuTitle)
			if title == "" {
				title = item.Title
			}
			result[index] = MenuItem{
				ID: item.ID, Title: title, URL: urls[item.ID],
				Children: project(children[item.ID]),
			}
		}
		return result
	}
	return Menu{Items: project(roots)}, nil
}

func resourcePublishedAt(item resource.Resource, now time.Time) bool {
	return item.DeletedAt == nil && item.IsPublic &&
		(item.PublishedAt == nil || !now.Before(item.PublishedAt.UTC())) &&
		(item.UnpublishedAt == nil || now.Before(item.UnpublishedAt.UTC()))
}

func menuURL(item resource.Resource, byID map[resource.ID]resource.Resource, now time.Time) (string, bool) {
	switch item.Type {
	case resourcetype.Page, resourcetype.Library:
		if item.Path == nil {
			return "", false
		}
		return *item.Path, true
	case resourcetype.Link:
		if item.ExternalURL == nil {
			return "", false
		}
		return *item.ExternalURL, true
	case resourcetype.ResourceLink:
		if item.TargetResourceID == nil {
			return "", false
		}
		target, exists := byID[*item.TargetResourceID]
		if !exists || target.SiteID != item.SiteID || target.Path == nil || !resourcePublishedAt(target, now) {
			return "", false
		}
		return *target.Path, true
	default:
		return "", false
	}
}

func (m authorization) requireSite(
	ctx context.Context,
	actor security.Actor,
	id site.ID,
	code permission.Code,
	action SiteAccessAction,
) error {
	if id <= 0 {
		return fmt.Errorf("%w: invalid site id", ErrValidation)
	}
	if err := m.authorizer.Check(ctx, actor, code); err != nil {
		return err
	}
	return m.policy.Check(ctx, actor, id, action)
}

func (m authorization) allowed(
	ctx context.Context,
	actor security.Actor,
	code permission.Code,
) (bool, error) {
	err := m.authorizer.Check(ctx, actor, code)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, security.ErrForbidden) || errors.Is(err, security.ErrUnauthenticated) {
		return false, nil
	}
	return false, err
}

func (m *Sites) sitePermissions(
	ctx context.Context,
	actor security.Actor,
) (PermissionSet, error) {
	codes := []permission.Code{SiteReadPermission, SiteCreatePermission, SiteUpdatePermission, SiteDeletePermission}
	values := make([]bool, len(codes))
	for index, code := range codes {
		allowed, err := m.allowed(ctx, actor, code)
		if err != nil {
			return PermissionSet{}, err
		}
		values[index] = allowed
	}
	return PermissionSet{Read: values[0], Create: values[1], Update: values[2], Delete: values[3]}, nil
}

func (m *Sites) siteCapabilities(
	ctx context.Context,
	actor security.Actor,
	items []site.Site,
	viewScope *SiteAccessScope,
) (map[site.ID]SiteCapabilities, error) {
	result := make(map[site.ID]SiteCapabilities, len(items))
	actions := []SiteAccessAction{SiteAccessView, SiteAccessEdit, SiteAccessDelete}
	for actionIndex, action := range actions {
		var scope SiteAccessScope
		if actionIndex == 0 && viewScope != nil {
			scope = *viewScope
		} else {
			var err error
			scope, err = m.policy.Scope(ctx, actor, action)
			if err != nil {
				return nil, err
			}
		}
		allowed := make(map[site.ID]struct{}, len(scope.SiteIDs))
		for _, id := range scope.SiteIDs {
			allowed[id] = struct{}{}
		}
		for _, item := range items {
			_, includes := allowed[item.ID]
			if !scope.All && !includes {
				continue
			}
			capabilities := result[item.ID]
			switch actionIndex {
			case 0:
				capabilities.View = true
			case 1:
				capabilities.View = true
				capabilities.Edit = true
			case 2:
				capabilities.View = true
				capabilities.Edit = true
				capabilities.Delete = true
			}
			result[item.ID] = capabilities
		}
	}
	return result, nil
}

func normalizePagination(page, perPage int) (int, int, error) {
	if page == 0 {
		page = 1
	}
	if perPage == 0 {
		perPage = 10
	}
	if page < 1 || perPage < 1 || perPage > 100 {
		return 0, 0, fmt.Errorf("%w: invalid pagination", ErrValidation)
	}
	return page, perPage, nil
}

func siteDTO(item site.Site, capabilities SiteCapabilities) SiteDTO {
	settings := make(map[string]any, len(item.Settings))
	for key, value := range item.Settings {
		settings[key] = value
	}
	return SiteDTO{
		ID:           item.ID,
		ProfileCode:  item.ProfileCode,
		Domain:       item.Domain,
		Locale:       item.Locale,
		Settings:     settings,
		IsPublic:     item.IsPublic,
		Capabilities: capabilities,
	}
}

func treeItem(runtime *site.Runtime, item resource.Child, canCreate bool) ResourceTreeItem {
	displayTitle := strings.TrimSpace(item.MenuTitle)
	if displayTitle == "" {
		displayTitle = item.Title
	}
	icon := "document"
	if runtime == nil {
		if item.Type == resourcetype.Link || item.Type == resourcetype.ResourceLink {
			icon = "link"
		} else if item.Type == resourcetype.Library {
			icon = "collection"
		}
	} else {
		if resourceType, exists := runtime.Profile().Registry().ResourceType(item.Type); exists {
			icon = iconOrDefault(resourceType.Metadata().Capabilities.DefaultIcon)
		}
		if item.Template != nil {
			if templateRuntime, exists := runtime.Profile().Template(*item.Template); exists {
				if templateIcon, allowed := allowedIcon(templateRuntime.Definition().Icon); allowed {
					icon = templateIcon
				}
			}
		}
	}
	return ResourceTreeItem{
		ID:              item.ID,
		Version:         item.Version,
		ParentID:        item.ParentID,
		TemplateCode:    item.Template,
		Icon:            icon,
		Title:           item.Title,
		MenuTitle:       item.MenuTitle,
		DisplayTitle:    displayTitle,
		Sort:            item.Sort,
		Deleted:         item.DeletedAt != nil,
		Published:       isPublished(item),
		InMenu:          item.InMenu,
		DeletedAt:       item.DeletedAt,
		HasChildren:     item.HasChildren,
		CanCreateChild:  canCreate && item.DeletedAt == nil,
		CanTransferSite: item.CanTransferSite,
	}
}

func isPublished(item resource.Child) bool {
	if item.DeletedAt != nil || !item.IsPublic {
		return false
	}
	now := time.Now().UTC()
	return (item.PublishedAt == nil || !now.Before(item.PublishedAt.UTC())) &&
		(item.UnpublishedAt == nil || now.Before(item.UnpublishedAt.UTC()))
}

func resourceDTO(item resource.Resource) ResourceDTO {
	fields := make(map[string]any, len(item.Fields))
	for key, value := range item.Fields {
		fields[key] = value
	}
	typeSettings := make(map[string]any, len(item.TypeSettings))
	for key, value := range item.TypeSettings {
		typeSettings[key] = value
	}
	return ResourceDTO{
		ImageMediaID:     item.ImageMediaID,
		ID:               item.ID,
		SiteID:           item.SiteID,
		Version:          item.Version,
		ParentID:         item.ParentID,
		Type:             item.Type,
		TemplateCode:     item.Template,
		Title:            item.Title,
		MenuTitle:        item.MenuTitle,
		Slug:             item.Slug,
		Path:             item.Path,
		Annotation:       item.Annotation,
		ContentType:      item.ContentType,
		Content:          item.Content,
		TargetResourceID: item.TargetResourceID,
		ExternalURL:      item.ExternalURL,
		IsPublic:         item.IsPublic,
		IsSearchable:     item.IsSearchable,
		InMenu:           item.InMenu,
		InSitemap:        item.InSitemap,
		Sort:             item.Sort,
		PublishedAt:      item.PublishedAt,
		UnpublishedAt:    item.UnpublishedAt,
		Deleted:          item.DeletedAt != nil,
		DeletedAt:        item.DeletedAt,
		Fields:           fields,
		TypeSettings:     typeSettings,
		Widgets:          resourceWidgets(item.Widgets),
	}
}

func resourceWidgets(source []widget.Binding) []ResourceWidget {
	result := make([]ResourceWidget, len(source))
	for index, binding := range source {
		bindings := widget.CloneParamBindings(binding.ParamBindings)
		if bindings == nil {
			bindings = widget.ParamBindings{}
		}
		params := make(map[string]any, len(binding.Params))
		for key, value := range binding.Params {
			params[key] = value
		}
		result[index] = ResourceWidget{
			ID: binding.ID, Code: binding.Code, Area: binding.Area, Position: binding.Position,
			View: widget.PublicView(binding.Presentation.View), Columns: binding.Presentation.Columns,
			MarginTop: binding.Presentation.MarginTop, MarginBottom: binding.Presentation.MarginBottom,
			Enabled: binding.Presentation.Enabled, Params: params, ParamBindings: bindings,
		}
	}
	return result
}

func appendResourceOptions(target *[]ResourceOption, nodes []resource.Node) {
	for _, node := range nodes {
		item := node.Resource
		if item.DeletedAt != nil {
			continue
		}
		displayTitle := strings.TrimSpace(item.MenuTitle)
		if displayTitle == "" {
			displayTitle = item.Title
		}
		*target = append(*target, ResourceOption{
			ID:           item.ID,
			ParentID:     item.ParentID,
			Type:         item.Type,
			DisplayTitle: displayTitle,
			Path:         item.Path,
		})
		appendResourceOptions(target, node.Children)
	}
}

func iconOrDefault(icon string) string {
	if normalized, allowed := allowedIcon(icon); allowed {
		return normalized
	}
	return "document"
}

func allowedIcon(icon string) (string, bool) {
	icon = strings.ToLower(strings.TrimSpace(icon))
	switch icon {
	case "document", "link", "folder", "tickets", "collection":
		return icon, true
	default:
		return "", false
	}
}

func cloneAnyMap(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		switch typed := value.(type) {
		case map[string]any:
			result[key] = cloneAnyMap(typed)
		case []any:
			items := make([]any, len(typed))
			copy(items, typed)
			result[key] = items
		case []string:
			result[key] = append([]string(nil), typed...)
		default:
			result[key] = typed
		}
	}
	return result
}

func validationError(err error) error {
	if errors.Is(err, security.ErrUnauthenticated) || errors.Is(err, security.ErrForbidden) {
		return err
	}
	if errors.Is(err, site.ErrConflict) || errors.Is(err, site.ErrNotFound) ||
		errors.Is(err, resource.ErrConflict) || errors.Is(err, resource.ErrRouteConflict) || errors.Is(err, resource.ErrNotFound) ||
		errors.Is(err, resource.ErrInvalidTree) || errors.Is(err, resource.ErrCrossSiteReference) ||
		errors.Is(err, resource.ErrIncompatibleTargetSite) {
		return err
	}
	var fieldErrors field.ValidationErrors
	if errors.As(err, &fieldErrors) {
		fields := make([]FieldValidationError, len(fieldErrors))
		for index, item := range fieldErrors {
			fields[index] = FieldValidationError{
				Key: item.Key, Rule: item.Rule, Param: item.Param,
			}
		}
		return ValidationError{
			Message: "request data is invalid",
			Fields:  fields,
		}
	}
	if errors.Is(err, site.ErrInvalid) || errors.Is(err, resource.ErrInvalid) ||
		errors.Is(err, resource.ErrInvalidReference) {
		return fmt.Errorf("%w: request data is invalid", ErrValidation)
	}
	return err
}
