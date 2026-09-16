package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
)

func TestPostgresMultipleFields(t *testing.T) {
	for _, kind := range []string{"tree", "library_item"} {
		t.Run(kind, func(t *testing.T) {
			conn, db, ctx := openOutboxIntegrationDatabase(t)
			suffix := fmt.Sprint(time.Now().UnixNano())
			var sid site.ID
			if err := conn.Pool().QueryRow(ctx, `INSERT INTO core.sites(profile_code,domain) VALUES('dev',$1) RETURNING id`, suffix+".test").Scan(&sid); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				conn.Pool().Exec(context.Background(), `DELETE FROM core.sites WHERE id=$1`, sid)
				conn.Pool().Exec(context.Background(), `DELETE FROM core.media WHERE file_id IN(SELECT id FROM core.files WHERE path=$1)`, suffix)
				conn.Pool().Exec(context.Background(), `DELETE FROM core.files WHERE path=$1`, suffix)
			})
			f, err := db.Files().CreateFile(ctx, file.File{Storage: "public", Name: suffix + ".png", Path: suffix, MIMEType: "image/png", Size: 1, ChecksumSHA256: fmt.Sprintf("%064d", 1)})
			if err != nil {
				t.Fatal(err)
			}
			first, err := db.Media().Create(ctx, nil, media.Media{FileID: f.ID, Params: map[string]any{}})
			if err != nil {
				t.Fatal(err)
			}
			second, err := db.Media().Create(ctx, nil, media.Media{FileID: f.ID, Params: map[string]any{}})
			if err != nil {
				t.Fatal(err)
			}
			schema, err := field.CompilePersistent([]field.Definition{
				{Key: "images", Label: "Images", Type: field.TypeMedia, Options: field.MediaOptions{Multiple: true}},
				{Key: "files", Label: "Files", Type: field.TypeFile, Options: field.FileOptions{Multiple: true}},
				{Key: "numbers", Label: "Numbers", Type: field.TypeInteger, Options: field.IntegerOptions{Multiple: true}},
			}, field.StandardTypes())
			if err != nil {
				t.Fatal(err)
			}
			normalize := func(images []any) (map[string]any, []field.StoredValue, map[string]file.ID) {
				values, err := schema.Validate(map[string]any{"images": images, "files": []any{int64(f.ID), int64(f.ID)}, "numbers": []any{int64(0), int64(7)}})
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
			initial := []any{int64(first.ID), int64(second.ID), int64(first.ID)}
			values, stored, files := normalize(initial)
			path := "/"
			root, err := db.Resources().Create(ctx, nil, resource.Resource{SiteID: sid, Type: resourcetype.Library, Title: "Root", Path: &path, TypeSettings: map[string]any{"item_url_pattern": "/{slug}"}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			var id resource.ID
			var update func([]any, bool)
			var read func() map[string]any
			if kind == "tree" {
				path := "/list"
				item, err := db.Resources().Create(ctx, nil, resource.Resource{SiteID: sid, ParentID: &root.ID, Type: resourcetype.Page, Title: "List", Slug: "list", Path: &path, Fields: values, FieldValues: stored, FileReferences: files}, nil)
				if err != nil {
					t.Fatal(err)
				}
				id = item.ID
				read = func() map[string]any {
					item, err := db.Resources().ByID(ctx, id)
					if err != nil {
						t.Fatal(err)
					}
					return item.Fields
				}
				update = func(images []any, restore bool) {
					current, err := db.Resources().ByID(ctx, id)
					if err != nil {
						t.Fatal(err)
					}
					next := current
					next.Fields, next.FieldValues, next.FileReferences = normalize(images)
					if restore {
						_, err = db.Resources().(resource.RevisionRepository).RestoreRevision(ctx, nil, current, next, 1)
					} else {
						_, err = db.Resources().Update(ctx, nil, current, next, nil)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
			} else {
				repo := db.Resources().(resource.LibraryItemRepository)
				item, err := repo.CreateLibraryItem(ctx, nil, resource.LibraryItem{SiteID: sid, LibraryID: root.ID, Title: "List", Slug: "list", Fields: values, FieldValues: stored, FileReferences: files}, true)
				if err != nil {
					t.Fatal(err)
				}
				id = item.ID
				read = func() map[string]any {
					item, err := repo.LibraryItemByID(ctx, id)
					if err != nil {
						t.Fatal(err)
					}
					return item.Fields
				}
				update = func(images []any, restore bool) {
					current, err := repo.LibraryItemByID(ctx, id)
					if err != nil {
						t.Fatal(err)
					}
					next := current
					next.Fields, next.FieldValues, next.FileReferences = normalize(images)
					if restore {
						_, err = db.Resources().(resource.RevisionRepository).RestoreLibraryItemRevision(ctx, nil, current, next, 1)
					} else {
						_, err = repo.UpdateLibraryItem(ctx, nil, current, next, true)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			assertImages := func(want []any) {
				got := read()
				a, _ := json.Marshal(got["images"])
				b, _ := json.Marshal(want)
				if string(a) != string(b) {
					t.Fatalf("images %s want %s", a, b)
				}
				numbers, _ := json.Marshal(got["numbers"])
				if string(numbers) != "[0,7]" {
					t.Fatalf("numbers %s", numbers)
				}
			}
			assertImages(initial)
			foreignPath := "/foreign"
			if _, err := db.Resources().Create(ctx, nil, resource.Resource{SiteID: sid, ParentID: &root.ID, Type: resourcetype.Page, Title: "Foreign", Slug: "foreign", Path: &foreignPath, Fields: values, FieldValues: stored, FileReferences: files}, nil); !errors.Is(err, media.ErrAlreadyAttached) {
				t.Fatalf("foreign media owner accepted: %v", err)
			}

			update([]any{int64(second.ID), int64(first.ID)}, false)
			assertImages([]any{int64(second.ID), int64(first.ID)})
			update(initial, true)
			assertImages(initial)
			update([]any{int64(first.ID)}, false)
			assertImages([]any{int64(first.ID)})
			if _, err := db.Media().ByID(ctx, first.ID); err != nil {
				t.Fatalf("remaining duplicate was deleted: %v", err)
			}
			if _, err := db.Media().ByID(ctx, second.ID); !errors.Is(err, media.ErrNotFound) {
				t.Fatalf("unused media remains: %v", err)
			}
			var count int
			if err := conn.Pool().QueryRow(ctx, `SELECT count(*) FROM core.resource_media_references WHERE resource_id=$1`, id).Scan(&count); err != nil || count != 1 {
				t.Fatalf("references %d: %v", count, err)
			}
			if err := conn.Pool().QueryRow(ctx, `SELECT count(*) FROM core.file_field_references WHERE owner_kind='resource' AND owner_id=$1`, id).Scan(&count); err != nil || count != 2 {
				t.Fatalf("file references %d: %v", count, err)
			}
		})
	}
}
