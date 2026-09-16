package seo

import (
	"encoding/json"
	"errors"
	"fmt"
	stdhtml "html"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/templating"
	xhtml "golang.org/x/net/html"
)

type Robots struct {
	Index  bool `json:"index"`
	Follow bool `json:"follow"`
}

type OpenGraph struct {
	Title       string `json:"title"`
	Description string `json:"description"`
}

type PublicData struct {
	Title        string    `json:"title"`
	Description  string    `json:"description"`
	Keywords     []string  `json:"keywords"`
	CanonicalURL string    `json:"canonical_url"`
	Robots       Robots    `json:"robots"`
	OpenGraph    OpenGraph `json:"open_graph"`
}

type Warning struct {
	Field    string `json:"field"`
	Variable string `json:"variable"`
	Message  string `json:"message"`
}

type Preview struct {
	PublicData
	Warnings              []Warning `json:"warnings"`
	TitleCharacters       int       `json:"title_characters"`
	DescriptionCharacters int       `json:"description_characters"`
}

type RenderInput struct {
	Site     site.Site
	Resource resource.Resource
	Preview  bool
}

type Renderer struct {
	allowed           map[string]struct{}
	variables         []string
	siteParams        []field.Definition
	maxTemplateLength int
	maxResultLength   int
}

func NewRenderer(
	profile kernel.Profile,
	maxTemplateLength int,
	maxResultLength int,
) (*Renderer, error) {
	if maxTemplateLength <= 0 || maxResultLength <= 0 {
		return nil, errors.New("SEO renderer limits must be positive")
	}
	allowed := map[string]struct{}{
		"resource.title":      {},
		"resource.menu_title": {},
		"resource.annotation": {},
		"resource.slug":       {},
		"resource.path":       {},
	}
	siteParams := scalarDefinitions(field.PublicDefinitions(profile.Params))
	for variable := range site.NewTemplateVariables(site.Site{}, siteParams).Allowed() {
		allowed[variable] = struct{}{}
	}
	for _, template := range profile.Templates {
		for _, definition := range template.Fields {
			if definition.Type == field.TypeFile {
				continue
			}
			allowed["resource.field."+definition.Key] = struct{}{}
		}
	}
	variables := make([]string, 0, len(allowed))
	for variable := range allowed {
		variables = append(variables, variable)
	}
	slices.Sort(variables)
	return &Renderer{
		allowed:           allowed,
		variables:         variables,
		siteParams:        siteParams,
		maxTemplateLength: maxTemplateLength,
		maxResultLength:   maxResultLength,
	}, nil
}

func scalarDefinitions(definitions []field.Definition) []field.Definition {
	result := make([]field.Definition, 0, len(definitions))
	for _, definition := range definitions {
		if definition.Type != field.TypeFile {
			result = append(result, definition)
		}
	}
	return field.CloneDefinitions(result)
}

func (r *Renderer) Variables() []string {
	return append([]string(nil), r.variables...)
}

func (r *Renderer) Validate(settings Settings) error {
	fields := []struct {
		key    string
		source string
	}{
		{"title_template", settings.TitleTemplate},
		{"description_template", settings.DescriptionTemplate},
		{"keywords_template", settings.KeywordsTemplate},
		{"canonical_template", settings.CanonicalTemplate},
		{"og_title_template", settings.OGTitleTemplate},
		{"og_description_template", settings.OGDescriptionTemplate},
	}
	for _, field := range fields {
		if field.source == "" {
			continue
		}
		if _, err := templating.Compile(
			field.source,
			r.allowed,
			templating.Limits{
				MaxSourceLength: r.maxTemplateLength,
				MaxResultLength: r.maxResultLength,
			},
		); err != nil {
			return ValidationError{Field: field.key, Err: err}
		}
	}
	return nil
}

func (r *Renderer) Render(settings Settings, input RenderInput) (Preview, error) {
	if err := r.Validate(settings); err != nil {
		return Preview{}, err
	}
	resolver := valueResolver{siteVariables: site.NewTemplateVariables(input.Site, r.siteParams), resource: input.Resource}
	warnings := make([]Warning, 0)
	render := func(field, source string) (string, error) {
		if source == "" {
			return "", nil
		}
		compiled, err := templating.Compile(source, r.allowed, templating.Limits{
			MaxSourceLength: r.maxTemplateLength,
			MaxResultLength: r.maxResultLength,
		})
		if err != nil {
			return "", ValidationError{Field: field, Err: err}
		}
		result, err := templating.Render(compiled, resolver.resolve, templating.PlainText)
		if err != nil {
			return "", ValidationError{Field: field, Err: err}
		}
		value := plainText(result.Value)
		if utf8.RuneCountInString(value) > r.maxResultLength {
			return "", ValidationError{Field: field, Err: fmt.Errorf(
				"result exceeds %d characters",
				r.maxResultLength,
			)}
		}
		for _, warning := range result.Warnings {
			warnings = append(warnings, Warning{
				Field:    field,
				Variable: warning.Variable,
				Message:  warning.Message,
			})
		}
		return value, nil
	}

	title, err := render("title_template", settings.TitleTemplate)
	if err != nil {
		return Preview{}, err
	}
	description, err := render("description_template", settings.DescriptionTemplate)
	if err != nil {
		return Preview{}, err
	}
	keywordsValue, err := render("keywords_template", settings.KeywordsTemplate)
	if err != nil {
		return Preview{}, err
	}
	canonical, err := render("canonical_template", settings.CanonicalTemplate)
	if err != nil {
		return Preview{}, err
	}
	ogTitle := title
	if settings.OGTitleTemplate != "" {
		ogTitle, err = render("og_title_template", settings.OGTitleTemplate)
		if err != nil {
			return Preview{}, err
		}
	}
	ogDescription := description
	if settings.OGDescriptionTemplate != "" {
		ogDescription, err = render(
			"og_description_template",
			settings.OGDescriptionTemplate,
		)
		if err != nil {
			return Preview{}, err
		}
	}
	robotsIndex := settings.RobotsIndex
	if input.Preview {
		robotsIndex = false
	}
	data := PublicData{
		Title:        title,
		Description:  description,
		Keywords:     splitKeywords(keywordsValue),
		CanonicalURL: canonical,
		Robots: Robots{
			Index:  robotsIndex,
			Follow: settings.RobotsFollow,
		},
		OpenGraph: OpenGraph{Title: ogTitle, Description: ogDescription},
	}
	return Preview{
		PublicData:            data,
		Warnings:              warnings,
		TitleCharacters:       utf8.RuneCountInString(title),
		DescriptionCharacters: utf8.RuneCountInString(description),
	}, nil
}

type ValidationError struct {
	Field string
	Err   error
}

func (e ValidationError) Error() string {
	return fmt.Sprintf("%s: %v", e.Field, e.Err)
}

func (e ValidationError) Unwrap() error { return e.Err }

type valueResolver struct {
	siteVariables site.TemplateVariables
	resource      resource.Resource
}

func (r valueResolver) resolve(variable string) (any, bool, error) {
	value, exists := r.value(variable)
	return value, exists, nil
}

func (r valueResolver) value(variable string) (string, bool) {
	switch variable {
	case "resource.title":
		return stringValue(r.resource.Title)
	case "resource.menu_title":
		return stringValue(r.resource.MenuTitle)
	case "resource.annotation":
		return stringValue(r.resource.Annotation)
	case "resource.slug":
		return stringValue(r.resource.Slug)
	case "resource.path":
		if r.resource.Path == nil {
			return "", false
		}
		return stringValue(*r.resource.Path)
	}
	if key, found := strings.CutPrefix(variable, "resource.field."); found {
		return scalarValue(r.resource.Fields[key])
	}
	if value, exists := r.siteVariables.Value(variable); exists {
		return scalarValue(value)
	}
	return "", false
}

func stringValue(value string) (string, bool) {
	value = plainText(value)
	return value, value != ""
}

func scalarValue(value any) (string, bool) {
	switch current := value.(type) {
	case nil:
		return "", false
	case string:
		return stringValue(current)
	case bool:
		return strconv.FormatBool(current), true
	case float64:
		return strconv.FormatFloat(current, 'f', -1, 64), true
	case float32:
		return strconv.FormatFloat(float64(current), 'f', -1, 32), true
	case int:
		return strconv.Itoa(current), true
	case int64:
		return strconv.FormatInt(current, 10), true
	case int32:
		return strconv.FormatInt(int64(current), 10), true
	case json.Number:
		return current.String(), true
	case []string:
		return stringValue(strings.Join(current, ", "))
	case []any:
		values := make([]string, 0, len(current))
		for _, item := range current {
			value, exists := scalarValue(item)
			if exists {
				values = append(values, value)
			}
		}
		return stringValue(strings.Join(values, ", "))
	default:
		return "", false
	}
}

func plainText(value string) string {
	tokenizer := xhtml.NewTokenizer(strings.NewReader(value))
	parts := make([]string, 0, 4)
	skip := 0
	for {
		tokenType := tokenizer.Next()
		switch tokenType {
		case xhtml.ErrorToken:
			if !errors.Is(tokenizer.Err(), io.EOF) {
				return strings.Join(strings.Fields(stdhtml.UnescapeString(value)), " ")
			}
			return strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
		case xhtml.StartTagToken:
			name, _ := tokenizer.TagName()
			if string(name) == "script" || string(name) == "style" {
				skip++
			}
		case xhtml.EndTagToken:
			name, _ := tokenizer.TagName()
			if skip > 0 && (string(name) == "script" || string(name) == "style") {
				skip--
			}
		case xhtml.TextToken:
			if skip == 0 {
				parts = append(parts, stdhtml.UnescapeString(string(tokenizer.Text())))
			}
		}
	}
}

func splitKeywords(value string) []string {
	if value == "" {
		return []string{}
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}
