package postgres

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/security"
)

func (r *Repository) CreateFile(
	ctx context.Context,
	item file.File,
) (file.File, error) {
	if ctx == nil {
		return file.File{}, errors.New("create file context is nil")
	}
	checksum, err := hex.DecodeString(item.ChecksumSHA256)
	if err != nil || len(checksum) != sha256Size {
		return file.File{}, errors.New("file checksum SHA-256 is invalid")
	}

	tx, err := r.connector.Pool().BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return file.File{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := lockMutation(ctx, tx); err != nil {
		return file.File{}, err
	}
	if err := lockNamespace(ctx, tx, item.Storage, item.FolderID, item.Name); err != nil {
		return file.File{}, err
	}
	if err := ensureNamespaceAvailable(
		ctx, tx, item.Storage, item.FolderID, item.Name, nil, nil,
	); err != nil {
		return file.File{}, err
	}

	result, err := scanFile(tx.QueryRow(ctx, `
INSERT INTO core.files
(
    folder_id, storage, name, mime_type, size,
    checksum_sha256, path, parent_id, created_by, updated_by
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING
    id, folder_id, storage, name, mime_type, size,
    checksum_sha256, path, parent_id,
    created_at, updated_at, created_by, updated_by;
`,
		item.FolderID,
		item.Storage,
		item.Name,
		item.MIMEType,
		item.Size,
		checksum,
		item.Path,
		item.ParentID,
		item.CreatedBy,
		item.UpdatedBy,
	))
	if err != nil {
		return file.File{}, translateError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return file.File{}, translateError(err)
	}
	return result, nil
}

func (r *Repository) CreateAvailableFile(
	ctx context.Context,
	item file.File,
) (file.File, error) {
	if ctx == nil {
		return file.File{}, errors.New("create available file context is nil")
	}
	checksum, err := hex.DecodeString(item.ChecksumSHA256)
	if err != nil || len(checksum) != sha256Size {
		return file.File{}, errors.New("file checksum SHA-256 is invalid")
	}
	tx, err := r.connector.Pool().BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return file.File{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockMutation(ctx, tx); err != nil {
		return file.File{}, err
	}
	item.Name, err = availableName(ctx, tx, item.Storage, item.FolderID, item.Name, true, nil, nil)
	if err != nil {
		return file.File{}, err
	}
	result, err := insertFile(ctx, tx, item, checksum)
	if err != nil {
		return file.File{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return file.File{}, translateError(err)
	}
	return result, nil
}

func (r *Repository) FileByID(
	ctx context.Context,
	id file.ID,
) (file.File, error) {
	if ctx == nil {
		return file.File{}, errors.New("get file context is nil")
	}
	result, err := scanFile(r.connector.Pool().QueryRow(ctx, fileSelect+`
WHERE id = $1;
`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return file.File{}, file.ErrNotFound
	}
	if err != nil {
		return file.File{}, fmt.Errorf("query file %d: %w", id, err)
	}
	return result, nil
}

func (r *Repository) ListFiles(
	ctx context.Context,
	storage filesystem.Code,
	folderID *file.FolderID,
) ([]file.File, error) {
	if ctx == nil {
		return nil, errors.New("list files context is nil")
	}
	rows, err := r.connector.Pool().Query(ctx, fileSelect+`
WHERE storage = $1
  AND folder_id IS NOT DISTINCT FROM $2
 AND parent_id IS NULL
ORDER BY name, id;
`, storage, folderID)
	if err != nil {
		return nil, fmt.Errorf("query files: %w", err)
	}
	defer rows.Close()
	return scanFiles(rows)
}

func (r *Repository) MoveFile(
	ctx context.Context,
	actorID *security.UserID,
	id file.ID,
	folderID *file.FolderID,
) (file.File, error) {
	if ctx == nil {
		return file.File{}, errors.New("move file context is nil")
	}
	tx, err := r.connector.Pool().BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return file.File{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := lockMutation(ctx, tx); err != nil {
		return file.File{}, err
	}
	current, err := scanFile(tx.QueryRow(ctx, fileSelect+`
WHERE id = $1
FOR UPDATE;
`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return file.File{}, file.ErrNotFound
	}
	if err != nil {
		return file.File{}, err
	}
	if err := lockNamespace(ctx, tx, current.Storage, folderID, current.Name); err != nil {
		return file.File{}, err
	}
	if err := ensureNamespaceAvailable(
		ctx, tx, current.Storage, folderID, current.Name, nil, &id,
	); err != nil {
		return file.File{}, err
	}

	result, err := scanFile(tx.QueryRow(ctx, `
UPDATE core.files
SET
    folder_id = $2,
    updated_at = now(),
    updated_by = $3
WHERE id = $1
RETURNING
    id, folder_id, storage, name, mime_type, size,
    checksum_sha256, path, parent_id,
    created_at, updated_at, created_by, updated_by;
`, id, folderID, actorID))
	if err != nil {
		return file.File{}, translateError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return file.File{}, translateError(err)
	}
	return result, nil
}

func (r *Repository) MoveFolder(
	ctx context.Context,
	actorID *security.UserID,
	id file.FolderID,
	parentID *file.FolderID,
) (file.Folder, error) {
	if ctx == nil {
		return file.Folder{}, errors.New("move file folder context is nil")
	}
	tx, err := r.connector.Pool().BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return file.Folder{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := lockMutation(ctx, tx); err != nil {
		return file.Folder{}, err
	}
	current, err := scanFolder(tx.QueryRow(ctx, `
SELECT
    id, parent_id, storage, name,
    created_at, updated_at, created_by, updated_by
FROM core.file_folders
WHERE id = $1
FOR UPDATE;
`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return file.Folder{}, file.ErrFolderNotFound
	}
	if err != nil {
		return file.Folder{}, err
	}
	if parentID != nil {
		var createsCycle bool
		if err := tx.QueryRow(ctx, `
WITH RECURSIVE ancestors AS
(
    SELECT id, parent_id
    FROM core.file_folders
    WHERE id = $2
    UNION ALL
    SELECT parent.id, parent.parent_id
    FROM core.file_folders AS parent
    JOIN ancestors AS child ON child.parent_id = parent.id
)
SELECT EXISTS (SELECT 1 FROM ancestors WHERE id = $1);
`, id, parentID).Scan(&createsCycle); err != nil {
			return file.Folder{}, err
		}
		if createsCycle {
			return file.Folder{}, file.ErrInvalidTree
		}
	}
	if err := lockNamespace(
		ctx, tx, current.Storage, parentID, current.Name,
	); err != nil {
		return file.Folder{}, err
	}
	if err := ensureNamespaceAvailable(
		ctx, tx, current.Storage, parentID, current.Name, &id, nil,
	); err != nil {
		return file.Folder{}, err
	}

	result, err := scanFolder(tx.QueryRow(ctx, `
UPDATE core.file_folders
SET
    parent_id = $2,
    updated_at = now(),
    updated_by = $3
WHERE id = $1
RETURNING
    id, parent_id, storage, name,
    created_at, updated_at, created_by, updated_by;
`, id, parentID, actorID))
	if err != nil {
		return file.Folder{}, translateError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return file.Folder{}, translateError(err)
	}
	return result, nil
}

func (r *Repository) RenameFile(
	ctx context.Context,
	actorID *security.UserID,
	id file.ID,
	name string,
) (file.File, error) {
	if ctx == nil {
		return file.File{}, errors.New("rename file context is nil")
	}
	tx, err := r.connector.Pool().BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return file.File{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockMutation(ctx, tx); err != nil {
		return file.File{}, err
	}
	current, err := lockedFile(ctx, tx, id)
	if err != nil {
		return file.File{}, err
	}
	name, err = availableName(ctx, tx, current.Storage, current.FolderID, name, true, nil, &id)
	if err != nil {
		return file.File{}, err
	}
	result, err := scanFile(tx.QueryRow(ctx, `
UPDATE core.files SET name = $2, updated_at = now(), updated_by = $3
WHERE id = $1
RETURNING id, folder_id, storage, name, mime_type, size,
          checksum_sha256, path, parent_id, created_at, updated_at,
          created_by, updated_by;
`, id, name, actorID))
	if err != nil {
		return file.File{}, translateError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return file.File{}, translateError(err)
	}
	return result, nil
}

func (r *Repository) RenameFolder(
	ctx context.Context,
	actorID *security.UserID,
	id file.FolderID,
	name string,
) (file.Folder, error) {
	if ctx == nil {
		return file.Folder{}, errors.New("rename file folder context is nil")
	}
	tx, err := r.connector.Pool().BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return file.Folder{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockMutation(ctx, tx); err != nil {
		return file.Folder{}, err
	}
	current, err := lockedFolder(ctx, tx, id)
	if err != nil {
		return file.Folder{}, err
	}
	name, err = availableName(ctx, tx, current.Storage, current.ParentID, name, false, &id, nil)
	if err != nil {
		return file.Folder{}, err
	}
	result, err := scanFolder(tx.QueryRow(ctx, `
UPDATE core.file_folders SET name = $2, updated_at = now(), updated_by = $3
WHERE id = $1
RETURNING id, parent_id, storage, name,
          created_at, updated_at, created_by, updated_by;
`, id, name, actorID))
	if err != nil {
		return file.Folder{}, translateError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return file.Folder{}, translateError(err)
	}
	return result, nil
}

func (r *Repository) MoveItems(
	ctx context.Context,
	actorID *security.UserID,
	input file.MoveItemsInput,
) ([]file.Folder, []file.File, error) {
	if ctx == nil {
		return nil, nil, errors.New("move filesystem items context is nil")
	}
	tx, err := r.connector.Pool().BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockMutation(ctx, tx); err != nil {
		return nil, nil, err
	}

	folders := make([]file.Folder, 0)
	files := make([]file.File, 0)
	for _, reference := range input.Items {
		switch reference.Kind {
		case file.ItemFolder:
			id := file.FolderID(reference.ID)
			current, err := lockedFolder(ctx, tx, id)
			if err != nil {
				return nil, nil, err
			}
			if current.Storage != input.Storage {
				return nil, nil, file.ErrStorageMismatch
			}
			if input.FolderID != nil {
				var cycle bool
				if err := tx.QueryRow(ctx, `
WITH RECURSIVE ancestors AS
(
    SELECT id, parent_id FROM core.file_folders WHERE id = $2
    UNION ALL
    SELECT parent.id, parent.parent_id
    FROM core.file_folders AS parent
    JOIN ancestors AS child ON child.parent_id = parent.id
)
SELECT EXISTS (SELECT 1 FROM ancestors WHERE id = $1);
`, id, input.FolderID).Scan(&cycle); err != nil {
					return nil, nil, err
				}
				if cycle {
					return nil, nil, file.ErrInvalidTree
				}
			}
			name, err := availableName(ctx, tx, current.Storage, input.FolderID, current.Name, false, &id, nil)
			if err != nil {
				return nil, nil, err
			}
			moved, err := scanFolder(tx.QueryRow(ctx, `
UPDATE core.file_folders
SET parent_id = $2, name = $3, updated_at = now(), updated_by = $4
WHERE id = $1
RETURNING id, parent_id, storage, name,
          created_at, updated_at, created_by, updated_by;
`, id, input.FolderID, name, actorID))
			if err != nil {
				return nil, nil, translateError(err)
			}
			folders = append(folders, moved)

		case file.ItemFile:
			id := file.ID(reference.ID)
			current, err := lockedFile(ctx, tx, id)
			if err != nil {
				return nil, nil, err
			}
			if current.Storage != input.Storage {
				return nil, nil, file.ErrStorageMismatch
			}
			name, err := availableName(ctx, tx, current.Storage, input.FolderID, current.Name, true, nil, &id)
			if err != nil {
				return nil, nil, err
			}
			moved, err := scanFile(tx.QueryRow(ctx, `
UPDATE core.files
SET folder_id = $2, name = $3, updated_at = now(), updated_by = $4
WHERE id = $1
RETURNING id, folder_id, storage, name, mime_type, size,
          checksum_sha256, path, parent_id, created_at, updated_at,
          created_by, updated_by;
`, id, input.FolderID, name, actorID))
			if err != nil {
				return nil, nil, translateError(err)
			}
			files = append(files, moved)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, translateError(err)
	}
	return folders, files, nil
}
