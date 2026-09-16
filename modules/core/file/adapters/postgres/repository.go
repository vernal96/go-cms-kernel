package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	connectorpostgres "github.com/vernal96/go-cms-kernel/connectors/postgres"
	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/security"
)

// MediaCascade updates owners in the file deletion transaction before storage is touched.
type MediaCascade func(context.Context, pgx.Tx, []int64, *security.UserID) error

type Repository struct {
	connector *connectorpostgres.Connector
	cascade   MediaCascade
}

func NewRepository(
	connector *connectorpostgres.Connector,
	cascade MediaCascade,
) (*Repository, error) {
	if connector == nil {
		return nil, errors.New("postgres connector is nil")
	}
	if connector.Pool() == nil {
		return nil, errors.New("postgres pool is nil")
	}
	if cascade == nil {
		return nil, errors.New("media cascade is nil")
	}
	return &Repository{connector: connector, cascade: cascade}, nil
}

func (r *Repository) NameAvailable(
	ctx context.Context,
	storage filesystem.Code,
	folderID *file.FolderID,
	name string,
) error {
	if ctx == nil {
		return errors.New("file namespace context is nil")
	}
	var exists bool
	if err := r.connector.Pool().QueryRow(ctx, namespaceQuery,
		storage,
		folderID,
		name,
		nil,
		nil,
	).Scan(&exists); err != nil {
		return fmt.Errorf("query file namespace: %w", err)
	}
	if exists {
		return file.ErrConflict
	}
	return nil
}

func (r *Repository) CreateFolder(
	ctx context.Context,
	item file.Folder,
) (file.Folder, error) {
	if ctx == nil {
		return file.Folder{}, errors.New("create file folder context is nil")
	}
	tx, err := r.connector.Pool().BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return file.Folder{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := lockMutation(ctx, tx); err != nil {
		return file.Folder{}, err
	}
	if err := lockNamespace(ctx, tx, item.Storage, item.ParentID, item.Name); err != nil {
		return file.Folder{}, err
	}
	if err := ensureNamespaceAvailable(
		ctx, tx, item.Storage, item.ParentID, item.Name, nil, nil,
	); err != nil {
		return file.Folder{}, err
	}

	result, err := scanFolder(tx.QueryRow(ctx, `
INSERT INTO core.file_folders
    (parent_id, storage, name, created_by, updated_by)
VALUES ($1, $2, $3, $4, $5)
RETURNING
    id, parent_id, storage, name,
    created_at, updated_at, created_by, updated_by;
`,
		item.ParentID,
		item.Storage,
		item.Name,
		item.CreatedBy,
		item.UpdatedBy,
	))
	if err != nil {
		return file.Folder{}, translateError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return file.Folder{}, translateError(err)
	}
	return result, nil
}

func (r *Repository) CreateAvailableFolder(
	ctx context.Context,
	item file.Folder,
) (file.Folder, error) {
	if ctx == nil {
		return file.Folder{}, errors.New("create available file folder context is nil")
	}
	tx, err := r.connector.Pool().BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return file.Folder{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockMutation(ctx, tx); err != nil {
		return file.Folder{}, err
	}
	item.Name, err = availableName(ctx, tx, item.Storage, item.ParentID, item.Name, false, nil, nil)
	if err != nil {
		return file.Folder{}, err
	}
	result, err := insertFolder(ctx, tx, item)
	if err != nil {
		return file.Folder{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return file.Folder{}, translateError(err)
	}
	return result, nil
}

func (r *Repository) FolderByID(
	ctx context.Context,
	id file.FolderID,
) (file.Folder, error) {
	if ctx == nil {
		return file.Folder{}, errors.New("get file folder context is nil")
	}
	result, err := scanFolder(r.connector.Pool().QueryRow(ctx, `
SELECT
    id, parent_id, storage, name,
    created_at, updated_at, created_by, updated_by
FROM core.file_folders
WHERE id = $1;
`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return file.Folder{}, file.ErrFolderNotFound
	}
	if err != nil {
		return file.Folder{}, fmt.Errorf("query file folder %d: %w", id, err)
	}
	return result, nil
}

func (r *Repository) ListFolders(
	ctx context.Context,
	storage filesystem.Code,
	parentID *file.FolderID,
) ([]file.Folder, error) {
	if ctx == nil {
		return nil, errors.New("list file folders context is nil")
	}
	rows, err := r.connector.Pool().Query(ctx, `
SELECT
    id, parent_id, storage, name,
    created_at, updated_at, created_by, updated_by
FROM core.file_folders
WHERE storage = $1
  AND parent_id IS NOT DISTINCT FROM $2
ORDER BY name, id;
`, storage, parentID)
	if err != nil {
		return nil, fmt.Errorf("query file folders: %w", err)
	}
	defer rows.Close()

	var result []file.Folder
	for rows.Next() {
		item, err := scanFolder(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (r *Repository) FolderAncestors(
	ctx context.Context,
	id file.FolderID,
) ([]file.Folder, error) {
	if ctx == nil {
		return nil, errors.New("list file folder ancestors context is nil")
	}
	rows, err := r.connector.Pool().Query(ctx, `
WITH RECURSIVE ancestors AS
(
    SELECT item.*, 0 AS depth
    FROM core.file_folders AS item
    WHERE item.id = $1
    UNION ALL
    SELECT parent.*, child.depth + 1
    FROM core.file_folders AS parent
    JOIN ancestors AS child ON child.parent_id = parent.id
)
SELECT id, parent_id, storage, name,
       created_at, updated_at, created_by, updated_by
FROM ancestors
ORDER BY depth DESC;
`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []file.Folder
	for rows.Next() {
		item, err := scanFolder(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, file.ErrFolderNotFound
	}
	return result, nil
}

func (r *Repository) ListFolderEntries(
	ctx context.Context,
	storage filesystem.Code,
	parentID *file.FolderID,
) ([]file.FolderEntry, error) {
	if ctx == nil {
		return nil, errors.New("list file folder entries context is nil")
	}
	rows, err := r.connector.Pool().Query(ctx, `
SELECT
    folder.id, folder.parent_id, folder.storage, folder.name,
    folder.created_at, folder.updated_at, folder.created_by, folder.updated_by,
    (SELECT count(*) FROM core.file_folders AS child WHERE child.parent_id = folder.id) +
    (SELECT count(*) FROM core.files AS child WHERE child.folder_id = folder.id AND child.parent_id IS NULL)
FROM core.file_folders AS folder
WHERE folder.storage = $1
  AND folder.parent_id IS NOT DISTINCT FROM $2
ORDER BY folder.name, folder.id;
`, storage, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []file.FolderEntry
	for rows.Next() {
		var entry file.FolderEntry
		var parent *int64
		if err := rows.Scan(
			&entry.Folder.ID, &parent, &entry.Folder.Storage, &entry.Folder.Name,
			&entry.Folder.CreatedAt, &entry.Folder.UpdatedAt,
			&entry.Folder.CreatedBy, &entry.Folder.UpdatedBy, &entry.ItemCount,
		); err != nil {
			return nil, err
		}
		if parent != nil {
			value := file.FolderID(*parent)
			entry.Folder.ParentID = &value
		}
		result = append(result, entry)
	}
	return result, rows.Err()
}

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
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM core.file_field_references WHERE file_id=ANY($1::bigint[]);`, ids).Scan(&impact.FileFieldReferences); err != nil {
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

type rowScanner interface {
	Scan(...any) error
}

func scanFolder(scanner rowScanner) (file.Folder, error) {
	var (
		result   file.Folder
		parentID *int64
	)
	if err := scanner.Scan(
		&result.ID,
		&parentID,
		&result.Storage,
		&result.Name,
		&result.CreatedAt,
		&result.UpdatedAt,
		&result.CreatedBy,
		&result.UpdatedBy,
	); err != nil {
		return file.Folder{}, err
	}
	if parentID != nil {
		value := file.FolderID(*parentID)
		result.ParentID = &value
	}
	return result, nil
}

func scanFile(scanner rowScanner) (file.File, error) {
	var (
		result   file.File
		folderID *int64
		parentID *int64
		checksum []byte
	)
	if err := scanner.Scan(
		&result.ID,
		&folderID,
		&result.Storage,
		&result.Name,
		&result.MIMEType,
		&result.Size,
		&checksum,
		&result.Path,
		&parentID,
		&result.CreatedAt,
		&result.UpdatedAt,
		&result.CreatedBy,
		&result.UpdatedBy,
	); err != nil {
		return file.File{}, err
	}
	if len(checksum) != sha256Size {
		return file.File{}, errors.New("stored file checksum SHA-256 is invalid")
	}
	result.ChecksumSHA256 = hex.EncodeToString(checksum)
	if folderID != nil {
		value := file.FolderID(*folderID)
		result.FolderID = &value
	}
	if parentID != nil {
		value := file.ID(*parentID)
		result.ParentID = &value
	}
	return result, nil
}

func scanFiles(rows pgx.Rows) ([]file.File, error) {
	var result []file.File
	for rows.Next() {
		item, err := scanFile(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func insertFolder(ctx context.Context, tx pgx.Tx, item file.Folder) (file.Folder, error) {
	result, err := scanFolder(tx.QueryRow(ctx, `
INSERT INTO core.file_folders
    (parent_id, storage, name, created_by, updated_by)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, parent_id, storage, name,
          created_at, updated_at, created_by, updated_by;
`, item.ParentID, item.Storage, item.Name, item.CreatedBy, item.UpdatedBy))
	if err != nil {
		return file.Folder{}, translateError(err)
	}
	return result, nil
}

func insertFile(
	ctx context.Context,
	tx pgx.Tx,
	item file.File,
	checksum []byte,
) (file.File, error) {
	result, err := scanFile(tx.QueryRow(ctx, `
INSERT INTO core.files
(
    folder_id, storage, name, mime_type, size,
    checksum_sha256, path, parent_id, created_by, updated_by
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING id, folder_id, storage, name, mime_type, size,
          checksum_sha256, path, parent_id,
          created_at, updated_at, created_by, updated_by;
`, item.FolderID, item.Storage, item.Name, item.MIMEType, item.Size,
		checksum, item.Path, item.ParentID, item.CreatedBy, item.UpdatedBy))
	if err != nil {
		return file.File{}, translateError(err)
	}
	return result, nil
}

func lockedFolder(ctx context.Context, tx pgx.Tx, id file.FolderID) (file.Folder, error) {
	result, err := scanFolder(tx.QueryRow(ctx, `
SELECT id, parent_id, storage, name,
       created_at, updated_at, created_by, updated_by
FROM core.file_folders WHERE id = $1 FOR UPDATE;
`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return file.Folder{}, file.ErrFolderNotFound
	}
	return result, err
}

func lockedFile(ctx context.Context, tx pgx.Tx, id file.ID) (file.File, error) {
	result, err := scanFile(tx.QueryRow(ctx, fileSelect+`WHERE id = $1 FOR UPDATE;`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return file.File{}, file.ErrNotFound
	}
	return result, err
}

func availableName(
	ctx context.Context,
	tx pgx.Tx,
	storage filesystem.Code,
	parentID *file.FolderID,
	original string,
	isFile bool,
	excludeFolder *file.FolderID,
	excludeFile *file.ID,
) (string, error) {
	extension := ""
	base := original
	if isFile {
		extension = filepath.Ext(original)
		base = original[:len(original)-len(extension)]
	}
	for index := 0; ; index++ {
		candidate := original
		if index > 0 {
			candidate = fmt.Sprintf("%s (%d)%s", base, index, extension)
		}
		var exists bool
		if err := tx.QueryRow(ctx, namespaceQuery,
			storage, parentID, candidate, excludeFolder, excludeFile,
		).Scan(&exists); err != nil {
			return "", err
		}
		if !exists {
			return candidate, nil
		}
	}
}

func ensureFilesUnused(ctx context.Context, tx pgx.Tx, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	var used bool
	if err := tx.QueryRow(ctx, `
SELECT EXISTS (
    SELECT 1 FROM core.media WHERE file_id = ANY($1::bigint[])
    UNION ALL
    SELECT 1 FROM core.file_field_references WHERE file_id = ANY($1::bigint[])
);
`, ids).Scan(&used); err != nil {
		return err
	}
	if used {
		return file.ErrInUse
	}
	return nil
}

func lockNamespace(
	ctx context.Context,
	tx pgx.Tx,
	storage filesystem.Code,
	parentID *file.FolderID,
	name string,
) error {
	parent := "root"
	if parentID != nil {
		parent = strconv.FormatInt(int64(*parentID), 10)
	}
	key := string(storage) + "\x1f" + parent + "\x1f" + name
	if _, err := tx.Exec(
		ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1, 0));`,
		key,
	); err != nil {
		return fmt.Errorf("lock file namespace: %w", err)
	}
	return nil
}

func lockMutation(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(
		ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended('core.filesystem.mutation', 0));`,
	); err != nil {
		return fmt.Errorf("lock filesystem mutation: %w", err)
	}
	return nil
}

func ensureNamespaceAvailable(
	ctx context.Context,
	tx pgx.Tx,
	storage filesystem.Code,
	parentID *file.FolderID,
	name string,
	excludeFolder *file.FolderID,
	excludeFile *file.ID,
) error {
	var exists bool
	if err := tx.QueryRow(
		ctx,
		namespaceQuery,
		storage,
		parentID,
		name,
		excludeFolder,
		excludeFile,
	).Scan(&exists); err != nil {
		return fmt.Errorf("query file namespace: %w", err)
	}
	if exists {
		return file.ErrConflict
	}
	return nil
}

func translateError(err error) error {
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) {
		return err
	}
	switch postgresError.Code {
	case pgerrcode.UniqueViolation:
		return file.ErrConflict
	case pgerrcode.ForeignKeyViolation:
		return file.ErrInvalidReference
	default:
		return err
	}
}

var _ file.Repository = (*Repository)(nil)
