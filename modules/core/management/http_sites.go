package management

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	kernel "github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
)

type contentHTTP struct {
	sites     *Sites
	resources *Resources
}

func SiteManagementRoutePrefixes() []string {
	return []string{"resources", "library-items", "menu", "media", "media-file-deletions"}
}

func registerContentRoutes(router chi.Router, sites *Sites, resources *Resources) {
	handler := &contentHTTP{sites: sites, resources: resources}
	router.Get("/sites/{siteID}/media/{mediaID}/settings", handler.getMediaSettings)
	router.Put("/sites/{siteID}/media/{mediaID}/settings", handler.saveMediaSettings)
	router.Post("/sites/{siteID}/media/{mediaID}/delete-file", handler.deleteMediaFile)
	router.Get("/sites/{siteID}/media-file-deletions/{operationID}", handler.mediaFileDeletionStatus)
	router.Get("/administration/resource-revisions", handler.administrationRevisionCount)
	router.Delete("/administration/resource-revisions", handler.administrationPurgeRevisions)
	router.Get("/sites/options", handler.listSiteOptions)
	router.Get("/sites", handler.listSites)
	router.Post("/sites", handler.createSite)
	router.Get("/site-profiles", handler.listProfiles)
	router.Get("/sites/{siteID}", handler.getSite)
	router.Patch("/sites/{siteID}", handler.updateSite)
	router.Delete("/sites/{siteID}", handler.deleteSite)
	router.Get("/sites/{siteID}/resources", handler.listResourceChildren)
	router.Post("/sites/{siteID}/resources", handler.createResource)
	router.Get("/sites/{siteID}/resources/metadata", handler.resourceMetadata)
	router.Get("/sites/{siteID}/resources/options", handler.resourceOptions)
	router.Get("/sites/{siteID}/resources/lookup", handler.resourceLookup)
	router.Get("/sites/{siteID}/resources/library-sources", handler.librarySources)
	router.Get("/sites/{siteID}/resources/{resourceID}", handler.getResource)
	router.Patch("/sites/{siteID}/resources/{resourceID}", handler.updateResource)
	router.Post("/sites/{siteID}/resources/{resourceID}/widgets", handler.createResourceWidget)
	router.Patch("/sites/{siteID}/resources/{resourceID}/widgets/{widgetID}", handler.updateResourceWidget)
	router.Delete("/sites/{siteID}/resources/{resourceID}/widgets/{widgetID}", handler.deleteResourceWidget)
	router.Put("/sites/{siteID}/resources/{resourceID}/widgets/order", handler.reorderResourceWidgets)
	router.Get("/sites/{siteID}/resources/{resourceID}/revisions", handler.listResourceRevisions)
	router.Get("/sites/{siteID}/resources/{resourceID}/revisions/{version}", handler.getResourceRevision)
	router.Post("/sites/{siteID}/resources/{resourceID}/revisions/{version}/restore", handler.restoreResourceRevision)
	router.Delete("/sites/{siteID}/resources/{resourceID}/revisions", handler.purgeResourceRevisions)
	router.Get("/sites/{siteID}/resources/{resourceID}/extensions/{extensionCode}", handler.getResourceExtension)
	router.Patch("/sites/{siteID}/resources/{resourceID}/extensions/{extensionCode}", handler.saveResourceExtension)
	router.Post("/sites/{siteID}/resources/{resourceID}/extensions/{extensionCode}/preview", handler.previewResourceExtension)
	router.Post("/sites/{siteID}/resources/{resourceID}/move", handler.moveResource)
	router.Post("/sites/{siteID}/resources/{resourceID}/transfer", handler.transferResource)
	router.Delete("/sites/{siteID}/resources/{resourceID}", handler.deleteResource)
	router.Post("/sites/{siteID}/resources/{resourceID}/restore", handler.restoreResource)
	router.Delete("/sites/{siteID}/resources/{resourceID}/permanent", handler.deleteResourcePermanent)
	router.Get("/sites/{siteID}/resources/{resourceID}/items", handler.listLibraryItems)
	router.Post("/sites/{siteID}/resources/{resourceID}/items", handler.createLibraryItem)
	router.Get("/sites/{siteID}/library-items/{itemID}", handler.getLibraryItem)
	router.Patch("/sites/{siteID}/library-items/{itemID}", handler.updateLibraryItem)
	router.Post("/sites/{siteID}/library-items/{itemID}/move", handler.moveLibraryItem)
	router.Delete("/sites/{siteID}/library-items/{itemID}", handler.deleteLibraryItem)
	router.Delete("/sites/{siteID}/library-items/{itemID}/permanent", handler.deleteLibraryItemPermanent)
	router.Post("/sites/{siteID}/library-items/{itemID}/restore", handler.restoreLibraryItem)
	router.Get("/sites/{siteID}/menu", handler.menu)
}

func (h *contentHTTP) administrationRevisionCount(response http.ResponseWriter, request *http.Request) {
	count, err := h.resources.AdministrationRevisionCount(request.Context(), actor(request))
	writeResult(response, http.StatusOK, struct {
		Count int64 `json:"count"`
	}{Count: count}, err)
}

func (h *contentHTTP) administrationPurgeRevisions(response http.ResponseWriter, request *http.Request) {
	count, err := h.resources.AdministrationPurgeRevisions(request.Context(), actor(request))
	writeResult(response, http.StatusOK, struct {
		Count int64 `json:"count"`
	}{Count: count}, err)
}

func revisionVersion(response http.ResponseWriter, request *http.Request) (int64, bool) {
	version, err := strconv.ParseInt(chi.URLParam(request, "version"), 10, 64)
	if err != nil || version <= 0 {
		writeBadRequest(response, "revision version is invalid")
		return 0, false
	}
	return version, true
}

func (h *contentHTTP) listResourceRevisions(response http.ResponseWriter, request *http.Request) {
	siteID, resourceID, ok := resourceWidgetResourceParams(response, request)
	if !ok {
		return
	}
	page, perPage, ok := parsePagination(response, request)
	if !ok {
		return
	}
	result, err := h.resources.Revisions(request.Context(), actor(request), siteID, resourceID, page, perPage)
	writeResult(response, http.StatusOK, result, err)
}

func (h *contentHTTP) getResourceRevision(response http.ResponseWriter, request *http.Request) {
	siteID, resourceID, ok := resourceWidgetResourceParams(response, request)
	if !ok {
		return
	}
	version, ok := revisionVersion(response, request)
	if !ok {
		return
	}
	result, err := h.resources.Revision(request.Context(), actor(request), siteID, resourceID, version)
	writeResult(response, http.StatusOK, result, err)
}

func (h *contentHTTP) restoreResourceRevision(response http.ResponseWriter, request *http.Request) {
	siteID, resourceID, ok := resourceWidgetResourceParams(response, request)
	if !ok {
		return
	}
	version, ok := revisionVersion(response, request)
	if !ok {
		return
	}
	var payload struct {
		ExpectedVersion int64 `json:"expected_version"`
	}
	if !decodeBody(response, request, &payload) {
		return
	}
	if payload.ExpectedVersion <= 0 {
		writeValidation(response, "expected_version is required")
		return
	}
	result, err := h.resources.RestoreRevision(request.Context(), actor(request), siteID, resourceID, version, payload.ExpectedVersion)
	writeResult(response, http.StatusOK, result, err)
}

func (h *contentHTTP) purgeResourceRevisions(response http.ResponseWriter, request *http.Request) {
	siteID, resourceID, ok := resourceWidgetResourceParams(response, request)
	if !ok {
		return
	}
	count, err := h.resources.PurgeRevisions(request.Context(), actor(request), siteID, resourceID)
	writeResult(response, http.StatusOK, struct {
		Count int64 `json:"count"`
	}{Count: count}, err)
}

func (h *contentHTTP) listSites(response http.ResponseWriter, request *http.Request) {
	page, perPage, ok := parsePagination(response, request)
	if !ok {
		return
	}
	result, err := h.sites.ListSites(
		request.Context(), actor(request), request.URL.Query().Get("search"), page, perPage,
	)
	writeResult(response, http.StatusOK, result, err)
}

func (h *contentHTTP) menu(response http.ResponseWriter, request *http.Request) {
	id, ok := siteID(response, request)
	if !ok {
		return
	}
	result, err := h.resources.Menu(request.Context(), actor(request), id)
	writeResult(response, http.StatusOK, result, err)
}

func (h *contentHTTP) listSiteOptions(response http.ResponseWriter, request *http.Request) {
	page, perPage, ok := parsePagination(response, request)
	if !ok {
		return
	}
	var exclude []site.ID
	if raw := strings.TrimSpace(request.URL.Query().Get("exclude_id")); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value <= 0 {
			writeBadRequest(response, "exclude_id is invalid")
			return
		}
		exclude = append(exclude, site.ID(value))
	}
	result, err := h.sites.ListSiteOptions(
		request.Context(), actor(request), request.URL.Query().Get("search"), page, perPage, exclude...,
	)
	writeResult(response, http.StatusOK, result, err)
}

type createSiteRequest struct {
	ProfileCode kernel.ProfileCode `json:"profile_code"`
	Name        string             `json:"name"`
	Domain      string             `json:"domain"`
	Locale      string             `json:"locale"`
	Settings    map[string]any     `json:"settings"`
	IsPublic    bool               `json:"is_public"`
}

func (h *contentHTTP) createSite(response http.ResponseWriter, request *http.Request) {
	var payload createSiteRequest
	if !decodeBody(response, request, &payload) {
		return
	}
	if payload.Settings == nil {
		payload.Settings = map[string]any{}
	}
	result, err := h.sites.CreateSite(request.Context(), actor(request), SiteCreateInput{
		ProfileCode: payload.ProfileCode,
		Name:        payload.Name,
		Domain:      payload.Domain,
		Locale:      payload.Locale,
		Settings:    payload.Settings,
		IsPublic:    payload.IsPublic,
	})
	writeResult(response, http.StatusCreated, result, err)
}

func (h *contentHTTP) getSite(response http.ResponseWriter, request *http.Request) {
	id, ok := siteID(response, request)
	if !ok {
		return
	}
	result, err := h.sites.Site(request.Context(), actor(request), id)
	writeResult(response, http.StatusOK, result, err)
}

type updateSiteRequest struct {
	ProfileCode kernel.ProfileCode `json:"profile_code"`
	Name        string             `json:"name"`
	Domain      string             `json:"domain"`
	Locale      string             `json:"locale"`
	Settings    map[string]any     `json:"settings"`
	IsPublic    *bool              `json:"is_public"`
}

func (h *contentHTTP) updateSite(response http.ResponseWriter, request *http.Request) {
	id, ok := siteID(response, request)
	if !ok {
		return
	}
	var payload updateSiteRequest
	if !decodeBody(response, request, &payload) {
		return
	}
	if payload.IsPublic == nil {
		writeValidation(response, "is_public is required")
		return
	}
	if payload.Settings == nil {
		writeValidation(response, "settings is required")
		return
	}
	result, err := h.sites.UpdateSite(request.Context(), actor(request), id, SiteUpdateInput{
		ProfileCode: payload.ProfileCode,
		Name:        payload.Name,
		Domain:      payload.Domain,
		Locale:      payload.Locale,
		Settings:    payload.Settings,
		IsPublic:    *payload.IsPublic,
	})
	writeResult(response, http.StatusOK, result, err)
}

func (h *contentHTTP) deleteSite(response http.ResponseWriter, request *http.Request) {
	id, ok := siteID(response, request)
	if !ok {
		return
	}
	if err := h.sites.DeleteSite(request.Context(), actor(request), id); err != nil {
		writeManagementError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (h *contentHTTP) listProfiles(response http.ResponseWriter, request *http.Request) {
	result, err := h.sites.Profiles(request.Context(), actor(request))
	writeResult(response, http.StatusOK, result, err)
}
