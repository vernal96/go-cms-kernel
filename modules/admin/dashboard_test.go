package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/vernal96/go-cms-kernel/modules/core/group"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/user"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

type dashboardSiteRepository struct {
	site.ManagementRepository
	query      site.StatisticsQuery
	statistics site.Statistics
	err        error
	calls      int
}

func (r *dashboardSiteRepository) Statistics(
	_ context.Context,
	query site.StatisticsQuery,
) (site.Statistics, error) {
	r.calls++
	r.query = query
	return r.statistics, r.err
}

type dashboardResourceRepository struct {
	resource.ManagementRepository
	query      resource.StatisticsQuery
	statistics resource.Statistics
	err        error
	calls      int
}

func (r *dashboardResourceRepository) Statistics(
	_ context.Context,
	query resource.StatisticsQuery,
) (resource.Statistics, error) {
	r.calls++
	r.query = query
	return r.statistics, r.err
}

type dashboardUserRepository struct {
	user.ManagementRepository
	statistics user.Statistics
	err        error
	calls      int
}

func (r *dashboardUserRepository) Statistics(context.Context) (user.Statistics, error) {
	r.calls++
	return r.statistics, r.err
}

type dashboardGroupRepository struct {
	group.ManagementRepository
	total int
	err   error
	calls int
}

func (r *dashboardGroupRepository) Count(context.Context) (int, error) {
	r.calls++
	return r.total, r.err
}

func TestManagementDashboardCollectsScopedStatistics(t *testing.T) {
	t.Parallel()
	sites := &dashboardSiteRepository{statistics: site.Statistics{
		Items: []site.Site{
			{ID: 7, Domain: "alpha.test", IsPublic: true},
			{ID: 8, Domain: "beta.test", IsPublic: false},
		},
		Total:   3,
		Public:  1,
		Private: 2,
	}}
	resources := &dashboardResourceRepository{statistics: resource.Statistics{
		Total:  6,
		BySite: map[site.ID]int{7: 4},
	}}
	users := &dashboardUserRepository{statistics: user.Statistics{
		Total: 9, Active: 7, Blocked: 2,
	}}
	groups := &dashboardGroupRepository{total: 5}
	management := &Management{
		repository:   sites,
		resourceRepo: resources,
		userRepo:     users,
		groupRepo:    groups,
		authorizer:   managementAuthorizer{denied: map[permission.Code]error{}},
		policy: scopedPolicy{scope: SiteAccessScope{
			SiteIDs: []site.ID{7, 8, 9},
		}},
	}

	result, err := management.Dashboard(context.Background(), security.User(1))
	if err != nil {
		t.Fatal(err)
	}
	if sites.query.Limit != dashboardSiteLimit || sites.query.Scope.All ||
		len(sites.query.Scope.SiteIDs) != 3 {
		t.Fatalf("site query = %#v", sites.query)
	}
	if len(resources.query.SiteIDs) != 2 || resources.query.SiteIDs[0] != 7 ||
		resources.query.SiteIDs[1] != 8 || resources.query.Scope.All {
		t.Fatalf("resource query = %#v", resources.query)
	}
	if result.Sites == nil || result.Sites.Total != 3 || result.Sites.Public != 1 ||
		result.Sites.Private != 2 || len(result.Sites.Items) != 2 ||
		result.Sites.Items[0].ResourceCount == nil ||
		*result.Sites.Items[0].ResourceCount != 4 ||
		result.Sites.Items[1].ResourceCount == nil ||
		*result.Sites.Items[1].ResourceCount != 0 {
		t.Fatalf("sites = %#v", result.Sites)
	}
	if result.Resources == nil || result.Resources.Total != 6 ||
		result.Users == nil || result.Users.Active != 7 || result.Users.Blocked != 2 ||
		result.Groups == nil || result.Groups.Total != 5 {
		t.Fatalf("dashboard = %#v", result)
	}
}

func TestManagementDashboardSkipsForbiddenSections(t *testing.T) {
	t.Parallel()
	sites := &dashboardSiteRepository{}
	resources := &dashboardResourceRepository{statistics: resource.Statistics{Total: 11}}
	users := &dashboardUserRepository{}
	groups := &dashboardGroupRepository{}
	management := &Management{
		repository:   sites,
		resourceRepo: resources,
		userRepo:     users,
		groupRepo:    groups,
		authorizer: managementAuthorizer{denied: map[permission.Code]error{
			SiteReadPermission:  security.ErrForbidden,
			UserReadPermission:  security.ErrForbidden,
			GroupReadPermission: security.ErrForbidden,
		}},
		policy: scopedPolicy{scope: SiteAccessScope{SiteIDs: []site.ID{3}}},
	}

	result, err := management.Dashboard(context.Background(), security.User(1))
	if err != nil {
		t.Fatal(err)
	}
	if result.Sites != nil || result.Users != nil || result.Groups != nil ||
		result.Resources == nil || result.Resources.Total != 11 {
		t.Fatalf("dashboard = %#v", result)
	}
	if sites.calls != 0 || users.calls != 0 || groups.calls != 0 || resources.calls != 1 ||
		len(resources.query.SiteIDs) != 0 || len(resources.query.Scope.SiteIDs) != 1 {
		t.Fatalf("calls: sites=%d resources=%d users=%d groups=%d query=%#v",
			sites.calls, resources.calls, users.calls, groups.calls, resources.query)
	}
}

func TestManagementDashboardPropagatesRepositoryErrors(t *testing.T) {
	t.Parallel()
	management := &Management{
		repository: &dashboardSiteRepository{err: errors.New("database detail")},
		authorizer: managementAuthorizer{denied: map[permission.Code]error{
			ResourceReadPermission: security.ErrForbidden,
			UserReadPermission:     security.ErrForbidden,
			GroupReadPermission:    security.ErrForbidden,
		}},
		policy: scopedPolicy{scope: SiteAccessScope{All: true}},
	}
	if _, err := management.Dashboard(context.Background(), security.User(1)); err == nil {
		t.Fatal("expected dashboard repository error")
	}
}

func TestManagementHTTPDashboardOmitsForbiddenSections(t *testing.T) {
	t.Parallel()
	management := &Management{
		repository: &dashboardSiteRepository{statistics: site.Statistics{
			Items:   []site.Site{{ID: 1, Domain: "example.test", IsPublic: true}},
			Total:   1,
			Public:  1,
			Private: 0,
		}},
		authorizer: managementAuthorizer{denied: map[permission.Code]error{
			ResourceReadPermission: security.ErrForbidden,
			UserReadPermission:     security.ErrForbidden,
			GroupReadPermission:    security.ErrForbidden,
		}},
		policy: scopedPolicy{scope: SiteAccessScope{All: true}},
	}
	router := chi.NewRouter()
	registerManagementRoutes(router, management)
	request := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	request = request.WithContext(httptransport.WithActor(request.Context(), security.User(1)))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload map[string]json.RawMessage
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if _, exists := payload["sites"]; !exists {
		t.Fatalf("sites missing: %s", response.Body.String())
	}
	for _, key := range []string{"resources", "users", "groups"} {
		if _, exists := payload[key]; exists {
			t.Fatalf("forbidden section %q leaked: %s", key, response.Body.String())
		}
	}
	var sitesPayload map[string]json.RawMessage
	if err := json.Unmarshal(payload["sites"], &sitesPayload); err != nil {
		t.Fatal(err)
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(sitesPayload["items"], &items); err != nil {
		t.Fatal(err)
	}
	if _, exists := items[0]["resource_count"]; exists {
		t.Fatalf("resource count leaked: %s", response.Body.String())
	}
}

func TestDashboardRouteRequiresAdminPanelPermission(t *testing.T) {
	t.Parallel()
	authorizer := &accessAuthorizer{err: security.ErrForbidden}
	runtime := &Runtime{
		users:         currentUserService{},
		authorization: authorizer,
	}
	handler, err := runtime.AdminHandler(&Management{})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	request = request.WithContext(httptransport.WithActor(request.Context(), security.User(1)))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if authorizer.code != AccessPermission {
		t.Fatalf("permission = %q", authorizer.code)
	}
}
