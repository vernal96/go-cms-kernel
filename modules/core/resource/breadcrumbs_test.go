package resource

import (
	"testing"
	"time"
)

func TestPublishedBreadcrumbUsesCurrentTimeAndDeletionState(t *testing.T) {
	now := time.Date(2026, time.October, 15, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Minute)
	future := now.Add(time.Minute)
	tests := []struct {
		name      string
		candidate BreadcrumbCandidate
		want      bool
	}{
		{name: "published", candidate: BreadcrumbCandidate{IsPublic: true}, want: true},
		{name: "not public", candidate: BreadcrumbCandidate{IsPublic: false}},
		{name: "starts later", candidate: BreadcrumbCandidate{IsPublic: true, PublishedAt: &future}},
		{name: "ended", candidate: BreadcrumbCandidate{IsPublic: true, UnpublishedAt: &past}},
		{name: "deleted", candidate: BreadcrumbCandidate{IsPublic: true, DeletedAt: &past}},
		{name: "publication boundary", candidate: BreadcrumbCandidate{IsPublic: true, PublishedAt: &now}, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := PublishedBreadcrumb(test.candidate, now); got != test.want {
				t.Fatalf("PublishedBreadcrumb() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestBreadcrumbTitleUsesMenuTitleThenResourceTitle(t *testing.T) {
	if got := BreadcrumbTitle(BreadcrumbCandidate{Title: "Page", MenuTitle: "Menu label"}); got != "Menu label" {
		t.Fatalf("menu title = %q", got)
	}
	if got := BreadcrumbTitle(BreadcrumbCandidate{Title: "Page"}); got != "Page" {
		t.Fatalf("fallback title = %q", got)
	}
}

func TestVisibleBreadcrumbsSkipsUnavailableIntermediatesAndTracksSchedule(t *testing.T) {
	base := time.Date(2026, time.October, 15, 12, 0, 0, 0, time.UTC)
	future := base.Add(time.Hour)
	past := base.Add(-time.Hour)
	candidates := []BreadcrumbCandidate{
		{ID: 1, Title: "Home", Link: "/", IsPublic: true},
		{ID: 2, Title: "Hidden", Link: "/hidden", IsPublic: false},
		{ID: 3, Title: "Deleted", Link: "/deleted", IsPublic: true, DeletedAt: &past},
		{ID: 4, Title: "Scheduled", Link: "/scheduled", IsPublic: true, PublishedAt: &future},
		{ID: 5, Title: "Current", Link: "/current", IsPublic: true},
	}
	if visible := VisibleBreadcrumbs(candidates, base); len(visible) != 2 || visible[0].ID != 1 || visible[1].ID != 5 {
		t.Fatalf("visible breadcrumbs before publication = %#v", visible)
	}
	// The candidate metadata stays warm; request-time filtering reflects its
	// scheduled publication without waiting for a cache expiry.
	if visible := VisibleBreadcrumbs(candidates, future); len(visible) != 3 || visible[1].ID != 4 {
		t.Fatalf("visible breadcrumbs after publication = %#v", visible)
	}
}
