package management

import (
	"context"
	"errors"
	"testing"

	"github.com/vernal96/go-cms-kernel/modules/core/access"
	"github.com/vernal96/go-cms-kernel/modules/core/group"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/security"
)

type policyGroupRepository struct {
	group.Repository
	scopes map[group.SiteAccessAction][]site.ID
	grants map[group.SiteAccessAction]map[site.ID]bool
}

func (r policyGroupRepository) EffectiveSiteIDs(_ context.Context, _ security.UserID, action group.SiteAccessAction) ([]site.ID, error) {
	return append([]site.ID(nil), r.scopes[action]...), nil
}

func (r policyGroupRepository) UserHasSiteAccess(_ context.Context, _ security.UserID, siteID site.ID, action group.SiteAccessAction) (bool, error) {
	return r.grants[action][siteID], nil
}

type policyAccess struct {
	access.Service
	privileged bool
	err        error
}

func (a policyAccess) IsPrivileged(context.Context, security.Actor) (bool, error) {
	return a.privileged, a.err
}

func TestGroupSiteAccessPolicyScopesAndChecksCapabilities(t *testing.T) {
	t.Parallel()
	repository := policyGroupRepository{
		scopes: map[group.SiteAccessAction][]site.ID{
			group.SiteAccessView: {1, 2},
			group.SiteAccessEdit: {2},
		},
		grants: map[group.SiteAccessAction]map[site.ID]bool{
			group.SiteAccessEdit: {2: true},
		},
	}
	policy, err := NewGroupSiteAccessPolicy(repository, policyAccess{})
	if err != nil {
		t.Fatal(err)
	}
	view, err := policy.Scope(context.Background(), security.User(9), SiteAccessView)
	if err != nil || len(view.SiteIDs) != 2 {
		t.Fatalf("view scope = %#v, %v", view, err)
	}
	edit, err := policy.Scope(context.Background(), security.User(9), SiteAccessEdit)
	if err != nil || len(edit.SiteIDs) != 1 || edit.SiteIDs[0] != 2 {
		t.Fatalf("edit scope = %#v, %v", edit, err)
	}
	if err := policy.Check(context.Background(), security.User(9), 2, SiteAccessEdit); err != nil {
		t.Fatalf("edit check = %v", err)
	}
	if err := policy.Check(context.Background(), security.User(9), 1, SiteAccessEdit); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("missing edit check = %v", err)
	}
}

func TestGroupSiteAccessPolicySuperUserIsUnrestricted(t *testing.T) {
	t.Parallel()
	policy, err := NewGroupSiteAccessPolicy(policyGroupRepository{}, policyAccess{privileged: true})
	if err != nil {
		t.Fatal(err)
	}
	scope, err := policy.Scope(context.Background(), security.User(1), SiteAccessDelete)
	if err != nil || !scope.All {
		t.Fatalf("scope = %#v, %v", scope, err)
	}
	if err := policy.Check(context.Background(), security.User(1), 999, SiteAccessDelete); err != nil {
		t.Fatal(err)
	}
}
