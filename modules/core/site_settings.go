package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/vernal96/go-cms-kernel/modules/core/site"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

// PublicSiteSettings projects the current immutable site snapshot. It never
// inherits an authenticated caller's privileges when resolving file references.
func PublicSiteSettings(ctx context.Context, runtime *site.Runtime) (map[string]any, error) {
	if runtime == nil {
		return nil, errors.New("site runtime is unavailable")
	}
	module, ok := runtime.Profile().Registry().Module("core")
	r, exists := module.(*Runtime)
	if !ok || !exists || r.publicFields == nil {
		return nil, errors.New("site public field projector is unavailable")
	}
	return r.publicFields.PublicSettings(ctx, runtime.Site().Settings, runtime.Profile().Profile().Params)
}

func (r *Runtime) serveSite(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if request.URL.RawQuery != "" {
		httptransport.WriteJSONError(response, http.StatusBadRequest, "invalid_query", "site endpoint accepts no query parameters")
		return
	}
	runtime, ok := SiteRuntimeFromContext(request.Context())
	if !ok {
		httptransport.WriteJSONError(response, http.StatusInternalServerError, "internal_error", "site context unavailable")
		return
	}
	settings, err := PublicSiteSettings(request.Context(), runtime)
	if err != nil {
		if r.logger != nil {
			r.logger.ErrorContext(request.Context(), "public site settings failed", "error", err)
		}
		httptransport.WriteJSONError(response, http.StatusInternalServerError, "internal_error", "site settings unavailable")
		return
	}
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	// Encoding errors are reported before writing any response body.
	raw, err := json.Marshal(struct {
		Settings map[string]any `json:"settings"`
	}{settings})
	if err != nil {
		httptransport.WriteJSONError(response, 500, "internal_error", "site settings unavailable")
		return
	}
	_, _ = response.Write(append(raw, '\n'))
}
