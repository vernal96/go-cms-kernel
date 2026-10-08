package core

import (
	"context"
	"testing"

	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
)

type widgetQueryProbe struct{ query resource.Query }

func (p *widgetQueryProbe) Query(_ context.Context, query resource.Query) (resource.Page, error) {
	p.query = query
	return resource.Page{}, nil
}

type widgetQueryRepository struct {
	resource.Repository
	query resource.QueryRepository
}

func (r widgetQueryRepository) Query(ctx context.Context, query resource.Query) (resource.Page, error) {
	return r.query.Query(ctx, query)
}

type widgetQueryDatabase struct {
	Database
	resources resource.Repository
}

func (d widgetQueryDatabase) Resources() resource.Repository { return d.resources }

func TestResourceQueryIsStableAndScopedToEachRuntime(t *testing.T) {
	firstProbe, secondProbe := &widgetQueryProbe{}, &widgetQueryProbe{}
	firstRuntime := &Runtime{database: widgetQueryDatabase{resources: widgetQueryRepository{query: firstProbe}}}
	secondRuntime := &Runtime{database: widgetQueryDatabase{resources: widgetQueryRepository{query: secondProbe}}}
	if err := buildWidgets(firstRuntime, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := buildWidgets(secondRuntime, nil, nil); err != nil {
		t.Fatal(err)
	}

	firstQuery := firstRuntime.ResourceQuery()
	secondQuery := secondRuntime.ResourceQuery()
	if firstQuery == nil || secondQuery == nil || firstQuery == secondQuery {
		t.Fatal("resource query service was not built independently for each runtime")
	}
	for _, test := range []struct {
		id      site.ID
		service interface {
			Query(context.Context, resource.Query) (resource.Page, error)
		}
		probe *widgetQueryProbe
	}{
		{id: 11, service: firstQuery, probe: firstProbe},
		{id: 22, service: secondQuery, probe: secondProbe},
	} {
		if _, err := test.service.Query(context.Background(), resource.Query{SiteID: test.id, Limit: 1, PublicOnly: true}); err != nil {
			t.Fatal(err)
		}
		if test.probe.query.SiteID != test.id || !test.probe.query.PublicOnly {
			t.Fatalf("runtime query escaped site/public scope: %#v", test.probe.query)
		}
	}
	if firstRuntime.ResourceQuery() != firstQuery || secondRuntime.ResourceQuery() != secondQuery {
		t.Fatal("resource query service changed after request execution")
	}
}
