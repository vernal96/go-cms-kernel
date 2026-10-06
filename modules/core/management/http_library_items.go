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
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
)

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
