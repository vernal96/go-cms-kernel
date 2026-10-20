package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/adapters/postgres/mediaoccurrence"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
)

func TestPostgresMediaHasOneActiveSelection(t *testing.T) {
	conn, db, ctx := openOutboxIntegrationDatabase(t)
	suffix := fmt.Sprint(time.Now().UnixNano())
	var sid int64
	if err := conn.Pool().QueryRow(ctx, `INSERT INTO core.sites(profile_code,name,domain) VALUES('dev','Media ownership',$1) RETURNING id`, "ownership-"+suffix+".test").Scan(&sid); err != nil {
		t.Fatal(err)
	}
	f, err := db.Files().CreateFile(ctx, file.File{Storage: "public", Name: suffix + ".png", Path: suffix, MIMEType: "image/png", Size: 1, ChecksumSHA256: fmt.Sprintf("%064d", 1)})
	if err != nil {
		t.Fatal(err)
	}
	medias := make([]media.Media, 4)
	for i := range medias {
		medias[i], err = db.Media().Create(ctx, nil, media.Media{FileID: f.ID, Params: map[string]any{}})
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		conn.Pool().Exec(cleanup, `DELETE FROM core.media_field_occurrences WHERE site_id=$1`, sid)
		conn.Pool().Exec(cleanup, `DELETE FROM core.file_field_references WHERE owner_kind='site' AND owner_id=$1`, sid)
		conn.Pool().Exec(cleanup, `DELETE FROM core.sites WHERE id=$1`, sid)
		for _, selection := range medias {
			conn.Pool().Exec(cleanup, `DELETE FROM core.media WHERE id=$1`, selection.ID)
		}
		conn.Pool().Exec(cleanup, `DELETE FROM core.files WHERE id=$1`, f.ID)
	})
	owner := media.FileOccurrence{OwnerKind: "site", OwnerID: sid, SiteID: sid, Container: "settings"}
	ref := func(id media.ID, key string) field.Reference {
		return field.Reference{Target: field.ReferenceMedia, ID: int64(id), Path: []string{key}}
	}
	replace := func(owner media.FileOccurrence, refs []field.Reference) error {
		tx, err := conn.Pool().Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(context.WithoutCancel(ctx))
		if err := mediaoccurrence.Replace(ctx, tx, owner, refs); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if err := replace(owner, []field.Reference{ref(medias[0].ID, "logo")}); err != nil {
		t.Fatal(err)
	}
	if err := replace(owner, []field.Reference{ref(medias[0].ID, "logo")}); err != nil {
		t.Fatalf("unchanged owner update rejected: %v", err)
	}
	if err := replace(owner, []field.Reference{ref(medias[0].ID, "logo"), ref(medias[0].ID, "icon")}); !errors.Is(err, media.ErrAlreadyAttached) {
		t.Fatalf("same Media in two tiles accepted: %v", err)
	}
	for _, kind := range []string{"resource", "forms.element", "forms.result", "mail_template"} {
		foreign := media.FileOccurrence{OwnerKind: kind, OwnerID: sid, SiteID: sid, Container: "other"}
		if err := replace(foreign, []field.Reference{ref(medias[0].ID, "asset")}); !errors.Is(err, media.ErrAlreadyAttached) {
			t.Fatalf("%s reused another tile's Media: %v", kind, err)
		}
	}
	if err := replace(owner, []field.Reference{ref(medias[0].ID, "logo"), ref(medias[1].ID, "icon")}); err != nil {
		t.Fatalf("distinct Media for same File rejected: %v", err)
	}
	duplicate := site.Site{ID: site.ID(sid), ProfileCode: "dev", Name: "Media ownership", Domain: "ownership-" + suffix + ".test", Locale: "en", Version: 1,
		Settings:         map[string]any{"logo": int64(medias[0].ID), "portals": []any{map[string]any{"icon": int64(medias[0].ID)}}},
		FileReferences:   map[string]media.ID{"logo": medias[0].ID, "portals[0].icon": medias[0].ID},
		MediaOccurrences: []field.Reference{ref(medias[0].ID, "logo"), {Target: field.ReferenceMedia, ID: int64(medias[0].ID), Path: []string{"portals", "0", "icon"}}},
	}
	if _, err := db.Sites().(site.ManagementRepository).Update(ctx, nil, duplicate); !errors.Is(err, media.ErrAlreadyAttached) {
		t.Fatalf("site logo/portal reused Media: %v", err)
	}
	duplicate.Settings["portals"] = []any{map[string]any{"icon": int64(medias[1].ID)}}
	duplicate.FileReferences["portals[0].icon"] = medias[1].ID
	duplicate.MediaOccurrences[1].ID = int64(medias[1].ID)
	if _, err := db.Sites().(site.ManagementRepository).Update(ctx, nil, duplicate); err != nil {
		t.Fatalf("site mirrored reference index rejected valid independent tiles: %v", err)
	}
	if _, err := conn.Pool().Exec(ctx, `INSERT INTO core.file_field_references(owner_kind,owner_id,field_key,media_id) VALUES('site',$1,'unindexed',$2)`, sid, medias[3].ID); err != nil {
		t.Fatal(err)
	}
	if err := replace(media.FileOccurrence{OwnerKind: "forms.element", OwnerID: sid, SiteID: sid, Container: "config"}, []field.Reference{ref(medias[3].ID, "asset")}); !errors.Is(err, media.ErrAlreadyAttached) {
		t.Fatalf("unindexed active file reference ignored: %v", err)
	}
	path := "/"
	if _, err := db.Resources().Create(ctx, nil, resource.Resource{
		SiteID: site.ID(sid), Type: resourcetype.Page,
		Title: "Image", Path: &path, ImageMediaID: &medias[0].ID,
	}, func(context.Context, media.ID) error { return nil }); !errors.Is(err, media.ErrAlreadyAttached) {
		t.Fatalf("resource image reused saved field Media: %v", err)
	}
	image, err := db.Resources().Create(ctx, nil, resource.Resource{
		SiteID: site.ID(sid), Type: resourcetype.Page,
		Title: "Image", Path: &path, ImageMediaID: &medias[2].ID,
	}, func(context.Context, media.ID) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := replace(media.FileOccurrence{OwnerKind: "resource", OwnerID: int64(image.ID), SiteID: sid, Container: "widget:1"}, []field.Reference{ref(medias[2].ID, "asset")}); !errors.Is(err, media.ErrAlreadyAttached) {
		t.Fatalf("widget reused native image Media: %v", err)
	}
	if _, err := db.Resources().Update(ctx, nil, image, image, func(context.Context, media.ID) error { return nil }); err != nil {
		t.Fatalf("unchanged native image update rejected: %v", err)
	}
}

func TestPostgresConcurrentMediaClaimsHaveOneWinner(t *testing.T) {
	conn, db, ctx := openOutboxIntegrationDatabase(t)
	suffix := fmt.Sprint(time.Now().UnixNano())
	var sid int64
	if err := conn.Pool().QueryRow(ctx, `INSERT INTO core.sites(profile_code,name,domain) VALUES('dev','Concurrent Media',$1) RETURNING id`, "claim-"+suffix+".test").Scan(&sid); err != nil {
		t.Fatal(err)
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
		conn.Pool().Exec(cleanup, `DELETE FROM core.sites WHERE id=$1`, sid)
		conn.Pool().Exec(cleanup, `DELETE FROM core.media WHERE id=$1`, m.ID)
		conn.Pool().Exec(cleanup, `DELETE FROM core.files WHERE id=$1`, f.ID)
	})
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, kind := range []string{"site", "forms.element"} {
		go func(kind string) {
			<-start
			tx, err := conn.Pool().Begin(ctx)
			if err != nil {
				results <- err
				return
			}
			defer tx.Rollback(context.WithoutCancel(ctx))
			err = mediaoccurrence.Replace(
				ctx, tx,
				media.FileOccurrence{
					OwnerKind: kind, OwnerID: sid, SiteID: sid, Container: "config",
				},
				[]field.Reference{{
					Target: field.ReferenceMedia,
					ID:     int64(m.ID), Path: []string{"asset"},
				}},
			)
			if err == nil {
				err = tx.Commit(ctx)
			}
			results <- err
		}(kind)
	}
	close(start)
	wins, conflicts := 0, 0
	for i := 0; i < 2; i++ {
		select {
		case err := <-results:
			if err == nil {
				wins++
			} else if errors.Is(err, media.ErrAlreadyAttached) {
				conflicts++
			} else {
				t.Fatalf("claim error: %v", err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("wins=%d conflicts=%d", wins, conflicts)
	}
	var count int
	if err := conn.Pool().QueryRow(ctx, `SELECT count(*) FROM core.media_field_occurrences WHERE media_id=$1`, m.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("saved occurrences=%d: %v", count, err)
	}
}
