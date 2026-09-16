package management

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	image "github.com/vernal96/go-cms-kernel/modules/core/image"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
)

type ImageStateDTO struct {
	MediaID           media.ID                `json:"media_id"`
	CurrentFile       FilesystemItemDTO       `json:"current_file"`
	OriginalFile      FilesystemItemDTO       `json:"original_file"`
	ExpectedUpdatedAt time.Time               `json:"expected_updated_at"`
	Transform         *image.TransformOptions `json:"transform"`
	CanRestore        bool                    `json:"can_restore"`
	Editable          bool                    `json:"editable"`
	Limits            image.Limits            `json:"limits"`
}

func ImageDTO(state media.ImageState, limits image.Limits) ImageStateDTO {
	return ImageStateDTO{state.Media.ID, fileItemDTO(state.Current), fileItemDTO(state.Original), state.Media.UpdatedAt, state.Transform, state.CanRestore, state.Editable, limits}
}
func registerImageRoutes(router chi.Router, h *filesHTTP) {
	router.Get("/files/{fileID}/thumbnail", h.thumbnail)
	router.Get("/media/{mediaID}/image", h.imageState)
	router.Post("/media/{mediaID}/image", h.editImage)
	router.Post("/media/{mediaID}/image/restore", h.restoreImage)
	router.Post("/media", h.createMedia)
}
func (h *filesHTTP) thumbnail(w http.ResponseWriter, r *http.Request) {
	id, ok := filesystemFileID(w, r)
	if !ok {
		return
	}
	spec := image.ThumbnailSpec{Fit: r.URL.Query().Get("fit"), Position: r.URL.Query().Get("position")}
	for key, dst := range map[string]*int{"width": &spec.Width, "height": &spec.Height} {
		if raw := r.URL.Query().Get(key); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil {
				writeBadRequest(w, "invalid thumbnail dimension")
				return
			}
			*dst = n
		}
	}
	for key, values := range r.URL.Query() {
		if len(values) != 1 || (key != "width" && key != "height" && key != "fit" && key != "position" && key != "profile") {
			writeBadRequest(w, "invalid thumbnail query")
			return
		}
	}
	service, err := h.files.thumbnailService(r.URL.Query().Get("profile"))
	if err != nil {
		writeManagementError(w, err)
		return
	}
	result, err := service.Get(r.Context(), actor(r), id, spec)
	if err != nil {
		writeManagementError(w, err)
		return
	}
	etag := fmt.Sprintf(`"%x"`, sha256.Sum256(result.Bytes))
	w.Header().Set("Content-Type", result.MIMEType)
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("Vary", "Authorization")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Write(result.Bytes)
}
func imageID(w http.ResponseWriter, r *http.Request) (media.ID, bool) {
	n, err := strconv.ParseInt(chi.URLParam(r, "mediaID"), 10, 64)
	if err != nil || n < 1 {
		writeBadRequest(w, "invalid media id")
		return 0, false
	}
	return media.ID(n), true
}
func (h *filesHTTP) imageState(w http.ResponseWriter, r *http.Request) {
	id, ok := imageID(w, r)
	if !ok {
		return
	}
	result, err := h.files.images.State(r.Context(), actor(r), id)
	writeResult(w, http.StatusOK, ImageDTO(result, h.files.images.Limits()), err)
}

type imageEditRequest struct {
	ExpectedUpdatedAt time.Time              `json:"expected_updated_at"`
	Transform         image.TransformOptions `json:"transform"`
}

func (h *filesHTTP) editImage(w http.ResponseWriter, r *http.Request) {
	id, ok := imageID(w, r)
	if !ok {
		return
	}
	var body imageEditRequest
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := h.files.images.Edit(r.Context(), actor(r), id, body.ExpectedUpdatedAt, body.Transform)
	writeResult(w, http.StatusOK, ImageDTO(result, h.files.images.Limits()), err)
}
func (h *filesHTTP) restoreImage(w http.ResponseWriter, r *http.Request) {
	id, ok := imageID(w, r)
	if !ok {
		return
	}
	var body struct {
		ExpectedUpdatedAt time.Time `json:"expected_updated_at"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := h.files.images.Restore(r.Context(), actor(r), id, body.ExpectedUpdatedAt)
	writeResult(w, http.StatusOK, ImageDTO(result, h.files.images.Limits()), err)
}
func (h *filesHTTP) createMedia(w http.ResponseWriter, r *http.Request) {
	var body struct {
		FileID int64 `json:"file_id"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := h.files.images.Create(r.Context(), actor(r), body.FileID)
	writeResult(w, http.StatusCreated, struct {
		ID media.ID `json:"id"`
	}{result.ID}, err)
}
