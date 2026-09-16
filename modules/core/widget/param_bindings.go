package widget

import (
	"fmt"
	"sort"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
)

// ResourceValueRef identifies a whole value on the current resource.
// It is configuration, never a resolved value or a cross-resource lookup.
type ResourceValueRef struct {
	Kind string `json:"kind"`
	Key  string `json:"key"`
}

type ParamBindings map[string]ResourceValueRef

func ResourceField(key string) ResourceValueRef {
	return ResourceValueRef{Kind: "resource_field", Key: key}
}
func ResourceProperty(key string) ResourceValueRef {
	return ResourceValueRef{Kind: "resource_property", Key: key}
}

func CloneParamBindings(source ParamBindings) ParamBindings {
	if source == nil {
		return nil
	}
	result := make(ParamBindings, len(source))
	for key, ref := range source {
		result[key] = ref
	}
	return result
}

type ValueSource struct {
	ResourceValueRef
	Label string `json:"label"`
	field.ValueShape
}

// ValueSources is shared by declaration validation and admin metadata. Keeping
// property types here avoids inferring semantic types from Go/JSON values.
func ValueSources(schema *field.Schema) []ValueSource {
	result := []ValueSource{
		{ResourceProperty("title"), "Название", field.ValueShape{Type: field.TypeString}},
		{ResourceProperty("menu_title"), "Название в меню", field.ValueShape{Type: field.TypeString}},
		{ResourceProperty("slug"), "Код", field.ValueShape{Type: field.TypeString}},
		{ResourceProperty("path"), "Путь", field.ValueShape{Type: field.TypeString}},
		{ResourceProperty("annotation"), "Аннотация", field.ValueShape{Type: field.TypeTextarea}},
		{ResourceProperty("content"), "Контент", field.ValueShape{Type: field.TypeTextarea}},
		{ResourceProperty("image_media_id"), "Изображение", field.ValueShape{Type: field.TypeMedia}},
	}
	if schema != nil {
		for _, def := range schema.Definitions() {
			shape, _ := schema.Shape(def.Key)
			result = append(result, ValueSource{ResourceField(def.Key), def.Label, shape})
		}
	}
	return result
}

// NormalizeConfiguration validates static configuration independently of current
// resource values. Restrictions on bound values are checked by NewResolved.
func (r *Runtime) NormalizeConfiguration(params map[string]any, bindings ParamBindings, source *field.Schema) (map[string]any, error) {
	if r == nil {
		return nil, fmt.Errorf("%w: widget runtime is nil", ErrInvalidParams)
	}
	deferred := make(map[string]struct{}, len(bindings))
	keys := make([]string, 0, len(bindings))
	for key := range bindings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		ref := bindings[key]
		target, exists := r.schema.Shape(key)
		if !exists {
			return nil, fmt.Errorf("%w: unknown widget parameter %q", ErrInvalidParams, key)
		}
		shape, exists := bindingSourceShape(ref, source)
		if !exists {
			return nil, fmt.Errorf("%w: parameter %q references unknown resource value %s:%s", ErrInvalidParams, key, ref.Kind, ref.Key)
		}
		if shape != target {
			return nil, fmt.Errorf("%w: parameter %q and source %s:%s must have the same type and multiplicity", ErrInvalidParams, key, ref.Kind, ref.Key)
		}
		deferred[key] = struct{}{}
	}
	normalized, err := r.schema.ValidateDeferred(params, deferred)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidParams, err)
	}
	return normalized, nil
}

// Inspect only the referenced field: resolving a binding must not clone an
// entire resource schema (including composite options) on every render.
func bindingSourceShape(ref ResourceValueRef, schema *field.Schema) (field.ValueShape, bool) {
	switch ref.Kind {
	case "resource_field":
		return schema.Shape(ref.Key)
	case "resource_property":
		for _, source := range ValueSources(nil) {
			if source.ResourceValueRef == ref {
				return source.ValueShape, true
			}
		}
	}
	return field.ValueShape{}, false
}

// ResourceValues is a request-local snapshot; callers supply already loaded data.
type ResourceValues struct {
	Fields     map[string]any
	Properties map[string]any
}

func (r *Runtime) NewResolved(params map[string]any, bindings ParamBindings, schema *field.Schema, values ResourceValues) (Instance, error) {
	resolved, err := r.ResolveParams(params, bindings, schema, values)
	if err != nil {
		return nil, err
	}
	return r.New(resolved)
}

// ResolveParams returns a fully validated request-local value map. Callers may
// additionally check infrastructure-backed constraints before creating an instance.
func (r *Runtime) ResolveParams(params map[string]any, bindings ParamBindings, schema *field.Schema, values ResourceValues) (map[string]any, error) {
	normalized, err := r.NormalizeConfiguration(params, bindings, schema)
	if err != nil {
		return nil, err
	}
	for key, ref := range bindings {
		source := values.Fields
		if ref.Kind == "resource_property" {
			source = values.Properties
		}
		if value, exists := source[ref.Key]; exists {
			normalized[key] = cloneParamValue(value)
		}
	}
	return r.NormalizeParams(normalized)
}

func cloneParamValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		return cloneMap(v)
	case []any:
		result := make([]any, len(v))
		for i, item := range v {
			result[i] = cloneParamValue(item)
		}
		return result
	case []string:
		return append([]string(nil), v...)
	case []int64:
		return append([]int64(nil), v...)
	default:
		return value
	}
}
