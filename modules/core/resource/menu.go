package resource

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
)

// MenuRepository returns only navigation metadata, including referenced targets.
type MenuRepository interface {
	MenuRecords(context.Context, site.ID, MenuInput) ([]MenuRecord, error)
}

type MenuInput struct {
	ParentID *ID
	Depth    int // Zero means unlimited.
}

type MenuRecord struct {
	ID                                    ID
	ParentID                              *ID
	Type                                  resourcetype.Code
	Title, MenuTitle                      string
	Path, ExternalURL                     *string
	TargetResourceID                      *ID
	Sort                                  int
	IsPublic, InMenu, InTree              bool
	PublishedAt, UnpublishedAt, DeletedAt *time.Time
}

type MenuItem struct {
	ID       ID         `json:"id"`
	Title    string     `json:"title"`
	URL      string     `json:"url"`
	Children []MenuItem `json:"children"`
}

type MenuResult struct {
	Items []MenuItem `json:"items"`
}

// BuildMenu deliberately does not inspect content, widgets or LibraryItems.
// Authorization belongs to the caller and must also run on cache hits.
func BuildMenu(ctx context.Context, repository MenuRepository, runtime *site.Runtime, input MenuInput, now time.Time) (MenuResult, *time.Time, error) {
	if input.Depth < 0 || (input.ParentID != nil && *input.ParentID <= 0) {
		return MenuResult{}, nil, ErrInvalid
	}
	records, err := repository.MenuRecords(ctx, runtime.Site().ID, input)
	if err != nil {
		return MenuResult{}, nil, err
	}
	byID := make(map[ID]MenuRecord, len(records))
	children := make(map[ID][]MenuRecord)
	var next *time.Time
	for _, item := range records {
		byID[item.ID] = item
		for _, boundary := range []*time.Time{item.PublishedAt, item.UnpublishedAt} {
			if boundary != nil && boundary.After(now) && (next == nil || boundary.Before(*next)) {
				next = cloneTime(boundary)
			}
		}
		if item.InTree {
			parent := ID(0)
			if item.ParentID != nil {
				parent = *item.ParentID
			}
			children[parent] = append(children[parent], item)
		}
	}
	visible := func(item MenuRecord) bool {
		return item.DeletedAt == nil && item.IsPublic && (item.PublishedAt == nil || !now.Before(*item.PublishedAt)) && (item.UnpublishedAt == nil || now.Before(*item.UnpublishedAt))
	}
	root := ID(0)
	if input.ParentID != nil {
		root = *input.ParentID
		parent, ok := byID[root]
		if !ok || !visible(parent) {
			return MenuResult{}, nil, ErrNotFound
		}
	}
	resolveURL := func(item MenuRecord) string {
		seen := map[ID]bool{}
		for {
			if seen[item.ID] || !visible(item) {
				return ""
			}
			seen[item.ID] = true
			t, ok := runtime.Profile().Registry().ResourceType(item.Type)
			if !ok || t.PathMode() != resourcetype.PathRoute || item.Path == nil {
				return ""
			}
			switch item.Type {
			case resourcetype.Link:
				if item.ExternalURL == nil {
					return ""
				}
				return *item.ExternalURL
			case resourcetype.ResourceLink:
				if item.TargetResourceID == nil {
					return ""
				}
				item, ok = byID[*item.TargetResourceID]
				if !ok {
					return ""
				}
			default:
				return *item.Path
			}
		}
	}
	for parent := range children {
		sort.Slice(children[parent], func(i, j int) bool {
			a, b := children[parent][i], children[parent][j]
			if a.Sort != b.Sort {
				return a.Sort < b.Sort
			}
			return a.ID < b.ID
		})
	}
	active := map[ID]bool{}
	var build func(ID, int) ([]MenuItem, error)
	build = func(parent ID, level int) ([]MenuItem, error) {
		result := []MenuItem{}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if input.Depth > 0 && level > input.Depth {
			return result, nil
		}
		if active[parent] {
			return nil, fmt.Errorf("%w: menu tree cycle", ErrInvalidTree)
		}
		active[parent] = true
		defer delete(active, parent)
		for _, item := range children[parent] {
			if !item.InMenu || !visible(item) {
				continue
			}
			url := resolveURL(item)
			if url == "" {
				continue
			}
			nested, err := build(item.ID, level+1)
			if err != nil {
				return nil, err
			}
			title := item.MenuTitle
			if strings.TrimSpace(title) == "" {
				title = item.Title
			}
			result = append(result, MenuItem{ID: item.ID, Title: title, URL: url, Children: nested})
		}
		return result, nil
	}
	items, err := build(root, 1)
	return MenuResult{Items: items}, next, err
}
