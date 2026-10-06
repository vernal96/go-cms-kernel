package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	connectorpostgres "github.com/vernal96/go-cms-kernel/connectors/postgres"
	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/security"
)

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
