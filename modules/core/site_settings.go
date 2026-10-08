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
		projected, err := projectPublicSettingValue(ctx, definition, value, files, mediaService)
		if err != nil {
			return nil, fmt.Errorf("project public site parameter %q: %w", definition.Key, err)
		}
		result[definition.Key] = projected
	}
	return result, nil
}

func projectPublicSettingValue(ctx context.Context, definition field.Definition, value any, files publicSettingsFiles, mediaService publicSettingsMedia) (any, error) {
	if value == nil {
		return nil, nil
	}
	switch definition.Type {
	case field.TypeRepeater:
		options, err := repeaterFieldOptions(definition.Options)
		if err != nil {
			return nil, fmt.Errorf("decode repeater fields: %w", err)
		}
		rows, ok := value.([]any)
		if !ok {
			return nil, errors.New("repeater value is invalid")
		}
		projectedRows := make([]map[string]any, 0, len(rows))
		for _, item := range rows {
			row, ok := item.(map[string]any)
			if !ok {
				return nil, errors.New("repeater row is invalid")
			}
			projectedRow := make(map[string]any)
			for _, nested := range options.Fields {
				if !nested.Public {
					continue
				}
				nestedValue, exists := row[nested.Key]
				if !exists {
					continue
				}
				projectedValue, err := projectPublicSettingValue(ctx, nested, nestedValue, files, mediaService)
				if err != nil {
					return nil, fmt.Errorf("field %q: %w", nested.Key, err)
				}
				projectedRow[nested.Key] = projectedValue
			}
			projectedRows = append(projectedRows, projectedRow)
		}
		return projectedRows, nil
	case field.TypeFile, field.TypeMedia:
		if multipleReferenceValue(definition) {
			values, ok := value.([]any)
			if !ok {
				return nil, errors.New("multiple file reference value is invalid")
			}
			result := make([]any, len(values))
			for index, item := range values {
				projected, err := projectPublicFileReference(ctx, definition, item, files, mediaService)
				if err != nil {
					return nil, err
				}
				result[index] = projected
			}
			return result, nil
		}
		return projectPublicFileReference(ctx, definition, value, files, mediaService)
	default:
		return value, nil
	}
}

func repeaterFieldOptions(value any) (field.RepeaterOptions, error) {
	switch options := value.(type) {
	case field.RepeaterOptions:
		options.Fields = field.CloneDefinitions(options.Fields)
		return options, nil
	case *field.RepeaterOptions:
		if options == nil {
			return field.RepeaterOptions{}, errors.New("repeater options are nil")
		}
		result := *options
		result.Fields = field.CloneDefinitions(options.Fields)
		return result, nil
	default:
		return field.DecodeOptions[field.RepeaterOptions](value)
	}
}

func multipleReferenceValue(definition field.Definition) bool {
	switch definition.Type {
	case field.TypeFile:
		options, _ := field.FileOptionsValue(definition.Options)
		return options.Multiple
	case field.TypeMedia:
		options, err := field.DecodeOptions[field.MediaOptions](definition.Options)
		return err == nil && options.Multiple
	default:
		return false
	}
}

func projectPublicFileReference(ctx context.Context, definition field.Definition, value any, files publicSettingsFiles, mediaService publicSettingsMedia) (any, error) {
	id, ok := value.(int64)
	if !ok || id <= 0 {
		return nil, fmt.Errorf("invalid file reference %q", definition.Key)
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
			return nil, nil
		}
		return nil, err
	}
	return publicFileValue{ID: id, URL: url}, nil
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
