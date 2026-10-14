package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
)

func TestPostgresMediaFileDeletionClearsResourceAndLibraryImage(t *testing.T) {
	for _, libraryItem := range []bool{false, true} {
		t.Run(fmt.Sprintf("library_item=%t", libraryItem), func(t *testing.T) {
			conn, db, ctx := openOutboxIntegrationDatabase(t)
			suffix := fmt.Sprint(time.Now().UnixNano())
			var sid int64
			if err := conn.Pool().QueryRow(ctx, `INSERT INTO core.sites(profile_code,name,domain) VALUES('dev','Image deletion',$1) RETURNING id`, "image-delete-"+suffix+".test").Scan(&sid); err != nil {
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
				conn.Pool().Exec(cleanup, `DELETE FROM core.media_field_occurrences WHERE media_id=$1`, m.ID)
				conn.Pool().Exec(cleanup, `DELETE FROM core.sites WHERE id=$1`, sid)
				conn.Pool().Exec(cleanup, `DELETE FROM core.media_file_deletions WHERE site_id=$1`, sid)
				conn.Pool().Exec(cleanup, `DELETE FROM core.files WHERE id=$1`, f.ID)
			})
			path := "/"
			root := resource.Resource{SiteID: site.ID(sid), Type: resourcetype.Library, Title: "Library", Path: &path, TypeSettings: map[string]any{"item_url_pattern": "/{slug}"}}
			if !libraryItem {
				root.ImageMediaID = &m.ID
			}
			owner, err := db.Resources().Create(ctx, nil, root, func(context.Context, media.ID) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			ownerID, beforeVersion := owner.ID, owner.Version
			if libraryItem {
				item, err := db.Resources().(resource.LibraryItemRepository).CreateLibraryItem(ctx, nil, resource.LibraryItem{SiteID: site.ID(sid), LibraryID: owner.ID, Title: "Item", Slug: "item", ImageMediaID: &m.ID}, true)
				if err != nil {
					t.Fatal(err)
				}
				ownerID, beforeVersion = item.ID, item.Version
			}
			input := media.DeleteFileInput{SiteID: sid, MediaID: m.ID, ExpectedFileID: f.ID, ExpectedUpdatedAt: m.UpdatedAt}
			clearing := db.FileOccurrenceOwners()[0]
			prepare := func(ctx context.Context, ref *media.FileOccurrence) (media.PreparedFileOwner, error) {
				if ref == nil || ref.OwnerID != int64(ownerID) || ref.Container != "image" || fmt.Sprint(ref.Path) != "[image_media_id]" {
					t.Fatalf("incorrect image owner: %+v", ref)
				}
				return media.PreparedFileOwner{Apply: func(ctx context.Context) (*media.ClearedFileReference, error) {
					version, err := clearing.ClearFileOccurrence(ctx, nil, *ref)
					return &media.ClearedFileReference{FileOccurrence: *ref, OwnerVersion: version}, err
				}}, nil
			}
			if _, err := conn.Pool().Exec(ctx, `INSERT INTO core.media_field_occurrences(owner_kind,owner_id,site_id,container,value_path,media_id,reference_target) VALUES('site',$1,$1,'settings',ARRAY['other'],$2,'media')`, sid, m.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.FileDeletions().DeleteMediaFile(ctx, nil, input, prepare); !errors.Is(err, media.ErrFileInUse) {
				t.Fatalf("other reference was not rejected: %v", err)
			}
			if _, err := conn.Pool().Exec(ctx, `DELETE FROM core.media_field_occurrences WHERE media_id=$1`, m.ID); err != nil {
				t.Fatal(err)
			}
			result, err := db.FileDeletions().DeleteMediaFile(ctx, nil, input, prepare)
			if err != nil {
				t.Fatal(err)
			}
			if result.ClearedReference == nil || result.ClearedReference.OwnerVersion != beforeVersion+1 {
				t.Fatalf("owner mutation skipped: %+v", result)
			}
			var savedImage *media.ID
			if libraryItem {
				item, err := db.Resources().(resource.LibraryItemRepository).LibraryItemByID(ctx, ownerID)
				if err != nil {
					t.Fatal(err)
				}
				savedImage = item.ImageMediaID
			} else {
				item, err := db.Resources().ByID(ctx, ownerID)
				if err != nil {
					t.Fatal(err)
				}
				savedImage = item.ImageMediaID
			}
			if savedImage != nil {
				t.Fatal("saved image reference survived")
			}
			if _, err := db.Media().ByID(ctx, m.ID); !errors.Is(err, media.ErrNotFound) {
				t.Fatalf("Media survived: %v", err)
			}
			if _, err := db.Files().FileByID(ctx, f.ID); !errors.Is(err, file.ErrNotFound) {
				t.Fatalf("File survived: %v", err)
			}
			var revisions int
			expectedRevisions := 1
			if libraryItem && !resource.DefaultRevisionPolicy().LibraryItems {
				expectedRevisions = 0
			}
			if err := conn.Pool().QueryRow(ctx, `SELECT count(*) FROM core.resource_revisions WHERE resource_id=$1 AND version=$2`, ownerID, beforeVersion+1).Scan(&revisions); err != nil || revisions != expectedRevisions {
				t.Fatalf("image deletion revision=%d: %v", revisions, err)
			}
			var events int
			if err := conn.Pool().QueryRow(ctx, `SELECT count(*) FROM core.outbox_messages WHERE topic=$1 AND message_key=convert_to($2,'UTF8')`, resource.EventUpdated, fmt.Sprint(ownerID)).Scan(&events); err != nil || events != 1 {
				t.Fatalf("image deletion event=%d: %v", events, err)
			}
		})
	}
}
