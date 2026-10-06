package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
)

func (r *Repository) Query(ctx context.Context, query resource.Query) (resource.Page, error) {
	if ctx == nil {
		return resource.Page{}, errors.New("query resources context is nil")
	}
	if err := query.Validate(); err != nil {
		return resource.Page{}, err
	}
	where, args, err := resourceQueryWhere(query)
	if err != nil {
		return resource.Page{}, err
	}
	whereArgs := append([]any(nil), args...)
	order, args, err := resourceQueryOrder(query.Sort, args)
	if err != nil {
		return resource.Page{}, err
	}
	limitArg := append(args, query.Limit)
	limit := "$" + strconv.Itoa(len(limitArg))
	offset := ""
	if query.PerPage > 0 {
		limitArg[len(limitArg)-1] = query.PerPage
		limit = "$" + strconv.Itoa(len(limitArg))
		limitArg = append(limitArg, (query.Page-1)*query.PerPage)
		offset = " OFFSET $" + strconv.Itoa(len(limitArg))
	}
	queriedAt := time.Now().UTC()
	tx, err := r.connector.Pool().BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return resource.Page{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `
SELECT id, site_id, parent_id, type, template, content_type,
       title, menu_title, slug, path, annotation, content, image_media_id,
       target_resource_id, external_url, is_public, is_searchable, in_menu,
       in_sitemap, sort, published_at, unpublished_at, type_settings, created_at,
       updated_at, created_by, updated_by, deleted_at, deleted_by
FROM core.resources r
WHERE `+where+`
ORDER BY `+order+`
LIMIT `+limit+offset+`;`, limitArg...)
	if err != nil {
		return resource.Page{}, fmt.Errorf("query resources: %w", err)
	}
	defer rows.Close()
	items := make([]resource.Resource, 0)
	for rows.Next() {
		item, scanErr := scanResource(rows)
		if scanErr != nil {
			return resource.Page{}, scanErr
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return resource.Page{}, fmt.Errorf("iterate resources: %w", err)
	}
	rows.Close()
	if err := loadResourceFields(ctx, tx, items); err != nil {
		return resource.Page{}, err
	}
	result := resource.Page{Items: items}
	if query.PerPage > 0 {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM core.resources r WHERE `+where+`;`, whereArgs...).Scan(&result.Total); err != nil {
			return resource.Page{}, fmt.Errorf("count resources: %w", err)
		}
	}
	if query.PublicOnly {
		var next *time.Time
		err := tx.QueryRow(ctx, `SELECT min(boundary) FROM core.resources r CROSS JOIN LATERAL (VALUES (r.published_at), (r.unpublished_at)) AS schedule(boundary) WHERE r.site_id=$1 AND r.is_public AND r.deleted_at IS NULL AND boundary > $2`, query.SiteID, queriedAt).Scan(&next)
		if err != nil {
			return resource.Page{}, err
		}
		if next != nil {
			result.ValidUntil = *next
		}
	}
	return result, nil
}

func resourceQueryWhere(query resource.Query) (string, []any, error) {
	args := []any{query.SiteID}
	where := []string{"r.site_id = $1"}
	add := func(value any) string { args = append(args, value); return "$" + strconv.Itoa(len(args)) }
	if len(query.IDs) > 0 {
		where = append(where, "r.id = ANY("+add(query.IDs)+"::bigint[])")
	} else if query.FilterByParent {
		where = append(where, "r.parent_id IS NOT DISTINCT FROM "+add(query.Parent)+"::bigint")
	}
	if len(query.ExcludeIDs) > 0 {
		where = append(where, "NOT (r.id = ANY("+add(query.ExcludeIDs)+"::bigint[]))")
	}
	if len(query.Types) > 0 {
		where = append(where, "r.type = ANY("+add(query.Types)+"::text[])")
	}
	if query.PublicOnly {
		where = append(where, "r.deleted_at IS NULL", "r.is_public", "(r.published_at IS NULL OR r.published_at <= now())", "(r.unpublished_at IS NULL OR now() < r.unpublished_at)")
	}
	for _, condition := range query.Filters {
		fragment, err := resourceQueryFilter(condition, add)
		if err != nil {
			return "", nil, err
		}
		where = append(where, fragment)
	}
	return strings.Join(where, " AND "), args, nil
}

func resourceQueryFilter(condition resource.FilterCondition, add func(any) string) (string, error) {
	return resourceQueryFilterFor(condition, add, "r", resourceQueryColumn)
}

type resourceQueryColumnResolver func(resource.FieldPath) (string, bool)

func resourceQueryFilterFor(condition resource.FilterCondition, add func(any) string, alias string, resolve resourceQueryColumnResolver) (string, error) {
	if err := condition.Validate(); err != nil {
		return "", err
	}
	column, custom := resolve(condition.Field)
	if custom {
		key := strings.TrimPrefix(string(condition.Field), "resource.field.")
		kind, value, err := filterStorageValue(condition.Kind, condition.Value)
		if err != nil {
			return "", err
		}
		column, err := fieldValueColumn(kind)
		if err != nil {
			return "", err
		}
		operator := filterOperatorSQL(condition.Operator)
		negative := condition.Operator == resource.FilterNotEqual || condition.Operator == resource.FilterNotIn
		if condition.Operator == resource.FilterNotEqual {
			operator = filterOperatorSQL(resource.FilterEqual)
		} else if condition.Operator == resource.FilterNotIn {
			operator = filterOperatorSQL(resource.FilterIn)
		}
		placeholder := add(value)
		comparison := "value." + column + " " + operator + " " + placeholder
		if condition.Operator == resource.FilterIn || condition.Operator == resource.FilterNotIn {
			comparison = "value." + column + " " + operator + " (" + placeholder + ")"
		}
		prefix := "EXISTS"
		if negative {
			prefix = "NOT EXISTS"
		}
		return prefix + " (SELECT 1 FROM core.resource_field_values value WHERE value.resource_id = " + alias + ".id AND value.site_id = " + alias + ".site_id AND value.field_key = " + add(key) + " AND value.value_kind = " + add(kind) + " AND " + comparison + ")", nil
	}
	operator := filterOperatorSQL(condition.Operator)
	if condition.Operator == resource.FilterIn || condition.Operator == resource.FilterNotIn {
		kind, err := resource.BuiltinFieldStorageKind(condition.Field)
		if err != nil {
			return "", err
		}
		switch kind {
		case field.StorageInteger:
			values, ok := integerValues(condition.Value)
			if !ok {
				return "", errors.New("resource query numeric set filter value is invalid")
			}
			return column + " " + operator + " (" + add(values) + "::bigint[])", nil
		case field.StorageBoolean:
			values, ok := booleanValues(condition.Value)
			if !ok {
				return "", errors.New("resource query boolean set filter value is invalid")
			}
			return column + " " + operator + " (" + add(values) + "::boolean[])", nil
		case field.StorageTimestamp:
			values, ok := timestampValues(condition.Value)
			if !ok {
				return "", errors.New("resource query timestamp set filter value is invalid")
			}
			return column + " " + operator + " (" + add(values) + "::timestamptz[])", nil
		default:
			values, ok := textValues(condition.Value)
			if !ok {
				return "", errors.New("resource query set filter value is invalid")
			}
			return column + " " + operator + " (" + add(values) + "::text[])", nil
		}
	}
	return column + " " + operator + " " + add(condition.Value), nil
}

func filterStorageValue(explicit field.StorageKind, value any) (field.StorageKind, any, error) {
	if !resource.ValidStorageKind(explicit) {
		return "", nil, fmt.Errorf("resource field storage kind %q is invalid", explicit)
	}
	reflected := reflect.ValueOf(value)
	if reflected.IsValid() && reflected.Kind() == reflect.Slice {
		normalized, ok := storageSetValue(explicit, value)
		if !ok {
			return "", nil, fmt.Errorf("resource field set value is incompatible with storage kind %q", explicit)
		}
		return explicit, normalized, nil
	}
	return explicit, value, nil
}

func storageSetValue(kind field.StorageKind, value any) (any, bool) {
	switch kind {
	case field.StorageString:
		return textValues(value)
	case field.StorageInteger, field.StorageReference:
		return integerValues(value)
	case field.StorageFloat:
		return floatValues(value)
	case field.StorageBoolean:
		return booleanValues(value)
	case field.StorageTimestamp:
		return timestampValues(value)
	default:
		return nil, false
	}
}

func fieldValueColumn(kind field.StorageKind) (string, error) {
	switch kind {
	case field.StorageString:
		return "value_string", nil
	case field.StorageInteger:
		return "value_integer", nil
	case field.StorageFloat:
		return "value_float", nil
	case field.StorageBoolean:
		return "value_boolean", nil
	case field.StorageTimestamp:
		return "value_timestamp", nil
	case field.StorageReference:
		return "value_reference", nil
	case field.StorageJSON:
		return "value_json", nil
	default:
		return "", fmt.Errorf("resource field storage kind %q is invalid", kind)
	}
}

func resourceQueryColumn(field resource.FieldPath) (string, bool) {
	switch field {
	case resource.FieldID:
		return "r.id", false
	case resource.FieldTitle:
		return "r.title", false
	case resource.FieldMenuTitle:
		return "r.menu_title", false
	case resource.FieldSlug:
		return "r.slug", false
	case resource.FieldPathValue:
		return "r.path", false
	case resource.FieldAnnotation:
		return "r.annotation", false
	case resource.FieldType:
		return "r.type", false
	case resource.FieldTemplate:
		return "r.template", false
	case resource.FieldSort:
		return "r.sort", false
	case resource.FieldIsPublic:
		return "r.is_public", false
	case resource.FieldIsSearchable:
		return "r.is_searchable", false
	case resource.FieldPublishedAt:
		return "r.published_at", false
	case resource.FieldCreatedAt:
		return "r.created_at", false
	case resource.FieldUpdatedAt:
		return "r.updated_at", false
	default:
		return "", true
	}
}

func filterOperatorSQL(operator resource.FilterOperator) string {
	switch operator {
	case resource.FilterEqual:
		return "="
	case resource.FilterNotEqual:
		return "<>"
	case resource.FilterIn:
		return "= ANY"
	case resource.FilterNotIn:
		return "<> ALL"
	case resource.FilterGreaterThan:
		return ">"
	case resource.FilterGreaterThanOrEqual:
		return ">="
	case resource.FilterLessThan:
		return "<"
	case resource.FilterLessThanOrEqual:
		return "<="
	}
	return ""
}

func textValues(value any) ([]string, bool) {
	switch typed := value.(type) {
	case []string:
		return typed, true
	case []any:
		result := make([]string, len(typed))
		for i, item := range typed {
			v, ok := item.(string)
			if !ok {
				return nil, false
			}
			result[i] = v
		}
		return result, true
	default:
		return nil, false
	}
}

func integerValues(value any) ([]int64, bool) {
	reflected := reflect.ValueOf(value)
	if !reflected.IsValid() || reflected.Kind() != reflect.Slice {
		return nil, false
	}
	result := make([]int64, reflected.Len())
	for index := 0; index < reflected.Len(); index++ {
		item := reflected.Index(index)
		switch item.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			result[index] = item.Int()
			continue
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32:
			result[index] = int64(item.Uint())
			continue
		}
		switch number := item.Interface().(type) {
		case float64:
			if number != math.Trunc(number) {
				return nil, false
			}
			result[index] = int64(number)
		case int64:
			result[index] = number
		case json.Number:
			parsed, err := number.Int64()
			if err != nil {
				return nil, false
			}
			result[index] = parsed
		default:
			return nil, false
		}
	}
	return result, true
}

func floatValues(value any) ([]float64, bool) {
	reflected := reflect.ValueOf(value)
	if !reflected.IsValid() || reflected.Kind() != reflect.Slice {
		return nil, false
	}
	result := make([]float64, reflected.Len())
	for index := 0; index < reflected.Len(); index++ {
		item := reflected.Index(index)
		switch item.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			result[index] = float64(item.Int())
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32:
			result[index] = float64(item.Uint())
		case reflect.Float32, reflect.Float64:
			result[index] = item.Float()
		default:
			number, ok := item.Interface().(json.Number)
			if !ok {
				return nil, false
			}
			parsed, err := number.Float64()
			if err != nil {
				return nil, false
			}
			result[index] = parsed
		}
	}
	return result, true
}

func booleanValues(value any) ([]bool, bool) {
	reflected := reflect.ValueOf(value)
	if !reflected.IsValid() || reflected.Kind() != reflect.Slice {
		return nil, false
	}
	result := make([]bool, reflected.Len())
	for index := 0; index < reflected.Len(); index++ {
		item, ok := reflected.Index(index).Interface().(bool)
		if !ok {
			return nil, false
		}
		result[index] = item
	}
	return result, true
}

func timestampValues(value any) ([]time.Time, bool) {
	reflected := reflect.ValueOf(value)
	if !reflected.IsValid() || reflected.Kind() != reflect.Slice {
		return nil, false
	}
	result := make([]time.Time, reflected.Len())
	for index := 0; index < reflected.Len(); index++ {
		item, ok := reflected.Index(index).Interface().(time.Time)
		if !ok {
			return nil, false
		}
		result[index] = item
	}
	return result, true
}

func resourceQueryOrder(sorts []resource.Sort, args []any) (string, []any, error) {
	parts := make([]string, 0, len(sorts)+1)
	seenID := false
	add := func(value any) string {
		args = append(args, value)
		return "$" + strconv.Itoa(len(args))
	}
	for _, sort := range sorts {
		if err := sort.Validate(); err != nil {
			return "", nil, err
		}
		column, err := resourceQuerySortExpression(sort, add, "r", resourceQueryColumn)
		if err != nil {
			return "", nil, err
		}
		direction := "ASC"
		if sort.Direction == resource.SortDescending {
			direction = "DESC"
		}
		parts = append(parts, column+" "+direction+" NULLS LAST")
		if sort.Field == resource.FieldID {
			seenID = true
		}
	}
	if !seenID {
		parts = append(parts, "r.id ASC")
	}
	return strings.Join(parts, ", "), args, nil
}

func resourceQuerySortExpression(item resource.Sort, add func(any) string, alias string, resolve resourceQueryColumnResolver) (string, error) {
	column, custom := resolve(item.Field)
	if !custom {
		return column, nil
	}
	valueColumn, err := fieldValueColumn(item.Kind)
	if err != nil {
		return "", err
	}
	key := strings.TrimPrefix(string(item.Field), "resource.field.")
	return "(SELECT value." + valueColumn + " FROM core.resource_field_values value WHERE value.resource_id=" + alias + ".id AND value.site_id=" + alias + ".site_id AND value.field_key=" + add(key) + " AND value.value_kind=" + add(item.Kind) + " AND value.position=0)", nil
}
