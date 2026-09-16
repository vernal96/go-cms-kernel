package management

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

type managementSiteRepository struct {
	query site.ListQuery
	page  site.Page
	err   error
}

type menuRepository struct {
	resource.ManagementRepository
	items []resource.Resource
}

func (r menuRepository) ListBySite(context.Context, site.ID) ([]resource.Resource, error) {
	return append([]resource.Resource(nil), r.items...), nil
}

type menuSites struct{ SiteCatalog }

func (menuSites) RuntimeByID(id site.ID) (*site.Runtime, bool) {
	return &site.Runtime{}, id == 7
}

func (r *managementSiteRepository) List(context.Context) ([]site.Site, error) {
	return append([]site.Site(nil), r.page.Items...), nil
}
func (r *managementSiteRepository) FindByID(context.Context, site.ID) (site.Site, error) {
	return site.Site{}, site.ErrNotFound
}
func (r *managementSiteRepository) FindByDomain(context.Context, string) (site.Site, error) {
	return site.Site{}, site.ErrNotFound
}
func (r *managementSiteRepository) ListPage(_ context.Context, query site.ListQuery) (site.Page, error) {
	r.query = query
	return r.page, r.err
}
func (r *managementSiteRepository) Create(context.Context, *security.UserID, site.Site) (site.Site, error) {
	return site.Site{}, nil
}
func (r *managementSiteRepository) Update(context.Context, *security.UserID, site.Site) (site.Site, error) {
	return site.Site{}, nil
}
func (r *managementSiteRepository) Delete(context.Context, site.ID) error { return nil }

type managementAuthorizer struct {
	denied map[permission.Code]error
}

func (a managementAuthorizer) Check(_ context.Context, _ security.Actor, code permission.Code) error {
	return a.denied[code]
}

type scopedPolicy struct {
	scope  SiteAccessScope
	scopes map[SiteAccessAction]SiteAccessScope
	checks map[SiteAccessAction]error
}

type siteSpecificPolicy struct {
	checks []site.ID
	denied map[site.ID]error
}

func (p *siteSpecificPolicy) Scope(context.Context, security.Actor, SiteAccessAction) (SiteAccessScope, error) {
	return SiteAccessScope{All: true}, nil
}

func (p *siteSpecificPolicy) Check(_ context.Context, _ security.Actor, siteID site.ID, _ SiteAccessAction) error {
	p.checks = append(p.checks, siteID)
	return p.denied[siteID]
}

func (p scopedPolicy) Scope(_ context.Context, _ security.Actor, action SiteAccessAction) (SiteAccessScope, error) {
	if scope, exists := p.scopes[action]; exists {
		return scope, nil
	}
	return p.scope, nil
}
func (p scopedPolicy) Check(_ context.Context, _ security.Actor, _ site.ID, action SiteAccessAction) error {
	return p.checks[action]
}

func TestManagementRequireSiteKeepsGlobalAndSiteChecksIndependent(t *testing.T) {
	t.Parallel()
	management := authorization{
		authorizer: managementAuthorizer{denied: map[permission.Code]error{ResourceReadPermission: security.ErrForbidden}},
		policy:     scopedPolicy{checks: map[SiteAccessAction]error{SiteAccessEdit: nil}},
	}
	if err := management.requireSite(context.Background(), security.User(1), 7, ResourceReadPermission, SiteAccessEdit); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("global permission error = %v", err)
	}
	management.authorizer = managementAuthorizer{denied: map[permission.Code]error{}}
	management.policy = scopedPolicy{checks: map[SiteAccessAction]error{SiteAccessEdit: security.ErrForbidden}}
	if err := management.requireSite(context.Background(), security.User(1), 7, ResourceReadPermission, SiteAccessEdit); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("site access error = %v", err)
	}
}

func TestDeleteLibraryItemRequiresSiteEditNotSiteDelete(t *testing.T) {
	management := &Resources{
		authorization: authorization{
			authorizer: managementAuthorizer{denied: map[permission.Code]error{}},
			policy: scopedPolicy{checks: map[SiteAccessAction]error{
				SiteAccessEdit:   security.ErrForbidden,
				SiteAccessDelete: nil,
			}},
		},
		libraryItems: &resource.LibraryService{},
	}
	if err := management.DeleteLibraryItem(context.Background(), security.User(1), 7, 1, false); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("delete library item site access error = %v", err)
	}
}

func TestTransferResourceRequiresEditAccessToBothSites(t *testing.T) {
	t.Parallel()
	policy := &siteSpecificPolicy{denied: map[site.ID]error{8: security.ErrForbidden}}
	management := &Resources{authorization: authorization{
		authorizer: managementAuthorizer{denied: map[permission.Code]error{}},
		policy:     policy,
	}}
	_, err := management.TransferResourceToSite(context.Background(), security.User(1), 7, 9, 8, 1)
	if !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("target edit access error = %v", err)
	}
	if len(policy.checks) != 2 || policy.checks[0] != 7 || policy.checks[1] != 8 {
		t.Fatalf("checked sites = %#v", policy.checks)
	}
}

func TestManagementUsesViewForCatalogEditForOptionsAndReturnsCapabilities(t *testing.T) {
	t.Parallel()
	repository := &managementSiteRepository{page: site.Page{Items: []site.Site{
		{ID: 1, Domain: "view.example", ProfileCode: "dev", Locale: "ru-RU"},
		{ID: 2, Domain: "edit.example", ProfileCode: "dev", Locale: "ru-RU"},
	}, Total: 2}}
	management := &Sites{
		authorization: authorization{
			authorizer: managementAuthorizer{denied: map[permission.Code]error{}},
			policy: scopedPolicy{scopes: map[SiteAccessAction]SiteAccessScope{
				SiteAccessView:   {SiteIDs: []site.ID{1, 2}},
				SiteAccessEdit:   {SiteIDs: []site.ID{2}},
				SiteAccessDelete: {SiteIDs: []site.ID{2}},
			}},
		},
		repository: repository,
	}
	list, err := management.ListSites(context.Background(), security.User(1), "", 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !list.Items[0].Capabilities.View || list.Items[0].Capabilities.Edit ||
		!list.Items[1].Capabilities.Edit || !list.Items[1].Capabilities.Delete {
		t.Fatalf("capabilities = %#v", list.Items)
	}
	if _, err := management.ListSiteOptions(context.Background(), security.User(1), "", 1, 10); err != nil {
		t.Fatal(err)
	}
	if repository.query.Scope.All || len(repository.query.Scope.SiteIDs) != 1 || repository.query.Scope.SiteIDs[0] != 2 {
		t.Fatalf("option scope = %#v", repository.query.Scope)
	}
}

func TestManagementListSitesAppliesDefaultsScopeAndPermissions(t *testing.T) {
	t.Parallel()
	repository := &managementSiteRepository{page: site.Page{
		Items: []site.Site{{ID: 7, Domain: "example.com", ProfileCode: "dev", Locale: "ru-RU"}},
		Total: 1,
	}}
	management := &Sites{
		authorization: authorization{
			authorizer: managementAuthorizer{denied: map[permission.Code]error{
				SiteDeletePermission: security.ErrForbidden,
			}},
			policy: scopedPolicy{scope: SiteAccessScope{SiteIDs: []site.ID{7}}},
		},
		repository: repository,
	}

	result, err := management.ListSites(context.Background(), security.User(1), " example ", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if repository.query.Page != 1 || repository.query.PerPage != 10 ||
		repository.query.Scope.All || len(repository.query.Scope.SiteIDs) != 1 ||
		repository.query.Scope.SiteIDs[0] != 7 {
		t.Fatalf("query = %#v", repository.query)
	}
	if len(result.Items) != 1 || result.Items[0].Domain != "example.com" ||
		!result.Permissions.Read || !result.Permissions.Create ||
		!result.Permissions.Update || result.Permissions.Delete {
		t.Fatalf("result = %#v", result)
	}
}

func TestManagementListSitesRejectsPermissionAndPagination(t *testing.T) {
	t.Parallel()
	management := &Sites{
		authorization: authorization{
			authorizer: managementAuthorizer{denied: map[permission.Code]error{
				SiteReadPermission: security.ErrForbidden,
			}},
			policy: scopedPolicy{},
		},
		repository: &managementSiteRepository{},
	}
	if _, err := management.ListSites(context.Background(), security.User(1), "", 1, 10); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("permission error = %v", err)
	}
	management.authorizer = managementAuthorizer{denied: map[permission.Code]error{}}
	if _, err := management.ListSites(context.Background(), security.User(1), "", -1, 10); !errors.Is(err, ErrValidation) {
		t.Fatalf("pagination error = %v", err)
	}
}

func TestResourceTreeItemUsesSafeTitleAndIconFallbacks(t *testing.T) {
	t.Parallel()
	page := treeItem(nil, resource.Child{
		ID:        1,
		Type:      resourcetype.Page,
		Title:     "Title",
		MenuTitle: " Menu title ",
		InMenu:    true,
	}, false)
	if page.DisplayTitle != "Menu title" || page.Icon != "document" || page.CanCreateChild || !page.InMenu {
		t.Fatalf("page item = %#v", page)
	}
	link := treeItem(nil, resource.Child{ID: 2, Type: resourcetype.Link, Title: "Link"}, true)
	if link.DisplayTitle != "Link" || link.Icon != "link" || !link.CanCreateChild {
		t.Fatalf("link item = %#v", link)
	}
	if iconOrDefault("unsafe/path") != "document" {
		t.Fatal("unsafe icon did not fall back to document")
	}
	if iconOrDefault("Collection") != "collection" {
		t.Fatal("collection icon was not normalized")
	}
}

func TestResourceTreeItemReportsEffectivePublication(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)
	cases := []struct {
		name string
		item resource.Child
		want bool
	}{
		{name: "public", item: resource.Child{IsPublic: true}, want: true},
		{name: "private", item: resource.Child{IsPublic: false}},
		{name: "starts later", item: resource.Child{IsPublic: true, PublishedAt: &future}},
		{name: "already ended", item: resource.Child{IsPublic: true, UnpublishedAt: &past}},
		{name: "deleted", item: resource.Child{IsPublic: true, DeletedAt: &past}},
	}
	for _, current := range cases {
		t.Run(current.name, func(t *testing.T) {
			if got := isPublished(current.item); got != current.want {
				t.Fatalf("published = %v, want %v", got, current.want)
			}
		})
	}
}

func TestMenuFiltersOrdersAndBuildsHierarchy(t *testing.T) {
	t.Parallel()
	past := time.Now().UTC().Add(-time.Hour)
	future := time.Now().UTC().Add(time.Hour)
	rootID := resource.ID(10)
	targetID := resource.ID(30)
	rootPath, childPath, targetPath := "/catalog", "/catalog/item", "/target"
	externalURL := "https://example.org"
	repository := menuRepository{items: []resource.Resource{
		{ID: 20, SiteID: 7, Type: resourcetype.Link, Title: "External", ExternalURL: &externalURL, IsPublic: true, InMenu: true, Sort: 2},
		{ID: rootID, SiteID: 7, Type: resourcetype.Page, Title: "Catalog", MenuTitle: " Menu catalog ", Path: &rootPath, IsPublic: true, InMenu: true, Sort: 1},
		{ID: 11, SiteID: 7, ParentID: &rootID, Type: resourcetype.Page, Title: "Child", Path: &childPath, IsPublic: true, InMenu: true, Sort: 3},
		{ID: 12, SiteID: 7, ParentID: &rootID, Type: resourcetype.Page, Title: "Private", Path: &childPath, InMenu: true},
		{ID: 13, SiteID: 7, ParentID: &rootID, Type: resourcetype.Page, Title: "Future", Path: &childPath, IsPublic: true, InMenu: true, PublishedAt: &future},
		{ID: 14, SiteID: 7, ParentID: &rootID, Type: resourcetype.Page, Title: "Deleted", Path: &childPath, IsPublic: true, InMenu: true, DeletedAt: &past},
		{ID: targetID, SiteID: 7, Type: resourcetype.Page, Title: "Target", Path: &targetPath, IsPublic: true, InMenu: false},
		{ID: 40, SiteID: 7, Type: resourcetype.ResourceLink, Title: "Internal", TargetResourceID: &targetID, IsPublic: true, InMenu: true, Sort: 2},
	}}
	content := &Resources{
		authorization: authorization{
			sites: menuSites{}, authorizer: managementAuthorizer{denied: map[permission.Code]error{}},
			policy: AllowAllSitesPolicy{},
		},
		resourceRepo: repository,
	}
	menu, err := content.Menu(context.Background(), security.User(1), 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(menu.Items) != 3 || menu.Items[0].ID != rootID || menu.Items[0].Title != "Menu catalog" ||
		len(menu.Items[0].Children) != 1 || menu.Items[0].Children[0].ID != 11 ||
		menu.Items[1].ID != 20 || menu.Items[2].ID != 40 || menu.Items[2].URL != targetPath {
		t.Fatalf("menu = %#v", menu)
	}
}

func TestResourceDTOReturnsGenericTargetAndTypeSettings(t *testing.T) {
	t.Parallel()
	targetID := resource.ID(42)
	item := resource.Resource{
		ID: 9, SiteID: 7, Type: "custom", Title: "Custom",
		TargetResourceID: &targetID,
		TypeSettings:     map[string]any{"unknown": map[string]any{"enabled": true}},
		Fields:           map[string]any{"rank": int64(3)},
	}
	dto := resourceDTO(item)
	if dto.TargetResourceID == nil || *dto.TargetResourceID != targetID ||
		dto.TypeSettings["unknown"] == nil || dto.Fields["rank"] != int64(3) {
		t.Fatalf("resource DTO = %#v", dto)
	}
}

var _ site.ManagementRepository = (*managementSiteRepository)(nil)
var _ security.Authorizer = managementAuthorizer{}
var _ SiteAccessPolicy = scopedPolicy{}
