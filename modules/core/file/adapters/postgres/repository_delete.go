package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/security"
)

func (r *Repository) DeleteFile(
	ctx context.Context,
	id file.ID,
	deletePhysical file.DeletePhysical,
) error {
	if ctx == nil {
		return errors.New("delete file context is nil")
	}
	if deletePhysical == nil {
		return errors.New("physical file deleter is nil")
	}
	tx, err := r.connector.Pool().BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := lockMutation(ctx, tx); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `
WITH RECURSIVE tree AS
(
    SELECT id
    FROM core.files
    WHERE id = $1
    UNION ALL
    SELECT child.id
    FROM core.files AS child
    JOIN tree AS parent ON child.parent_id = parent.id
)
SELECT
    item.id, item.folder_id, item.storage, item.name,
    item.mime_type, item.size, item.checksum_sha256,
    item.path, item.parent_id, item.created_at, item.updated_at,
    item.created_by, item.updated_by
FROM core.files AS item
JOIN tree ON tree.id = item.id
ORDER BY item.id
FOR UPDATE OF item;
`, id)
	if err != nil {
		return err
	}
	items, err := scanFiles(rows)
	rows.Close()
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return file.ErrNotFound
	}
	ids := make([]int64, len(items))
	for index, item := range items {
		ids[index] = int64(item.ID)
	}
	if err := ensureFilesUnused(ctx, tx, ids); err != nil {
		return err
	}
	if err := deletePhysical(ctx, items); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM core.files WHERE id = $1;`, id); err != nil {
		return translateError(err)
	}
	return tx.Commit(ctx)
}

func (r *Repository) DeleteFolder(
	ctx context.Context,
	id file.FolderID,
	deletePhysical file.DeletePhysical,
) error {
	if ctx == nil {
		return errors.New("delete file folder context is nil")
	}
	if deletePhysical == nil {
		return errors.New("physical file deleter is nil")
	}
	tx, err := r.connector.Pool().BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := lockMutation(ctx, tx); err != nil {
		return err
	}
	folderRows, err := tx.Query(ctx, `
WITH RECURSIVE tree AS
(
    SELECT id
    FROM core.file_folders
    WHERE id = $1
    UNION ALL
    SELECT child.id
    FROM core.file_folders AS child
    JOIN tree AS parent ON child.parent_id = parent.id
)
SELECT item.id
FROM core.file_folders AS item
JOIN tree ON tree.id = item.id
ORDER BY item.id
FOR UPDATE OF item;
`, id)
	if err != nil {
		return err
	}
	folderCount := 0
	for folderRows.Next() {
		var lockedID file.FolderID
		if err := folderRows.Scan(&lockedID); err != nil {
			folderRows.Close()
			return err
		}
		folderCount++
	}
	err = folderRows.Err()
	folderRows.Close()
	if err != nil {
		return err
	}
	if folderCount == 0 {
		return file.ErrFolderNotFound
	}

	rows, err := tx.Query(ctx, `
WITH RECURSIVE folder_tree AS
(
    SELECT id
    FROM core.file_folders
    WHERE id = $1
    UNION ALL
    SELECT child.id
    FROM core.file_folders AS child
    JOIN folder_tree AS parent ON child.parent_id = parent.id
),
file_tree AS
(
    SELECT item.id
    FROM core.files AS item
    WHERE item.folder_id IN (SELECT id FROM folder_tree)
    UNION
    SELECT child.id
    FROM core.files AS child
    JOIN file_tree AS parent ON child.parent_id = parent.id
)
SELECT
    item.id, item.folder_id, item.storage, item.name,
    item.mime_type, item.size, item.checksum_sha256,
    item.path, item.parent_id, item.created_at, item.updated_at,
    item.created_by, item.updated_by
FROM core.files AS item
JOIN file_tree ON file_tree.id = item.id
ORDER BY item.id
FOR UPDATE OF item;
`, id)
	if err != nil {
		return err
	}
	items, err := scanFiles(rows)
	rows.Close()
	if err != nil {
		return err
	}
	ids := make([]int64, len(items))
	for index, item := range items {
		ids[index] = int64(item.ID)
	}
	if err := ensureFilesUnused(ctx, tx, ids); err != nil {
		return err
	}
	if err := deletePhysical(ctx, items); err != nil {
		return err
	}
	if _, err := tx.Exec(
		ctx,
		`DELETE FROM core.file_folders WHERE id = $1;`,
		id,
	); err != nil {
		return translateError(err)
	}
	return tx.Commit(ctx)
}

func (r *Repository) DeleteItems(
	ctx context.Context,
	items []file.ItemReference,
	deletePhysical file.DeletePhysical,
) error {
	return r.deleteItems(ctx, nil, items, deletePhysical, "", nil)
}

func (r *Repository) DeleteImpact(ctx context.Context, items []file.ItemReference) (file.DeleteImpact, error) {
	var impact file.DeleteImpact
	err := r.deleteItems(ctx, nil, items, nil, "", &impact)
	return impact, err
}

func (r *Repository) DeleteConfirmed(ctx context.Context, actorID *security.UserID, items []file.ItemReference, token string, deletePhysical file.DeletePhysical) error {
	return r.deleteItems(ctx, actorID, items, deletePhysical, token, nil)
}

func (r *Repository) deleteItems(ctx context.Context, actorID *security.UserID, items []file.ItemReference, deletePhysical file.DeletePhysical, token string, preview *file.DeleteImpact) error {
	if ctx == nil {
		return errors.New("delete filesystem items context is nil")
	}
	if deletePhysical == nil && preview == nil {
		return errors.New("physical file deleter is nil")
	}
	fileIDs := make([]int64, 0)
	folderIDs := make([]int64, 0)
	for _, item := range items {
		if item.Kind == file.ItemFile {
			fileIDs = append(fileIDs, item.ID)
		} else if item.Kind == file.ItemFolder {
			folderIDs = append(folderIDs, item.ID)
		}
	}
	tx, err := r.connector.Pool().BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockMutation(ctx, tx); err != nil {
		return err
	}

	var selectedCount int
	if err := tx.QueryRow(ctx, `
SELECT
    (SELECT count(*) FROM core.files WHERE id = ANY($1::bigint[])) +
    (SELECT count(*) FROM core.file_folders WHERE id = ANY($2::bigint[]));
`, fileIDs, folderIDs).Scan(&selectedCount); err != nil {
		return err
	}
	if selectedCount != len(items) {
		return file.ErrNotFound
	}

	rows, err := tx.Query(ctx, `
WITH RECURSIVE folder_tree AS
(
    SELECT id FROM core.file_folders WHERE id = ANY($2::bigint[])
    UNION
    SELECT child.id
    FROM core.file_folders AS child
    JOIN folder_tree AS parent ON child.parent_id = parent.id
),
file_tree AS
(
    SELECT item.id
    FROM core.files AS item
    WHERE item.id = ANY($1::bigint[])
       OR item.folder_id IN (SELECT id FROM folder_tree)
    UNION
    SELECT child.id
    FROM core.files AS child
    JOIN file_tree AS parent ON child.parent_id = parent.id
)
SELECT item.id, item.folder_id, item.storage, item.name,
       item.mime_type, item.size, item.checksum_sha256,
       item.path, item.parent_id, item.created_at, item.updated_at,
       item.created_by, item.updated_by
FROM core.files AS item
JOIN file_tree ON file_tree.id = item.id
ORDER BY item.id
FOR UPDATE OF item;
`, fileIDs, folderIDs)
	if err != nil {
		return err
	}
	physical, err := scanFiles(rows)
	rows.Close()
	if err != nil {
		return err
	}
	ids := make([]int64, len(physical))
	for index, item := range physical {
		ids[index] = int64(item.ID)
	}

	impact := file.DeleteImpact{SelectedCount: len(items), TotalFiles: len(physical)}
	for _, item := range physical {
		if item.ParentID != nil {
			impact.DerivedFiles++
		}
	}
	var mediaIDs []int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(array_agg(id ORDER BY id),'{}'::bigint[]) FROM (SELECT id FROM core.media WHERE file_id=ANY($1::bigint[]) ORDER BY id FOR UPDATE) locked;`, ids).Scan(&mediaIDs); err != nil {
		return err
	}
	impact.MediaReferences = len(mediaIDs)
	// Owner rows must stay stable through physical deletion and FK clearing.
	// Acquire these locks before touching storage, so a competing move/edit
	// either completes before the snapshot or conflicts without data loss.
	var owners []string
	for kind, query := range []string{
		`SELECT id FROM core.resources WHERE image_media_id=ANY($1::bigint[]) OR id IN (SELECT resource_id FROM core.resource_media_references WHERE media_id=ANY($1::bigint[])) ORDER BY id FOR UPDATE`,
		`SELECT id FROM core.library_items WHERE image_media_id=ANY($1::bigint[]) OR id IN (SELECT resource_id FROM core.resource_media_references WHERE media_id=ANY($1::bigint[])) ORDER BY id FOR UPDATE`,
		`SELECT id FROM core.users WHERE avatar_media_id=ANY($1::bigint[]) ORDER BY id FOR UPDATE`,
	} {
		rows, err := tx.Query(ctx, query, mediaIDs)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			owners = append(owners, fmt.Sprintf("%d:%d", kind, id))
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	if err := tx.QueryRow(ctx, `SELECT COALESCE(array_agg(DISTINCT site_id ORDER BY site_id),'{}'::bigint[]) FROM (SELECT site_id FROM core.resources WHERE image_media_id=ANY($1::bigint[]) UNION SELECT site_id FROM core.library_items WHERE image_media_id=ANY($1::bigint[]) UNION SELECT e.site_id FROM core.resource_entities e JOIN core.resource_media_references mr ON mr.resource_id=e.id WHERE mr.media_id=ANY($1::bigint[])) owners;`, mediaIDs).Scan(&impact.ResourceSites); err != nil {
		return err
	}
	// The FileExplorer cascade does not own site, widget or optional-module
	// lifecycles. Their normalized occurrences must block it before bytes are
	// touched, rather than relying on a later RESTRICT foreign-key failure.
	if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM core.file_field_references WHERE media_id=ANY($1::bigint[])) + (SELECT count(*) FROM core.media_field_occurrences WHERE media_id=ANY($1::bigint[]));`, mediaIDs).Scan(&impact.FileFieldReferences); err != nil {
		return err
	}
	var mediaFields string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(string_agg(resource_id::text || ':' || field_key || ':' || position::text || ':' || value_path::text || ':' || media_id::text, ',' ORDER BY resource_id,field_key,position,value_path),'') FROM core.resource_media_references WHERE media_id=ANY($1::bigint[])`, mediaIDs).Scan(&mediaFields); err != nil {
		return err
	}
	impact.Token = fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%v/%v/%v/%v/%d/%s/%v", items, ids, mediaIDs, impact.ResourceSites, impact.FileFieldReferences, mediaFields, owners))))
	if preview != nil {
		*preview = impact
		return nil
	}
	if token != "" && token != impact.Token {
		return file.ErrConflict
	}
	if impact.FileFieldReferences > 0 || (token == "" && impact.MediaReferences > 0) {
		return file.ErrInUse
	}

	if token != "" {
		if err := r.cascade(ctx, tx, mediaIDs, actorID); err != nil {
			return err
		}
	}
	if err := deletePhysical(ctx, physical); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM core.files WHERE id = ANY($1::bigint[]);`, fileIDs); err != nil {
		return translateError(err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM core.file_folders WHERE id = ANY($1::bigint[]);`, folderIDs); err != nil {
		return translateError(err)
	}
	return tx.Commit(ctx)
}

const (
	sha256Size = 32
	fileSelect = `
SELECT
    id, folder_id, storage, name, mime_type, size,
    checksum_sha256, path, parent_id,
    created_at, updated_at, created_by, updated_by
FROM core.files
`
	namespaceQuery = `
SELECT EXISTS
(
    SELECT 1
    FROM core.file_folders
    WHERE storage = $1
      AND parent_id IS NOT DISTINCT FROM $2
      AND name = $3
      AND ($4::bigint IS NULL OR id <> $4)
    UNION ALL
    SELECT 1
    FROM core.files
    WHERE storage = $1
      AND folder_id IS NOT DISTINCT FROM $2
      AND name = $3
      AND ($5::bigint IS NULL OR id <> $5)
);
`
)
