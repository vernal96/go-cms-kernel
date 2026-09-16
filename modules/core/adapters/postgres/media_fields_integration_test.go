package postgres

import (
	"context"
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

func TestPostgresMediaFieldsOwnershipLifecycle(t *testing.T) {
	for _, storage := range []string{"tree", "library_item"} {
		for _, operation := range []string{"unchanged", "replace", "clear", "delete", "delete_subtree"} {
			t.Run(storage+"/"+operation, func(t *testing.T) {
				conn, db, ctx := openOutboxIntegrationDatabase(t)
				suffix := fmt.Sprint(time.Now().UnixNano())
				var siteID site.ID
				if err := conn.Pool().QueryRow(ctx, `INSERT INTO core.sites(profile_code,domain) VALUES('dev',$1) RETURNING id`, "field-owner-"+suffix+".test").Scan(&siteID); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					conn.Pool().Exec(context.Background(), `DELETE FROM core.sites WHERE id=$1`, siteID)
					conn.Pool().Exec(context.Background(), `DELETE FROM core.files WHERE path=$1`, suffix)
				})
				f, err := db.Files().CreateFile(ctx, file.File{Storage: "public", Name: suffix + ".png", Path: suffix, MIMEType: "image/png", Size: 1, ChecksumSHA256: fmt.Sprintf("%064d", 1)})
				if err != nil {
					t.Fatal(err)
				}
				old, err := db.Media().Create(ctx, nil, media.Media{FileID: f.ID, Params: map[string]any{}})
				if err != nil {
					t.Fatal(err)
				}
				fields := func(id media.ID) []field.StoredValue {
					return []field.StoredValue{{Key: "photo", Kind: field.StorageReference, ReferenceTarget: field.ReferenceMedia, Value: int64(id)}}
				}
				path := "/"
				root, err := db.Resources().Create(ctx, nil, resource.Resource{SiteID: siteID, Type: resourcetype.Library, Title: "Library", Path: &path, TypeSettings: map[string]any{"item_url_pattern": "/{slug}"}}, nil)
				if err != nil {
					t.Fatal(err)
				}
				var id resource.ID
				var tree resource.Resource
				var item resource.LibraryItem
				library := db.Resources().(resource.LibraryItemRepository)
				if storage == "tree" {
					childPath := "/child"
					tree, err = db.Resources().Create(ctx, nil, resource.Resource{SiteID: siteID, ParentID: &root.ID, Type: resourcetype.Page, Title: "Child", Slug: "child", Path: &childPath, FieldValues: fields(old.ID)}, nil)
					id = tree.ID
				} else {
					item, err = library.CreateLibraryItem(ctx, nil, resource.LibraryItem{SiteID: siteID, LibraryID: root.ID, Title: "Item", Slug: "item", FieldValues: fields(old.ID)}, false)
					id = item.ID
				}
				if err != nil {
					t.Fatal(err)
				}
				var replacement media.Media
				if operation == "replace" || operation == "clear" || operation == "unchanged" {
					var values []field.StoredValue
					if operation == "unchanged" {
						values = fields(old.ID)
					}
					if operation == "replace" {
						replacement, err = db.Media().Create(ctx, nil, media.Media{FileID: f.ID, Params: map[string]any{}})
						if err != nil {
							t.Fatal(err)
						}
						values = fields(replacement.ID)
					}
					if storage == "tree" {
						next := tree
						next.FieldValues = values
						_, err = db.Resources().Update(ctx, nil, tree, next, nil)
					} else {
						next := item
						next.FieldValues = values
						_, err = library.UpdateLibraryItem(ctx, nil, item, next, false)
					}
				} else if operation == "delete_subtree" {
					err = db.Resources().Delete(ctx, root.ID)
				} else if storage == "tree" {
					err = db.Resources().Delete(ctx, id)
				} else {
					err = library.DeleteLibraryItem(ctx, id)
				}
				if err != nil {
					t.Fatal(err)
				}
				if operation == "unchanged" {
					if _, err := db.Media().ByID(ctx, old.ID); err != nil {
						t.Fatal("unchanged active Media was removed", err)
					}
					if storage == "tree" {
						err = db.Resources().Delete(ctx, id)
					} else {
						err = library.DeleteLibraryItem(ctx, id)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				if _, err := db.Media().ByID(ctx, old.ID); !errors.Is(err, media.ErrNotFound) {
					t.Fatalf("detached Media survived: %v", err)
				}
				if _, err := db.Files().FileByID(ctx, f.ID); err != nil {
					t.Fatalf("source file was deleted: %v", err)
				}
				if replacement.ID != 0 {
					if _, err := db.Media().ByID(ctx, replacement.ID); err != nil {
						t.Fatal("active Media was deleted", err)
					}
					if storage == "tree" {
						err = db.Resources().Delete(ctx, id)
					} else {
						err = library.DeleteLibraryItem(ctx, id)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				if err := db.Files().DeleteFile(ctx, f.ID, func(context.Context, []file.File) error { return nil }); err != nil {
					t.Fatalf("file blocked after owner release: %v", err)
				}
			})
		}
	}
}
