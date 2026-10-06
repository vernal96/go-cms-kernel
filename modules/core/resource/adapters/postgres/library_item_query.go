package postgres

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
)

func (r *Repository) QueryLibraryItems(ctx context.Context, query resource.LibraryItemQuery) (resource.LibraryItemPage, error) {
	if err := query.Validate(); err != nil {
		return resource.LibraryItemPage{}, err
	}
	sorts, _ := resource.LibraryItemSorts(query)
	primaryCustom := len(sorts) > 0 && resource.IsCustomFieldPath(sorts[0].Field)
	items := make([]resource.LibraryItem, 0, query.Limit+1)
	if primaryCustom {
		cursor, err := resource.DecodeLibraryCursor(query)
		if err != nil {
			return resource.LibraryItemPage{}, err
		}
		if query.Cursor == "" || len(cursor.Values) == 0 || cursor.Values[0] != nil {
			present := true
			loaded, err := r.queryLibraryItemBranch(ctx, query, &present, query.Limit+1)
			if err != nil {
				return resource.LibraryItemPage{}, err
			}
			items = append(items, loaded...)
		}
		if len(items) <= query.Limit {
			missing := false
			loaded, err := r.queryLibraryItemBranch(ctx, query, &missing, query.Limit+1-len(items))
			if err != nil {
				return resource.LibraryItemPage{}, err
			}
			items = append(items, loaded...)
		}
	} else {
		loaded, err := r.queryLibraryItemBranch(ctx, query, nil, query.Limit+1)
		if err != nil {
			return resource.LibraryItemPage{}, err
		}
		items = loaded
	}

	page := resource.LibraryItemPage{}
	hasMore := len(items) > query.Limit
	if hasMore {
		items = items[:query.Limit]
	}
	if err := r.loadLibraryItemsFields(ctx, r.connector.Pool(), items); err != nil {
		return resource.LibraryItemPage{}, err
	}
	if hasMore {
		var err error
		page.NextCursor, err = resource.EncodeLibraryCursor(query, items[len(items)-1])
		if err != nil {
			return resource.LibraryItemPage{}, err
		}
	}
	page.Items = items
	return page, nil
}

func (r *Repository) queryLibraryItemBranch(ctx context.Context, query resource.LibraryItemQuery, customValues *bool, limitValue int) ([]resource.LibraryItem, error) {
	args := make([]any, 0, 16)
	add := func(value any) string {
		args = append(args, value)
		return "$" + strconv.Itoa(len(args))
	}
	sorts, idDirection := resource.LibraryItemSorts(query)
	primaryCustom := customValues != nil && len(sorts) > 0 && resource.IsCustomFieldPath(sorts[0].Field)

	from := "core.library_items item JOIN core.resources library ON library.id=item.library_id AND library.site_id=item.site_id"
	joins := make([]string, 0, len(sorts))
	expressions := make([]libraryItemSortExpression, 0, len(sorts))
	sortAliases := make(map[resource.FieldPath]libraryItemSortAlias, len(sorts))
	order := make([]string, 0, len(sorts)+1)
	for index, item := range sorts {
		column, custom := libraryItemQueryColumn(item.Field)
		if custom {
			valueColumn, err := fieldValueColumn(item.Kind)
			if err != nil {
				return nil, err
			}
			kindLiteral, err := fieldStorageKindSQL(item.Kind)
			if err != nil {
				return nil, err
			}
			alias := "sort_value_" + strconv.Itoa(index)
			key := strings.TrimPrefix(string(item.Field), "resource.field.")
			if primaryCustom && index == 0 && *customValues {
				from = "core.resource_field_values " + alias +
					" JOIN core.library_item_routes route ON route.resource_id=" + alias + ".resource_id AND route.site_id=" + alias + ".site_id AND route.library_id=" + alias + ".library_id" +
					" JOIN core.library_items item ON item.id=route.resource_id AND item.site_id=route.site_id AND item.library_id=route.library_id" +
					" JOIN core.resources library ON library.id=item.library_id AND library.site_id=item.site_id"
			} else {
				fieldKey := add(key)
				joins = append(joins, "LEFT JOIN core.resource_field_values "+alias+
					" ON "+alias+".resource_id=item.id AND "+alias+".site_id=item.site_id"+
					" AND "+alias+".library_id=item.library_id"+
					" AND "+alias+".field_key="+fieldKey+" AND "+alias+".value_kind="+kindLiteral+
					" AND "+alias+".position=0 AND NOT "+alias+".is_multi")
			}
			column = alias + "." + valueColumn
			if _, exists := sortAliases[item.Field]; !exists {
				sortAliases[item.Field] = libraryItemSortAlias{name: alias, valueColumn: valueColumn}
			}
		}
		expressions = append(expressions, libraryItemSortExpression{sort: item, sql: column})
		order = append(order, column+" "+sortDirectionSQL(item.Direction)+" NULLS LAST")
	}
	order = append(order, "item.id "+sortDirectionSQL(idDirection))

	where := []string{
		"item.site_id=" + add(query.SiteID),
		"item.library_id=" + add(query.LibraryID),
		"library.type='library'",
		"library.deleted_at IS NULL",
	}
	if search := strings.TrimSpace(query.Search); search != "" {
		if id, parseErr := strconv.ParseInt(search, 10, 64); parseErr == nil && id > 0 {
			where = append(where, "item.id="+add(resource.ID(id)))
		} else {
			pattern := add("%" + strings.ToLower(search) + "%")
			where = append(where, "(lower(item.title) LIKE "+pattern+" OR lower(item.slug) LIKE "+pattern+")")
		}
	}
	if query.PublicOnly {
		where = append(where, "item.deleted_at IS NULL", "item.is_public", "(item.published_at IS NULL OR item.published_at<=now())", "(item.unpublished_at IS NULL OR now()<item.unpublished_at)")
	}
	if query.Deleted != nil {
		if *query.Deleted {
			where = append(where, "item.deleted_at IS NOT NULL")
		} else {
			where = append(where, "item.deleted_at IS NULL")
		}
	}
	for _, condition := range query.Filters {
		var fragment string
		var err error
		if alias, exists := sortAliases[condition.Field]; exists {
			fragment, err = libraryItemSortAliasFilter(condition, alias, add)
		} else {
			fragment, err = resourceQueryFilterFor(condition, add, "item", libraryItemQueryColumn)
		}
		if err != nil {
			return nil, err
		}
		where = append(where, fragment)
		if libraryItemPartitionFilter(condition) {
			partitionFragment, err := resourceQueryFilterFor(condition, add, "item", libraryItemPartitionQueryColumn)
			if err != nil {
				return nil, err
			}
			where = append(where, partitionFragment)
		}
	}
	if primaryCustom {
		alias := "sort_value_0"
		if *customValues {
			kindLiteral, err := fieldStorageKindSQL(sorts[0].Kind)
			if err != nil {
				return nil, err
			}
			where = append(where,
				alias+".site_id=item.site_id",
				alias+".library_id="+add(query.LibraryID),
				alias+".field_key="+add(strings.TrimPrefix(string(sorts[0].Field), "resource.field.")),
				alias+".value_kind="+kindLiteral,
				alias+".position=0", "NOT "+alias+".is_multi",
				"route.library_id=item.library_id",
			)
		} else {
			where = append(where, alias+".resource_id IS NULL")
		}
	}
	if query.Cursor != "" {
		cursor, err := resource.DecodeLibraryCursor(query)
		if err != nil {
			return nil, err
		}
		where = append(where, libraryItemKeysetPredicate(
			expressions,
			cursor,
			idDirection,
			primaryCustom && *customValues,
			add,
		))
	}
	limit := add(limitValue)
	ordering := strings.Join(order, ", ")
	// Limit before joining versions: joining the full missing-field tail can
	// turn a bounded version lookup into work proportional to the collection.
	// Carry sort keys through the page so its outer order is explicit, while
	// retaining the inner query's index ordering or top-N sort.
	pageColumns := libraryItemColumns
	pageOrder := make([]string, 0, len(expressions)+1)
	for index, expression := range expressions {
		alias := "page_sort_" + strconv.Itoa(index)
		pageColumns += ", " + expression.sql + " AS " + alias
		pageOrder = append(pageOrder, "page."+alias+" "+sortDirectionSQL(expression.sort.Direction)+" NULLS LAST")
	}
	pageOrder = append(pageOrder, "page.id "+sortDirectionSQL(idDirection))
	page := `SELECT ` + pageColumns + ` FROM ` + from + ` ` + strings.Join(joins, " ") + ` WHERE ` + strings.Join(where, " AND ") + ` ORDER BY ` + ordering + ` LIMIT ` + limit
	rows, err := r.connector.Pool().Query(ctx, `SELECT `+strings.ReplaceAll(libraryItemColumns, "item.", "page.")+`, entity.version FROM (`+page+`) page LEFT JOIN core.resource_entities entity ON entity.id=page.id ORDER BY `+strings.Join(pageOrder, ", ")+`;`, args...)
	if err != nil {
		return nil, fmt.Errorf("query library items: %w", err)
	}
	defer rows.Close()
	items := make([]resource.LibraryItem, 0, limitValue)
	for rows.Next() {
		item, err := scanLibraryItemWithVersion(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (r *Repository) LibraryItemTemplateCodes(ctx context.Context, siteID site.ID, libraryID resource.ID) ([]template.Code, error) {
	rows, err := r.connector.Pool().Query(ctx, `SELECT template FROM core.library_item_template_usage WHERE site_id=$1 AND library_id=$2 ORDER BY template;`, siteID, libraryID)
	if err != nil {
		return nil, translateError(err)
	}
	defer rows.Close()
	result := make([]template.Code, 0, 4)
	for rows.Next() {
		var code template.Code
		if err := rows.Scan(&code); err != nil {
			return nil, err
		}
		result = append(result, code)
	}
	return result, translateError(rows.Err())
}

func (r *Repository) LibraryItemWidgetCodes(ctx context.Context, siteID site.ID, libraryID resource.ID) ([]widget.Code, error) {
	rows, err := r.connector.Pool().Query(ctx, `
SELECT DISTINCT binding.widget_code
FROM core.library_item_routes route
JOIN core.resource_widgets binding ON binding.resource_id=route.resource_id
WHERE route.site_id=$1 AND route.library_id=$2
ORDER BY binding.widget_code;`, siteID, libraryID)
	if err != nil {
		return nil, translateError(err)
	}
	defer rows.Close()
	result := make([]widget.Code, 0, 4)
	for rows.Next() {
		var code widget.Code
		if err := rows.Scan(&code); err != nil {
			return nil, err
		}
		result = append(result, code)
	}
	return result, translateError(rows.Err())
}

func ensureLibraryItemTemplateUsage(ctx context.Context, tx pgx.Tx, siteID site.ID, libraryID resource.ID, code *template.Code) error {
	if code == nil {
		return nil
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO core.library_item_template_usage (site_id, library_id, template)
VALUES ($1, $2, $3)
ON CONFLICT DO NOTHING;`, siteID, libraryID, *code); err != nil {
		return translateError(err)
	}
	return nil
}

func pruneLibraryItemTemplateUsage(ctx context.Context, tx pgx.Tx, siteID site.ID, libraryID resource.ID, code *template.Code) error {
	if code == nil {
		return nil
	}
	// Serialize cleanup for one usage tuple. Without this lock, two items can
	// concurrently leave the same template, each observe the other's
	// uncommitted old row, and both incorrectly retain stale metadata.
	if _, err := tx.Exec(ctx, `
SELECT 1
FROM core.library_item_template_usage
WHERE site_id=$1 AND library_id=$2 AND template=$3
FOR UPDATE;`, siteID, libraryID, *code); err != nil {
		return translateError(err)
	}
	if _, err := tx.Exec(ctx, `
DELETE FROM core.library_item_template_usage usage
WHERE usage.site_id=$1 AND usage.library_id=$2 AND usage.template=$3
  AND NOT EXISTS (
      SELECT 1
      FROM core.library_items item
      WHERE item.site_id=$1 AND item.library_id=$2 AND item.template=$3
  );`, siteID, libraryID, *code); err != nil {
		return translateError(err)
	}
	return nil
}

func sameTemplateCode(left, right *template.Code) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

type libraryItemSortExpression struct {
	sort resource.Sort
	sql  string
}

type libraryItemSortAlias struct {
	name        string
	valueColumn string
}

func libraryItemSortAliasFilter(condition resource.FilterCondition, alias libraryItemSortAlias, add func(any) string) (string, error) {
	if err := condition.Validate(); err != nil {
		return "", err
	}
	kind, value, err := filterStorageValue(condition.Kind, condition.Value)
	if err != nil {
		return "", err
	}
	column, err := fieldValueColumn(kind)
	if err != nil {
		return "", err
	}
	if column != alias.valueColumn {
		return "", fmt.Errorf("sort alias for field %q has incompatible storage kind %q", condition.Field, kind)
	}

	operator := filterOperatorSQL(condition.Operator)
	negative := condition.Operator == resource.FilterNotEqual || condition.Operator == resource.FilterNotIn
	if condition.Operator == resource.FilterNotEqual {
		operator = filterOperatorSQL(resource.FilterEqual)
	} else if condition.Operator == resource.FilterNotIn {
		operator = filterOperatorSQL(resource.FilterIn)
	}
	placeholder := add(value)
	comparison := alias.name + "." + alias.valueColumn + " " + operator + " " + placeholder
	if condition.Operator == resource.FilterIn || condition.Operator == resource.FilterNotIn {
		comparison = alias.name + "." + alias.valueColumn + " " + operator + " (" + placeholder + ")"
	}
	if negative {
		return "(" + alias.name + ".resource_id IS NULL OR NOT (" + comparison + "))", nil
	}
	return comparison, nil
}

func libraryItemQueryColumn(path resource.FieldPath) (string, bool) {
	switch path {
	case resource.FieldID:
		return "item.id", false
	case resource.FieldTitle:
		return "item.title", false
	case resource.FieldSlug:
		return "item.slug", false
	case resource.FieldAnnotation:
		return "item.annotation", false
	case resource.FieldTemplate:
		return "item.template", false
	case resource.FieldIsPublic:
		return "item.is_public", false
	case resource.FieldIsSearchable:
		return "item.is_searchable", false
	case resource.FieldPublishedAt:
		return "item.published_at", false
	case resource.FieldCreatedAt:
		return "item.created_at", false
	case resource.FieldUpdatedAt:
		return "item.updated_at", false
	default:
		return "", true
	}
}

func libraryItemPartitionQueryColumn(path resource.FieldPath) (string, bool) {
	if path == resource.FieldPublishedAt {
		return "item.partition_at", false
	}
	return libraryItemQueryColumn(path)
}

func libraryItemPartitionFilter(condition resource.FilterCondition) bool {
	if condition.Field != resource.FieldPublishedAt {
		return false
	}
	switch condition.Operator {
	case resource.FilterEqual, resource.FilterIn, resource.FilterGreaterThan,
		resource.FilterGreaterThanOrEqual, resource.FilterLessThan, resource.FilterLessThanOrEqual:
		return true
	default:
		return false
	}
}

func libraryItemKeysetPredicate(expressions []libraryItemSortExpression, cursor resource.LibraryCursor, idDirection resource.SortDirection, primaryCustomPresent bool, add func(any) string) string {
	branches := make([]string, 0, len(expressions)+1)
	prefix := make([]string, 0, len(expressions))
	for index, expression := range expressions {
		value := cursor.Values[index]
		if value != nil {
			operator := ">"
			if expression.sort.Direction == resource.SortDescending {
				operator = "<"
			}
			comparison := expression.sql + operator + add(value)
			if !(primaryCustomPresent && index == 0) {
				comparison = "(" + comparison + " OR " + expression.sql + " IS NULL)"
			}
			branches = append(branches, "("+strings.Join(append(append([]string(nil), prefix...), comparison), " AND ")+")")
		}
		if value == nil {
			prefix = append(prefix, expression.sql+" IS NULL")
		} else {
			prefix = append(prefix, expression.sql+" IS NOT DISTINCT FROM "+add(value))
		}
	}
	idOperator := ">"
	if idDirection == resource.SortDescending {
		idOperator = "<"
	}
	idComparison := "item.id" + idOperator + add(cursor.ID)
	branches = append(branches, "("+strings.Join(append(prefix, idComparison), " AND ")+")")
	return "(" + strings.Join(branches, " OR ") + ")"
}

func sortDirectionSQL(direction resource.SortDirection) string {
	if direction == resource.SortDescending {
		return "DESC"
	}
	return "ASC"
}

func fieldStorageKindSQL(kind field.StorageKind) (string, error) {
	switch kind {
	case field.StorageString, field.StorageInteger, field.StorageFloat, field.StorageBoolean,
		field.StorageTimestamp, field.StorageReference, field.StorageJSON:
		return "'" + string(kind) + "'", nil
	default:
		return "", fmt.Errorf("resource field storage kind %q is invalid", kind)
	}
}
