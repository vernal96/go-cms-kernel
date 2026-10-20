package publicfields

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"

	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
	"github.com/vernal96/go-cms-kernel/security"
)

type FileURLService interface {
	PublicURL(context.Context, security.Actor, file.ID) (string, error)
}

type MediaResolver interface {
	Resolve(context.Context, security.Actor, media.ID) (media.ResolvedMedia, error)
}

type MediaValue struct {
	ID       int64          `json:"id"`
	URL      string         `json:"url"`
	MIMEType string         `json:"mime_type"`
	Size     int64          `json:"size"`
	Settings map[string]any `json:"settings"`
}

type Projector struct {
	templates map[template.Code][]field.Definition
	settings  map[string][]field.Definition
	files     FileURLService
	medias    MediaResolver
}

type mediaKey struct {
	id   media.ID
	code string
}

func New(templates []template.Definition, settings []media.SettingsDefinition, fileService FileURLService, mediaService MediaResolver) (*Projector, error) {
	settingsFields, err := media.SettingsFields(settings)
	if err != nil {
		return nil, fmt.Errorf("load media settings definitions: %w", err)
	}
	byTemplate := make(map[template.Code][]field.Definition, len(templates))
	for _, definition := range templates {
		byTemplate[definition.Code] = field.CloneDefinitions(definition.Fields)
	}
	return &Projector{templates: byTemplate, settings: settingsFields, files: fileService, medias: mediaService}, nil
}

// TemplateFields projects all fields declared by a resource's template.
func (p *Projector) TemplateFields(ctx context.Context, code *template.Code, values map[string]any) (map[string]any, error) {
	if code == nil {
		return cloneMap(values), nil
	}
	definitions, exists := p.templates[*code]
	if !exists {
		return nil, fmt.Errorf("public field template %q is unavailable", *code)
	}
	return p.project(ctx, values, definitions, nil, false)
}

// SelectedTemplateFields projects only the selected top-level template fields.
func (p *Projector) SelectedTemplateFields(ctx context.Context, code *template.Code, values map[string]any, keys []string) (map[string]any, error) {
	if code == nil {
		return nil, errors.New("public field template is unavailable")
	}
	definitions, exists := p.templates[*code]
	if !exists {
		return nil, fmt.Errorf("public field template %q is unavailable", *code)
	}
	selected := make(map[string]bool, len(keys))
	for _, key := range keys {
		selected[key] = true
	}
	return p.project(ctx, values, definitions, selected, false)
}

// PublicSettings projects only public site fields and keeps repeater privacy rules.
func (p *Projector) PublicSettings(ctx context.Context, values map[string]any, definitions []field.Definition) (map[string]any, error) {
	return p.project(ctx, values, definitions, nil, true)
}

// WidgetOutput projects values whose keys are declared by a public widget's
// fields while preserving computed output keys that have no declaration.
func (p *Projector) WidgetOutput(ctx context.Context, values map[string]any, definitions []field.Definition) (map[string]any, error) {
	result := cloneMap(values)
	resolved := make(map[mediaKey]any)
	for _, definition := range definitions {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		value, exists := values[definition.Key]
		if !exists {
			continue
		}
		projected, err := p.projectValue(ctx, definition, value, false, resolved)
		if err != nil {
			return nil, fmt.Errorf("widget field %q: %w", definition.Key, err)
		}
		result[definition.Key] = projected
	}
	return result, nil
}

func (p *Projector) project(ctx context.Context, values map[string]any, definitions []field.Definition, selected map[string]bool, onlyPublic bool) (map[string]any, error) {
	return p.projectValues(ctx, values, definitions, selected, onlyPublic, make(map[mediaKey]any))
}

func (p *Projector) projectValues(
	ctx context.Context,
	values map[string]any,
	definitions []field.Definition,
	selected map[string]bool,
	onlyPublic bool,
	resolved map[mediaKey]any,
) (map[string]any, error) {
	result := make(map[string]any)
	for _, definition := range definitions {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if onlyPublic && !definition.Public {
			continue
		}
		if selected != nil && !selected[definition.Key] {
			continue
		}
		value, exists := values[definition.Key]
		if !exists {
			continue
		}
		projected, err := p.projectValue(ctx, definition, value, onlyPublic, resolved)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", definition.Key, err)
		}
		result[definition.Key] = projected
	}
	return result, nil
}

func (p *Projector) projectValue(ctx context.Context, definition field.Definition, value any, onlyPublic bool, resolved map[mediaKey]any) (any, error) {
	if value == nil {
		return nil, nil
	}
	switch definition.Type {
	case field.TypeRepeater:
		options, err := repeaterOptions(definition.Options)
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
			projected, err := p.projectValues(ctx, row, options.Fields, nil, onlyPublic, resolved)
			if err != nil {
				return nil, err
			}
			projectedRows = append(projectedRows, projected)
		}
		return projectedRows, nil
	case field.TypeMedia:
		options, err := field.DecodeOptions[field.MediaOptions](definition.Options)
		if err != nil {
			return nil, fmt.Errorf("decode media options: %w", err)
		}
		if options.Multiple {
			items, ok := value.([]any)
			if !ok {
				return nil, errors.New("multiple media value is invalid")
			}
			result := make([]any, len(items))
			for index, item := range items {
				projected, err := p.projectMedia(ctx, item, options.SettingsCode, resolved)
				if err != nil {
					return nil, err
				}
				result[index] = projected
			}
			return result, nil
		}
		return p.projectMedia(ctx, value, options.SettingsCode, resolved)
	default:
		return value, nil
	}
}

func (p *Projector) projectMedia(ctx context.Context, value any, settingsCode string, resolved map[mediaKey]any) (any, error) {
	id, ok := mediaIDValue(value)
	if !ok || id <= 0 {
		return nil, errors.New("media reference is invalid")
	}
	mediaID := media.ID(id)
	key := mediaKey{id: mediaID, code: settingsCode}
	if item, exists := resolved[key]; exists {
		return item, nil
	}
	if p.medias == nil {
		return nil, errors.New("media service is unavailable")
	}
	// Media and file records are internal references. Public exposure is
	// gated by the field's public schema and the backing disk's visibility.
	resolvedMedia, err := p.medias.Resolve(ctx, security.System(), mediaID)
	if err != nil {
		if unavailable(err) {
			resolved[key] = nil
			return nil, nil
		}
		return nil, fmt.Errorf("resolve media %d: %w", id, err)
	}
	if p.files == nil {
		return nil, errors.New("file URL service is unavailable")
	}
	// PublicURL verifies visibility and delegates presentation to the disk
	// driver: local storage uses the application route; S3/CDN uses its URL.
	url, err := p.files.PublicURL(ctx, security.System(), resolvedMedia.File.ID)
	if err != nil {
		if unavailable(err) {
			resolved[key] = nil
			return nil, nil
		}
		return nil, fmt.Errorf("resolve media file URL: %w", err)
	}
	settings, exists := p.settings[settingsCode]
	if !exists {
		return nil, fmt.Errorf("media settings %q are unavailable", settingsCode)
	}
	metadata := make(map[string]any, len(settings))
	storedSettings, _ := resolvedMedia.Media.Params["settings"].(map[string]any)
	for _, definition := range settings {
		if value, exists := storedSettings[definition.Key]; exists {
			metadata[definition.Key] = value
		}
	}
	item := MediaValue{ID: id, URL: url, MIMEType: resolvedMedia.File.MIMEType, Size: resolvedMedia.File.Size, Settings: metadata}
	resolved[key] = item
	return item, nil
}

func mediaIDValue(value any) (int64, bool) {
	switch typed := value.(type) {
	case int64:
		return typed, true
	case json.Number:
		var rational big.Rat
		if _, ok := rational.SetString(string(typed)); !ok || !rational.IsInt() || !rational.Num().IsInt64() {
			return 0, false
		}
		return rational.Num().Int64(), true
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || math.Trunc(typed) != typed || typed >= math.Exp2(63) || typed < math.Exp2(63)*-1 {
			return 0, false
		}
		id := int64(typed)
		return id, float64(id) == typed
	default:
		return 0, false
	}
}

func repeaterOptions(value any) (field.RepeaterOptions, error) {
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

func unavailable(err error) bool {
	return errors.Is(err, file.ErrNotFound) || errors.Is(err, media.ErrNotFound) ||
		errors.Is(err, media.ErrInvalidReference) || errors.Is(err, security.ErrForbidden) ||
		errors.Is(err, security.ErrUnauthenticated) || errors.Is(err, file.ErrUnauthorized) ||
		errors.Is(err, filesystem.ErrInvalidVisibility)
}

func cloneMap(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
