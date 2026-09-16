package admin

import (
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	image "github.com/vernal96/go-cms-kernel/modules/core/image"
	"github.com/vernal96/go-cms-kernel/modules/core/management"
)

func registerProfileImageRoutes(r chi.Router, h *managementHTTP) {
	r.Get("/profile/avatar/image", h.profileImageState)
	r.Post("/profile/avatar/image", h.profileImageEdit)
	r.Post("/profile/avatar/image/restore", h.profileImageRestore)
	r.Get("/profile/avatar/image/source", h.profileImageSource)
	r.Get("/profile/avatar/thumbnail", h.profileThumbnail)
}
func (h *managementHTTP) profileImageState(w http.ResponseWriter, r *http.Request) {
	state, err := h.management.ProfileImageState(r.Context(), actor(r))
	writeResult(w, http.StatusOK, management.ImageDTO(state, h.management.images.Limits()), err)
}
func (h *managementHTTP) profileImageEdit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpectedUpdatedAt time.Time              `json:"expected_updated_at"`
		Transform         image.TransformOptions `json:"transform"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	state, err := h.management.EditProfileImage(r.Context(), actor(r), body.ExpectedUpdatedAt, body.Transform)
	writeResult(w, http.StatusOK, management.ImageDTO(state, h.management.images.Limits()), err)
}
func (h *managementHTTP) profileImageRestore(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpectedUpdatedAt time.Time `json:"expected_updated_at"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	state, err := h.management.RestoreProfileImage(r.Context(), actor(r), body.ExpectedUpdatedAt)
	writeResult(w, http.StatusOK, management.ImageDTO(state, h.management.images.Limits()), err)
}
func (h *managementHTTP) profileImageSource(w http.ResponseWriter, r *http.Request) {
	opened, err := h.management.OpenProfileImageSource(r.Context(), actor(r))
	if err != nil {
		writeManagementError(w, err)
		return
	}
	defer opened.Body.Close()
	w.Header().Set("Content-Type", opened.File.MIMEType)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	io.Copy(w, opened.Body)
}
func (h *managementHTTP) profileThumbnail(w http.ResponseWriter, r *http.Request) {
	result, err := h.management.ProfileThumbnail(r.Context(), actor(r))
	if err != nil {
		writeManagementError(w, err)
		return
	}
	w.Header().Set("Content-Type", result.MIMEType)
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(result.Bytes)
}
