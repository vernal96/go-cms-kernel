package resource

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

type deniedRouteAuthorizer struct{}

func (deniedRouteAuthorizer) Check(
	context.Context,
	security.Actor,
	permission.Code,
) error {
	return security.ErrForbidden
}

type allowPreviewPolicy struct {
	calls int
}

func (p *allowPreviewPolicy) AllowPreview(
	context.Context,
	security.Actor,
	*site.Runtime,
	Resource,
) error {
	p.calls++
	return nil
}

func TestRouteResolverChecksPublicationPathModeAndPermission(t *testing.T) {
	service, repository, _ := newTestService(t)
	siteRuntime, exists := service.sites.RuntimeByID(1)
	if !exists {
		t.Fatal("site runtime is unavailable")
	}
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	path := "/article"

	tests := []struct {
		name string
		item Resource
		want error
	}{
		{
			name: "published",
			item: Resource{
				ID:       1,
				SiteID:   1,
				Type:     "page",
				Path:     &path,
				IsPublic: true,
			},
		},
		{
			name: "private",
			item: Resource{
				ID:     1,
				SiteID: 1,
				Type:   "page",
				Path:   &path,
			},
			want: ErrNotFound,
		},
		{
			name: "scheduled",
			item: Resource{
				ID:          1,
				SiteID:      1,
				Type:        "page",
				Path:        &path,
				IsPublic:    true,
				PublishedAt: timePointer(now.Add(time.Minute)),
			},
			want: ErrNotFound,
		},
		{
			name: "expired",
			item: Resource{
				ID:            1,
				SiteID:        1,
				Type:          "page",
				Path:          &path,
				IsPublic:      true,
				UnpublishedAt: timePointer(now),
			},
			want: ErrNotFound,
		},
		{
			name: "deleted",
			item: Resource{
				ID:        1,
				SiteID:    1,
				Type:      "page",
				Path:      &path,
				IsPublic:  true,
				DeletedAt: timePointer(now),
			},
			want: ErrNotFound,
		},
		{
			name: "no path type",
			item: Resource{
				ID:       1,
				SiteID:   1,
				Type:     "no_path",
				Path:     &path,
				IsPublic: true,
			},
			want: ErrNotFound,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository.items = map[ID]Resource{test.item.ID: test.item}
			resolver, err := NewRouteResolver(
				repository,
				testAuthorizer{},
				WithRouteClock(func() time.Time { return now }),
			)
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := resolver.ResolvePublishedByPath(
				context.Background(),
				security.Guest(),
				siteRuntime,
				path,
				ResolveRouteOptions{},
			)
			if test.want == nil {
				if err != nil || resolved.ID != test.item.ID {
					t.Fatalf("resolved = %#v, error = %v", resolved, err)
				}
				return
			}
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}

	repository.items = map[ID]Resource{1: {
		ID:       1,
		SiteID:   1,
		Type:     "page",
		Path:     &path,
		IsPublic: true,
	}}
	denied, err := NewRouteResolver(
		repository,
		deniedRouteAuthorizer{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := denied.ResolvePublishedByPath(
		context.Background(),
		security.Guest(),
		siteRuntime,
		path,
		ResolveRouteOptions{},
	); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("permission error = %v", err)
	}
}

func TestRouteResolverRequiresExplicitPreviewPolicy(t *testing.T) {
	service, repository, _ := newTestService(t)
	siteRuntime, exists := service.sites.RuntimeByID(1)
	if !exists {
		t.Fatal("site runtime is unavailable")
	}
	path := "/draft"
	repository.items[1] = Resource{
		ID:     1,
		SiteID: 1,
		Type:   "page",
		Path:   &path,
	}

	withoutPolicy, err := NewRouteResolver(
		repository,
		testAuthorizer{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := withoutPolicy.ResolvePublishedByPath(
		context.Background(),
		security.User(42),
		siteRuntime,
		path,
		ResolveRouteOptions{Preview: true},
	); !errors.Is(err, ErrNotFound) {
		t.Fatalf("preview without policy error = %v", err)
	}

	policy := &allowPreviewPolicy{}
	withPolicy, err := NewRouteResolver(
		repository,
		testAuthorizer{},
		WithPreviewPolicy(policy),
	)
	if err != nil {
		t.Fatal(err)
	}
	item, err := withPolicy.ResolvePublishedByPath(
		context.Background(),
		security.User(42),
		siteRuntime,
		path,
		ResolveRouteOptions{Preview: true},
	)
	if err != nil || item.ID != 1 || policy.calls != 1 {
		t.Fatalf(
			"item = %#v, error = %v, policy calls = %d",
			item,
			err,
			policy.calls,
		)
	}
}

func TestNormalizeLookupPathMatchesResourceLookupPolicy(t *testing.T) {
	tests := []struct {
		path string
		ok   bool
	}{
		{path: "/", ok: true},
		{path: "/section", ok: true},
		{path: "/section/child", ok: true},
		{path: "", ok: false},
		{path: "section", ok: false},
		{path: "/section/", ok: false},
		{path: "/section//child", ok: false},
		{path: "/Section", ok: false},
	}
	for _, test := range tests {
		normalized, err := NormalizeLookupPath(test.path)
		if test.ok {
			if err != nil || normalized != test.path ||
				!validLookupPath(test.path) {
				t.Fatalf(
					"path %q normalized = %q, error = %v",
					test.path,
					normalized,
					err,
				)
			}
			continue
		}
		if err == nil || validLookupPath(test.path) {
			t.Fatalf("path %q unexpectedly valid", test.path)
		}
	}
}

func timePointer(value time.Time) *time.Time {
	return &value
}
