package management

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	image "github.com/vernal96/go-cms-kernel/modules/core/image"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
	"github.com/vernal96/go-cms-kernel/modules/resourceextension"
	"github.com/vernal96/go-cms-kernel/security"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

type contentHTTP struct {
	sites     *Sites
	resources *Resources
}

// SiteManagementRoutePrefixes returns the first path segments owned by the
// mandatory Core site-management API. Optional module contributions must not
// claim any of these prefixes.
func SiteManagementRoutePrefixes() []string {
	return []string{"resources", "library-items", "menu", "media"}
}

func registerContentRoutes(router chi.Router, sites *Sites, resources *Resources) {
	handler := &contentHTTP{sites: sites, resources: resources}
	router.Get("/sites/{siteID}/media/{mediaID}/settings", handler.getMediaSettings)
	router.Put("/sites/{siteID}/media/{mediaID}/settings", handler.saveMediaSettings)
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

func (h *contentHTTP) listResourceChildren(response http.ResponseWriter, request *http.Request) {
	id, ok := siteID(response, request)
	if !ok {
		return
	}
	var parentID *resource.ID
	if raw := request.URL.Query().Get("parent_id"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed <= 0 {
			writeBadRequest(response, "parent_id is invalid")
			return
		}
		value := resource.ID(parsed)
		parentID = &value
	}
	result, err := h.resources.ResourceChildren(request.Context(), actor(request), id, parentID)
	writeResult(response, http.StatusOK, result, err)
}

func (h *contentHTTP) resourceMetadata(response http.ResponseWriter, request *http.Request) {
	id, ok := siteID(response, request)
	if !ok {
		return
	}
	result, err := h.resources.ResourceMetadata(request.Context(), actor(request), id)
	writeResult(response, http.StatusOK, result, err)
}

type createResourceRequest struct {
	ParentID         *resource.ID      `json:"parent_id"`
	Type             resourcetype.Code `json:"type"`
	Template         *template.Code    `json:"template_code"`
	ContentType      *string           `json:"content_type"`
	Content          string            `json:"content"`
	TargetResourceID *resource.ID      `json:"target_resource_id"`
	Title            string            `json:"title"`
	MenuTitle        string            `json:"menu_title"`
	Slug             string            `json:"slug"`
	ExternalURL      *string           `json:"external_url"`
	Fields           map[string]any    `json:"fields"`
	TypeSettings     map[string]any    `json:"type_settings"`
}

func (h *contentHTTP) createResource(response http.ResponseWriter, request *http.Request) {
	id, ok := siteID(response, request)
	if !ok {
		return
	}
	var payload createResourceRequest
	if !decodeBody(response, request, &payload) {
		return
	}
	if payload.Fields == nil || payload.TypeSettings == nil {
		writeValidation(response, "fields and type_settings are required")
		return
	}
	result, err := h.resources.CreateResource(request.Context(), actor(request), id, ResourceCreateInput{
		ParentID:         payload.ParentID,
		Type:             payload.Type,
		Template:         payload.Template,
		ContentType:      payload.ContentType,
		Content:          payload.Content,
		TargetResourceID: payload.TargetResourceID,
		Title:            payload.Title,
		MenuTitle:        payload.MenuTitle,
		Slug:             payload.Slug,
		ExternalURL:      payload.ExternalURL,
		Fields:           payload.Fields,
		TypeSettings:     payload.TypeSettings,
	})
	writeResult(response, http.StatusCreated, result, err)
}

func (h *contentHTTP) resourceOptions(response http.ResponseWriter, request *http.Request) {
	siteID, ok := siteID(response, request)
	if !ok {
		return
	}
	result, err := h.resources.ResourceOptions(request.Context(), actor(request), siteID)
	writeResult(response, http.StatusOK, result, err)
}

func (h *contentHTTP) resourceLookup(response http.ResponseWriter, request *http.Request) {
	siteID, ok := siteID(response, request)
	if !ok {
		return
	}
	page, perPage, ok := parsePagination(response, request)
	if !ok {
		return
	}
	result, err := h.resources.ResourceLookup(request.Context(), actor(request), siteID, request.URL.Query().Get("search"), page, perPage)
	writeResult(response, http.StatusOK, result, err)
}

func (h *contentHTTP) getResource(response http.ResponseWriter, request *http.Request) {
	siteID, ok := siteID(response, request)
	if !ok {
		return
	}
	resourceID, ok := resourceID(response, request)
	if !ok {
		return
	}
	result, err := h.resources.Resource(request.Context(), actor(request), siteID, resourceID)
	writeResult(response, http.StatusOK, result, err)
}

type updateResourceRequest struct {
	ImageMediaID     *media.ID         `json:"image_media_id"`
	ExpectedVersion  int64             `json:"expected_version"`
	ParentID         *resource.ID      `json:"parent_id"`
	Type             resourcetype.Code `json:"type"`
	Template         *template.Code    `json:"template_code"`
	Title            string            `json:"title"`
	MenuTitle        string            `json:"menu_title"`
	Slug             string            `json:"slug"`
	Annotation       string            `json:"annotation"`
	Content          string            `json:"content"`
	ContentType      *string           `json:"content_type"`
	TargetResourceID *resource.ID      `json:"target_resource_id"`
	ExternalURL      *string           `json:"external_url"`
	IsPublic         *bool             `json:"is_public"`
	IsSearchable     *bool             `json:"is_searchable"`
	InMenu           *bool             `json:"in_menu"`
	InSitemap        *bool             `json:"in_sitemap"`
	Sort             *int              `json:"sort"`
	PublishedAt      *time.Time        `json:"published_at"`
	UnpublishedAt    *time.Time        `json:"unpublished_at"`
	Fields           map[string]any    `json:"fields"`
	TypeSettings     map[string]any    `json:"type_settings"`
}

func (h *contentHTTP) updateResource(response http.ResponseWriter, request *http.Request) {
	siteID, ok := siteID(response, request)
	if !ok {
		return
	}
	resourceID, ok := resourceID(response, request)
	if !ok {
		return
	}
	var payload updateResourceRequest
	if !decodeBody(response, request, &payload) {
		return
	}
	if payload.IsPublic == nil || payload.IsSearchable == nil ||
		payload.InMenu == nil || payload.InSitemap == nil || payload.Sort == nil ||
		payload.Fields == nil || payload.TypeSettings == nil {
		writeValidation(response, "resource flags, sort, fields and type_settings are required")
		return
	}
	if payload.ExpectedVersion <= 0 {
		writeValidation(response, "expected_version is required")
		return
	}
	result, err := h.resources.UpdateResource(
		request.Context(), actor(request), siteID, resourceID,
		ResourceUpdateInput{
			ImageMediaID:     payload.ImageMediaID,
			ExpectedVersion:  payload.ExpectedVersion,
			ParentID:         payload.ParentID,
			Type:             payload.Type,
			Template:         payload.Template,
			Title:            payload.Title,
			MenuTitle:        payload.MenuTitle,
			Slug:             payload.Slug,
			Annotation:       payload.Annotation,
			Content:          payload.Content,
			ContentType:      payload.ContentType,
			TargetResourceID: payload.TargetResourceID,
			ExternalURL:      payload.ExternalURL,
			IsPublic:         *payload.IsPublic,
			IsSearchable:     *payload.IsSearchable,
			InMenu:           *payload.InMenu,
			InSitemap:        *payload.InSitemap,
			Sort:             *payload.Sort,
			PublishedAt:      payload.PublishedAt,
			UnpublishedAt:    payload.UnpublishedAt,
			Fields:           payload.Fields,
			TypeSettings:     payload.TypeSettings,
		},
	)
	writeResult(response, http.StatusOK, result, err)
}

type resourceWidgetPresentationRequest struct {
	ExpectedVersion int64                `json:"expected_version"`
	View            widget.ViewCode      `json:"view"`
	Columns         int                  `json:"columns"`
	MarginTop       int                  `json:"margin_top"`
	MarginBottom    int                  `json:"margin_bottom"`
	Enabled         *bool                `json:"enabled"`
	Params          map[string]any       `json:"params"`
	ParamBindings   widget.ParamBindings `json:"param_bindings"`
}

type createResourceWidgetRequest struct {
	Code widget.Code     `json:"code"`
	Area widget.AreaCode `json:"area"`
	resourceWidgetPresentationRequest
}

func (h *contentHTTP) createResourceWidget(response http.ResponseWriter, request *http.Request) {
	siteID, resourceID, ok := resourceWidgetResourceParams(response, request)
	if !ok {
		return
	}
	var payload createResourceWidgetRequest
	if !decodeBody(response, request, &payload) {
		return
	}
	if payload.Params == nil {
		writeValidation(response, "widget params are required")
		return
	}
	if payload.ExpectedVersion <= 0 {
		writeValidation(response, "expected_version is required")
		return
	}
	result, err := h.resources.CreateResourceWidget(request.Context(), actor(request), siteID, resourceID, resource.CreateWidgetInput{
		Code: payload.Code, Area: payload.Area, View: payload.View, Columns: payload.Columns,
		ExpectedVersion: payload.ExpectedVersion,
		MarginTop:       payload.MarginTop, MarginBottom: payload.MarginBottom,
		Enabled: payload.Enabled, Params: payload.Params, ParamBindings: payload.ParamBindings,
	})
	writeResult(response, http.StatusCreated, result, err)
}

func (h *contentHTTP) updateResourceWidget(response http.ResponseWriter, request *http.Request) {
	siteID, resourceID, ok := resourceWidgetResourceParams(response, request)
	if !ok {
		return
	}
	bindingID, ok := widgetID(response, request)
	if !ok {
		return
	}
	var payload resourceWidgetPresentationRequest
	if !decodeBody(response, request, &payload) {
		return
	}
	if payload.Params == nil || payload.Enabled == nil {
		writeValidation(response, "widget params and enabled are required")
		return
	}
	if payload.ExpectedVersion <= 0 {
		writeValidation(response, "expected_version is required")
		return
	}
	result, err := h.resources.UpdateResourceWidget(request.Context(), actor(request), siteID, resourceID, bindingID, resource.UpdateWidgetInput{
		View: payload.View, Columns: payload.Columns, MarginTop: payload.MarginTop,
		ExpectedVersion: payload.ExpectedVersion,
		MarginBottom:    payload.MarginBottom, Enabled: payload.Enabled, Params: payload.Params, ParamBindings: payload.ParamBindings,
	})
	writeResult(response, http.StatusOK, result, err)
}

func (h *contentHTTP) deleteResourceWidget(response http.ResponseWriter, request *http.Request) {
	siteID, resourceID, ok := resourceWidgetResourceParams(response, request)
	if !ok {
		return
	}
	bindingID, ok := widgetID(response, request)
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
	if err := h.resources.DeleteResourceWidget(request.Context(), actor(request), siteID, resourceID, bindingID, payload.ExpectedVersion); err != nil {
		writeManagementError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

type reorderResourceWidgetsRequest struct {
	ExpectedVersion int64          `json:"expected_version"`
	Items           []widget.Order `json:"items"`
}

func (h *contentHTTP) reorderResourceWidgets(response http.ResponseWriter, request *http.Request) {
	siteID, resourceID, ok := resourceWidgetResourceParams(response, request)
	if !ok {
		return
	}
	var payload reorderResourceWidgetsRequest
	if !decodeBody(response, request, &payload) {
		return
	}
	if payload.Items == nil {
		writeValidation(response, "widget order items are required")
		return
	}
	if payload.ExpectedVersion <= 0 {
		writeValidation(response, "expected_version is required")
		return
	}
	result, err := h.resources.ReorderResourceWidgets(request.Context(), actor(request), siteID, resourceID, payload.ExpectedVersion, payload.Items)
	writeResult(response, http.StatusOK, struct {
		Items []ResourceWidget `json:"items"`
	}{Items: result}, err)
}

func resourceWidgetResourceParams(response http.ResponseWriter, request *http.Request) (site.ID, resource.ID, bool) {
	siteID, ok := siteID(response, request)
	if !ok {
		return 0, 0, false
	}
	resourceID, ok := resourceID(response, request)
	if !ok {
		return 0, 0, false
	}
	return siteID, resourceID, true
}

func widgetID(response http.ResponseWriter, request *http.Request) (widget.BindingID, bool) {
	parsed, err := strconv.ParseInt(chi.URLParam(request, "widgetID"), 10, 64)
	if err != nil || parsed <= 0 {
		writeBadRequest(response, "widget_id is invalid")
		return 0, false
	}
	return widget.BindingID(parsed), true
}

type moveResourceRequest struct {
	ParentID        *resource.ID `json:"parent_id"`
	Position        *int         `json:"position"`
	ExpectedVersion int64        `json:"expected_version"`
}

func (h *contentHTTP) moveResource(response http.ResponseWriter, request *http.Request) {
	siteID, ok := siteID(response, request)
	if !ok {
		return
	}
	resourceID, ok := resourceID(response, request)
	if !ok {
		return
	}
	var payload moveResourceRequest
	if !decodeBody(response, request, &payload) {
		return
	}
	if payload.Position == nil || *payload.Position < 0 || payload.ExpectedVersion <= 0 {
		writeValidation(response, "position is required")
		return
	}
	result, err := h.resources.MoveResource(request.Context(), actor(request), siteID, resourceID, payload.ParentID, *payload.Position, payload.ExpectedVersion)
	writeResult(response, http.StatusOK, result, err)
}

type transferResourceRequest struct {
	TargetSiteID    site.ID `json:"target_site_id"`
	ExpectedVersion int64   `json:"expected_version"`
}

func (h *contentHTTP) transferResource(response http.ResponseWriter, request *http.Request) {
	sourceSiteID, ok := siteID(response, request)
	if !ok {
		return
	}
	resourceID, ok := resourceID(response, request)
	if !ok {
		return
	}
	var payload transferResourceRequest
	if !decodeBody(response, request, &payload) {
		return
	}
	if payload.TargetSiteID <= 0 || payload.ExpectedVersion <= 0 {
		writeValidation(response, "target_site_id and expected_version are required")
		return
	}
	result, err := h.resources.TransferResourceToSite(
		request.Context(), actor(request), sourceSiteID, resourceID,
		payload.TargetSiteID, payload.ExpectedVersion,
	)
	writeResult(response, http.StatusOK, result, err)
}

func (h *contentHTTP) getResourceExtension(
	response http.ResponseWriter,
	request *http.Request,
) {
	siteID, resourceID, code, ok := resourceExtensionParams(response, request)
	if !ok {
		return
	}
	result, err := h.resources.ResourceExtension(
		request.Context(), actor(request), siteID, resourceID, code,
	)
	writeResult(response, http.StatusOK, result, err)
}

func (h *contentHTTP) saveResourceExtension(
	response http.ResponseWriter,
	request *http.Request,
) {
	siteID, resourceID, code, ok := resourceExtensionParams(response, request)
	if !ok {
		return
	}
	var payload json.RawMessage
	if !decodeBody(response, request, &payload) {
		return
	}
	result, err := h.resources.SaveResourceExtension(
		request.Context(), actor(request), siteID, resourceID, code, payload,
	)
	writeResult(response, http.StatusOK, result, err)
}

func (h *contentHTTP) previewResourceExtension(
	response http.ResponseWriter,
	request *http.Request,
) {
	siteID, resourceID, code, ok := resourceExtensionParams(response, request)
	if !ok {
		return
	}
	var payload json.RawMessage
	if !decodeBody(response, request, &payload) {
		return
	}
	result, err := h.resources.PreviewResourceExtension(
		request.Context(), actor(request), siteID, resourceID, code, payload,
	)
	writeResult(response, http.StatusOK, result, err)
}

func resourceExtensionParams(
	response http.ResponseWriter,
	request *http.Request,
) (site.ID, resource.ID, resourceextension.Code, bool) {
	siteID, ok := siteID(response, request)
	if !ok {
		return 0, 0, "", false
	}
	resourceID, ok := resourceID(response, request)
	if !ok {
		return 0, 0, "", false
	}
	code := resourceextension.Code(chi.URLParam(request, "extensionCode"))
	if code == "" {
		writeBadRequest(response, "extension code is invalid")
		return 0, 0, "", false
	}
	return siteID, resourceID, code, true
}

func (h *contentHTTP) deleteResource(response http.ResponseWriter, request *http.Request) {
	h.resourceDelete(response, request, false)
}

func (h *contentHTTP) deleteResourcePermanent(response http.ResponseWriter, request *http.Request) {
	h.resourceDelete(response, request, true)
}

func (h *contentHTTP) resourceDelete(response http.ResponseWriter, request *http.Request, permanent bool) {
	siteID, ok := siteID(response, request)
	if !ok {
		return
	}
	resourceID, ok := resourceID(response, request)
	if !ok {
		return
	}
	err := h.resources.DeleteResource(request.Context(), actor(request), siteID, resourceID, permanent)
	if err != nil {
		writeManagementError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

type restoreResourceRequest struct {
	WithDescendants bool `json:"with_descendants"`
}

func (h *contentHTTP) restoreResource(response http.ResponseWriter, request *http.Request) {
	siteID, ok := siteID(response, request)
	if !ok {
		return
	}
	resourceID, ok := resourceID(response, request)
	if !ok {
		return
	}
	var payload restoreResourceRequest
	if !decodeBody(response, request, &payload) {
		return
	}
	err := h.resources.RestoreResource(request.Context(), actor(request), siteID, resourceID, payload.WithDescendants)
	if err != nil {
		writeManagementError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

type libraryItemRequest struct {
	ImageMediaID    *media.ID      `json:"image_media_id"`
	ExpectedVersion int64          `json:"expected_version"`
	Template        *template.Code `json:"template_code"`
	Title           string         `json:"title"`
	Slug            string         `json:"slug"`
	Annotation      string         `json:"annotation"`
	Content         string         `json:"content"`
	IsPublic        *bool          `json:"is_public"`
	IsSearchable    *bool          `json:"is_searchable"`
	PublishedAt     *time.Time     `json:"published_at"`
	UnpublishedAt   *time.Time     `json:"unpublished_at"`
	Fields          map[string]any `json:"fields"`
}

type semanticFilterRequest struct {
	Field    string                  `json:"field"`
	Operator resource.FilterOperator `json:"operator"`
	Value    any                     `json:"value"`
}

type semanticSortRequest struct {
	Field     string                 `json:"field"`
	Direction resource.SortDirection `json:"direction"`
}

func (h *contentHTTP) listLibraryItems(response http.ResponseWriter, request *http.Request) {
	siteID, libraryID, ok := resourceWidgetResourceParams(response, request)
	if !ok {
		return
	}
	limit := 25
	if raw := request.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			writeBadRequest(response, "limit is invalid")
			return
		}
		limit = parsed
	}
	filters, sorts, err := parseLibraryItemSemanticQuery(request)
	if err != nil {
		writeBadRequest(response, err.Error())
		return
	}
	result, err := h.resources.LibraryItems(request.Context(), actor(request), siteID, libraryID, LibraryItemsInput{
		Cursor: request.URL.Query().Get("cursor"), Limit: limit, Search: request.URL.Query().Get("search"),
		Filters: filters, Sort: sorts,
	})
	writeResult(response, http.StatusOK, result, err)
}

func parseLibraryItemSemanticQuery(request *http.Request) ([]resource.FilterCondition, []resource.Sort, error) {
	var filterRequests []semanticFilterRequest
	if raw := request.URL.Query().Get("filters"); raw != "" {
		if err := decodeQueryJSON(raw, &filterRequests); err != nil {
			return nil, nil, errors.New("filters are invalid")
		}
	}
	filters := make([]resource.FilterCondition, len(filterRequests))
	for index, item := range filterRequests {
		path, ok := libraryItemHTTPField(item.Field)
		if !ok {
			return nil, nil, errors.New("filter field is invalid")
		}
		filters[index] = resource.FilterCondition{Field: path, Operator: item.Operator, Value: item.Value}
	}
	var sortRequests []semanticSortRequest
	if raw := request.URL.Query().Get("sort"); raw != "" {
		if err := decodeQueryJSON(raw, &sortRequests); err != nil {
			return nil, nil, errors.New("sort is invalid")
		}
	}
	sorts := make([]resource.Sort, len(sortRequests))
	for index, item := range sortRequests {
		path, ok := libraryItemHTTPField(item.Field)
		if !ok {
			return nil, nil, errors.New("sort field is invalid")
		}
		sorts[index] = resource.Sort{Field: path, Direction: item.Direction}
	}
	return filters, sorts, nil
}

func decodeQueryJSON(raw string, target any) error {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("query JSON contains trailing data")
	}
	return nil
}

func libraryItemHTTPField(value string) (resource.FieldPath, bool) {
	fields := map[string]resource.FieldPath{
		"id": resource.FieldID, "title": resource.FieldTitle, "slug": resource.FieldSlug,
		"template": resource.FieldTemplate, "is_public": resource.FieldIsPublic,
		"is_searchable": resource.FieldIsSearchable, "published_at": resource.FieldPublishedAt,
		"created_at": resource.FieldCreatedAt, "updated_at": resource.FieldUpdatedAt,
	}
	if path, exists := fields[value]; exists {
		return path, true
	}
	path := resource.FieldPath(value)
	return path, resource.IsCustomFieldPath(path) && resource.ValidFieldPath(path)
}

func (h *contentHTTP) createLibraryItem(response http.ResponseWriter, request *http.Request) {
	siteID, libraryID, ok := resourceWidgetResourceParams(response, request)
	if !ok {
		return
	}
	var payload libraryItemRequest
	if !decodeBody(response, request, &payload) {
		return
	}
	if payload.Fields == nil {
		writeValidation(response, "fields are required")
		return
	}
	result, err := h.resources.CreateLibraryItem(request.Context(), actor(request), siteID, libraryID, LibraryItemCreateInput{ImageMediaID: payload.ImageMediaID, Template: payload.Template, Title: payload.Title, Slug: payload.Slug, Annotation: payload.Annotation, Content: payload.Content, IsPublic: payload.IsPublic, IsSearchable: payload.IsSearchable, PublishedAt: payload.PublishedAt, UnpublishedAt: payload.UnpublishedAt, Fields: payload.Fields})
	writeResult(response, http.StatusCreated, result, err)
}

func (h *contentHTTP) getLibraryItem(response http.ResponseWriter, request *http.Request) {
	siteID, itemID, ok := libraryItemParams(response, request)
	if !ok {
		return
	}
	result, err := h.resources.LibraryItem(request.Context(), actor(request), siteID, itemID)
	writeResult(response, http.StatusOK, result, err)
}

func (h *contentHTTP) updateLibraryItem(response http.ResponseWriter, request *http.Request) {
	siteID, itemID, ok := libraryItemParams(response, request)
	if !ok {
		return
	}
	var payload libraryItemRequest
	if !decodeBody(response, request, &payload) {
		return
	}
	if payload.Fields == nil || payload.IsPublic == nil || payload.IsSearchable == nil {
		writeValidation(response, "fields and publication flags are required")
		return
	}
	if payload.ExpectedVersion <= 0 {
		writeValidation(response, "expected_version is required")
		return
	}
	result, err := h.resources.UpdateLibraryItem(request.Context(), actor(request), siteID, itemID, LibraryItemUpdateInput{ExpectedVersion: payload.ExpectedVersion, LibraryItemCreateInput: LibraryItemCreateInput{ImageMediaID: payload.ImageMediaID, Template: payload.Template, Title: payload.Title, Slug: payload.Slug, Annotation: payload.Annotation, Content: payload.Content, PublishedAt: payload.PublishedAt, UnpublishedAt: payload.UnpublishedAt, Fields: payload.Fields}, IsPublic: payload.IsPublic, IsSearchable: payload.IsSearchable})
	writeResult(response, http.StatusOK, result, err)
}

func (h *contentHTTP) moveLibraryItem(response http.ResponseWriter, request *http.Request) {
	siteID, itemID, ok := libraryItemParams(response, request)
	if !ok {
		return
	}
	var payload struct {
		LibraryID       resource.ID `json:"library_id"`
		ExpectedVersion int64       `json:"expected_version"`
	}
	if !decodeBody(response, request, &payload) {
		return
	}
	if payload.ExpectedVersion <= 0 {
		writeValidation(response, "expected_version is required")
		return
	}
	result, err := h.resources.MoveLibraryItem(request.Context(), actor(request), siteID, itemID, payload.LibraryID, payload.ExpectedVersion)
	writeResult(response, http.StatusOK, result, err)
}

func (h *contentHTTP) deleteLibraryItem(response http.ResponseWriter, request *http.Request) {
	h.changeLibraryItemDeleted(response, request, false, false)
}
func (h *contentHTTP) deleteLibraryItemPermanent(response http.ResponseWriter, request *http.Request) {
	h.changeLibraryItemDeleted(response, request, false, true)
}
func (h *contentHTTP) restoreLibraryItem(response http.ResponseWriter, request *http.Request) {
	h.changeLibraryItemDeleted(response, request, true, false)
}
func (h *contentHTTP) changeLibraryItemDeleted(response http.ResponseWriter, request *http.Request, restore, permanent bool) {
	siteID, itemID, ok := libraryItemParams(response, request)
	if !ok {
		return
	}
	var err error
	if restore {
		err = h.resources.RestoreLibraryItem(request.Context(), actor(request), siteID, itemID)
	} else {
		err = h.resources.DeleteLibraryItem(request.Context(), actor(request), siteID, itemID, permanent)
	}
	if err != nil {
		writeManagementError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func libraryItemParams(response http.ResponseWriter, request *http.Request) (site.ID, resource.ID, bool) {
	siteID, ok := siteID(response, request)
	if !ok {
		return 0, 0, false
	}
	parsed, err := strconv.ParseInt(chi.URLParam(request, "itemID"), 10, 64)
	if err != nil || parsed <= 0 {
		writeBadRequest(response, "item_id is invalid")
		return 0, 0, false
	}
	return siteID, resource.ID(parsed), true
}

func actor(request *http.Request) security.Actor {
	current, _ := httptransport.ActorFromContext(request.Context())
	return current
}

func siteID(response http.ResponseWriter, request *http.Request) (site.ID, bool) {
	parsed, err := strconv.ParseInt(chi.URLParam(request, "siteID"), 10, 64)
	if err != nil || parsed <= 0 {
		writeBadRequest(response, "site_id is invalid")
		return 0, false
	}
	return site.ID(parsed), true
}

func resourceID(response http.ResponseWriter, request *http.Request) (resource.ID, bool) {
	parsed, err := strconv.ParseInt(chi.URLParam(request, "resourceID"), 10, 64)
	if err != nil || parsed <= 0 {
		writeBadRequest(response, "resource_id is invalid")
		return 0, false
	}
	return resource.ID(parsed), true
}

func parsePagination(response http.ResponseWriter, request *http.Request) (int, int, bool) {
	page, perPage := 1, 10
	var err error
	if raw := request.URL.Query().Get("page"); raw != "" {
		page, err = strconv.Atoi(raw)
		if err != nil || page < 1 {
			writeBadRequest(response, "page is invalid")
			return 0, 0, false
		}
	}
	if raw := request.URL.Query().Get("per_page"); raw != "" {
		perPage, err = strconv.Atoi(raw)
		if err != nil || perPage < 1 || perPage > 100 {
			writeBadRequest(response, "per_page is invalid")
			return 0, 0, false
		}
	}
	return page, perPage, true
}

func decodeBody(response http.ResponseWriter, request *http.Request, target any) bool {
	request.Body = http.MaxBytesReader(response, request.Body, 1<<20)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeBadRequest(response, "request body is invalid")
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeBadRequest(response, "request body contains trailing data")
		return false
	}
	return true
}

func writeResult(response http.ResponseWriter, status int, result any, err error) {
	if err != nil {
		writeManagementError(response, err)
		return
	}
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(result)
}

func writeManagementError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, image.ErrInvalidTransform), errors.Is(err, image.ErrUnsupportedFormat), errors.Is(err, image.ErrLimit):
		writeValidation(response, err.Error())
	case errors.Is(err, media.ErrSettings):
		writeValidation(response, err.Error())
	case errors.Is(err, media.ErrSettingsConflict):
		httptransport.WriteJSONError(response, http.StatusConflict, "media_conflict", "media changed; reload settings")
	case errors.Is(err, media.ErrImageConflict):
		httptransport.WriteJSONError(response, http.StatusConflict, "image_conflict", "image changed; reload editor")
	case errors.Is(err, media.ErrNotFound):
		httptransport.WriteJSONError(response, http.StatusNotFound, "not_found", "media not found")
	case errors.Is(err, security.ErrUnauthenticated):
		writeUnauthorized(response)
	case errors.Is(err, security.ErrForbidden):
		httptransport.WriteJSONError(response, http.StatusForbidden, "forbidden", "operation is forbidden")
	case errors.Is(err, site.ErrNotFound), errors.Is(err, resource.ErrNotFound), errors.Is(err, resource.ErrRevisionNotFound), errors.Is(err, file.ErrNotFound), errors.Is(err, file.ErrFolderNotFound), errors.Is(err, file.ErrStorageNotFound):
		httptransport.WriteJSONError(response, http.StatusNotFound, "not_found", "requested object was not found")
	case errors.Is(err, site.ErrConflict), errors.Is(err, resource.ErrConflict), errors.Is(err, file.ErrConflict):
		httptransport.WriteJSONError(response, http.StatusConflict, "conflict", "object conflicts with existing data")
	case errors.Is(err, resource.ErrRouteConflict):
		httptransport.WriteJSONError(response, http.StatusConflict, "route_conflict", "route conflicts with existing content")
	case errors.Is(err, resource.ErrRouteMutationRequiresMaintenance):
		httptransport.WriteJSONError(response, http.StatusConflict, "route_mutation_requires_maintenance", "route mutation requires offline validation")
	case errors.Is(err, resource.ErrIncompatibleTargetSite):
		httptransport.WriteJSONError(response, http.StatusConflict, "incompatible_target_site", "target site is incompatible with the resource")
	case errors.Is(err, resource.ErrCrossSiteReference):
		httptransport.WriteJSONError(response, http.StatusConflict, "cross_site_reference", "resource transfer has cross-site references")
	case errors.Is(err, file.ErrInUse):
		httptransport.WriteJSONError(response, http.StatusConflict, "file_in_use", "file is used by content")
	case errors.Is(err, file.ErrStorageMismatch):
		httptransport.WriteJSONError(response, http.StatusConflict, "storage_mismatch", "items cannot be moved between disks")
	case errors.Is(err, file.ErrInvalidTree):
		httptransport.WriteJSONError(response, http.StatusConflict, "invalid_tree", "folder cannot be moved into itself")
	case errors.Is(err, resource.ErrInvalidTree):
		httptransport.WriteJSONError(response, http.StatusConflict, "invalid_tree", "resource tree operation is invalid")
	case errors.Is(err, resource.ErrReferenced):
		httptransport.WriteJSONError(response, http.StatusConflict, "resource_referenced", "resource is referenced")
	case errors.Is(err, file.ErrInvalidReference):
		writeValidation(response, "file reference is invalid")
	case errors.Is(err, ErrValidation):
		var validation ValidationError
		if errors.As(err, &validation) && len(validation.Fields) > 0 {
			writeFieldValidation(response, validation)
			return
		}
		writeValidation(response, err.Error())
	default:
		httptransport.WriteJSONError(response, http.StatusInternalServerError, "internal_error", "operation failed")
	}
}

func writeBadRequest(response http.ResponseWriter, message string) {
	httptransport.WriteJSONError(response, http.StatusBadRequest, "bad_request", message)
}

func writeUnauthorized(response http.ResponseWriter) {
	response.Header().Set("WWW-Authenticate", "Bearer")
	httptransport.WriteJSONError(
		response,
		http.StatusUnauthorized,
		"unauthenticated",
		"authentication required",
	)
}

func writeValidation(response http.ResponseWriter, message string) {
	httptransport.WriteJSONError(response, http.StatusUnprocessableEntity, "validation_failed", message)
}

func writeFieldValidation(response http.ResponseWriter, validation ValidationError) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(http.StatusUnprocessableEntity)
	_ = json.NewEncoder(response).Encode(struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Details struct {
				Fields []FieldValidationError `json:"fields"`
			} `json:"details"`
		} `json:"error"`
	}{Error: struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Details struct {
			Fields []FieldValidationError `json:"fields"`
		} `json:"details"`
	}{
		Code:    "validation_failed",
		Message: validation.Message,
		Details: struct {
			Fields []FieldValidationError `json:"fields"`
		}{Fields: validation.Fields},
	}})
}
