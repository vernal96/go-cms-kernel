package widgets

import (
	"context"
	"sync"
	"testing"

	"github.com/vernal96/go-cms-kernel/cache"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
)

type listRepository struct {
	queries []resource.Query
	page    resource.Page
}

func (r *listRepository) Query(_ context.Context, query resource.Query) (resource.Page, error) {
	r.queries = append(r.queries, query)
	return r.page, nil
}

type widgetCache struct {
	mu      sync.Mutex
	entries map[string][]byte
	tags    map[cache.Tag]map[string]bool
}

func (s *widgetCache) Code() cache.Code         { return "test" }
func (*widgetCache) Ping(context.Context) error { return nil }
func (s *widgetCache) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.entries[key]
	if !ok {
		return nil, cache.ErrMiss
	}
	return append([]byte(nil), value...), nil
}
func (s *widgetCache) Set(_ context.Context, key string, value []byte, options cache.SetOptions) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[key] = append([]byte(nil), value...)
	for _, tag := range options.Tags {
		if s.tags[tag] == nil {
			s.tags[tag] = map[string]bool{}
		}
		s.tags[tag][key] = true
	}
	return nil
}
func (*widgetCache) Exists(context.Context, string) (bool, error) { return false, nil }
func (s *widgetCache) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, key)
	return nil
}
func (s *widgetCache) InvalidateTag(_ context.Context, tag cache.Tag) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key := range s.tags[tag] {
		delete(s.entries, key)
	}
	return nil
}
func (*widgetCache) Close() error { return nil }

func TestResourceListExplicitIDsStillApplyExclusionsAndCache(t *testing.T) {
	repository := &listRepository{page: resource.Page{Items: []resource.Resource{{ID: 10, SiteID: 7, Title: "One", Type: resourcetype.Page, Slug: "one", Fields: map[string]any{}}, {ID: 20, SiteID: 7, Title: "Two", Type: resourcetype.Page, Slug: "two", Fields: map[string]any{}}}}}
	service, err := resource.NewQueryService(repository)
	if err != nil {
		t.Fatal(err)
	}
	store := &widgetCache{entries: map[string][]byte{}, tags: map[cache.Tag]map[string]bool{}}
	current := NewResourceList(service, store, []resourcetype.Code{resourcetype.Page}, nil)
	instance, err := current.New(map[string]any{"parent_mode": "root", "resources": []any{float64(20), float64(10)}, "exclude": []any{float64(20)}, "limit": int64(20), "pagination_enabled": false, "exclude_current": false, "filters": []any{}, "sorting": []any{}})
	if err != nil {
		t.Fatal(err)
	}
	input := widget.RenderInput{Site: widget.SiteSnapshot{ID: 7}, Resource: widget.ResourceSnapshot{ID: 3}}
	first, err := instance.Render(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	_, err = instance.Render(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(repository.queries) != 1 {
		t.Fatalf("queries = %d", len(repository.queries))
	}
	query := repository.queries[0]
	if query.FilterByParent || len(query.IDs) != 2 || len(query.ExcludeIDs) != 1 || query.ExcludeIDs[0] != 20 || !query.PublicOnly {
		t.Fatalf("query = %#v", query)
	}
	items := first["items"].([]map[string]any)
	if len(items) != 2 || items[0]["id"] != resource.ID(10) {
		t.Fatalf("items = %#v", items)
	}
	if err := store.InvalidateTag(context.Background(), cache.Tag("site:7:resources")); err != nil {
		t.Fatal(err)
	}
	_, err = instance.Render(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(repository.queries) != 2 {
		t.Fatalf("invalidation did not reload: %d", len(repository.queries))
	}
}

func TestResourceListValidatesSelectedParentAndPagination(t *testing.T) {
	_, err := parseResourceListConfig(map[string]any{"parent_mode": "selected", "limit": int64(10), "pagination_enabled": true, "per_page": int64(20)})
	if err == nil {
		t.Fatalf("error = %v", err)
	}
	_, err = resource.NewQueryService(nil)
	if err == nil {
		t.Fatal("missing query service error")
	}
}
