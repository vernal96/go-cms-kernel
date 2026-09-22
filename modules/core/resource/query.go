package resource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
)

// FieldPath is a transport and database-neutral resource field identifier.
// Custom template values retain the existing resource.field.<key> namespace.
type FieldPath string

const (
	FieldID           FieldPath = "resource.id"
	FieldTitle        FieldPath = "resource.title"
	FieldMenuTitle    FieldPath = "resource.menu_title"
	FieldSlug         FieldPath = "resource.slug"
	FieldPathValue    FieldPath = "resource.path"
	FieldAnnotation   FieldPath = "resource.annotation"
	FieldType         FieldPath = "resource.type"
	FieldTemplate     FieldPath = "resource.template"
	FieldSort         FieldPath = "resource.sort"
	FieldIsPublic     FieldPath = "resource.is_public"
	FieldIsSearchable FieldPath = "resource.is_searchable"
	FieldPublishedAt  FieldPath = "resource.published_at"
	FieldCreatedAt    FieldPath = "resource.created_at"
	FieldUpdatedAt    FieldPath = "resource.updated_at"
)

type FilterOperator string

const (
	FilterEqual              FilterOperator = "eq"
	FilterNotEqual           FilterOperator = "neq"
	FilterIn                 FilterOperator = "in"
	FilterNotIn              FilterOperator = "not_in"
	FilterGreaterThan        FilterOperator = "gt"
	FilterGreaterThanOrEqual FilterOperator = "gte"
	FilterLessThan           FilterOperator = "lt"
	FilterLessThanOrEqual    FilterOperator = "lte"
)

type SortDirection string

const (
	SortAscending  SortDirection = "asc"
	SortDescending SortDirection = "desc"
)

type FilterCondition struct {
	Field    FieldPath
	Operator FilterOperator
	Value    any
	Kind     field.StorageKind
}

type Sort struct {
	Field     FieldPath
	Direction SortDirection
	Kind      field.StorageKind
}

// Query expresses resource selection without leaking adapter/SQL concerns.
// When FilterByParent is true, Parent nil means root resources.
type Query struct {
	SiteID         site.ID
	FilterByParent bool
	Parent         *ID
	IDs            []ID
	ExcludeIDs     []ID
	Types          []resourcetype.Code
	Filters        []FilterCondition
	Sort           []Sort
	Fields         []FieldPath
	Limit          int
	Page           int
	PerPage        int
	PublicOnly     bool
}

type Page struct {
	// ValidUntil bounds cached public collections at the next publication transition.
	ValidUntil time.Time
	Items      []Resource
	Total      int
}

type QueryRepository interface {
	Query(context.Context, Query) (Page, error)
}

type QueryService struct{ repository QueryRepository }

func NewQueryService(repository QueryRepository) (*QueryService, error) {
	if repository == nil {
		return nil, errors.New("resource query repository is nil")
	}
	return &QueryService{repository: repository}, nil
}

func (s *QueryService) Query(ctx context.Context, query Query) (Page, error) {
	if ctx == nil {
		return Page{}, errors.New("resource query context is nil")
	}
	if s == nil || s.repository == nil {
		return Page{}, errors.New("resource query service is nil")
	}
	if err := query.Validate(); err != nil {
		return Page{}, err
	}
	result, err := s.repository.Query(ctx, query)
	if err != nil {
		return Page{}, fmt.Errorf("query resources: %w", err)
	}
	for index := range result.Items {
		result.Items[index] = Clone(result.Items[index])
	}
	return result, nil
}

func (q Query) Validate() error {
	if q.SiteID <= 0 {
		return errors.New("resource query site id is invalid")
	}
	if q.Parent != nil && *q.Parent <= 0 {
		return errors.New("resource query parent id is invalid")
	}
	if q.Limit <= 0 || q.Limit > 100 {
		return errors.New("resource query limit must be between 1 and 100")
	}
	if q.PerPage < 0 || q.Page < 0 {
		return errors.New("resource query pagination is invalid")
	}
	if q.PerPage > 0 && (q.PerPage > q.Limit || q.Page < 1) {
		return errors.New("resource query page is invalid")
	}
	for _, id := range append(append([]ID(nil), q.IDs...), q.ExcludeIDs...) {
		if id <= 0 {
			return errors.New("resource query id is invalid")
		}
	}
	for _, typ := range q.Types {
		if typ == "" || strings.TrimSpace(string(typ)) != string(typ) {
			return errors.New("resource query type is invalid")
		}
	}
	for _, field := range q.Fields {
		if !ValidFieldPath(field) {
			return fmt.Errorf("resource query field %q is invalid", field)
		}
	}
	for _, condition := range q.Filters {
		if err := condition.Validate(); err != nil {
			return err
		}
	}
	for _, item := range q.Sort {
		if err := item.Validate(); err != nil {
			return errors.New("resource query sort is invalid")
		}
	}
	return nil
}

func (c FilterCondition) Validate() error {
	if !ValidFieldPath(c.Field) {
		return fmt.Errorf("resource query filter field %q is invalid", c.Field)
	}
	switch c.Operator {
	case FilterEqual, FilterNotEqual, FilterIn, FilterNotIn, FilterGreaterThan, FilterGreaterThanOrEqual, FilterLessThan, FilterLessThanOrEqual:
	default:
		return fmt.Errorf("resource query filter operator %q is invalid", c.Operator)
	}
	if (c.Operator == FilterIn || c.Operator == FilterNotIn) && !isSlice(c.Value) {
		return errors.New("resource query set filter value is invalid")
	}
	kind, err := c.storageKind()
	if err != nil {
		return err
	}
	if !operatorSupportsStorageKind(c.Operator, kind) {
		return fmt.Errorf("resource query field %q storage kind %q does not support operator %q", c.Field, kind, c.Operator)
	}
	if err := validateFilterValue(kind, c.Operator, c.Value); err != nil {
		return fmt.Errorf("resource query field %q: %w", c.Field, err)
	}
	return nil
}

func (s Sort) Validate() error {
	if !SortableField(s.Field) || (s.Direction != SortAscending && s.Direction != SortDescending) {
		return errors.New("resource query sort is invalid")
	}
	kind, err := sortStorageKind(s)
	if err != nil {
		return err
	}
	if !sortableStorageKind(kind) {
		return fmt.Errorf("resource query field %q storage kind %q is not sortable", s.Field, kind)
	}
	return nil
}

func (c FilterCondition) storageKind() (field.StorageKind, error) {
	if IsCustomFieldPath(c.Field) {
		if !field.ValidStorageKind(c.Kind) {
			return "", fmt.Errorf("resource query custom field %q requires a valid storage kind", c.Field)
		}
		return c.Kind, nil
	}
	return BuiltinFieldStorageKind(c.Field)
}

func sortStorageKind(s Sort) (field.StorageKind, error) {
	if IsCustomFieldPath(s.Field) {
		if !field.ValidStorageKind(s.Kind) {
			return "", fmt.Errorf("resource query custom sort field %q requires a valid storage kind", s.Field)
		}
		return s.Kind, nil
	}
	return BuiltinFieldStorageKind(s.Field)
}

func ValidStorageKind(kind field.StorageKind) bool {
	return field.ValidStorageKind(kind)
}

func ValidFieldPath(field FieldPath) bool {
	switch field {
	case FieldID, FieldTitle, FieldMenuTitle, FieldSlug, FieldPathValue, FieldAnnotation,
		FieldType, FieldTemplate, FieldSort, FieldIsPublic, FieldIsSearchable,
		FieldPublishedAt, FieldCreatedAt, FieldUpdatedAt:
		return true
	}
	key, found := strings.CutPrefix(string(field), "resource.field.")
	return found && key != "" && strings.TrimSpace(key) == key && !strings.ContainsAny(key, ". \t\r\n")
}
func SortableField(field FieldPath) bool {
	switch field {
	case FieldID, FieldTitle, FieldMenuTitle, FieldSlug, FieldPathValue, FieldType,
		FieldTemplate, FieldSort, FieldPublishedAt, FieldCreatedAt, FieldUpdatedAt:
		return true
	}
	return strings.HasPrefix(string(field), "resource.field.")
}

func IsCustomFieldPath(path FieldPath) bool {
	return strings.HasPrefix(string(path), "resource.field.")
}

func BuiltinFieldStorageKind(path FieldPath) (field.StorageKind, error) {
	switch path {
	case FieldID, FieldSort:
		return field.StorageInteger, nil
	case FieldIsPublic, FieldIsSearchable:
		return field.StorageBoolean, nil
	case FieldPublishedAt, FieldCreatedAt, FieldUpdatedAt:
		return field.StorageTimestamp, nil
	case FieldTitle, FieldMenuTitle, FieldSlug, FieldPathValue, FieldAnnotation, FieldType, FieldTemplate:
		return field.StorageString, nil
	default:
		return "", fmt.Errorf("resource query field %q has no storage kind", path)
	}
}

func isSlice(value any) bool {
	if value == nil {
		return false
	}
	return reflect.ValueOf(value).Kind() == reflect.Slice
}

func operatorSupportsStorageKind(operator FilterOperator, kind field.StorageKind) bool {
	switch operator {
	case FilterGreaterThan, FilterGreaterThanOrEqual, FilterLessThan, FilterLessThanOrEqual:
		return sortableStorageKind(kind)
	case FilterIn, FilterNotIn:
		return kind != field.StorageJSON
	case FilterEqual, FilterNotEqual:
		return field.ValidStorageKind(kind)
	default:
		return false
	}
}

func sortableStorageKind(kind field.StorageKind) bool {
	switch kind {
	case field.StorageString, field.StorageInteger, field.StorageFloat, field.StorageTimestamp:
		return true
	default:
		return false
	}
}

func validateFilterValue(kind field.StorageKind, operator FilterOperator, value any) error {
	if operator == FilterIn || operator == FilterNotIn {
		items := reflect.ValueOf(value)
		if items.Len() == 0 {
			return errors.New("set filter value is empty")
		}
		for index := 0; index < items.Len(); index++ {
			if !valueMatchesStorageKind(kind, items.Index(index).Interface()) {
				return fmt.Errorf("set filter value at index %d is incompatible with storage kind %q", index, kind)
			}
		}
		return nil
	}
	if !valueMatchesStorageKind(kind, value) {
		return fmt.Errorf("filter value is incompatible with storage kind %q", kind)
	}
	return nil
}

func valueMatchesStorageKind(kind field.StorageKind, value any) bool {
	switch kind {
	case field.StorageString:
		_, ok := value.(string)
		return ok
	case field.StorageBoolean:
		_, ok := value.(bool)
		return ok
	case field.StorageTimestamp:
		_, ok := value.(time.Time)
		return ok
	case field.StorageInteger, field.StorageReference:
		switch typed := value.(type) {
		case json.Number:
			_, err := typed.Int64()
			return err == nil
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, ID:
			return true
		default:
			return false
		}
	case field.StorageFloat:
		switch value.(type) {
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, float32, float64, ID, json.Number:
			return true
		default:
			return false
		}
	case field.StorageJSON:
		return value != nil
	default:
		return false
	}
}

// Normalized returns an order-insensitive canonical copy suitable for result
// cache identity. Sort order and explicit ID order are intentionally kept.
func (q Query) Normalized() Query {
	q.IDs = append([]ID(nil), q.IDs...)
	sort.Slice(q.IDs, func(i, j int) bool { return q.IDs[i] < q.IDs[j] })
	q.Types = append([]resourcetype.Code(nil), q.Types...)
	sort.Slice(q.Types, func(i, j int) bool { return q.Types[i] < q.Types[j] })
	q.ExcludeIDs = append([]ID(nil), q.ExcludeIDs...)
	sort.Slice(q.ExcludeIDs, func(i, j int) bool { return q.ExcludeIDs[i] < q.ExcludeIDs[j] })
	q.Fields = append([]FieldPath(nil), q.Fields...)
	sort.Slice(q.Fields, func(i, j int) bool { return q.Fields[i] < q.Fields[j] })
	q.Filters = append([]FilterCondition(nil), q.Filters...)
	sort.SliceStable(q.Filters, func(i, j int) bool {
		left, _ := json.Marshal(q.Filters[i])
		right, _ := json.Marshal(q.Filters[j])
		return string(left) < string(right)
	})
	return q
}
