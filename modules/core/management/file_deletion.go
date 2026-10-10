package management

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
)

func (h *contentHTTP) fileDeletions(r *http.Request, sid site.ID) (media.FileDeletionService, error) {
	if err := h.sites.requireSite(r.Context(), actor(r), sid, SiteReadPermission, SiteAccessEdit); err != nil {
		return nil, err
	}
	runtime, ok := h.sites.sites.RuntimeByID(sid)
	if !ok {
		return nil, site.ErrNotFound
	}
	module, ok := runtime.Profile().Registry().Module("core")
	if !ok {
		return nil, media.ErrFileDeleteUnsupported
	}
	provider, ok := module.(interface {
		MediaFileDeletions() media.FileDeletionService
	})
	if !ok || provider.MediaFileDeletions() == nil {
		return nil, media.ErrFileDeleteUnsupported
	}
	return provider.MediaFileDeletions(), nil
}

func (h *contentHTTP) deleteMediaFile(w http.ResponseWriter, r *http.Request) {
	sid, ok := siteID(w, r)
	if !ok {
		return
	}
	id, ok := imageID(w, r)
	if !ok {
		return
	}
	var body struct {
		ExpectedFileID    file.ID   `json:"expected_file_id"`
		ExpectedUpdatedAt time.Time `json:"expected_updated_at"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if body.ExpectedFileID <= 0 || body.ExpectedUpdatedAt.IsZero() {
		writeValidation(w, "expected_file_id and expected_updated_at are required")
		return
	}
	service, err := h.fileDeletions(r, sid)
	if err != nil {
		writeManagementError(w, err)
		return
	}
	result, err := service.Delete(r.Context(), actor(r), media.DeleteFileInput{SiteID: int64(sid), MediaID: id, ExpectedFileID: body.ExpectedFileID, ExpectedUpdatedAt: body.ExpectedUpdatedAt})
	status := http.StatusOK
	if result.Status == "pending" {
		status = http.StatusAccepted
	}
	writeResult(w, status, result, err)
}

func (h *contentHTTP) mediaFileDeletionStatus(w http.ResponseWriter, r *http.Request) {
	sid, ok := siteID(w, r)
	if !ok {
		return
	}
	service, err := h.fileDeletions(r, sid)
	if err != nil {
		writeManagementError(w, err)
		return
	}
	result, err := service.Status(r.Context(), actor(r), int64(sid), chi.URLParam(r, "operationID"))
	writeResult(w, http.StatusOK, result, err)
}
