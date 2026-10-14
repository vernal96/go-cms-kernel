package httpserver_test

import (
	"context"
	"errors"
	"sync"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/modules/core"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourceviews"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
)

type recordingViews struct {
	mu      sync.Mutex
	counts  map[[2]int64]int64
	failure bool
}

func (v *recordingViews) Increment(_ context.Context, siteID site.ID, id resource.ID) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.failure {
		return errors.New("store unavailable")
	}
	v.counts[[2]int64{int64(siteID), int64(id)}]++
	return nil
}
func (v *recordingViews) Open(context.Context) (resourceviews.Accumulator, error) { return v, nil }
func (v *recordingViews) Claim(context.Context, site.ID) (*resourceviews.Batch, error) {
	return nil, nil
}
func (v *recordingViews) Acknowledge(context.Context, resourceviews.Batch) error { return nil }
func (v *recordingViews) Close() error                                           { return nil }
func (v *recordingViews) ApplyBatch(context.Context, resourceviews.Batch) error  { return nil }
func (v *recordingViews) Total(_ context.Context, siteID site.ID, id resource.ID) (int64, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.counts[[2]int64{int64(siteID), int64(id)}], nil
}

type viewsHTTPDatabase struct {
	core.Database
	views *recordingViews
}

func (d viewsHTTPDatabase) ResourceViews() resourceviews.Repository { return d.views }

type viewsHTTPDatabaseFactory struct {
	databaseFactory
	views *recordingViews
}

func (f viewsHTTPDatabaseFactory) Build(connector kernel.DBConnector) (kernel.ModuleDatabase, error) {
	database, err := f.databaseFactory.Build(connector)
	if err != nil {
		return nil, err
	}
	return viewsHTTPDatabase{Database: database.(core.Database), views: f.views}, nil
}
