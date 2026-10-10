package postgres

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
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
    SELECT 1 FROM core.file_field_references r JOIN core.media m ON m.id=r.media_id WHERE m.file_id = ANY($1::bigint[])
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
