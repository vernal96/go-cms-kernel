package resource

import (
	"context"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/site"
)

// BreadcrumbCandidate is a tree resource that may appear in a public
// breadcrumb chain. Publication is evaluated at request time because it can
// change without a resource mutation.
type BreadcrumbCandidate struct {
	ID            ID
	Title         string
	MenuTitle     string
	Link          string
	IsPublic      bool
	PublishedAt   *time.Time
	UnpublishedAt *time.Time
	DeletedAt     *time.Time
}

// BreadcrumbRepository loads one resource's focused ancestry and, optionally,
// a separate root-path home candidate.
type BreadcrumbRepository interface {
	BreadcrumbCandidates(context.Context, site.ID, ID, bool) ([]BreadcrumbCandidate, error)
}

func PublishedBreadcrumb(candidate BreadcrumbCandidate, now time.Time) bool {
	return candidate.DeletedAt == nil && candidate.IsPublic &&
		(candidate.PublishedAt == nil || !now.Before(*candidate.PublishedAt)) &&
		(candidate.UnpublishedAt == nil || now.Before(*candidate.UnpublishedAt))
}

func VisibleBreadcrumbs(candidates []BreadcrumbCandidate, now time.Time) []BreadcrumbCandidate {
	result := make([]BreadcrumbCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if PublishedBreadcrumb(candidate, now) {
			result = append(result, candidate)
		}
	}
	return result
}

func BreadcrumbTitle(candidate BreadcrumbCandidate) string {
	if candidate.MenuTitle != "" {
		return candidate.MenuTitle
	}
	return candidate.Title
}
