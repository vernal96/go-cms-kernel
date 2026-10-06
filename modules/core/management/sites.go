package management

import (
	"context"
	"errors"
	"fmt"

	kernel "github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/group"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
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
