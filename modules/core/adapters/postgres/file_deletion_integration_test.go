package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
)

func TestPostgresMediaFileDeletionGuardsAndDurableRetry(t *testing.T) {
	conn, db, ctx := openOutboxIntegrationDatabase(t)
	suffix := fmt.Sprint(time.Now().UnixNano())
	var sid int64
	if err := conn.Pool().QueryRow(ctx, `INSERT INTO core.sites(profile_code,name,domain)VALUES('dev','Deletion test',$1)RETURNING id`, "deletion-"+suffix+".test").Scan(&sid); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		conn.Pool().Exec(cleanup, `DELETE FROM core.file_field_references WHERE (owner_kind='site' AND owner_id=$1) OR (owner_kind='resource' AND owner_id IN(SELECT id FROM core.resource_entities WHERE site_id=$1))`, sid)
		conn.Pool().Exec(cleanup, `DELETE FROM core.sites WHERE id=$1`, sid)
		conn.Pool().Exec(cleanup, `DELETE FROM core.media_file_deletions WHERE site_id=$1`, sid)
		conn.Pool().Exec(cleanup, `DELETE FROM core.files WHERE name LIKE $1`, suffix+"%")
	})
	newMedia := func(name string) (file.File, media.Media) {
		f, err := db.Files().CreateFile(ctx, file.File{Storage: "public", Name: suffix + name + ".png", Path: suffix + name, MIMEType: "image/png", Size: 1, ChecksumSHA256: fmt.Sprintf("%064d", 1)})
		if err != nil {
			t.Fatal(err)
		}
		m, err := db.Media().Create(ctx, nil, media.Media{FileID: f.ID, Params: map[string]any{}})
		if err != nil {
			t.Fatal(err)
		}
		return f, m
	}
	f, m := newMedia("original")
	repo := db.FileDeletions()
	input := media.DeleteFileInput{SiteID: sid, MediaID: m.ID, ExpectedFileID: f.ID, ExpectedUpdatedAt: m.UpdatedAt}
	preparedCalls := 0
	prepare := func(context.Context, *media.FileOccurrence) (media.PreparedFileOwner, error) {
		preparedCalls++
		return media.PreparedFileOwner{}, nil
	}
	other, err := db.Media().Create(ctx, nil, media.Media{FileID: f.ID, Params: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.DeleteMediaFile(ctx, nil, input, prepare); !errors.Is(err, media.ErrFileInUse) || preparedCalls != 0 {
		t.Fatalf("second Media guard: %v calls=%d", err, preparedCalls)
	}
	if err := db.Media().Delete(ctx, other.ID); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"site", "resource", "forms.result"} {
		if _, err := conn.Pool().Exec(ctx, `INSERT INTO core.media_field_occurrences(owner_kind,owner_id,site_id,container,value_path,media_id,reference_target)VALUES($1,$2,$2,'config',ARRAY['icon'],$3,'media')`, kind, sid, m.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.DeleteMediaFile(ctx, nil, input, prepare); !errors.Is(err, media.ErrFileInUse) {
			t.Fatalf("protected %s: %v", kind, err)
		}
		if kind == "resource" {
			cascade := db.Files().(file.CascadeRepository)
			impact, err := cascade.DeleteImpact(ctx, []file.ItemReference{{Kind: file.ItemFile, ID: int64(f.ID)}})
			if err != nil || impact.FileFieldReferences == 0 {
				t.Fatalf("FileExplorer omitted protected widget: %+v %v", impact, err)
			}
			physicalCalls := 0
			err = cascade.DeleteConfirmed(ctx, nil, []file.ItemReference{{Kind: file.ItemFile, ID: int64(f.ID)}}, impact.Token, func(context.Context, []file.File) error { physicalCalls++; return nil })
			if !errors.Is(err, file.ErrInUse) || physicalCalls != 0 {
				t.Fatalf("FileExplorer touched protected bytes: %v calls=%d", err, physicalCalls)
			}
		}
		if _, err := conn.Pool().Exec(ctx, `DELETE FROM core.media_field_occurrences WHERE media_id=$1`, m.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := conn.Pool().Exec(ctx, `INSERT INTO core.media_field_occurrences(owner_kind,owner_id,site_id,container,value_path,media_id,reference_target)VALUES('site',$1,$1,'settings',ARRAY['first'],$2,'file'),('site',$1,$1,'settings',ARRAY['second'],$2,'file')`, sid, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.DeleteMediaFile(ctx, nil, input, prepare); !errors.Is(err, media.ErrFileInUse) {
		t.Fatalf("duplicate occurrence: %v", err)
	}
	conn.Pool().Exec(ctx, `DELETE FROM core.media_field_occurrences WHERE media_id=$1`, m.ID)
	stale := input
	stale.ExpectedUpdatedAt = stale.ExpectedUpdatedAt.Add(-time.Second)
	if _, err := repo.DeleteMediaFile(ctx, nil, stale, prepare); !errors.Is(err, media.ErrFileDeleteConflict) {
		t.Fatalf("stale guard: %v", err)
	}
	result, err := repo.DeleteMediaFile(ctx, nil, input, prepare)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "pending" || result.ClearedReference != nil {
		t.Fatal(result)
	}
	if _, err := db.Media().ByID(ctx, m.ID); !errors.Is(err, media.ErrNotFound) {
		t.Fatal("Media remained after committed deletion", err)
	}
	if _, err := db.Files().FileByID(ctx, f.ID); !errors.Is(err, file.ErrNotFound) {
		t.Fatal("File remained after committed deletion", err)
	}
	pending, err := repo.CleanFileDeletion(ctx, sid, result.OperationID, func(context.Context, []file.File) error { return errors.New("disk offline") })
	if err != nil || pending.Status != "pending" {
		t.Fatalf("pending=%v error=%v", pending, err)
	}
	done, err := repo.CleanFileDeletion(ctx, sid, result.OperationID, func(_ context.Context, files []file.File) error {
		if len(files) != 1 || files[0].Path != f.Path {
			t.Fatalf("lost manifest: %v", files)
		}
		return nil
	})
	if err != nil || done.Status != "completed" {
		t.Fatalf("done=%v error=%v", done, err)
	}
	if _, err := repo.CleanFileDeletion(ctx, sid, result.OperationID, func(context.Context, []file.File) error { t.Fatal("completed cleanup repeated"); return nil }); err != nil {
		t.Fatal(err)
	}
	if status, err := repo.FileDeletion(ctx, sid, result.OperationID); err != nil || status.Status != "completed" {
		t.Fatal(status, err)
	}
}

func TestPostgresMediaFileDeletionPrunesNestedReferenceAndResourceHistory(t *testing.T) {
	conn, db, ctx := openOutboxIntegrationDatabase(t)
	suffix := fmt.Sprint(time.Now().UnixNano())
	var sid int64
	if err := conn.Pool().QueryRow(ctx, `INSERT INTO core.sites(profile_code,name,domain)VALUES('dev','Nested delete',$1)RETURNING id`, "nested-delete-"+suffix+".test").Scan(&sid); err != nil {
		t.Fatal(err)
	}
	var mediaIDs []media.ID
	var files []file.File
	for i := 0; i < 3; i++ {
		f, err := db.Files().CreateFile(ctx, file.File{Storage: "public", Name: fmt.Sprintf("%s-%d.png", suffix, i), Path: fmt.Sprintf("%s-%d", suffix, i), MIMEType: "image/png", Size: 1, ChecksumSHA256: fmt.Sprintf("%064d", 1)})
		if err != nil {
			t.Fatal(err)
		}
		m, err := db.Media().Create(ctx, nil, media.Media{FileID: f.ID, Params: map[string]any{}})
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
		mediaIDs = append(mediaIDs, m.ID)
	}
	path := "/"
	owner, err := db.Resources().Create(ctx, nil, resource.Resource{SiteID: site.ID(sid), Type: resourcetype.Library, Title: "Library", Path: &path, TypeSettings: map[string]any{"item_url_pattern": "/{slug}"}, FieldValues: []field.StoredValue{{Key: "portals", Kind: field.StorageJSON, Value: []any{map[string]any{"name": "kept", "icons": []any{int64(mediaIDs[0]), int64(mediaIDs[1]), int64(mediaIDs[2])}}}, References: []field.Reference{{Target: field.ReferenceFile, ID: int64(mediaIDs[0]), Path: []string{"0", "icons", "0"}}, {Target: field.ReferenceFile, ID: int64(mediaIDs[1]), Path: []string{"0", "icons", "1"}}, {Target: field.ReferenceFile, ID: int64(mediaIDs[2]), Path: []string{"0", "icons", "2"}}}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		conn.Pool().Exec(cleanup, `DELETE FROM core.file_field_references WHERE owner_kind='resource' AND owner_id=$1`, owner.ID)
		conn.Pool().Exec(cleanup, `DELETE FROM core.sites WHERE id=$1`, sid)
		conn.Pool().Exec(cleanup, `DELETE FROM core.media_file_deletions WHERE site_id=$1`, sid)
		for _, f := range files {
			conn.Pool().Exec(cleanup, `DELETE FROM core.files WHERE id=$1`, f.ID)
		}
	})
	clicked, err := db.Media().ByID(ctx, mediaIDs[1])
	if err != nil {
		t.Fatal(err)
	}
	clearing := db.FileOccurrenceOwners()[0]
	result, err := db.FileDeletions().DeleteMediaFile(ctx, nil, media.DeleteFileInput{SiteID: sid, MediaID: clicked.ID, ExpectedFileID: clicked.FileID, ExpectedUpdatedAt: clicked.UpdatedAt}, func(ctx context.Context, ref *media.FileOccurrence) (media.PreparedFileOwner, error) {
		if ref == nil || ref.OwnerID != int64(owner.ID) {
			t.Fatal("owner discovery", ref)
		}
		return media.PreparedFileOwner{Apply: func(ctx context.Context) (*media.ClearedFileReference, error) {
			version, err := clearing.ClearFileOccurrence(ctx, nil, *ref)
			return &media.ClearedFileReference{FileOccurrence: *ref, OwnerVersion: version}, err
		}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ClearedReference == nil || result.ClearedReference.OwnerVersion != owner.Version+1 {
		t.Fatal(result)
	}
	remaining, err := db.Resources().ByID(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	rows := remaining.Fields["portals"].([]any)
	icons := rows[0].(map[string]any)["icons"].([]any)
	if len(icons) != 2 || rows[0].(map[string]any)["name"] != "kept" {
		t.Fatal(remaining.Fields)
	}
	var newPath []string
	if err := conn.Pool().QueryRow(ctx, `SELECT value_path FROM core.resource_media_references WHERE resource_id=$1 AND media_id=$2`, owner.ID, mediaIDs[2]).Scan(&newPath); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(newPath) != "[0 icons 1]" {
		t.Fatal("reference index not rebuilt", newPath)
	}
	var revisions, events int
	if err := conn.Pool().QueryRow(ctx, `SELECT (SELECT count(*) FROM core.resource_revisions WHERE resource_id=$1),(SELECT count(*) FROM core.outbox_messages WHERE topic='resource.updated')`, owner.ID).Scan(&revisions, &events); err != nil {
		t.Fatal(err)
	}
	if revisions == 0 || events == 0 {
		t.Fatal("owner lifecycle skipped", revisions, events)
	}
}

func TestPostgresMediaFileDeletionWaitsForChildCreation(t *testing.T) {
	conn, db, ctx := openOutboxIntegrationDatabase(t)
	suffix := fmt.Sprint(time.Now().UnixNano())
	parent, err := db.Files().CreateFile(ctx, file.File{
		Storage: "public", Name: suffix + "-parent.png", Path: suffix + "-parent",
		MIMEType: "image/png", Size: 1, ChecksumSHA256: fmt.Sprintf("%064d", 1),
	})
	if err != nil {
		t.Fatal(err)
	}
	parentMedia, err := db.Media().Create(ctx, nil, media.Media{FileID: parent.ID, Params: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		conn.Pool().Exec(cleanup, `DELETE FROM core.media_file_deletions WHERE media_id=$1`, parentMedia.ID)
		conn.Pool().Exec(cleanup, `DELETE FROM core.files WHERE id=$1`, parent.ID)
	})

	childTx, err := conn.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer childTx.Rollback(context.WithoutCancel(ctx))
	// Match CreateFile/CreateAvailableFile: serialize filesystem mutations, then
	// keep the parent's FK key-share lock until the new child commits.
	if _, err := childTx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('core.filesystem.mutation', 0))`); err != nil {
		t.Fatal(err)
	}
	if _, err := childTx.Exec(ctx, `SELECT id FROM core.files WHERE id=$1 FOR KEY SHARE`, parent.ID); err != nil {
		t.Fatal(err)
	}
	var childID file.ID
	if err := childTx.QueryRow(ctx, `
INSERT INTO core.files(storage,name,mime_type,size,checksum_sha256,path,parent_id)
VALUES('public',$1,'image/png',1,decode($2,'hex'),$3,$4) RETURNING id`,
		suffix+"-child.png", parent.ChecksumSHA256, suffix+"-child", parent.ID).Scan(&childID); err != nil {
		t.Fatal(err)
	}
	var childMediaID media.ID
	if err := childTx.QueryRow(ctx, `INSERT INTO core.media(file_id) VALUES($1) RETURNING id`, childID).Scan(&childMediaID); err != nil {
		t.Fatal(err)
	}

	deleteCtx, cancel := context.WithCancel(ctx)
	deleted := make(chan error, 1)
	finished := make(chan struct{})
	defer func() {
		cancel()
		<-finished
	}()
	go func() {
		defer close(finished)
		_, err := db.FileDeletions().DeleteMediaFile(deleteCtx, nil, media.DeleteFileInput{
			SiteID: 1, MediaID: parentMedia.ID, ExpectedFileID: parent.ID, ExpectedUpdatedAt: parentMedia.UpdatedAt,
		}, func(context.Context, *media.FileOccurrence) (media.PreparedFileOwner, error) {
			return media.PreparedFileOwner{}, nil
		})
		deleted <- err
	}()

	// Observe a real PostgreSQL barrier. Without the mutation lock the recursive
	// SELECT has already taken its snapshot and waits on the parent's key-share.
	var blockedQuery string
	deadline := time.Now().Add(5 * time.Second)
	for blockedQuery == "" {
		if err := conn.Pool().QueryRow(ctx, `
SELECT COALESCE((SELECT query FROM pg_stat_activity
WHERE $1=ANY(pg_blocking_pids(pid)) LIMIT 1),'')`, int32(childTx.Conn().PgConn().PID())).Scan(&blockedQuery); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-deleted:
			t.Fatalf("deletion did not wait for child transaction: %v", err)
		default:
		}
		if blockedQuery == "" {
			if time.Now().After(deadline) {
				t.Fatal("deletion did not reach the PostgreSQL lock barrier")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if err := childTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(blockedQuery, "core.filesystem.mutation") {
		t.Errorf("deletion took a lock before the filesystem mutation lock: %s", blockedQuery)
	}
	select {
	case err := <-deleted:
		if !errors.Is(err, media.ErrFileInUse) {
			t.Errorf("deletion ignored committed child's Media: %v; blocked query: %s", err, blockedQuery)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	for _, id := range []file.ID{parent.ID, childID} {
		if _, err := db.Files().FileByID(ctx, id); err != nil {
			t.Errorf("file %d was removed: %v", id, err)
		}
	}
	for _, id := range []media.ID{parentMedia.ID, childMediaID} {
		if _, err := db.Media().ByID(ctx, id); err != nil {
			t.Errorf("Media %d was removed: %v", id, err)
		}
	}
	var operations int
	if err := conn.Pool().QueryRow(ctx, `SELECT count(*) FROM core.media_file_deletions WHERE media_id=$1`, parentMedia.ID).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if operations != 0 {
		t.Error("rejected deletion queued physical cleanup")
	}
}
