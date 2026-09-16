package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/security"
)

func TestPostgresRepeaterPersistenceAndReferences(t *testing.T) {
	for _, storage := range []string{"tree", "library_item"} {
		t.Run(storage, func(t *testing.T) {
			conn, db, ctx := openOutboxIntegrationDatabase(t)
			suffix := fmt.Sprint(time.Now().UnixNano())
			var siteID site.ID
			if err := conn.Pool().QueryRow(ctx, `INSERT INTO core.sites(profile_code,domain) VALUES('dev',$1) RETURNING id`, "repeater-"+suffix+".test").Scan(&siteID); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				conn.Pool().Exec(context.Background(), `DELETE FROM core.sites WHERE id=$1`, siteID)
				conn.Pool().Exec(context.Background(), `DELETE FROM core.media WHERE file_id IN (SELECT id FROM core.files WHERE path=$1)`, suffix)
				conn.Pool().Exec(context.Background(), `DELETE FROM core.files WHERE path=$1`, suffix)
			})
			f, err := db.Files().CreateFile(ctx, file.File{Storage: "public", Name: suffix + ".png", Path: suffix, MIMEType: "image/png", Size: 1, ChecksumSHA256: fmt.Sprintf("%064d", 1)})
			if err != nil {
				t.Fatal(err)
			}
			makeMedia := func() media.ID {
				m, err := db.Media().Create(ctx, nil, media.Media{FileID: f.ID, Params: map[string]any{}})
				if err != nil {
					t.Fatal(err)
				}
				return m.ID
			}
			first, second := makeMedia(), makeMedia()
			third, fourth := makeMedia(), makeMedia()
			schema, err := field.CompilePersistent([]field.Definition{{Key: "slides", Type: field.TypeRepeater, Label: "Slides", Options: field.RepeaterOptions{Fields: []field.Definition{
				{Key: "gallery", Type: field.TypeMedia, Label: "Gallery", Options: field.MediaOptions{Multiple: true}},
				{Key: "title", Type: field.TypeString, Label: "Title"}, {Key: "image", Type: field.TypeMedia, Label: "Image"}, {Key: "file", Type: field.TypeFile, Label: "File"},
			}, MaxItems: 10}}}, field.StandardTypes())
			if err != nil {
				t.Fatal(err)
			}
			normalize := func(rows []any) (map[string]any, []field.StoredValue, map[string]file.ID) {
				values, err := schema.Validate(map[string]any{"slides": rows})
				if err != nil {
					t.Fatal(err)
				}
				stored, err := schema.StoredValues(values)
				if err != nil {
					t.Fatal(err)
				}
				refs, err := schema.FileReferences(values)
				if err != nil {
					t.Fatal(err)
				}
				files := map[string]file.ID{}
				for _, ref := range refs {
					files[ref.Key] = file.ID(ref.ID)
				}
				return values, stored, files
			}
			initial := []any{map[string]any{"title": "First", "image": int64(first), "file": int64(f.ID), "gallery": []any{int64(third), int64(fourth)}}, map[string]any{"title": "Second", "image": int64(second)}}
			values, stored, files := normalize(initial)
			path := "/"
			root, err := db.Resources().Create(ctx, nil, resource.Resource{SiteID: siteID, Type: resourcetype.Library, Title: "Library", Path: &path, TypeSettings: map[string]any{"item_url_pattern": "/{slug}"}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			var id resource.ID
			var read func() (map[string]any, []field.StoredValue)
			var update func([]any)
			var remove func() error
			if storage == "tree" {
				childPath := "/repeater"
				item, err := db.Resources().Create(ctx, nil, resource.Resource{SiteID: siteID, ParentID: &root.ID, Type: resourcetype.Page, Title: "Repeater", Slug: "repeater", Path: &childPath, Fields: values, FieldValues: stored, FileReferences: files}, nil)
				if err != nil {
					t.Fatal(err)
				}
				id = item.ID
				read = func() (map[string]any, []field.StoredValue) {
					item, err := db.Resources().ByID(ctx, id)
					if err != nil {
						t.Fatal(err)
					}
					return item.Fields, item.FieldValues
				}
				update = func(rows []any) {
					current, err := db.Resources().ByID(ctx, id)
					if err != nil {
						t.Fatal(err)
					}
					next := current
					next.Fields, next.FieldValues, next.FileReferences = normalize(rows)
					if _, err := db.Resources().Update(ctx, nil, current, next, nil); err != nil {
						t.Fatal(err)
					}
				}
				remove = func() error { return db.Resources().Delete(ctx, id) }
			} else {
				repo := db.Resources().(resource.LibraryItemRepository)
				item, err := repo.CreateLibraryItem(ctx, nil, resource.LibraryItem{SiteID: siteID, LibraryID: root.ID, Title: "Repeater", Slug: "repeater", Fields: values, FieldValues: stored, FileReferences: files}, false)
				if err != nil {
					t.Fatal(err)
				}
				id = item.ID
				read = func() (map[string]any, []field.StoredValue) {
					item, err := repo.LibraryItemByID(ctx, id)
					if err != nil {
						t.Fatal(err)
					}
					return item.Fields, item.FieldValues
				}
				update = func(rows []any) {
					current, err := repo.LibraryItemByID(ctx, id)
					if err != nil {
						t.Fatal(err)
					}
					next := current
					next.Fields, next.FieldValues, next.FileReferences = normalize(rows)
					if _, err := repo.UpdateLibraryItem(ctx, nil, current, next, false); err != nil {
						t.Fatal(err)
					}
				}
				remove = func() error { return repo.DeleteLibraryItem(ctx, id) }
			}
			assertRows := func(want []any) {
				got, persisted := read()
				a, _ := json.Marshal(got["slides"])
				b, _ := json.Marshal(want)
				if string(a) != string(b) {
					t.Fatalf("rows=%s want=%s", a, b)
				}
				if len(persisted) != 1 || persisted[0].Kind != field.StorageJSON {
					t.Fatalf("storage=%#v", persisted)
				}
			}
			assertRows(initial)
			var count int
			if err := conn.Pool().QueryRow(ctx, `SELECT count(*) FROM core.resource_field_values WHERE resource_id=$1`, id).Scan(&count); err != nil || count != 1 {
				t.Fatalf("EAV rows=%d err=%v", count, err)
			}
			var key string
			if err := conn.Pool().QueryRow(ctx, `SELECT field_key FROM core.file_field_references WHERE owner_kind='resource' AND owner_id=$1`, id).Scan(&key); err != nil || key != "slides[0].file" {
				t.Fatalf("file key=%s err=%v", key, err)
			}
			if err := db.Files().DeleteFile(ctx, f.ID, func(context.Context, []file.File) error { return nil }); err == nil {
				t.Fatal("referenced file deletion accepted")
			}
			reordered := []any{initial[1], initial[0]}
			update(reordered)
			assertRows(reordered)
			if err := conn.Pool().QueryRow(ctx, `SELECT field_key FROM core.file_field_references WHERE owner_kind='resource' AND owner_id=$1`, id).Scan(&key); err != nil || key != "slides[1].file" {
				t.Fatalf("reordered file key=%s err=%v", key, err)
			}
			// The same cascade used by confirmed filesystem deletion removes a nested
			// member atomically. Rollback restores both the JSON value and reference.
			cascade := db.Resources().(interface {
				ClearMediaReferences(context.Context, pgx.Tx, []int64, *security.UserID) error
			})
			tx, err := conn.Pool().Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := cascade.ClearMediaReferences(ctx, tx, []int64{int64(second), int64(third)}, nil); err != nil {
				tx.Rollback(ctx)
				t.Fatal(err)
			}
			if err := tx.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			assertRows(reordered)
			tx, err = conn.Pool().Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := cascade.ClearMediaReferences(ctx, tx, []int64{int64(second), int64(third)}, nil); err != nil {
				tx.Rollback(ctx)
				t.Fatal(err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			assertRows([]any{map[string]any{"title": "Second"}, map[string]any{"title": "First", "image": int64(first), "file": int64(f.ID), "gallery": []any{int64(fourth)}}})
			var remainingPath []string
			if err := conn.Pool().QueryRow(ctx, `SELECT value_path FROM core.resource_media_references WHERE resource_id=$1 AND media_id=$2`, id, fourth).Scan(&remainingPath); err != nil || fmt.Sprint(remainingPath) != "[1 gallery 0]" {
				t.Fatalf("remaining path %v: %v", remainingPath, err)
			}
			update([]any{map[string]any{"title": "Second"}})
			if _, err := db.Media().ByID(ctx, first); !errors.Is(err, media.ErrNotFound) {
				t.Fatalf("removed row retained media: %v", err)
			}
			if err := conn.Pool().QueryRow(ctx, `SELECT count(*) FROM core.file_field_references WHERE owner_kind='resource' AND owner_id=$1`, id).Scan(&count); err != nil || count != 0 {
				t.Fatalf("stale file refs=%d err=%v", count, err)
			}
			if err := remove(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
