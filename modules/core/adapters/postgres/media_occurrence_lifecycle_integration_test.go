package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
)

func TestPostgresWidgetOccurrencesFollowDeleteTransferAndOwnerDelete(t *testing.T) {
	conn, db, ctx := openOutboxIntegrationDatabase(t)
	suffix := fmt.Sprint(time.Now().UnixNano())
	siteIDs := make([]site.ID, 2)
	for index := range siteIDs {
		if err := conn.Pool().QueryRow(ctx, `INSERT INTO core.sites(profile_code,name,domain) VALUES('guard','Guard',$1) RETURNING id`, fmt.Sprintf("occurrence-%s-%d.test", suffix, index)).Scan(&siteIDs[index]); err != nil {
			t.Fatal(err)
		}
	}
	f, err := db.Files().CreateFile(ctx, file.File{Storage: "public", Name: suffix + ".png", Path: suffix, MIMEType: "image/png", Size: 1, ChecksumSHA256: fmt.Sprintf("%064d", 1)})
	if err != nil {
		t.Fatal(err)
	}
	m, err := db.Media().Create(ctx, nil, media.Media{FileID: f.ID, Params: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = conn.Pool().Exec(cleanup, `DELETE FROM core.sites WHERE id=ANY($1::bigint[])`, []int64{int64(siteIDs[0]), int64(siteIDs[1])})
		_, _ = conn.Pool().Exec(cleanup, `DELETE FROM core.media WHERE id=$1`, m.ID)
		_, _ = conn.Pool().Exec(cleanup, `DELETE FROM core.files WHERE id=$1`, f.ID)
	})

	path := "/movable-" + suffix
	owner, err := db.Resources().Create(ctx, nil, resource.Resource{
		SiteID: siteIDs[0], Type: resourcetype.Page, Title: "Movable", Slug: "movable-" + suffix, Path: &path,
		Fields: map[string]any{}, TypeSettings: map[string]any{},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	reference := field.Reference{Target: field.ReferenceFile, ID: int64(m.ID), Path: []string{"asset"}}
	createWidget := func(version int64) widget.Binding {
		binding, err := db.Resources().(resource.WidgetRepository).CreateWidget(ctx, nil, owner.ID, version, widget.Binding{
			Code: "fields_tile", Area: "body", Params: map[string]any{"asset": int64(m.ID)}, References: []field.Reference{reference},
			Presentation: widget.Presentation{Columns: 12, Enabled: true},
		}, false)
		if err != nil {
			t.Fatal(err)
		}
		return binding
	}
	version := owner.Version
	first := createWidget(version)
	if err := conn.Pool().QueryRow(ctx, `SELECT version FROM core.resource_entities WHERE id=$1`, owner.ID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	assertOccurrence := func(want int, wantSite site.ID) {
		t.Helper()
		var count int
		if err := conn.Pool().QueryRow(ctx, `SELECT count(*) FROM core.media_field_occurrences WHERE owner_kind='resource' AND owner_id=$1 AND site_id=$2`, owner.ID, wantSite).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Fatalf("occurrences for site %d = %d, want %d", wantSite, count, want)
		}
	}
	assertOccurrence(1, siteIDs[0])
	if err := db.Resources().(resource.WidgetRepository).DeleteWidget(ctx, nil, owner.ID, version, first.ID, false); err != nil {
		t.Fatal(err)
	}
	assertOccurrence(0, siteIDs[0])
	if err := conn.Pool().QueryRow(ctx, `SELECT version FROM core.resource_entities WHERE id=$1`, owner.ID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	_ = createWidget(version)
	if err := conn.Pool().QueryRow(ctx, `SELECT version FROM core.resource_entities WHERE id=$1`, owner.ID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Resources().(resource.SiteTransferRepository).TransferToSite(ctx, nil, owner.ID, siteIDs[0], siteIDs[1], version, "guard", "guard"); err != nil {
		t.Fatal(err)
	}
	assertOccurrence(0, siteIDs[0])
	assertOccurrence(1, siteIDs[1])
	if err := db.Resources().Delete(ctx, owner.ID); err != nil {
		t.Fatal(err)
	}
	assertOccurrence(0, siteIDs[1])
}
