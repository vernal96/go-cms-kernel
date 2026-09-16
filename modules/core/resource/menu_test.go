package resource

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/site"
)

type menuTestRepository []MenuRecord

func (r menuTestRepository) MenuRecords(context.Context, site.ID, MenuInput) ([]MenuRecord, error) {
	return r, nil
}

func TestMenuTreeLinksAndPublication(t *testing.T) {
	service, _, _ := newTestService(t)
	runtime, _ := service.sites.RuntimeByID(1)
	now := time.Now()
	record := func(id ID, parent *ID, path string) MenuRecord {
		return MenuRecord{ID: id, ParentID: parent, Type: "page", Path: &path, Title: path, IsPublic: true, InMenu: true, InTree: true}
	}
	id := func(v ID) *ID { return &v }
	root := record(1, nil, "/")
	projects := record(2, nil, "/projects")
	child := record(3, id(2), "/projects/a")
	footer := record(4, nil, "/footer")
	footer.InMenu = false
	link := record(5, id(4), "/footer/a")
	link.Type = "resource_link"
	link.TargetResourceID = id(3)
	link.MenuTitle = "Footer label"
	hidden := record(6, id(2), "/projects/hidden")
	hidden.InMenu = false
	grandchild := record(7, id(6), "/projects/hidden/a")
	future := record(8, nil, "/future")
	future.PublishedAt = timePointer(now.Add(time.Minute))
	external := record(9, nil, "/external")
	external.Type = "link"
	external.ExternalURL = cloneStringPointer("https://example.org")
	cycle := record(10, nil, "/cycle")
	cycle.Type = "resource_link"
	cycle.TargetResourceID = id(10)
	private := record(11, nil, "/private")
	private.IsPublic = false
	privateLink := record(12, nil, "/private-link")
	privateLink.Type = "resource_link"
	privateLink.TargetResourceID = id(11)
	chain := record(13, nil, "/chain")
	chain.Type = "resource_link"
	chain.TargetResourceID = id(5)
	repo := menuTestRepository{root, projects, child, footer, link, hidden, grandchild, future, external, cycle, private, privateLink, chain}
	result, next, err := BuildMenu(context.Background(), repo, runtime, MenuInput{}, now)
	if err != nil {
		t.Fatal(err)
	}
	var ids []ID
	for _, item := range result.Items {
		ids = append(ids, item.ID)
	}
	if !reflect.DeepEqual(ids, []ID{1, 2, 9, 13}) || len(result.Items[1].Children) != 1 || result.Items[3].URL != *child.Path {
		t.Fatalf("menu=%#v", result)
	}
	if next == nil || !next.Equal(*future.PublishedAt) {
		t.Fatalf("next=%v", next)
	}
	result, _, err = BuildMenu(context.Background(), repo, runtime, MenuInput{ParentID: id(4)}, now)
	if err != nil || len(result.Items) != 1 || result.Items[0].URL != *child.Path || result.Items[0].Title != "Footer label" {
		t.Fatalf("footer=%#v err=%v", result, err)
	}
	result, _, err = BuildMenu(context.Background(), repo, runtime, MenuInput{Depth: 1}, now)
	if err != nil || len(result.Items[1].Children) != 0 {
		t.Fatalf("depth=%#v err=%v", result, err)
	}
	for _, parent := range []ID{11, 99} {
		_, _, err = BuildMenu(context.Background(), repo, runtime, MenuInput{ParentID: id(parent)}, now)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("parent=%d err=%v", parent, err)
		}
	}
	result, _, err = BuildMenu(context.Background(), repo, runtime, MenuInput{}, now.Add(time.Minute))
	if err != nil || len(result.Items) != 5 {
		t.Fatalf("publication=%#v err=%v", result, err)
	}
}

func cloneStringPointer(value string) *string { return &value }
