package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
)

func TestCustomNegativeFilterUsesNotExists(t *testing.T) {
	args := []any{}
	add := func(value any) string { args = append(args, value); return "$" + string(rune('0'+len(args))) }
	fragment, err := resourceQueryFilterFor(resource.FilterCondition{
		Field: resource.FieldPath("resource.field.tags"), Operator: resource.FilterNotIn,
		Value: []string{"blocked", "hidden"}, Kind: field.StorageString,
	}, add, "item", libraryItemQueryColumn)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(fragment, "NOT EXISTS") || !strings.Contains(fragment, "value.resource_id = item.id") || !strings.Contains(fragment, "= ANY") {
		t.Fatalf("negative filter SQL = %s", fragment)
	}
}

func TestCustomSortAliasFilterReusesJoinedValue(t *testing.T) {
	for _, test := range []struct {
		name      string
		operator  resource.FilterOperator
		value     any
		want      string
		forbidden string
	}{
		{name: "positive", operator: resource.FilterEqual, value: int64(7), want: "rank.value_integer = $1", forbidden: "EXISTS"},
		{name: "negative includes missing", operator: resource.FilterNotIn, value: []int64{7, 8}, want: "rank.resource_id IS NULL", forbidden: "NOT EXISTS"},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := []any{}
			add := func(value any) string {
				args = append(args, value)
				return "$" + string(rune('0'+len(args)))
			}
			fragment, err := libraryItemSortAliasFilter(resource.FilterCondition{
				Field: "resource.field.rank", Operator: test.operator, Value: test.value, Kind: field.StorageInteger,
			}, libraryItemSortAlias{name: "rank", valueColumn: "value_integer"}, add)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(fragment, test.want) || strings.Contains(fragment, test.forbidden) || len(args) != 1 {
				t.Fatalf("alias filter SQL/args = %s / %#v", fragment, args)
			}
		})
	}
}

func TestPublishedAtPartitionFilterOnlyUsesPrunableOperators(t *testing.T) {
	for _, operator := range []resource.FilterOperator{
		resource.FilterEqual, resource.FilterIn, resource.FilterGreaterThan,
		resource.FilterGreaterThanOrEqual, resource.FilterLessThan, resource.FilterLessThanOrEqual,
	} {
		if !libraryItemPartitionFilter(resource.FilterCondition{Field: resource.FieldPublishedAt, Operator: operator}) {
			t.Fatalf("operator %q did not constrain partition_at", operator)
		}
	}
	for _, operator := range []resource.FilterOperator{resource.FilterNotEqual, resource.FilterNotIn} {
		if libraryItemPartitionFilter(resource.FilterCondition{Field: resource.FieldPublishedAt, Operator: operator}) {
			t.Fatalf("operator %q unexpectedly constrained partition_at", operator)
		}
	}
	if libraryItemPartitionFilter(resource.FilterCondition{Field: resource.FieldCreatedAt, Operator: resource.FilterGreaterThan}) {
		t.Fatal("created_at unexpectedly constrained partition_at")
	}
}

func TestLibraryNamespaceStructuralOverlap(t *testing.T) {
	t.Parallel()
	library := func(path, pattern string) resource.Resource {
		return resource.Resource{Path: &path, TypeSettings: map[string]any{"item_url_pattern": pattern}}
	}
	for _, test := range []struct {
		name        string
		left, right resource.Resource
		overlaps    bool
	}{
		{name: "different depth", left: library("/blog", "/{slug}"), right: library("/blog/archive", "/{slug}"), overlaps: false},
		{name: "literal rejects year", left: library("/blog", "/{year}/{slug}"), right: library("/blog/archive", "/{slug}"), overlaps: false},
		{name: "year can overlap descendant", left: library("/blog", "/{year}/{slug}"), right: library("/blog/2026", "/{slug}"), overlaps: true},
		{name: "different literal prefixes", left: library("/blog", "/news-{slug}"), right: library("/blog", "/docs-{slug}"), overlaps: false},
		{name: "id and slug overlap", left: library("/blog", "/{id}"), right: library("/blog", "/{slug}"), overlaps: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if actual := libraryNamespacesMayOverlap(test.left, test.right); actual != test.overlaps {
				t.Fatalf("overlap = %v, want %v", actual, test.overlaps)
			}
		})
	}
}

func TestLibraryItemTransactionRetryIsBoundedAndRespectsCancellation(t *testing.T) {
	attempts := 0
	_, err := retryLibraryItemTransaction(context.Background(), func() (resource.LibraryItem, error) {
		attempts++
		return resource.LibraryItem{}, &pgconn.PgError{Code: pgerrcode.SerializationFailure}
	})
	if err == nil || attempts != libraryItemTransactionAttempts {
		t.Fatalf("retry result = %v after %d attempts", err, attempts)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	attempts = 0
	_, err = retryLibraryItemTransaction(ctx, func() (resource.LibraryItem, error) {
		attempts++
		return resource.LibraryItem{}, nil
	})
	if !errors.Is(err, context.Canceled) || attempts != 0 {
		t.Fatalf("canceled retry result = %v after %d attempts", err, attempts)
	}
}

func TestLibraryKeysetUsesMissingLastAndStableID(t *testing.T) {
	args := []any{}
	add := func(value any) string { args = append(args, value); return "$x" }
	predicate := libraryItemKeysetPredicate([]libraryItemSortExpression{{
		sort: resource.Sort{Field: resource.FieldPublishedAt, Direction: resource.SortDescending},
		sql:  "item.published_at",
	}}, resource.LibraryCursor{Values: []any{int64(7)}, ID: 12}, resource.SortAscending, false, add)
	if !strings.Contains(predicate, "item.published_at<$x OR item.published_at IS NULL") || !strings.Contains(predicate, "item.id>$x") {
		t.Fatalf("keyset SQL = %s", predicate)
	}
	nullPredicate := libraryItemKeysetPredicate([]libraryItemSortExpression{{
		sort: resource.Sort{Field: resource.FieldPublishedAt, Direction: resource.SortAscending},
		sql:  "item.published_at",
	}}, resource.LibraryCursor{Values: []any{nil}, ID: 12}, resource.SortAscending, false, add)
	if strings.Contains(nullPredicate, "published_at>") || !strings.Contains(nullPredicate, "published_at IS NULL") {
		t.Fatalf("null keyset SQL = %s", nullPredicate)
	}
	presentCustom := libraryItemKeysetPredicate([]libraryItemSortExpression{{
		sort: resource.Sort{Field: "resource.field.salary", Direction: resource.SortAscending},
		sql:  "sort_value_0.value_integer",
	}}, resource.LibraryCursor{Values: []any{int64(20)}, ID: 12}, resource.SortAscending, true, add)
	if strings.Contains(presentCustom, "value_integer IS NULL") || !strings.Contains(presentCustom, "value_integer>$x") {
		t.Fatalf("present custom keyset SQL = %s", presentCustom)
	}
}
