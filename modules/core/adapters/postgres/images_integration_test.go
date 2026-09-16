package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
)

func TestPostgresImageDeletionImpactCascadeAndFields(t *testing.T) {
	conn, db, ctx := openOutboxIntegrationDatabase(t)
	suffix := fmt.Sprint(time.Now().UnixNano())
	files := db.Files()
	cascade := files.(file.CascadeRepository)
	var siteID, ownerID, userID, itemID int64
	if err := conn.Pool().QueryRow(ctx, `INSERT INTO core.sites(profile_code,domain) VALUES('dev',$1) RETURNING id`, "images-"+suffix+".test").Scan(&siteID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		conn.Pool().Exec(context.Background(), `DELETE FROM core.sites WHERE id=$1`, siteID)
		conn.Pool().Exec(context.Background(), `DELETE FROM core.users WHERE id=$1`, userID)
		conn.Pool().Exec(context.Background(), `DELETE FROM core.files WHERE storage=$1`, "images-"+suffix)
	})
	makeFile := func(parent *file.ID) file.File {
		t.Helper()
		f, err := files.CreateFile(ctx, file.File{Storage: filesystem.Code("images-" + suffix), Name: fmt.Sprintf("image-%d.png", time.Now().UnixNano()), Path: fmt.Sprint(time.Now().UnixNano()), MIMEType: "image/png", Size: 1, ChecksumSHA256: fmt.Sprintf("%064d", 1), ParentID: parent})
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	root := makeFile(nil)
	child := makeFile(&root.ID)
	m, err := db.Media().Create(ctx, nil, media.Media{FileID: child.ID, Params: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if err = conn.Pool().QueryRow(ctx, `WITH entity AS (INSERT INTO core.resource_entities(site_id,storage_kind) VALUES($1,'tree') RETURNING id) INSERT INTO core.resources(id,site_id,title,image_media_id,path) SELECT id,$1,'Image owner',$2,'/' FROM entity RETURNING id`, siteID, m.ID).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	if err = conn.Pool().QueryRow(ctx, `INSERT INTO core.users(login,email,password_hash,name,avatar_media_id) VALUES($1,$2,'test','Image user',$3) RETURNING id`, "img"+suffix, "img"+suffix+"@test.local", m.ID).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err = conn.Pool().QueryRow(ctx, `WITH entity AS (INSERT INTO core.resource_entities(site_id,storage_kind) VALUES($1,'library_item') RETURNING id) INSERT INTO core.library_items(id,site_id,library_id,partition_at,title,slug,image_media_id) SELECT id,$1,$2,now(),'Image item','image-item',$3 FROM entity RETURNING id`, siteID, ownerID, m.ID).Scan(&itemID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Pool().Exec(ctx, `INSERT INTO core.library_item_routes(resource_id,site_id,library_id,slug) VALUES($1,$2,$3,'image-item')`, itemID, siteID, ownerID); err != nil {
		t.Fatal(err)
	}
	for _, owner := range []int64{ownerID, itemID} {
		linked, err := db.Media().Create(ctx, nil, media.Media{FileID: child.ID, Params: map[string]any{}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Pool().Exec(ctx, `INSERT INTO core.resource_field_values(resource_id,site_id,field_key,position,is_multi,value_kind,value_reference) VALUES($1,$2,'media_test',0,false,'reference',$3)`, owner, siteID, linked.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Pool().Exec(ctx, `INSERT INTO core.resource_media_references(resource_id,field_key,position,media_id) VALUES($1,'media_test',0,$2)`, owner, linked.ID); err != nil {
			t.Fatal(err)
		}
	}
	items := []file.ItemReference{{Kind: file.ItemFile, ID: int64(root.ID)}}
	impact, err := cascade.DeleteImpact(ctx, items)
	if err != nil {
		t.Fatal(err)
	}
	if impact.SelectedCount != 1 || impact.TotalFiles != 2 || impact.DerivedFiles != 1 || impact.MediaReferences != 3 || impact.FileFieldReferences != 0 {
		t.Fatalf("impact %+v", impact)
	}
	deleted := 0
	physical := func(_ context.Context, f []file.File) error { deleted += len(f); return nil }
	if err := files.DeleteFile(ctx, root.ID, physical); !errors.Is(err, file.ErrInUse) || deleted != 0 {
		t.Fatal("safe delete allowed referenced image", err)
	}
	if _, err = conn.Pool().Exec(ctx, `INSERT INTO core.file_field_references(owner_kind,owner_id,field_key,file_id) VALUES('resource',$1,'generic_file',$2)`, ownerID, child.ID); err != nil {
		t.Fatal(err)
	}
	blocked, err := cascade.DeleteImpact(ctx, items)
	if err != nil || blocked.FileFieldReferences != 1 {
		t.Fatal("missing generic references", err)
	}
	if err = cascade.DeleteConfirmed(ctx, nil, items, blocked.Token, physical); !errors.Is(err, file.ErrInUse) || deleted != 0 {
		t.Fatal("unsafe generic field cascade", err)
	}
	if _, err = conn.Pool().Exec(ctx, `DELETE FROM core.file_field_references WHERE owner_kind='resource' AND owner_id=$1`, ownerID); err != nil {
		t.Fatal(err)
	}
	if err = cascade.DeleteConfirmed(ctx, nil, items, blocked.Token, physical); !errors.Is(err, file.ErrConflict) {
		t.Fatal("stale impact accepted", err)
	}
	impact, err = cascade.DeleteImpact(ctx, items)
	if err != nil {
		t.Fatal(err)
	}
	if err = cascade.DeleteConfirmed(ctx, nil, items, impact.Token, physical); err != nil {
		t.Fatal(err)
	}
	var updatedOwners, revisions, events int
	if err := conn.Pool().QueryRow(ctx, `SELECT count(*) FROM core.resource_entities WHERE id=ANY($1::bigint[]) AND version=2`, []int64{ownerID, itemID}).Scan(&updatedOwners); err != nil || updatedOwners != 2 {
		t.Fatalf("owner versions: %d, %v", updatedOwners, err)
	}
	if err := conn.Pool().QueryRow(ctx, `SELECT count(*) FROM core.resource_revisions WHERE resource_id=$1 AND version=2 AND NOT (snapshot->'fields' ? 'media_test') AND snapshot->>'image_media_id' IS NULL`, ownerID).Scan(&revisions); err != nil || revisions != 1 {
		t.Fatalf("cascade revision: %d, %v", revisions, err)
	}
	if err := conn.Pool().QueryRow(ctx, `SELECT count(*) FROM core.outbox_messages WHERE topic IN ('resource.updated','user.updated')`).Scan(&events); err != nil || events < 3 {
		t.Fatalf("cascade events: %d, %v", events, err)
	}
	if deleted != 2 {
		t.Fatalf("physical deletion count %d", deleted)
	}
	if _, err = files.FileByID(ctx, child.ID); !errors.Is(err, file.ErrNotFound) {
		t.Fatal("child survived", err)
	}
	if _, err = db.Media().ByID(ctx, m.ID); !errors.Is(err, media.ErrNotFound) {
		t.Fatal("media survived", err)
	}
	var remaining int
	if err := conn.Pool().QueryRow(ctx, `SELECT count(*) FROM core.resource_field_values WHERE resource_id=ANY($1::bigint[]) AND field_key='media_test'`, []int64{ownerID, itemID}).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatal("dangling Media field values", remaining, err)
	}
	var cleared bool
	if err = conn.Pool().QueryRow(ctx, `SELECT (SELECT image_media_id IS NULL FROM core.resources WHERE id=$1) AND (SELECT avatar_media_id IS NULL FROM core.users WHERE id=$2) AND (SELECT image_media_id IS NULL FROM core.library_items WHERE id=$3)`, ownerID, userID, itemID).Scan(&cleared); err != nil || !cleared {
		t.Fatal("dangling owners", err)
	}
}
func TestPostgresConcurrentMediaImageSwitch(t *testing.T) {
	conn, db, ctx := openOutboxIntegrationDatabase(t)
	suffix := fmt.Sprint(time.Now().UnixNano())
	files := db.Files()
	storage := "image-cas-" + suffix
	t.Cleanup(func() { conn.Pool().Exec(context.Background(), `DELETE FROM core.files WHERE storage=$1`, storage) })
	create := func(parent *file.ID) file.File {
		t.Helper()
		f, err := files.CreateFile(ctx, file.File{Storage: filesystem.Code(storage), Name: fmt.Sprint(time.Now().UnixNano()) + ".png", Path: fmt.Sprint(time.Now().UnixNano()), MIMEType: "image/png", Size: 1, ChecksumSHA256: fmt.Sprintf("%064d", 1), ParentID: parent})
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	root := create(nil)
	a, b := create(&root.ID), create(&root.ID)
	m, err := db.Media().Create(ctx, nil, media.Media{FileID: root.ID, Params: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	repo := db.Media().(media.ImageRepository)
	results := make(chan error, 2)
	for _, f := range []file.File{a, b} {
		go func(f file.File) {
			next := media.Clone(m)
			next.FileID = f.ID
			_, err := repo.UpdateImage(ctx, nil, next, m.UpdatedAt, func(context.Context, []media.Usage) error { return nil })
			results <- err
		}(f)
	}
	x, y := <-results, <-results
	if (x == nil) == (y == nil) {
		t.Fatalf("both edits won/lost: %v %v", x, y)
	}
	if x != nil && !errors.Is(x, media.ErrImageConflict) || y != nil && !errors.Is(y, media.ErrImageConflict) {
		t.Fatal(x, y)
	}
	final, err := db.Media().ByID(ctx, m.ID)
	if err != nil || (final.FileID != a.ID && final.FileID != b.ID) {
		t.Fatal("incorrect final media", err)
	}
}
