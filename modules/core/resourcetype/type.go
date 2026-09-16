package resourcetype

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
)

type Code string

const (
	Page         Code = "page"
	Link         Code = "link"
	ResourceLink Code = "resource_link"
	Library      Code = "library"
)

type Capabilities struct {
	SupportsTemplate       bool
	SupportsContent        bool
	SupportsWidgets        bool
	SupportsFields         bool
	SupportsExternalURL    bool
	SupportsTargetResource bool
	MutableType            bool
	OwnsLibraryItems       bool
	DefaultIcon            string
}

type ContentTypeOption struct {
	Code   string
	Label  string
	Editor field.EditorCode
}

type Metadata struct {
	Label            string
	Capabilities     Capabilities
	SettingsFields   []field.Definition
	SettingsDefaults map[string]any
	ContentTypes     []ContentTypeOption
}

type PathMode string

const (
	PathRoute PathMode = "route"
	PathNone  PathMode = "none"
)

type Payload struct {
	Template         *template.Code
	ContentType      *string
	Content          string
	TargetResourceID *int64
	ExternalURL      *string
	Fields           map[string]any
	TypeSettings     map[string]any
}

type Type interface {
	Code() Code
	PathMode() PathMode
	Metadata() Metadata
	Normalize(Payload) (Payload, error)
}

func StandardTypes() []Type {
	return []Type{
		pageType{},
		linkType{},
		resourceLinkType{},
		libraryType{},
	}
}

type pageType struct{}

func (pageType) Code() Code {
	return Page
}

func (pageType) PathMode() PathMode {
	return PathRoute
}

func (pageType) Metadata() Metadata {
	return Metadata{
		Label:            "Страница",
		Capabilities:     Capabilities{SupportsTemplate: true, SupportsContent: true, SupportsWidgets: true, SupportsFields: true, MutableType: true, DefaultIcon: "Document"},
		SettingsDefaults: map[string]any{},
		ContentTypes:     []ContentTypeOption{{Code: "html", Label: "HTML", Editor: "html"}},
	}
}

func (pageType) Normalize(payload Payload) (Payload, error) {
	if payload.Template != nil && *payload.Template == "" {
		return Payload{}, errors.New("page template code is empty")
	}
	if payload.TargetResourceID != nil {
		return Payload{}, errors.New(
			"page target_resource_id must be empty",
		)
	}
	if payload.ExternalURL != nil {
		return Payload{}, errors.New("page external_url must be empty")
	}
	if len(payload.TypeSettings) != 0 {
		return Payload{}, errors.New("page type settings must be empty")
	}

	if payload.ContentType == nil || *payload.ContentType == "" {
		contentType := "html"
		payload.ContentType = &contentType
	}
	if *payload.ContentType != "html" {
		return Payload{}, fmt.Errorf(
			"unsupported page content_type %q",
			*payload.ContentType,
		)
	}

	return clonePayload(payload), nil
}

type linkType struct{}

func (linkType) Code() Code {
	return Link
}

func (linkType) PathMode() PathMode {
	return PathRoute
}

func (linkType) Metadata() Metadata {
	return Metadata{
		Label:            "Ссылка",
		Capabilities:     Capabilities{SupportsExternalURL: true, MutableType: true, DefaultIcon: "Link"},
		SettingsDefaults: map[string]any{},
	}
}

func (linkType) Normalize(payload Payload) (Payload, error) {
	if payload.Template != nil {
		return Payload{}, errors.New("link template must be empty")
	}
	if payload.ContentType != nil {
		return Payload{}, errors.New("link content_type must be empty")
	}
	if payload.Content != "" {
		return Payload{}, errors.New("link content must be empty")
	}
	if payload.TargetResourceID != nil {
		return Payload{}, errors.New(
			"link target_resource_id must be empty",
		)
	}
	if len(payload.Fields) != 0 {
		return Payload{}, errors.New("link settings must be empty")
	}
	if len(payload.TypeSettings) != 0 {
		return Payload{}, errors.New("link type settings must be empty")
	}
	if payload.ExternalURL == nil ||
		!validExternalURL(*payload.ExternalURL) {
		return Payload{}, errors.New(
			"link external_url is invalid",
		)
	}

	return clonePayload(payload), nil
}

type resourceLinkType struct{}

func (resourceLinkType) Code() Code {
	return ResourceLink
}

func (resourceLinkType) PathMode() PathMode {
	return PathRoute
}

func (resourceLinkType) Metadata() Metadata {
	return Metadata{
		Label:            "Ссылка на ресурс",
		Capabilities:     Capabilities{SupportsTargetResource: true, MutableType: true, DefaultIcon: "Link"},
		SettingsDefaults: map[string]any{},
	}
}

func (resourceLinkType) Normalize(
	payload Payload,
) (Payload, error) {
	if payload.Template != nil {
		return Payload{}, errors.New(
			"resource_link template must be empty",
		)
	}
	if payload.ContentType != nil {
		return Payload{}, errors.New(
			"resource_link content_type must be empty",
		)
	}
	if payload.Content != "" {
		return Payload{}, errors.New(
			"resource_link content must be empty",
		)
	}
	if payload.ExternalURL != nil {
		return Payload{}, errors.New(
			"resource_link external_url must be empty",
		)
	}
	if len(payload.Fields) != 0 {
		return Payload{}, errors.New(
			"resource_link settings must be empty",
		)
	}
	if len(payload.TypeSettings) != 0 {
		return Payload{}, errors.New("resource_link type settings must be empty")
	}
	if payload.TargetResourceID == nil ||
		*payload.TargetResourceID <= 0 {
		return Payload{}, errors.New(
			"resource_link target_resource_id is required",
		)
	}

	return clonePayload(payload), nil
}

type libraryType struct{}

func (libraryType) Code() Code         { return Library }
func (libraryType) PathMode() PathMode { return PathRoute }
func (libraryType) Metadata() Metadata {
	required := true
	return Metadata{
		Label:        "Библиотека",
		Capabilities: Capabilities{SupportsTemplate: true, SupportsContent: true, SupportsWidgets: true, SupportsFields: true, MutableType: false, OwnsLibraryItems: true, DefaultIcon: "Collection"},
		SettingsFields: []field.Definition{
			{Key: "item_url_pattern", Type: field.TypeString, Label: "Шаблон URL ресурса", Required: &required},
			{Key: "default_item_template", Type: field.TypeString, Label: "Шаблон ресурса по умолчанию", Editor: "resource-template"},
		},
		SettingsDefaults: map[string]any{"item_url_pattern": DefaultItemURLPattern},
		ContentTypes:     []ContentTypeOption{{Code: "html", Label: "HTML", Editor: "html"}},
	}
}
func (libraryType) Normalize(payload Payload) (Payload, error) {
	if payload.Template != nil && *payload.Template == "" {
		return Payload{}, errors.New("library template code is empty")
	}
	if payload.TargetResourceID != nil {
		return Payload{}, errors.New("library target_resource_id must be empty")
	}
	if payload.ExternalURL != nil {
		return Payload{}, errors.New("library external_url must be empty")
	}
	if payload.ContentType == nil || *payload.ContentType == "" {
		contentType := "html"
		payload.ContentType = &contentType
	}
	if *payload.ContentType != "html" {
		return Payload{}, fmt.Errorf("unsupported library content_type %q", *payload.ContentType)
	}
	payload.TypeSettings = cloneMap(payload.TypeSettings)
	if payload.TypeSettings["item_url_pattern"] == nil || payload.TypeSettings["item_url_pattern"] == "" {
		payload.TypeSettings["item_url_pattern"] = DefaultItemURLPattern
	}
	if payload.TypeSettings["default_item_template"] == nil || payload.TypeSettings["default_item_template"] == "" {
		delete(payload.TypeSettings, "default_item_template")
	}
	if err := ValidateLibrarySettings(payload.TypeSettings); err != nil {
		return Payload{}, err
	}
	return clonePayload(payload), nil
}

const DefaultItemURLPattern = "/{slug}"

func ValidateLibrarySettings(settings map[string]any) error {
	for key := range settings {
		if key != "item_url_pattern" && key != "default_item_template" {
			return fmt.Errorf("unknown library type setting %q", key)
		}
	}
	pattern := ""
	if value, exists := settings["item_url_pattern"]; exists && value != nil {
		var ok bool
		pattern, ok = value.(string)
		if !ok {
			return errors.New("library item_url_pattern is invalid")
		}
	}
	if pattern == "" {
		pattern = DefaultItemURLPattern
	}
	if err := ValidateItemURLPattern(pattern); err != nil {
		return err
	}
	if value, exists := settings["default_item_template"]; exists && value != nil && value != "" {
		code, ok := value.(string)
		if !ok || code == "" || strings.TrimSpace(code) != code {
			return errors.New("library default_item_template is invalid")
		}
	}
	return nil
}

func ValidateItemURLPattern(pattern string) error {
	if pattern == "" || pattern[0] != '/' || strings.HasSuffix(pattern, "/") || strings.Contains(pattern, "//") {
		return errors.New("library item_url_pattern is invalid")
	}
	allowed := map[string]bool{"id": true, "slug": true, "year": true, "month": true, "day": true}
	unique := false
	for cursor := 0; cursor < len(pattern); {
		open := strings.IndexByte(pattern[cursor:], '{')
		if open < 0 {
			break
		}
		open += cursor
		close := strings.IndexByte(pattern[open:], '}')
		if close < 0 {
			return errors.New("library item_url_pattern has unmatched token")
		}
		close += open
		token := pattern[open+1 : close]
		if !allowed[token] {
			return fmt.Errorf("library item_url_pattern token %q is unsupported", token)
		}
		if token == "id" || token == "slug" {
			unique = true
		}
		cursor = close + 1
	}
	if strings.Count(pattern, "{") != strings.Count(pattern, "}") || !unique {
		return errors.New("library item_url_pattern must contain {id} or {slug}")
	}
	return nil
}

func validExternalURL(value string) bool {
	if value == "" || strings.TrimSpace(value) != value {
		return false
	}

	if strings.HasPrefix(value, "/") {
		if strings.HasPrefix(value, "//") {
			return false
		}
		parsed, err := url.ParseRequestURI(value)
		return err == nil && parsed.Scheme == "" && parsed.Host == ""
	}

	parsed, err := url.Parse(value)
	if err != nil {
		return false
	}

	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		return parsed.Host != ""

	case "mailto", "tel":
		return parsed.Opaque != "" || parsed.Path != ""

	default:
		return false
	}
}

func clonePayload(payload Payload) Payload {
	if payload.Template != nil {
		value := *payload.Template
		payload.Template = &value
	}
	if payload.ContentType != nil {
		value := *payload.ContentType
		payload.ContentType = &value
	}
	if payload.TargetResourceID != nil {
		value := *payload.TargetResourceID
		payload.TargetResourceID = &value
	}
	if payload.ExternalURL != nil {
		value := *payload.ExternalURL
		payload.ExternalURL = &value
	}

	payload.Fields = cloneMap(payload.Fields)
	payload.TypeSettings = cloneMap(payload.TypeSettings)
	return payload
}

func cloneMap(source map[string]any) map[string]any {
	if source == nil {
		return map[string]any{}
	}

	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = cloneValue(value)
	}
	return result
}

func cloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMap(typed)
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = cloneValue(item)
		}
		return result
	case []string:
		return append([]string(nil), typed...)
	default:
		return typed
	}
}
