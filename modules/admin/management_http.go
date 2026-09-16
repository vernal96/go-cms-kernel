package admin

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/vernal96/go-cms-kernel/modules/core/access"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/group"
	image "github.com/vernal96/go-cms-kernel/modules/core/image"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/user"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

type managementHTTP struct {
	management *Management
}

func registerManagementRoutes(router chi.Router, management *Management) {
	handler := &managementHTTP{management: management}
	registerProfileRoutes(router, handler)
	registerIdentityRoutes(router, handler)
	router.Get("/navigation", handler.navigation)
	router.Get("/dashboard", handler.dashboard)
}

func (h *managementHTTP) navigation(response http.ResponseWriter, request *http.Request) {
	var selectedSiteID *site.ID
	if raw := request.URL.Query().Get("site_id"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed <= 0 {
			writeBadRequest(response, "site_id is invalid")
			return
		}
		value := site.ID(parsed)
		selectedSiteID = &value
	}
	result, err := h.management.Navigation(request.Context(), actor(request), selectedSiteID)
	writeResult(response, http.StatusOK, result, err)
}

func (h *managementHTTP) dashboard(response http.ResponseWriter, request *http.Request) {
	result, err := h.management.Dashboard(request.Context(), actor(request))
	writeResult(response, http.StatusOK, result, err)
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
	case errors.Is(err, image.ErrInvalidTransform), errors.Is(err, image.ErrLimit), errors.Is(err, image.ErrUnsupportedFormat):
		writeValidation(response, err.Error())
	case errors.Is(err, media.ErrImageConflict):
		httptransport.WriteJSONError(response, http.StatusConflict, "image_conflict", "image changed; reload editor")
	case errors.Is(err, media.ErrNotFound):
		httptransport.WriteJSONError(response, http.StatusNotFound, "not_found", "media not found")
	case errors.Is(err, security.ErrUnauthenticated):
		writeUnauthorized(response)
	case errors.Is(err, security.ErrForbidden):
		httptransport.WriteJSONError(response, http.StatusForbidden, "forbidden", "operation is forbidden")
	case errors.Is(err, access.ErrNotPrivileged):
		httptransport.WriteJSONError(response, http.StatusForbidden, "forbidden", "privileged access is required")
	case errors.Is(err, site.ErrNotFound), errors.Is(err, resource.ErrNotFound), errors.Is(err, resource.ErrRevisionNotFound), errors.Is(err, user.ErrNotFound), errors.Is(err, group.ErrNotFound), errors.Is(err, file.ErrNotFound), errors.Is(err, file.ErrFolderNotFound), errors.Is(err, file.ErrStorageNotFound):
		httptransport.WriteJSONError(response, http.StatusNotFound, "not_found", "requested object was not found")
	case errors.Is(err, site.ErrConflict), errors.Is(err, resource.ErrConflict), errors.Is(err, user.ErrConflict), errors.Is(err, group.ErrConflict), errors.Is(err, file.ErrConflict):
		httptransport.WriteJSONError(response, http.StatusConflict, "conflict", "object conflicts with existing data")
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
	case errors.Is(err, group.ErrProtected):
		httptransport.WriteJSONError(response, http.StatusConflict, "protected_group", "admin group is protected")
	case errors.Is(err, user.ErrSelfBlock):
		httptransport.WriteJSONError(response, http.StatusConflict, "self_block_forbidden", "current user cannot be blocked")
	case errors.Is(err, user.ErrLastAdministrator), errors.Is(err, group.ErrLastAdministrator):
		httptransport.WriteJSONError(response, http.StatusConflict, "last_administrator", "at least one active administrator is required")
	case errors.Is(err, group.ErrInvalidReference), errors.Is(err, user.ErrInvalidReference), errors.Is(err, permission.ErrUnknown):
		writeValidation(response, "request data is invalid")
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
