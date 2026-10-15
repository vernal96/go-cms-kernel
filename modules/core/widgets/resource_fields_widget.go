package widgets

import (
	"context"
	"fmt"
	"strings"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
)

var ResourceFields = widget.NewRef("resource_fields")

type resourceFieldsWidget struct{}

func (resourceFieldsWidget) Definition() widget.Definition {
	return widget.Definition{
		Reference:   ResourceFields,
		Label:       "Поля ресурса",
		Description: "Возвращает выбранные поля текущего ресурса",
		Fields: []field.Definition{{
			Key:      "fields",
			Type:     field.TypeString,
			Label:    "Поля",
			Required: true,
			Options:  field.StringOptions{Multiple: true},
			Editor:   "template-field-picker",
		}},
	}
}

func (resourceFieldsWidget) ValidateConfiguration(params map[string]any, bindings widget.ParamBindings, source *field.Schema) error {
	if _, bound := bindings["fields"]; bound {
		return fmt.Errorf("parameter %q cannot be bound to a resource value", "fields")
	}
	selected, ok := params["fields"].([]string)
	if !ok || len(selected) == 0 {
		return fmt.Errorf("at least one template field must be selected")
	}
	if source == nil {
		return fmt.Errorf("resource template schema is required")
	}
	seen := make(map[string]struct{}, len(selected))
	for _, key := range selected {
		if strings.TrimSpace(key) != key || key == "" {
			return fmt.Errorf("selected template field key %q is invalid", key)
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("template field %q is selected more than once", key)
		}
		seen[key] = struct{}{}
		if _, exists := source.Shape(key); !exists {
			return fmt.Errorf("template field %q does not exist in the current resource template", key)
		}
	}
	return nil
}

func (resourceFieldsWidget) New(params map[string]any) (widget.Instance, error) {
	selected, ok := params["fields"].([]string)
	if !ok || len(selected) == 0 {
		return nil, fmt.Errorf("at least one template field must be selected")
	}
	return resourceFieldsInstance{fields: append([]string(nil), selected...)}, nil
}

type resourceFieldsInstance struct {
	fields []string
}

func (i resourceFieldsInstance) Render(ctx context.Context, input widget.RenderInput) (map[string]any, error) {
	if ctx == nil {
		return nil, fmt.Errorf("widget render context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	values := make(map[string]any, len(i.fields))
	for _, key := range i.fields {
		values[key] = input.Resource.Fields[key]
	}
	return map[string]any{"fields": values}, nil
}
