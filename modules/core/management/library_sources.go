package management

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/security"
)

type LibrarySource struct {
	ID       resource.ID `json:"id"`
	SiteID   site.ID     `json:"site_id"`
	SiteName string      `json:"site_name"`
	Domain   string      `json:"domain"`
	Title    string      `json:"title"`
	Path     *string     `json:"path"`
}

func (m *Resources) checkLibrarySource(ctx context.Context, actor security.Actor, settings map[string]any) error {
	id := resource.ID(resourcetype.SourceLibraryID(settings))
	if id <= 0 {
		return ErrValidation
	}
	source, err := m.resourceRepo.ByID(ctx, id)
	if err != nil {
		return err
	}
	if err := m.requireSite(ctx, actor, source.SiteID, ResourceReadPermission, SiteAccessView); err != nil {
		return err
	}
	if source.Type != resourcetype.Library || source.DeletedAt != nil {
		return ErrValidation
	}
	return nil
}

func (h *contentHTTP) librarySources(response http.ResponseWriter, request *http.Request) {
	target, ok := siteID(response, request)
	if !ok {
		return
	}
	m := h.resources
	if err := m.requireSite(request.Context(), actor(request), target, ResourceReadPermission, SiteAccessEdit); err != nil {
		writeManagementError(response, err)
		return
	}
	selected, err := optionalPositiveID(request.URL.Query().Get("selected_id"))
	if err != nil {
		writeBadRequest(response, "selected_id is invalid")
		return
	}
	sourceSite, err := optionalPositiveID(request.URL.Query().Get("source_site_id"))
	if err != nil {
		writeBadRequest(response, "source_site_id is invalid")
		return
	}
	var items []resource.Resource
	if selected > 0 {
		item, err := m.resourceRepo.ByID(request.Context(), resource.ID(selected))
		if err != nil {
			writeManagementError(response, err)
			return
		}
		items = []resource.Resource{item}
		sourceSite = int64(item.SiteID)
	}
	if sourceSite <= 0 {
		writeBadRequest(response, "source_site_id is required")
		return
	}
	if err := m.requireSite(request.Context(), actor(request), site.ID(sourceSite), ResourceReadPermission, SiteAccessView); err != nil {
		writeManagementError(response, err)
		return
	}
	if selected == 0 {
		items, err = m.resourceRepo.ListBySite(request.Context(), site.ID(sourceSite))
		if err != nil {
			writeManagementError(response, err)
			return
		}
	}
	runtime, exists := m.sites.RuntimeByID(site.ID(sourceSite))
	if !exists {
		writeManagementError(response, site.ErrNotFound)
		return
	}
	search := strings.ToLower(strings.TrimSpace(request.URL.Query().Get("search")))
	options := make([]LibrarySource, 0)
	for _, item := range items {
		if item.Type != resourcetype.Library || item.DeletedAt != nil {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(item.Title), search) && (item.Path == nil || !strings.Contains(strings.ToLower(*item.Path), search)) {
			continue
		}
		options = append(options, LibrarySource{ID: item.ID, SiteID: item.SiteID, SiteName: runtime.Site().Name, Domain: runtime.Site().Domain, Title: item.Title, Path: item.Path})
	}
	page, perPage, ok := parsePagination(response, request)
	if !ok {
		return
	}
	page, perPage, err = normalizePagination(page, perPage)
	if err != nil {
		writeManagementError(response, err)
		return
	}
	total := len(options)
	start := min((page-1)*perPage, total)
	end := min(start+perPage, total)
	writeResult(response, http.StatusOK, struct {
		Items      []LibrarySource `json:"items"`
		Pagination Pagination      `json:"pagination"`
	}{options[start:end], Pagination{Page: page, PerPage: perPage, Total: total}}, nil)
}

func optionalPositiveID(raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, ErrValidation
	}
	return id, nil
}
