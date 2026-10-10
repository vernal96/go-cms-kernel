package postgres

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/vernal96/go-cms-kernel/modules/core/adapters/postgres/mediaoccurrence"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

func (r *Repository) Kind() string                      { return "resource" }
func (r *Repository) UpdatePermission() permission.Code { return "core.resource.update" }

func (r *Repository) ClearFileOccurrence(ctx context.Context, actor *security.UserID, ref media.FileOccurrence) (int64, error) {
	tx, err := mediaoccurrence.Transaction(ctx, r.connector.Pool())
	if err != nil {
		return 0, err
	}
	var version int64
	if err := tx.QueryRow(ctx, `SELECT version FROM core.resource_entities WHERE id=$1 AND site_id=$2 FOR UPDATE`, ref.OwnerID, ref.SiteID).Scan(&version); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, media.ErrFileDeleteConflict
		}
		return 0, err
	}
	if strings.HasPrefix(ref.Container, "fields:") {
		if err := r.ClearMediaReferences(ctx, tx, []int64{int64(ref.MediaID)}, actor); err != nil {
			return 0, err
		}
		if err := rebuildResourceFileReferences(ctx, tx, ref.OwnerID); err != nil {
			return 0, err
		}
	} else if strings.HasPrefix(ref.Container, "widget:") {
		id, err := strconv.ParseInt(strings.TrimPrefix(ref.Container, "widget:"), 10, 64)
		if err != nil {
			return 0, media.ErrFileDeleteConflict
		}
		if err := r.clearWidgetFileOccurrence(ctx, tx, actor, ref, id); err != nil {
			return 0, err
		}
	} else {
		return 0, media.ErrFileDeleteUnsupported
	}
	if err := tx.QueryRow(ctx, `SELECT version FROM core.resource_entities WHERE id=$1`, ref.OwnerID).Scan(&version); err != nil {
		return 0, err
	}
	return version, nil
}

func rebuildResourceFileReferences(ctx context.Context, tx pgx.Tx, id int64) error {
	rows, err := tx.Query(ctx, `SELECT mr.field_key,fv.is_multi,mr.position,mr.value_path,mr.media_id FROM core.resource_media_references mr JOIN core.resource_field_values fv USING(resource_id,field_key,position) WHERE mr.resource_id=$1 AND mr.reference_target='file' ORDER BY mr.field_key,mr.position,mr.value_path`, id)
	if err != nil {
		return err
	}
	type ref struct {
		key string
		id  int64
	}
	var refs []ref
	for rows.Next() {
		var key string
		var multiple bool
		var position int
		var path []string
		var mediaID int64
		if err := rows.Scan(&key, &multiple, &position, &path, &mediaID); err != nil {
			rows.Close()
			return err
		}
		parts := []string{key}
		if multiple {
			parts = append(parts, strconv.Itoa(position))
		}
		parts = append(parts, path...)
		refs = append(refs, ref{field.ReferenceKey(parts), mediaID})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM core.file_field_references WHERE owner_kind='resource' AND owner_id=$1`, id); err != nil {
		return err
	}
	for _, ref := range refs {
		if _, err := tx.Exec(ctx, `INSERT INTO core.file_field_references(owner_kind,owner_id,field_key,media_id)VALUES('resource',$1,$2,$3)`, id, ref.key, ref.id); err != nil {
			return fmt.Errorf("rebuild file reference: %w", err)
		}
	}
	return nil
}
