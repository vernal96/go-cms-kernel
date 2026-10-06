package management

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
	"github.com/vernal96/go-cms-kernel/modules/resourceextension"
)

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
