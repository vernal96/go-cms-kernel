package admin

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/vernal96/go-cms-kernel/modules/core/group"
	"github.com/vernal96/go-cms-kernel/modules/core/user"
	"github.com/vernal96/go-cms-kernel/permission"
)

func registerIdentityRoutes(router chi.Router, handler *managementHTTP) {
	router.Get("/users", handler.listUsers)
	router.Post("/users", handler.createUser)
	router.Get("/users/{userID}", handler.getUser)
	router.Patch("/users/{userID}", handler.updateUser)
	router.Put("/users/{userID}/password", handler.changeUserPassword)
	router.Get("/users/{userID}/groups", handler.userGroups)
	router.Put("/users/{userID}/groups", handler.replaceUserGroups)
	router.Post("/users/{userID}/block", handler.blockUser)
	router.Post("/users/{userID}/unblock", handler.unblockUser)
	router.Get("/groups/options", handler.listGroupOptions)
	router.Get("/groups", handler.listGroups)
	router.Post("/groups", handler.createGroup)
	router.Get("/groups/{groupID}", handler.getGroup)
	router.Patch("/groups/{groupID}", handler.updateGroup)
	router.Delete("/groups/{groupID}", handler.deleteGroup)
	router.Get("/permission-catalog", handler.permissionCatalog)
}

func (h *managementHTTP) listUsers(response http.ResponseWriter, request *http.Request) {
	page, perPage, ok := parsePagination(response, request)
	if !ok {
		return
	}
	result, err := h.management.ListUsers(request.Context(), actor(request), request.URL.Query().Get("search"), user.ListStatus(request.URL.Query().Get("status")), page, perPage)
	writeResult(response, http.StatusOK, result, err)
}

type userRequest struct {
	Login      string  `json:"login"`
	Email      string  `json:"email"`
	Name       string  `json:"name"`
	LastName   *string `json:"last_name"`
	MiddleName *string `json:"middle_name"`
	Phone      *string `json:"phone"`
}

type createUserRequest struct {
	userRequest
	Password string     `json:"password"`
	GroupIDs []group.ID `json:"group_ids"`
}

func (h *managementHTTP) createUser(response http.ResponseWriter, request *http.Request) {
	var payload createUserRequest
	if !decodeBody(response, request, &payload) {
		return
	}
	result, err := h.management.CreateUser(request.Context(), actor(request), UserCreateInput{Login: payload.Login, Email: payload.Email, Password: payload.Password, Name: payload.Name, LastName: payload.LastName, MiddleName: payload.MiddleName, Phone: payload.Phone, GroupIDs: payload.GroupIDs})
	writeResult(response, http.StatusCreated, result, err)
}

func (h *managementHTTP) getUser(response http.ResponseWriter, request *http.Request) {
	id, ok := adminUserID(response, request)
	if !ok {
		return
	}
	result, err := h.management.User(request.Context(), actor(request), id)
	writeResult(response, http.StatusOK, result, err)
}

func (h *managementHTTP) updateUser(response http.ResponseWriter, request *http.Request) {
	id, ok := adminUserID(response, request)
	if !ok {
		return
	}
	var payload userRequest
	if !decodeBody(response, request, &payload) {
		return
	}
	result, err := h.management.UpdateUser(request.Context(), actor(request), id, UserUpdateInput{Login: payload.Login, Email: payload.Email, Name: payload.Name, LastName: payload.LastName, MiddleName: payload.MiddleName, Phone: payload.Phone})
	writeResult(response, http.StatusOK, result, err)
}

func (h *managementHTTP) changeUserPassword(response http.ResponseWriter, request *http.Request) {
	id, ok := adminUserID(response, request)
	if !ok {
		return
	}
	var payload struct {
		Password string `json:"password"`
	}
	if !decodeBody(response, request, &payload) {
		return
	}
	if err := h.management.ChangeUserPassword(request.Context(), actor(request), id, payload.Password); err != nil {
		writeManagementError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (h *managementHTTP) userGroups(response http.ResponseWriter, request *http.Request) {
	id, ok := adminUserID(response, request)
	if !ok {
		return
	}
	result, err := h.management.UserGroups(request.Context(), actor(request), id)
	writeResult(response, http.StatusOK, result, err)
}

func (h *managementHTTP) replaceUserGroups(response http.ResponseWriter, request *http.Request) {
	id, ok := adminUserID(response, request)
	if !ok {
		return
	}
	var payload struct {
		GroupIDs []group.ID `json:"group_ids"`
	}
	if !decodeBody(response, request, &payload) {
		return
	}
	if err := h.management.ReplaceUserGroups(request.Context(), actor(request), id, payload.GroupIDs); err != nil {
		writeManagementError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (h *managementHTTP) blockUser(response http.ResponseWriter, request *http.Request) {
	id, ok := adminUserID(response, request)
	if !ok {
		return
	}
	if err := h.management.BlockUser(request.Context(), actor(request), id); err != nil {
		writeManagementError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (h *managementHTTP) unblockUser(response http.ResponseWriter, request *http.Request) {
	id, ok := adminUserID(response, request)
	if !ok {
		return
	}
	if err := h.management.UnblockUser(request.Context(), actor(request), id); err != nil {
		writeManagementError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (h *managementHTTP) listGroups(response http.ResponseWriter, request *http.Request) {
	page, perPage, ok := parsePagination(response, request)
	if !ok {
		return
	}
	result, err := h.management.ListGroups(request.Context(), actor(request), request.URL.Query().Get("search"), page, perPage)
	writeResult(response, http.StatusOK, result, err)
}

func (h *managementHTTP) listGroupOptions(response http.ResponseWriter, request *http.Request) {
	page, perPage, ok := parsePagination(response, request)
	if !ok {
		return
	}
	result, err := h.management.ListGroupOptions(request.Context(), actor(request), request.URL.Query().Get("search"), page, perPage)
	writeResult(response, http.StatusOK, result, err)
}

type groupRequest struct {
	Name            string             `json:"name"`
	PermissionCodes *[]permission.Code `json:"permission_codes"`
	SiteAccess      *[]GroupSiteAccess `json:"site_access"`
}

func (h *managementHTTP) createGroup(response http.ResponseWriter, request *http.Request) {
	var payload struct {
		Code            string            `json:"code"`
		Name            string            `json:"name"`
		PermissionCodes []permission.Code `json:"permission_codes"`
		SiteAccess      []GroupSiteAccess `json:"site_access"`
	}
	if !decodeBody(response, request, &payload) {
		return
	}
	result, err := h.management.CreateGroup(request.Context(), actor(request), GroupCreateInput{Code: payload.Code, Name: payload.Name, PermissionCodes: payload.PermissionCodes, SiteAccess: payload.SiteAccess})
	writeResult(response, http.StatusCreated, result, err)
}

func (h *managementHTTP) getGroup(response http.ResponseWriter, request *http.Request) {
	id, ok := adminGroupID(response, request)
	if !ok {
		return
	}
	result, err := h.management.Group(request.Context(), actor(request), id)
	writeResult(response, http.StatusOK, result, err)
}

func (h *managementHTTP) updateGroup(response http.ResponseWriter, request *http.Request) {
	id, ok := adminGroupID(response, request)
	if !ok {
		return
	}
	var payload groupRequest
	if !decodeBody(response, request, &payload) {
		return
	}
	result, err := h.management.UpdateGroup(request.Context(), actor(request), id, GroupUpdateInput{Name: payload.Name, PermissionCodes: payload.PermissionCodes, SiteAccess: payload.SiteAccess})
	writeResult(response, http.StatusOK, result, err)
}

func (h *managementHTTP) deleteGroup(response http.ResponseWriter, request *http.Request) {
	id, ok := adminGroupID(response, request)
	if !ok {
		return
	}
	if err := h.management.DeleteGroup(request.Context(), actor(request), id); err != nil {
		writeManagementError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (h *managementHTTP) permissionCatalog(response http.ResponseWriter, request *http.Request) {
	result, err := h.management.PermissionCatalog(request.Context(), actor(request))
	writeResult(response, http.StatusOK, result, err)
}

func adminUserID(response http.ResponseWriter, request *http.Request) (user.ID, bool) {
	parsed, err := strconv.ParseInt(chi.URLParam(request, "userID"), 10, 64)
	if err != nil || parsed <= 0 {
		writeBadRequest(response, "user_id is invalid")
		return 0, false
	}
	return user.ID(parsed), true
}

func adminGroupID(response http.ResponseWriter, request *http.Request) (group.ID, bool) {
	parsed, err := strconv.ParseInt(chi.URLParam(request, "groupID"), 10, 64)
	if err != nil || parsed <= 0 {
		writeBadRequest(response, "group_id is invalid")
		return 0, false
	}
	return group.ID(parsed), true
}
