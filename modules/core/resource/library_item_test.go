package resource

import (
	"strings"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
)

func TestEffectiveLibraryItemURLUsesCurrentLibraryPath(t *testing.T) {
	firstPath := "/news"
	library := Resource{
		Path: &firstPath,
		TypeSettings: map[string]any{
			"item_url_pattern": "/{year}/{month}/{slug}",
		},
	}
	publishedAt := time.Date(2026, time.August, 22, 12, 0, 0, 0, time.UTC)
	item := LibraryItem{ID: 42, Slug: "release", PublishedAt: &publishedAt}

	url, err := EffectiveLibraryItemURL(library, item)
	if err != nil || url != "/news/2026/08/release" {
		t.Fatalf("first effective URL = %q, %v", url, err)
	}
	movedPath := "/company/updates"
	library.Path = &movedPath
	url, err = EffectiveLibraryItemURL(library, item)
	if err != nil || url != "/company/updates/2026/08/release" {
		t.Fatalf("moved effective URL = %q, %v", url, err)
	}
	key, matched := MatchLibraryItemPattern(
		library.TypeSettings["item_url_pattern"].(string),
		"/2026/08/release",
	)
	if !matched || key.Slug != item.Slug || key.ID != 0 {
		t.Fatalf("matched key = %#v, matched = %t", key, matched)
	}

	library.TypeSettings["item_url_pattern"] = "/{year}/{month}/{day}/{id}"
	url, err = EffectiveLibraryItemURL(library, item)
	if err != nil || url != "/company/updates/2026/08/22/42" {
		t.Fatalf("dated ID URL = %q, %v", url, err)
	}
	key, matched = MatchLibraryItemPattern("/{year}/{month}/{day}/{id}", "/2026/08/22/42")
	if !matched || key.ID != item.ID || key.Slug != "" {
		t.Fatalf("matched dated ID key = %#v, matched = %t", key, matched)
	}
	createdAt := time.Date(2024, time.January, 3, 0, 0, 0, 0, time.UTC)
	item.PublishedAt = nil
	item.CreatedAt = createdAt
	url, err = EffectiveLibraryItemURL(library, item)
	if err != nil || url != "/company/updates/2024/01/03/42" {
		t.Fatalf("created-at fallback URL = %q, %v", url, err)
	}

	library.TypeSettings["item_url_pattern"] = resourcetype.DefaultItemURLPattern
	url, err = EffectiveLibraryItemURL(library, item)
	if err != nil || url != "/company/updates/release" {
		t.Fatalf("default-pattern URL = %q, %v", url, err)
	}
}

func TestLibraryItemQueryCursorBindsFullQueryAndSortTuple(t *testing.T) {
	query := LibraryItemQuery{
		SiteID: site.ID(3), LibraryID: ID(8), Limit: 25, Search: "article",
		Filters: []FilterCondition{{Field: FieldIsPublic, Operator: FilterEqual, Value: true}},
		Sort: []Sort{
			{Field: FieldPublishedAt, Direction: SortDescending},
			{Field: FieldPath("resource.field.rank"), Direction: SortAscending, Kind: field.StorageInteger},
		},
	}
	published := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	item := LibraryItem{ID: 91, PublishedAt: &published, Fields: map[string]any{"rank": int64(4)}}
	cursor, err := EncodeLibraryCursor(query, item)
	if err != nil {
		t.Fatal(err)
	}
	query.Cursor = cursor
	decoded, err := DecodeLibraryCursor(query)
	if err != nil || decoded.ID != item.ID || len(decoded.Values) != 2 || decoded.Values[1] != int64(4) {
		t.Fatalf("decoded cursor = %#v, %v", decoded, err)
	}
	query.Search = "different"
	if _, err := DecodeLibraryCursor(query); err == nil {
		t.Fatal("cursor from another query was accepted")
	}
	query.Search = "article"
	query.Filters[0].Value = false
	if _, err := DecodeLibraryCursor(query); err == nil {
		t.Fatal("cursor from another filter was accepted")
	}
	query.Filters[0].Value = true
	query.Sort[0].Direction = SortAscending
	if _, err := DecodeLibraryCursor(query); err == nil {
		t.Fatal("cursor from another sort was accepted")
	}
}

func TestTypedQueryValidationRequiresStorageSemantics(t *testing.T) {
	custom := FieldPath("resource.field.value")
	for _, condition := range []FilterCondition{
		{Field: custom, Operator: FilterEqual, Value: "x"},
		{Field: custom, Operator: FilterGreaterThan, Value: true, Kind: field.StorageBoolean},
		{Field: custom, Operator: FilterIn, Value: []any{}, Kind: field.StorageString},
	} {
		if err := condition.Validate(); err == nil {
			t.Fatalf("condition %#v was accepted", condition)
		}
	}
	valid := []FilterCondition{
		{Field: custom, Operator: FilterNotIn, Value: []string{"x", "y"}, Kind: field.StorageString},
		{Field: custom, Operator: FilterEqual, Value: map[string]any{"opaque": []any{1.0}}, Kind: field.StorageJSON},
		{Field: custom, Operator: FilterIn, Value: []int64{1, 2}, Kind: field.StorageReference},
	}
	for _, condition := range valid {
		if err := condition.Validate(); err != nil {
			t.Fatalf("condition %#v: %v", condition, err)
		}
	}
	if err := (Sort{Field: custom, Direction: SortAscending, Kind: field.StorageBoolean}).Validate(); err == nil || !strings.Contains(err.Error(), "not sortable") {
		t.Fatalf("boolean custom sort error = %v", err)
	}
}
