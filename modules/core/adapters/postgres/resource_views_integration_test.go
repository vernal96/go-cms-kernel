package postgres

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/resourceviews"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
)

func TestPostgresResourceViewsIdempotentConcurrentBatchesAndContentIsolation(t *testing.T) {
	connector, database, ctx := openOutboxIntegrationDatabase(t)
	createdSite, err := database.Sites().(site.ManagementRepository).Create(ctx, nil, site.Site{ProfileCode: "dev", Name: "Views test", Domain: fmt.Sprintf("views-%d.test", time.Now().UnixNano()), Locale: "ru-RU", Settings: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = connector.Pool().Exec(context.Background(), `DELETE FROM core.sites WHERE id=$1`, createdSite.ID)
	})
	item, err := database.Resources().Create(ctx, nil, resource.Resource{SiteID: createdSite.ID, Type: resourcetype.Page, Title: "Viewable", Slug: "views", Path: viewsPath("/views"), IsPublic: true, Fields: map[string]any{}, TypeSettings: map[string]any{}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	stats := database.ResourceViews()
	batch := resourceviews.Batch{ID: fmt.Sprintf("views-%d", time.Now().UnixNano()), SiteID: item.SiteID, Counts: map[resource.ID]int64{item.ID: 7}}
	committedID := batch.ID
	var wg sync.WaitGroup
	failures := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); failures <- stats.ApplyBatch(ctx, batch) }()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if total, err := stats.Total(ctx, item.SiteID, item.ID); err != nil || total != 7 {
		t.Fatalf("total=%d error=%v", total, err)
	}
	batch.ID += "-next"
	batch.Counts[item.ID] = 3
	if err := stats.ApplyBatch(ctx, batch); err != nil {
		t.Fatal(err)
	}
	if total, err := stats.Total(ctx, item.SiteID, item.ID); err != nil || total != 10 {
		t.Fatalf("next total=%d error=%v", total, err)
	}
	after, err := database.Resources().ByID(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != item.Version || !after.UpdatedAt.Equal(item.UpdatedAt) {
		t.Fatal("statistics changed resource content version/timestamp")
	}
	// Saved and pending counters follow the same globally stable resource ID on transfer.
	target, err := database.Sites().(site.ManagementRepository).Create(ctx, nil, site.Site{ProfileCode: "dev", Name: "Views target", Domain: fmt.Sprintf("views-target-%d.test", time.Now().UnixNano()), Locale: "ru-RU", Settings: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = connector.Pool().Exec(context.Background(), `DELETE FROM core.sites WHERE id=$1`, target.ID)
	})
	_, err = database.Resources().(resource.SiteTransferRepository).TransferToSite(ctx, nil, item.ID, item.SiteID, target.ID, item.Version, "dev", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if total, err := stats.Total(ctx, target.ID, item.ID); err != nil || total != 10 {
		t.Fatalf("transfer saved total=%d error=%v", total, err)
	}
	// A permanently removed identity cannot poison surviving resources' batches.
	batch.ID += "-missing"
	batch.Counts[resource.ID(9223372036854775807)] = 11
	if err := stats.ApplyBatch(ctx, batch); err != nil {
		t.Fatal(err)
	}
	if total, err := stats.Total(ctx, target.ID, item.ID); err != nil || total != 13 {
		t.Fatalf("mixed deleted batch total=%d error=%v", total, err)
	}
	// Deleting the original site must not erase deduplication for transferred resources.
	if _, err := connector.Pool().Exec(ctx, `DELETE FROM core.sites WHERE id=$1`, item.SiteID); err != nil {
		t.Fatal(err)
	}
	if err := stats.ApplyBatch(ctx, resourceviews.Batch{ID: committedID, SiteID: item.SiteID, Counts: map[resource.ID]int64{item.ID: 7}}); err != nil {
		t.Fatal(err)
	}
	if total, err := stats.Total(ctx, target.ID, item.ID); err != nil || total != 13 {
		t.Fatalf("site deletion erased batch marker: total=%d error=%v", total, err)
	}
}

func viewsPath(path string) *string { return &path }
