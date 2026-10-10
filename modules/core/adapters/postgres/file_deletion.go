package postgres

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vernal96/go-cms-kernel/modules/core/adapters/postgres/medialock"
	"github.com/vernal96/go-cms-kernel/modules/core/adapters/postgres/mediaoccurrence"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	resourcepostgres "github.com/vernal96/go-cms-kernel/modules/core/resource/adapters/postgres"
	"github.com/vernal96/go-cms-kernel/security"
)

type fileDeletionRepository struct {
	pool      *pgxpool.Pool
	resources *resourcepostgres.Repository
}

func (d *Database) FileDeletions() media.FileDeletionRepository {
	return &fileDeletionRepository{pool: d.connector.Pool(), resources: d.resources.(*resourcepostgres.Repository)}
}

func (d *Database) FileOccurrenceOwners() []media.FileOccurrenceOwner {
	return []media.FileOccurrenceOwner{d.resources.(*resourcepostgres.Repository)}
}

func (r *fileDeletionRepository) DeleteMediaFile(ctx context.Context, actor *security.UserID, input media.DeleteFileInput, prepare media.PrepareFileOwner) (result media.FileDeletion, resultErr error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	// Match FileExplorer's lock order and finish in-flight child creation before
	// taking the recursive tree snapshot used by guards and physical cleanup.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('core.filesystem.mutation', 0));`); err != nil {
		return result, fmt.Errorf("lock filesystem mutation: %w", err)
	}
	if err := medialock.Lock(ctx, tx, input.MediaID); err != nil {
		return result, err
	}
	var currentFile file.ID
	var updated time.Time
	if err := tx.QueryRow(ctx, `SELECT file_id,updated_at FROM core.media WHERE id=$1 FOR UPDATE`, input.MediaID).Scan(&currentFile, &updated); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return result, media.ErrNotFound
		}
		return result, err
	}
	if currentFile != input.ExpectedFileID || !updated.Equal(input.ExpectedUpdatedAt) {
		return result, media.ErrFileDeleteConflict
	}
	rows, err := tx.Query(ctx, `WITH RECURSIVE tree AS (SELECT id FROM core.files WHERE id=$1 UNION ALL SELECT f.id FROM core.files f JOIN tree t ON f.parent_id=t.id) SELECT f.id,f.storage,f.path FROM core.files f JOIN tree t ON t.id=f.id ORDER BY f.id FOR UPDATE OF f`, currentFile)
	if err != nil {
		return result, err
	}
	var files []file.File
	for rows.Next() {
		var f file.File
		if err := rows.Scan(&f.ID, &f.Storage, &f.Path); err != nil {
			rows.Close()
			return result, err
		}
		files = append(files, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return result, err
	}
	ids := make([]file.ID, len(files))
	for i, f := range files {
		ids[i] = f.ID
	}
	var other bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM core.media WHERE file_id=ANY($1::bigint[]) AND id<>$2)`, ids, input.MediaID).Scan(&other); err != nil {
		return result, err
	}
	if other {
		return result, media.ErrFileInUse
	}
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM core.resources WHERE image_media_id=$1 UNION ALL SELECT 1 FROM core.library_items WHERE image_media_id=$1 UNION ALL SELECT 1 FROM core.users WHERE avatar_media_id=$1)`, input.MediaID).Scan(&other); err != nil {
		return result, err
	}
	if other {
		return result, media.ErrFileInUse
	}
	ctx = mediaoccurrence.WithTransaction(ctx, r.pool, tx)
	occurrences, err := r.occurrences(ctx, tx, input.MediaID)
	if err != nil {
		return result, err
	}
	if len(occurrences) > 1 {
		return result, media.ErrFileInUse
	}
	var owner *media.FileOccurrence
	if len(occurrences) == 1 {
		owner = &occurrences[0]
		if owner.Target != field.ReferenceFile {
			return result, media.ErrFileInUse
		}
		if owner.SiteID != input.SiteID {
			return result, security.ErrForbidden
		}
	}
	prepared, err := prepare(ctx, owner)
	if err != nil {
		return result, err
	}
	published := false
	defer func() {
		if !published && prepared.Abort != nil {
			prepared.Abort()
		}
	}()
	result = media.FileDeletion{OperationID: rand.Text(), Status: "pending", SiteID: input.SiteID, MediaID: input.MediaID, DeletedFileIDs: ids}
	result.StatusURL = fmt.Sprintf("/api/sites/%d/media-file-deletions/%s", input.SiteID, result.OperationID)
	if prepared.Apply != nil {
		result.ClearedReference, err = prepared.Apply(ctx)
		if err != nil {
			return result, err
		}
	}
	// RESTRICT occurrence FKs ensure an omitted or concurrently inserted owner
	// cannot turn into a dangling value. No physical bytes have changed yet.
	if _, err := tx.Exec(ctx, `DELETE FROM core.file_field_references WHERE owner_kind='resource' AND media_id=$1`, input.MediaID); err != nil {
		return result, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM core.media WHERE id=$1`, input.MediaID); err != nil {
		return result, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM core.files WHERE id=$1`, currentFile); err != nil {
		return result, err
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return result, err
	}
	manifest, err := json.Marshal(files)
	if err != nil {
		return result, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO core.media_file_deletions(operation_id,site_id,media_id,status,result,files) VALUES($1,$2,$3,'pending',$4,$5)`, result.OperationID, result.SiteID, result.MediaID, raw, manifest); err != nil {
		return result, err
	}
	if err := tx.Commit(ctx); err != nil {
		// A lost COMMIT response is ambiguous. If the durable operation exists,
		// publish the prepared owner rather than reporting a rolled-back mutation.
		check, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		var committed bool
		checkErr := r.pool.QueryRow(check, `SELECT EXISTS(SELECT 1 FROM core.media_file_deletions WHERE operation_id=$1)`, result.OperationID).Scan(&committed)
		if checkErr != nil || !committed {
			return result, err
		}
	}
	published = true
	if prepared.Publish != nil {
		prepared.Publish()
	}
	return result, nil
}

func (r *fileDeletionRepository) occurrences(ctx context.Context, tx interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, id media.ID) ([]media.FileOccurrence, error) {
	rows, err := tx.Query(ctx, `SELECT o.owner_kind,o.owner_id,o.site_id,o.container,o.value_path,o.reference_target,COALESCE(l.library_id,0) FROM core.media_field_occurrences o LEFT JOIN core.library_items l ON o.owner_kind='resource' AND l.id=o.owner_id WHERE o.media_id=$1
 UNION ALL SELECT 'resource',mr.resource_id,e.site_id,'fields:'||mr.field_key||':'||mr.position::text,mr.value_path,mr.reference_target,COALESCE(l.library_id,0) FROM core.resource_media_references mr JOIN core.resource_entities e ON e.id=mr.resource_id LEFT JOIN core.library_items l ON l.id=e.id WHERE mr.media_id=$1
 UNION ALL SELECT f.owner_kind,f.owner_id,CASE WHEN f.owner_kind='site' THEN f.owner_id ELSE e.site_id END,'unindexed',ARRAY[f.field_key],'media',0
 FROM core.file_field_references f LEFT JOIN core.resource_entities e ON f.owner_kind='resource' AND e.id=f.owner_id WHERE f.media_id=$1
 AND NOT EXISTS(SELECT 1 FROM core.media_field_occurrences o WHERE o.owner_kind=f.owner_kind AND o.owner_id=f.owner_id AND o.media_id=f.media_id)
 AND NOT EXISTS(SELECT 1 FROM core.resource_media_references mr WHERE f.owner_kind='resource' AND mr.resource_id=f.owner_id AND mr.media_id=f.media_id)`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var refs []media.FileOccurrence
	for rows.Next() {
		ref := media.FileOccurrence{MediaID: id}
		if err := rows.Scan(&ref.OwnerKind, &ref.OwnerID, &ref.SiteID, &ref.Container, &ref.Path, &ref.Target, &ref.LibraryID); err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}

func (r *fileDeletionRepository) FileOccurrences(ctx context.Context, id media.ID) ([]media.FileOccurrence, error) {
	return r.occurrences(ctx, r.pool, id)
}

func (r *fileDeletionRepository) FileDeletion(ctx context.Context, siteID int64, id string) (media.FileDeletion, error) {
	var raw []byte
	var status string
	err := r.pool.QueryRow(ctx, `SELECT result,status FROM core.media_file_deletions WHERE operation_id=$1 AND site_id=$2`, id, siteID).Scan(&raw, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return media.FileDeletion{}, media.ErrNotFound
	}
	if err != nil {
		return media.FileDeletion{}, err
	}
	var result media.FileDeletion
	if err := json.Unmarshal(raw, &result); err != nil {
		return result, err
	}
	result.SiteID = siteID
	result.Status = status
	return result, nil
}

func (r *fileDeletionRepository) CleanFileDeletion(ctx context.Context, siteID int64, id string, physical file.DeletePhysical) (media.FileDeletion, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return media.FileDeletion{}, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	var raw, manifest []byte
	var status string
	err = tx.QueryRow(ctx, `SELECT result,files,status FROM core.media_file_deletions WHERE operation_id=$1 AND site_id=$2 FOR UPDATE`, id, siteID).Scan(&raw, &manifest, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return media.FileDeletion{}, media.ErrNotFound
	}
	if err != nil {
		return media.FileDeletion{}, err
	}
	var result media.FileDeletion
	if err := json.Unmarshal(raw, &result); err != nil {
		return result, err
	}
	result.SiteID = siteID
	result.Status = status
	if status == "completed" {
		return result, nil
	}
	var files []file.File
	if err := json.Unmarshal(manifest, &files); err != nil {
		return result, err
	}
	cleanupErr := physical(ctx, files)
	var message any
	if cleanupErr == nil {
		result.Status = "completed"
	} else {
		message = cleanupErr.Error()
	}
	if _, err := tx.Exec(ctx, `UPDATE core.media_file_deletions SET status=$2,attempts=attempts+1,last_error=$3,completed_at=CASE WHEN $2='completed' THEN clock_timestamp() ELSE NULL END WHERE operation_id=$1`, id, result.Status, message); err != nil {
		return result, err
	}
	if err := tx.Commit(ctx); err != nil {
		return result, err
	}
	return result, nil
}

func (r *fileDeletionRepository) PendingFileDeletions(ctx context.Context, limit int) ([]media.FileDeletion, error) {
	rows, err := r.pool.Query(ctx, `SELECT site_id,operation_id FROM core.media_file_deletions WHERE status='pending' ORDER BY attempts,created_at LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []media.FileDeletion
	for rows.Next() {
		var item media.FileDeletion
		if err := rows.Scan(&item.SiteID, &item.OperationID); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
