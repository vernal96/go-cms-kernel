package core

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourceviews"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

// Navigation heuristics deliberately accept ordinary unmarked GETs: API clients
// do not always supply Fetch Metadata. Known automation is excluded explicitly.
func resourceViewNavigation(request *http.Request) bool {
	if request.Method != http.MethodGet || httptransport.PreviewFromContext(request.Context()) {
		return false
	}
	for _, header := range []string{"Purpose", "Sec-Purpose", "X-Purpose", "X-Moz", "Sec-Fetch-Purpose"} {
		value := strings.ToLower(request.Header.Get(header))
		if strings.Contains(value, "prefetch") || strings.Contains(value, "prerender") || strings.Contains(value, "preview") {
			return false
		}
	}
	agent := strings.ToLower(request.UserAgent())
	for _, marker := range []string{"bot", "crawler", "spider", "slurp", "bingpreview", "facebookexternalhit", "headlesschrome", "lighthouse", "pagespeed", "whatsapp", "telegram", "vkshare", "pinterest", "embedly"} {
		if strings.Contains(agent, marker) {
			return false
		}
	}
	return true
}

func recordResourceView(request *http.Request, accumulator resourceviews.Accumulator, item resource.Resource, logger *slog.Logger) {
	if accumulator == nil {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 250*time.Millisecond)
	defer cancel()
	if err := accumulator.Increment(ctx, item.SiteID, item.ID); err != nil && logger != nil {
		logger.WarnContext(ctx, "resource view accumulation failed", "event", "resource_views.increment.failed", "site_id", item.SiteID, "resource_id", item.ID, "error", err)
	}
}
