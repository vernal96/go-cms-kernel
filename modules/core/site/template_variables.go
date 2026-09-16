package site

import "github.com/vernal96/go-cms-kernel/modules/core/field"

const TemplateVariableSource = "site"

type TemplateVariable struct {
	Variable string         `json:"variable"`
	Label    string         `json:"label"`
	Type     field.TypeCode `json:"type"`
	Source   string         `json:"source"`
}

// TemplateVariables is the Site-owned allowlist and typed value source shared
// by features that render site placeholders. It intentionally exposes only
// stable built-ins and active Profile.Params.
type TemplateVariables struct {
	item   Site
	params []field.Definition
}

func NewTemplateVariables(item Site, params []field.Definition) TemplateVariables {
	return TemplateVariables{item: item, params: field.CloneDefinitions(params)}
}

func (v TemplateVariables) Metadata() []TemplateVariable {
	result := []TemplateVariable{
		{Variable: "site.id", Label: "ID сайта", Type: field.TypeInteger, Source: TemplateVariableSource},
		{Variable: "site.profile_code", Label: "Профиль сайта", Type: field.TypeString, Source: TemplateVariableSource},
		{Variable: "site.domain", Label: "Домен сайта", Type: field.TypeString, Source: TemplateVariableSource},
		{Variable: "site.locale", Label: "Локаль сайта", Type: field.TypeString, Source: TemplateVariableSource},
		{Variable: "site.is_public", Label: "Сайт опубликован", Type: field.TypeCheckbox, Source: TemplateVariableSource},
	}
	for _, definition := range v.params {
		result = append(result, TemplateVariable{Variable: "site.field." + definition.Key, Label: definition.Label, Type: definition.Type, Source: TemplateVariableSource})
	}
	return result
}

func (v TemplateVariables) Allowed() map[string]struct{} {
	result := make(map[string]struct{}, len(v.params)+5)
	for _, item := range v.Metadata() {
		result[item.Variable] = struct{}{}
	}
	return result
}

func (v TemplateVariables) Definition(variable string) (field.Definition, bool) {
	const prefix = "site.field."
	if len(variable) <= len(prefix) || variable[:len(prefix)] != prefix {
		return field.Definition{}, false
	}
	key := variable[len(prefix):]
	for _, definition := range v.params {
		if definition.Key == key {
			return definition, true
		}
	}
	return field.Definition{}, false
}

func (v TemplateVariables) Value(variable string) (any, bool) {
	switch variable {
	case "site.id":
		return int64(v.item.ID), v.item.ID > 0
	case "site.profile_code":
		return string(v.item.ProfileCode), v.item.ProfileCode != ""
	case "site.domain":
		return v.item.Domain, v.item.Domain != ""
	case "site.locale":
		return v.item.Locale, v.item.Locale != ""
	case "site.is_public":
		return v.item.IsPublic, true
	}
	definition, exists := v.Definition(variable)
	if !exists {
		return nil, false
	}
	value, exists := v.item.Settings[definition.Key]
	return value, exists
}
