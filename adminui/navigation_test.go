package adminui_test

import (
	"strings"
	"testing"

	"github.com/vernal96/go-cms-kernel/adminui"
)

func TestCompileValidatesAndSortsNestedNavigation(t *testing.T) {
	t.Parallel()

	items, err := adminui.Compile([]adminui.Source{
		{
			Code: "feature",
			Items: []adminui.NavigationItem{
				{Code: "zeta", Label: " Zeta ", Route: "feature.zeta", Order: 200, Scope: adminui.NavigationSite},
				{
					Code: "group", Label: "Group", Order: 100, Scope: adminui.NavigationSite,
					Children: []adminui.NavigationItem{
						{Code: "group.second", Label: "Second", Route: "feature.second", Order: 100, Scope: adminui.NavigationSite},
						{Code: "group.first", Label: "First", Route: "feature.first", Order: 100, Scope: adminui.NavigationSite},
					},
				},
				{Code: "alpha", Label: "Alpha", Route: "feature.alpha", Order: 200, Scope: adminui.NavigationSite},
			},
		},
	}, adminui.NavigationSite, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(items) != 3 || items[0].Code != "group" || items[1].Code != "alpha" || items[2].Code != "zeta" {
		t.Fatalf("items = %#v", items)
	}
	if len(items[0].Children) != 2 || items[0].Children[0].Code != "group.first" || items[0].Children[1].Code != "group.second" {
		t.Fatalf("children = %#v", items[0].Children)
	}
	if items[2].Label != "Zeta" {
		t.Fatalf("trimmed label = %q", items[2].Label)
	}
}

func TestCompileRejectsMalformedAndDuplicateNavigation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		items []adminui.NavigationItem
		want  string
	}{
		{name: "empty code", items: []adminui.NavigationItem{{Label: "Item", Route: "feature.item", Scope: adminui.NavigationSite}}, want: "invalid code"},
		{name: "empty label", items: []adminui.NavigationItem{{Code: "item", Route: "feature.item", Scope: adminui.NavigationSite}}, want: "label is empty"},
		{name: "missing leaf route", items: []adminui.NavigationItem{{Code: "item", Label: "Item", Scope: adminui.NavigationSite}}, want: "invalid route"},
		{name: "route on group", items: []adminui.NavigationItem{{Code: "group", Label: "Group", Route: "feature.group", Scope: adminui.NavigationSite, Children: []adminui.NavigationItem{{Code: "child", Label: "Child", Route: "feature.child", Scope: adminui.NavigationSite}}}}, want: "must not declare a route"},
		{name: "wrong scope", items: []adminui.NavigationItem{{Code: "item", Label: "Item", Route: "feature.item", Scope: adminui.NavigationGlobal}}, want: "expected \"site\""},
		{name: "duplicate nested code", items: []adminui.NavigationItem{{Code: "item", Label: "Item", Route: "feature.item", Scope: adminui.NavigationSite}, {Code: "group", Label: "Group", Scope: adminui.NavigationSite, Children: []adminui.NavigationItem{{Code: "item", Label: "Again", Route: "feature.again", Scope: adminui.NavigationSite}}}}, want: "already contributed"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := adminui.Compile([]adminui.Source{{Code: "feature", Items: test.items}}, adminui.NavigationSite, nil)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestMergeAttachesContributionsAcrossScopes(t *testing.T) {
	global := []adminui.NavigationItem{
		{Code: "sites", Route: "core.sites", Order: 100},
		{Code: "tools", Order: 400, Children: []adminui.NavigationItem{
			{Code: "administration", Route: "core.administration", Order: 900},
		}},
	}
	site := []adminui.NavigationItem{
		{Code: "forms", Parent: "tools", Route: "forms.list", Order: 60},
		{Code: "mail", Parent: "tools", Route: "mail.templates", Order: 50},
	}
	items, err := adminui.Merge(global, site)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || len(items[1].Children) != 3 {
		t.Fatalf("unexpected merged menu: %#v", items)
	}
	for i, code := range []string{"mail", "forms", "administration"} {
		if items[1].Children[i].Code != code || items[1].Children[i].Parent != "" {
			t.Fatalf("unexpected child: %#v", items[1].Children[i])
		}
	}
	if len(global[1].Children) != 1 || site[0].Parent != "tools" {
		t.Fatal("merge mutated declarations")
	}
}

func TestMergeRejectsInvalidParent(t *testing.T) {
	for _, parent := range []string{"missing", "leaf", "child"} {
		_, err := adminui.Merge([]adminui.NavigationItem{
			{Code: "leaf", Route: "core.sites"},
			{Code: "child", Parent: parent, Route: "forms.list"},
		})
		if err == nil {
			t.Fatalf("accepted parent %q", parent)
		}
	}
}
