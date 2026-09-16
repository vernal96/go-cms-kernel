package admin

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/user"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

func registerProfileRoutes(router chi.Router, handler *managementHTTP) {
	router.Get("/profile", handler.getProfile)
	router.Patch("/profile", handler.updateProfile)
	router.Put("/profile/password", handler.changeProfilePassword)
	router.Put("/profile/preferences", handler.updateProfilePreferences)
	router.Put("/profile/avatar", handler.selectProfileAvatar)
	router.Post("/profile/avatar/upload", handler.uploadProfileAvatar)
	router.Delete("/profile/avatar", handler.removeProfileAvatar)
	router.Get("/profile/avatar/preview", handler.previewProfileAvatar)
	if handler.management.images != nil {
		registerProfileImageRoutes(router, handler)
	}
}

func (h *managementHTTP) getProfile(response http.ResponseWriter, request *http.Request) {
	result, err := h.management.Profile(request.Context(), actor(request))
	writeResult(response, http.StatusOK, result, err)
}

func (h *managementHTTP) updateProfile(response http.ResponseWriter, request *http.Request) {
	var payload struct {
		Name       string  `json:"name"`
		LastName   *string `json:"last_name"`
		MiddleName *string `json:"middle_name"`
		Phone      *string `json:"phone"`
	}
	if !decodeBody(response, request, &payload) {
		return
	}
	result, err := h.management.UpdateProfile(request.Context(), actor(request), user.UpdateCurrentInput{
		Name: payload.Name, LastName: payload.LastName,
		MiddleName: payload.MiddleName, Phone: payload.Phone,
	})
	writeResult(response, http.StatusOK, result, err)
}

func (h *managementHTTP) changeProfilePassword(response http.ResponseWriter, request *http.Request) {
	var payload struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if !decodeBody(response, request, &payload) {
		return
	}
	if err := h.management.ChangeProfilePassword(
		request.Context(), actor(request), payload.CurrentPassword, payload.NewPassword,
	); err != nil {
		writeManagementError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (h *managementHTTP) updateProfilePreferences(response http.ResponseWriter, request *http.Request) {
	var payload struct {
		ColorScheme user.ColorScheme `json:"color_scheme"`
		AccentColor user.AccentColor `json:"accent_color"`
	}
	if !decodeBody(response, request, &payload) {
		return
	}
	result, err := h.management.UpdateProfilePreferences(
		request.Context(),
		actor(request),
		user.Preferences{
			ColorScheme: payload.ColorScheme,
			AccentColor: payload.AccentColor,
		},
	)
	writeResult(response, http.StatusOK, result, err)
}

func (h *managementHTTP) selectProfileAvatar(response http.ResponseWriter, request *http.Request) {
	var payload struct {
		FileID file.ID `json:"file_id"`
	}
	if !decodeBody(response, request, &payload) {
		return
	}
	if payload.FileID <= 0 {
		writeValidation(response, "file_id is invalid")
		return
	}
	result, err := h.management.SelectProfileAvatar(request.Context(), actor(request), payload.FileID)
	writeResult(response, http.StatusOK, result, err)
}

func (h *managementHTTP) uploadProfileAvatar(response http.ResponseWriter, request *http.Request) {
	request.Body = http.MaxBytesReader(response, request.Body, h.management.avatarMaxSize+(1<<20))
	if err := request.ParseMultipartForm(1 << 20); err != nil {
		var maxBytes *http.MaxBytesError
		if errors.As(err, &maxBytes) {
			httptransport.WriteJSONError(response, http.StatusRequestEntityTooLarge, "avatar_too_large", "avatar is too large")
			return
		}
		writeBadRequest(response, "multipart request is invalid")
		return
	}
	if request.MultipartForm != nil {
		defer request.MultipartForm.RemoveAll()
	}
	uploaded, header, err := request.FormFile("file")
	if err != nil {
		writeBadRequest(response, "file is required")
		return
	}
	defer uploaded.Close()
	if header.Size > h.management.avatarMaxSize {
		httptransport.WriteJSONError(response, http.StatusRequestEntityTooLarge, "avatar_too_large", "avatar is too large")
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), h.management.uploadTimeout)
	defer cancel()
	result, err := h.management.UploadProfileAvatar(
		ctx, actor(request), header.Filename, io.LimitReader(uploaded, h.management.avatarMaxSize+1),
	)
	writeResult(response, http.StatusCreated, result, err)
}

func (h *managementHTTP) removeProfileAvatar(response http.ResponseWriter, request *http.Request) {
	result, err := h.management.RemoveProfileAvatar(request.Context(), actor(request))
	writeResult(response, http.StatusOK, result, err)
}

func (h *managementHTTP) previewProfileAvatar(response http.ResponseWriter, request *http.Request) {
	opened, err := h.management.OpenProfileAvatar(request.Context(), actor(request))
	if err != nil {
		writeManagementError(response, err)
		return
	}
	defer opened.Body.Close()
	response.Header().Set("Content-Type", opened.File.MIMEType)
	response.Header().Set("Content-Length", strconv.FormatInt(opened.File.Size, 10))
	response.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": opened.File.Name}))
	response.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.Copy(response, opened.Body)
}
