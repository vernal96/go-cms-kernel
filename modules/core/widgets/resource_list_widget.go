package widgets

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/vernal96/go-cms-kernel/cache"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
)

var ResourceList = widget.NewRef("resource_list")

const resourceListCacheTTL = 5 * time.Minute

type resourceListWidget struct {
	query  *resource.QueryService
	cache  cache.Store
	fields []field.Choice
	types  []field.Choice
}

// NewResourceList builds the site-scoped core widget implementation. Services
// and the module-local durable store are captured during module runtime build.
func NewResourceList(query *resource.QueryService, store cache.Store, types []resourcetype.Code, templates []template.Definition) widget.Widget {
	fieldChoices := resourceListFieldChoices(templates)
	typeChoices := make([]field.Choice, len(types))
	for index, code := range types {
		typeChoices[index] = field.Choice{Value: string(code), Label: string(code)}
	}
	return resourceListWidget{query: query, cache: store, fields: fieldChoices, types: typeChoices}
}

func resourceListFieldChoices(templates []template.Definition) []field.Choice {
	choices := []field.Choice{
		{Value: string(resource.FieldID), Label: "ID"}, {Value: string(resource.FieldTitle), Label: "Заголовок"},
		{Value: string(resource.FieldMenuTitle), Label: "Заголовок меню"}, {Value: string(resource.FieldSlug), Label: "Slug"},
		{Value: string(resource.FieldPathValue), Label: "Путь"}, {Value: string(resource.FieldAnnotation), Label: "Аннотация"},
		{Value: string(resource.FieldType), Label: "Тип"}, {Value: string(resource.FieldTemplate), Label: "Шаблон"},
		{Value: string(resource.FieldSort), Label: "Сортировка"},
	}
	seen := map[string]bool{}
	for _, item := range choices {
		seen[item.Value] = true
	}
	for _, definition := range templates {
		for _, item := range definition.Fields {
			value := "resource.field." + item.Key
			if !seen[value] {
				choices = append(choices, field.Choice{Value: value, Label: definition.Label + ": " + item.Label})
				seen[value] = true
			}
		}
	}
	sort.Slice(choices[9:], func(i, j int) bool { return choices[9+i].Label < choices[9+j].Label })
	return choices
}

func (w resourceListWidget) Definition() widget.Definition {
	trueValue := true
	return widget.Definition{
		Reference: ResourceList, Label: "Список ресурсов", Description: "Выводит опубликованные ресурсы текущего сайта",
		Fields: []field.Definition{
			{Key: "parent_mode", Type: field.TypeRadio, Label: "Родитель", Required: &trueValue, Options: field.RadioOptions{Choices: []field.Choice{{Value: "root", Label: "Корень"}, {Value: "current", Label: "Текущий ресурс"}, {Value: "selected", Label: "Выбранный ресурс"}}}},
			{Key: "parent_resource", Type: field.TypeInteger, Label: "Выбранный ресурс", Editor: "resource-picker", VisibleWhen: &field.VisibleWhen{Field: "parent_mode", Value: "selected"}},
			{Key: "resources", Type: field.TypeJSON, Label: "Ресурсы", Editor: "resource-multi-picker"},
			{Key: "exclude", Type: field.TypeJSON, Label: "Исключить ресурсы", Editor: "resource-multi-picker"},
			{Key: "resource_types", Type: field.TypeSelect, Label: "Типы ресурсов", Options: field.SelectOptions{Choices: w.types, Multiple: true}, Editor: "resource-type-picker"},
			{Key: "limit", Type: field.TypeInteger, Label: "Лимит", Required: &trueValue, Rules: []string{"min=1", "max=100"}},
			{Key: "pagination_enabled", Type: field.TypeCheckbox, Label: "Пагинация"},
			{Key: "per_page", Type: field.TypeInteger, Label: "На странице", Rules: []string{"min=1", "max=100"}, VisibleWhen: &field.VisibleWhen{Field: "pagination_enabled", Value: true}},
			{Key: "fields", Type: field.TypeSelect, Label: "Поля", Options: field.SelectOptions{Choices: w.fields, Multiple: true}, Editor: "resource-field-picker"},
			{Key: "exclude_current", Type: field.TypeCheckbox, Label: "Исключить текущий ресурс"},
			{Key: "filters", Type: field.TypeJSON, Label: "Фильтры", Editor: "filter-builder"},
			{Key: "sorting", Type: field.TypeJSON, Label: "Сортировка", Editor: "sort-builder"},
		},
		EditorTabs:    []field.EditorTab{{Code: "selection", Label: "Выборка", Fields: []string{"parent_mode", "parent_resource", "resources", "exclude", "resource_types", "exclude_current", "limit", "pagination_enabled", "per_page"}}, {Code: "output", Label: "Вывод", Fields: []string{"fields", "filters", "sorting"}}},
		SummaryFields: []string{"parent_mode", "limit", "resource_types"},
	}
}

func (w resourceListWidget) New(values map[string]any) (widget.Instance, error) {
	if w.query == nil {
		return nil, errors.New("resource query service is nil")
	}
	config, err := parseResourceListConfig(values)
	if err != nil {
		return nil, err
	}
	return resourceListInstance{widget: w, config: config}, nil
}

type resourceListConfig struct {
	parentMode                 string
	parent                     *resource.ID
	ids, exclude               []resource.ID
	types                      []resourcetype.Code
	fields                     []resource.FieldPath
	filters                    []resource.FilterCondition
	sorting                    []resource.Sort
	limit, perPage             int
	pagination, excludeCurrent bool
}
type resourceListInstance struct {
	widget resourceListWidget
	config resourceListConfig
}

func (i resourceListInstance) Render(ctx context.Context, input widget.RenderInput) (map[string]any, error) {
	if ctx == nil {
		return nil, errors.New("widget render context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	query := resource.Query{SiteID: site.ID(input.Site.ID), IDs: i.config.ids, ExcludeIDs: append([]resource.ID(nil), i.config.exclude...), Types: i.config.types, Filters: i.config.filters, Sort: i.config.sorting, Fields: i.config.fields, Limit: i.config.limit, PublicOnly: true}
	if len(query.IDs) == 0 {
		query.FilterByParent = true
		switch i.config.parentMode {
		case "root":
		case "current":
			current := resource.ID(input.Resource.ID)
			query.Parent = &current
		case "selected":
			query.Parent = i.config.parent
		}
	}
	if i.config.excludeCurrent && input.Resource.ID > 0 {
		query.ExcludeIDs = append(query.ExcludeIDs, resource.ID(input.Resource.ID))
	}
	if i.config.pagination {
		query.Page = 1
		query.PerPage = i.config.perPage
	}
	key, err := resourceListCacheKey(query)
	if err != nil {
		return nil, err
	}
	result, err := cache.RememberJSON(ctx, i.widget.cache, key, cache.SetOptions{TTL: resourceListCacheTTL, Tags: []cache.Tag{cache.Tag(fmt.Sprintf("site:%d", input.Site.ID)), cache.Tag(fmt.Sprintf("site:%d:resources", input.Site.ID))}}, func(ctx context.Context) (resourceListResult, error) {
		page, err := i.widget.query.Query(ctx, query)
		if err != nil {
			return resourceListResult{}, err
		}
		return buildResourceListResult(page, query), nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"items": result.Items, "pagination": result.Pagination}, nil
}

type resourceListResult struct {
	Items      []map[string]any        `json:"items"`
	Pagination *resourceListPagination `json:"pagination"`
}
type resourceListPagination struct {
	Page    int `json:"page"`
	PerPage int `json:"per_page"`
	Total   int `json:"total"`
	Pages   int `json:"pages"`
}

func buildResourceListResult(page resource.Page, query resource.Query) resourceListResult {
	result := resourceListResult{Items: make([]map[string]any, len(page.Items))}
	for n, item := range page.Items {
		result.Items[n] = projectResource(item, query.Fields)
	}
	if query.PerPage > 0 {
		result.Pagination = &resourceListPagination{Page: query.Page, PerPage: query.PerPage, Total: page.Total, Pages: int(math.Ceil(float64(page.Total) / float64(query.PerPage)))}
	}
	return result
}
func projectResource(item resource.Resource, fields []resource.FieldPath) map[string]any {
	selected := map[resource.FieldPath]bool{}
	for _, field := range fields {
		selected[field] = true
	}
	if len(selected) == 0 {
		for _, field := range []resource.FieldPath{resource.FieldID, resource.FieldTitle, resource.FieldSlug, resource.FieldPathValue, resource.FieldAnnotation, resource.FieldType, resource.FieldTemplate} {
			selected[field] = true
		}
	}
	selected[resource.FieldID] = true
	output := map[string]any{"id": item.ID}
	for field := range selected {
		switch field {
		case resource.FieldTitle:
			output["title"] = item.Title
		case resource.FieldMenuTitle:
			output["menu_title"] = item.MenuTitle
		case resource.FieldSlug:
			output["slug"] = item.Slug
		case resource.FieldPathValue:
			output["path"] = item.Path
		case resource.FieldAnnotation:
			output["annotation"] = item.Annotation
		case resource.FieldType:
			output["type"] = item.Type
		case resource.FieldTemplate:
			output["template"] = item.Template
		case resource.FieldSort:
			output["sort"] = item.Sort
		default:
			if key, ok := strings.CutPrefix(string(field), "resource.field."); ok {
				fields, exists := output["fields"].(map[string]any)
				if !exists {
					fields = map[string]any{}
					output["fields"] = fields
				}
				fields[key] = item.Fields[key]
			}
		}
	}
	return output
}

func parseResourceListConfig(values map[string]any) (resourceListConfig, error) {
	config := resourceListConfig{parentMode: stringValue(values, "parent_mode"), limit: intValue(values, "limit"), pagination: boolValue(values, "pagination_enabled"), excludeCurrent: boolValue(values, "exclude_current")}
	if config.parentMode != "root" && config.parentMode != "current" && config.parentMode != "selected" {
		return config, errors.New("parent_mode is invalid")
	}
	if config.limit < 1 || config.limit > 100 {
		return config, errors.New("limit is invalid")
	}
	if config.parentMode == "selected" {
		parent := resource.ID(intValue(values, "parent_resource"))
		if parent <= 0 {
			return config, errors.New("parent_resource is required")
		}
		config.parent = &parent
	}
	var err error
	if config.ids, err = resourceIDs(values["resources"]); err != nil {
		return config, err
	}
	if config.exclude, err = resourceIDs(values["exclude"]); err != nil {
		return config, err
	}
	if config.types, err = resourceTypes(values["resource_types"]); err != nil {
		return config, err
	}
	if config.fields, err = resourceFields(values["fields"]); err != nil {
		return config, err
	}
	if config.filters, err = resourceFilters(values["filters"]); err != nil {
		return config, err
	}
	if config.sorting, err = resourceSorting(values["sorting"]); err != nil {
		return config, err
	}
	if config.pagination {
		config.perPage = intValue(values, "per_page")
		if config.perPage < 1 || config.perPage > config.limit {
			return config, errors.New("per_page is invalid")
		}
	}
	return config, nil
}
func stringValue(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return value
}
func boolValue(values map[string]any, key string) bool { value, _ := values[key].(bool); return value }
func intValue(values map[string]any, key string) int {
	switch value := values[key].(type) {
	case int64:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		number, _ := value.Int64()
		return int(number)
	}
	return 0
}
func resourceIDs(value any) ([]resource.ID, error) {
	if value == nil {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, errors.New("resource ids are invalid")
	}
	result := make([]resource.ID, len(items))
	for index, item := range items {
		number := intValue(map[string]any{"value": item}, "value")
		if number <= 0 {
			return nil, errors.New("resource id is invalid")
		}
		result[index] = resource.ID(number)
	}
	return result, nil
}
func resourceTypes(value any) ([]resourcetype.Code, error) {
	if value == nil {
		return nil, nil
	}
	items, ok := value.([]string)
	if !ok {
		return nil, errors.New("resource types are invalid")
	}
	result := make([]resourcetype.Code, len(items))
	for index, item := range items {
		if item == "" {
			return nil, errors.New("resource type is invalid")
		}
		result[index] = resourcetype.Code(item)
	}
	return result, nil
}
func resourceFields(value any) ([]resource.FieldPath, error) {
	if value == nil {
		return nil, nil
	}
	items, ok := value.([]string)
	if !ok {
		return nil, errors.New("resource fields are invalid")
	}
	result := make([]resource.FieldPath, len(items))
	for index, item := range items {
		result[index] = resource.FieldPath(item)
		if !resource.ValidFieldPath(result[index]) {
			return nil, errors.New("resource field is invalid")
		}
	}
	return result, nil
}
func resourceFilters(value any) ([]resource.FilterCondition, error) {
	if value == nil {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, errors.New("filters are invalid")
	}
	result := make([]resource.FilterCondition, len(items))
	for index, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			return nil, errors.New("filter is invalid")
		}
		fieldValue, _ := object["field"].(string)
		operator, _ := object["operator"].(string)
		kind, _ := object["value_kind"].(string)
		result[index] = resource.FilterCondition{Field: resource.FieldPath(fieldValue), Operator: resource.FilterOperator(operator), Value: object["value"], Kind: field.StorageKind(kind)}
		if err := result[index].Validate(); err != nil {
			return nil, err
		}
	}
	return result, nil
}
func resourceSorting(value any) ([]resource.Sort, error) {
	if value == nil {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, errors.New("sorting is invalid")
	}
	result := make([]resource.Sort, len(items))
	for index, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			return nil, errors.New("sort is invalid")
		}
		fieldValue, _ := object["field"].(string)
		direction, _ := object["direction"].(string)
		kind, _ := object["value_kind"].(string)
		result[index] = resource.Sort{Field: resource.FieldPath(fieldValue), Direction: resource.SortDirection(direction), Kind: field.StorageKind(kind)}
		if !resource.SortableField(result[index].Field) || (result[index].Direction != resource.SortAscending && result[index].Direction != resource.SortDescending) {
			return nil, errors.New("sort is invalid")
		}
	}
	return result, nil
}
func resourceListCacheKey(query resource.Query) (string, error) {
	normalized := query.Normalized()
	raw, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "widget:resource-list:v1:" + hex.EncodeToString(sum[:]), nil
}
