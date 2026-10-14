package management

import (
	"context"
	"testing"

	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/resourceviews"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/security"
)

type sourceViewRepository struct {
	resource.ManagementRepository
	source  resource.Resource
	missing bool
}

func (r sourceViewRepository) ByID(context.Context, resource.ID) (resource.Resource, error) {
	if r.missing {
		return resource.Resource{}, resource.ErrNotFound
	}
	return r.source, nil
}

type storedViews struct {
	siteID site.ID
	id     resource.ID
}

func (r *storedViews) ApplyBatch(context.Context, resourceviews.Batch) error { return nil }
func (r *storedViews) Total(_ context.Context, siteID site.ID, id resource.ID) (int64, error) {
	r.siteID = siteID
	r.id = id
	return 37, nil
}

func TestMirrorViewsUseSourceIdentityWithoutSourceEditorAccess(t *testing.T) {
	policy := &siteSpecificPolicy{denied: map[site.ID]error{1: security.ErrForbidden}}
	stats := &storedViews{}
	management := &Resources{authorization: authorization{authorizer: managementAuthorizer{}, policy: policy}, views: stats, resourceRepo: sourceViewRepository{source: resource.Resource{ID: 10, SiteID: 1, Type: resourcetype.Library}}}
	mirror := resource.Resource{ID: 20, SiteID: 2, Type: resourcetype.LibraryMirror, TypeSettings: map[string]any{"source_library_id": int64(10)}}
	if err := management.requireSite(context.Background(), security.User(8), 2, ResourceReadPermission, SiteAccessEdit); err != nil {
		t.Fatal(err)
	}
	total, err := management.resourceViewsTotal(context.Background(), mirror)
	if err != nil || total != 37 || stats.siteID != 1 || stats.id != 10 {
		t.Fatalf("total=%d site=%d id=%d error=%v", total, stats.siteID, stats.id, err)
	}
	if len(policy.checks) != 1 || policy.checks[0] != 2 {
		t.Fatalf("required source editor access: %v", policy.checks)
	}
	management.resourceRepo = sourceViewRepository{missing: true}
	if total, err := management.resourceViewsTotal(context.Background(), mirror); err != nil || total != 0 {
		t.Fatalf("deleted source total=%d error=%v", total, err)
	}
}
