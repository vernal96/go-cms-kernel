package resource

import (
	"context"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
)

// RouteTarget contains identity only. Publication and permissions are evaluated
// against the current entity on every request, never stored in the URL cache.
type RouteTarget struct {
	ID        ID
	SiteID    site.ID
	Kind      StorageKind
	LibraryID ID
}

// CacheReadRepository supports independent URL and widget cache misses without
// loading the complete resource for each missing widget.
type CacheReadRepository interface {
	LookupRoute(context.Context, site.ID, string) (RouteTarget, error)
	WidgetsByID(context.Context, ID, []widget.BindingID) ([]widget.Binding, error)
}
