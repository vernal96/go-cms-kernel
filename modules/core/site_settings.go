package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/security"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

type publicSettingsFiles interface {
	URL(context.Context, security.Actor, file.ID) (string, error)
}
type publicSettingsMedia interface {
	Get(context.Context, security.Actor, media.ID) (media.Media, error)
}

type publicFileValue struct {
	ID  int64  `json:"id"`
	URL string `json:"url"`
}

// PublicSiteSettings projects the current immutable site snapshot. It never
// inherits an authenticated caller's privileges when resolving file references.
func PublicSiteSettings(ctx context.Context, runtime *site.Runtime) (map[string]any, error) {
	if runtime == nil {
		return nil, errors.New("site runtime is unavailable")
	}
	var files publicSettingsFiles
	var mediaService publicSettingsMedia
	module, ok := runtime.Profile().Registry().Module("core")
	if r, exists := module.(*Runtime); ok && exists && r.services != nil {
		files, mediaService = r.services.Files, r.services.Media
	}
	return projectPublicSettings(ctx, runtime.Site().Settings, runtime.Profile().Profile().Params, files, mediaService)
}

func projectPublicSettings(ctx context.Context, values map[string]any, definitions []field.Definition, files publicSettingsFiles, mediaService publicSettingsMedia) (map[string]any, error) {
	result := make(map[string]any)
	for _, definition := range definitions {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !definition.Public {
			continue
		}
		value, exists := values[definition.Key]
		if !exists {
			continue
		}
		if value == nil || (definition.Type != field.TypeFile && definition.Type != field.TypeMedia) {
			result[definition.Key] = value
			continue
		}
		// Site settings have already been normalized by the compiled schema.
		id, ok := value.(int64)
		if !ok || id <= 0 {
			return nil, fmt.Errorf("invalid site reference %q", definition.Key)
		}
		fileID := file.ID(id)
		var err error
		if definition.Type == field.TypeMedia {
			if mediaService == nil {
				return nil, errors.New("site media service unavailable")
			}
			var item media.Media
			item, err = mediaService.Get(ctx, security.Guest(), media.ID(id))
			fileID = item.FileID
		}
		var url string
		if err == nil {
			if files == nil {
				return nil, errors.New("site file service unavailable")
			}
			url, err = files.URL(ctx, security.Guest(), fileID)
		}
		if err != nil {
			if errors.Is(err, file.ErrNotFound) || errors.Is(err, media.ErrNotFound) ||
				errors.Is(err, security.ErrForbidden) || errors.Is(err, security.ErrUnauthenticated) ||
				errors.Is(err, file.ErrUnauthorized) || errors.Is(err, filesystem.ErrInvalidVisibility) {
				result[definition.Key] = nil
				continue
			}
			return nil, fmt.Errorf("resolve public site parameter %q: %w", definition.Key, err)
		}
		result[definition.Key] = publicFileValue{ID: id, URL: url}
	}
	return result, nil
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
