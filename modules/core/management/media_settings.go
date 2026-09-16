package management

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/security"
)

func (m *Sites) mediaSettings(ctx context.Context, actor security.Actor, id site.ID, action SiteAccessAction) (*media.SettingsService, error) {
	if err := m.requireSite(ctx, actor, id, SiteReadPermission, action); err != nil {
		return nil, err
	}
	runtime, exists := m.sites.RuntimeByID(id)
	if !exists {
		return nil, site.ErrNotFound
	}
	core, exists := runtime.Profile().Registry().Module(kernel.ModuleCode("core"))
	if exists {
		if provider, ok := core.(interface{ MediaSettings() *media.SettingsService }); ok && provider.MediaSettings() != nil {
			return provider.MediaSettings(), nil
		}
	}
	return nil, fmt.Errorf("%w: no settings configured", media.ErrSettings)
}
func (h *contentHTTP) getMediaSettings(w http.ResponseWriter, r *http.Request) {
	sid, ok := siteID(w, r)
	if !ok {
		return
	}
	id, ok := imageID(w, r)
	if !ok {
		return
	}
	service, err := h.sites.mediaSettings(r.Context(), actor(r), sid, SiteAccessView)
	if err != nil {
		writeManagementError(w, err)
		return
	}
	result, err := service.Get(r.Context(), actor(r), id, r.URL.Query().Get("code"))
	writeResult(w, http.StatusOK, result, validationError(err))
}
func (h *contentHTTP) saveMediaSettings(w http.ResponseWriter, r *http.Request) {
	sid, ok := siteID(w, r)
	if !ok {
		return
	}
	id, ok := imageID(w, r)
	if !ok {
		return
	}
	service, err := h.sites.mediaSettings(r.Context(), actor(r), sid, SiteAccessEdit)
	if err != nil {
		writeManagementError(w, err)
		return
	}
	var body struct {
		Code              string         `json:"code"`
		Values            map[string]any `json:"values"`
		ExpectedUpdatedAt time.Time      `json:"expected_updated_at"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := service.Save(r.Context(), actor(r), id, body.Code, body.Values, body.ExpectedUpdatedAt)
	writeResult(w, http.StatusOK, result, validationError(err))
}
