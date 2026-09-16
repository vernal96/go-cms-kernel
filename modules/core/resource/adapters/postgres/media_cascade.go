package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/security"
)

// ClearMediaReferences participates in the filesystem transaction. Every owner
// goes through its hooks, history policy and outbox before physical deletion.
func (r *Repository) ClearMediaReferences(ctx context.Context, tx pgx.Tx, mediaIDs []int64, actorID *security.UserID) error {
	rows, err := tx.Query(ctx, `SELECT id FROM core.resources WHERE image_media_id=ANY($1::bigint[])
 UNION SELECT id FROM core.library_items WHERE image_media_id=ANY($1::bigint[])
 UNION SELECT resource_id FROM core.resource_media_references WHERE media_id=ANY($1::bigint[]) ORDER BY id`, mediaIDs)
	if err != nil {
		return err
	}
	var ids []resource.ID
	for rows.Next() {
		var id resource.ID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		before, err := r.eventState(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := clearStructuredMediaReferences(ctx, tx, id, mediaIDs); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM core.resource_field_values fv USING core.resource_media_references mr
   WHERE fv.resource_id=$1 AND fv.resource_id=mr.resource_id AND fv.field_key=mr.field_key AND fv.position=mr.position AND cardinality(mr.value_path)=0 AND mr.media_id=ANY($2::bigint[])`, id, mediaIDs); err != nil {
			return err
		}
		query := `UPDATE core.resources SET image_media_id=CASE WHEN image_media_id=ANY($2::bigint[]) THEN NULL ELSE image_media_id END, updated_at=clock_timestamp(), updated_by=$3 WHERE id=$1`
		if before.Data.StorageKind == resource.StorageLibraryItem {
			query = `UPDATE core.library_items SET image_media_id=CASE WHEN image_media_id=ANY($2::bigint[]) THEN NULL ELSE image_media_id END, updated_at=clock_timestamp(), updated_by=$3 WHERE id=$1`
		}
		if _, err := tx.Exec(ctx, query, id, mediaIDs, actorID); err != nil {
			return err
		}
		after, err := r.eventState(ctx, tx, id)
		if err != nil {
			return err
		}
		if _, err := resource.PrepareMutation(ctx, &before, after); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `UPDATE core.resource_entities SET version=version+1 WHERE id=$1 RETURNING version`, id).Scan(&after.Version); err != nil {
			return err
		}
		if resource.MediaCascadeRecordsRevision(ctx, after) {
			if err := r.appendCurrentRevision(ctx, tx, id, after.Version, actorID); err != nil {
				return err
			}
		}
		if err := r.appendStateEvent(ctx, tx, resource.EventUpdated, after, actorID); err != nil {
			return err
		}
	}
	return nil
}

// Rebuild reference paths after pruning JSON array members. Paths are collected
// before mutation so deleting several indices cannot target a shifted neighbor.
func clearStructuredMediaReferences(ctx context.Context, tx pgx.Tx, id resource.ID, mediaIDs []int64) error {
	rows, err := tx.Query(ctx, `SELECT fv.field_key,fv.position,fv.value_json,
 (SELECT jsonb_agg(jsonb_build_object('target','media','id',mr.media_id,'path',mr.value_path)) FROM core.resource_media_references mr WHERE mr.resource_id=fv.resource_id AND mr.field_key=fv.field_key AND mr.position=fv.position)
 FROM core.resource_field_values fv WHERE fv.resource_id=$1 AND fv.value_kind='json' AND EXISTS
 (SELECT 1 FROM core.resource_media_references mr WHERE mr.resource_id=fv.resource_id AND mr.field_key=fv.field_key AND mr.position=fv.position AND mr.media_id=ANY($2::bigint[]))`, id, mediaIDs)
	if err != nil {
		return err
	}
	type value struct {
		key       string
		position  int
		raw, refs []byte
	}
	var values []value
	for rows.Next() {
		var item value
		if err := rows.Scan(&item.key, &item.position, &item.raw, &item.refs); err != nil {
			rows.Close()
			return err
		}
		values = append(values, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	removed := map[int64]bool{}
	for _, id := range mediaIDs {
		removed[id] = true
	}
	for _, item := range values {
		var data any
		decoder := json.NewDecoder(bytes.NewReader(item.raw))
		decoder.UseNumber()
		if err := decoder.Decode(&data); err != nil {
			return err
		}
		var refs []field.Reference
		if err := json.Unmarshal(item.refs, &refs); err != nil {
			return err
		}
		paths := map[string]bool{}
		for _, ref := range refs {
			if removed[ref.ID] {
				paths[jsonPathKey(ref.Path)] = true
			}
		}
		mapping := map[string][]string{}
		data, _ = pruneReferenceValues(data, nil, nil, paths, mapping)
		raw, err := json.Marshal(data)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE core.resource_field_values SET value_json=$4 WHERE resource_id=$1 AND field_key=$2 AND position=$3`, id, item.key, item.position, raw); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM core.resource_media_references WHERE resource_id=$1 AND field_key=$2 AND position=$3`, id, item.key, item.position); err != nil {
			return err
		}
		for _, ref := range refs {
			if removed[ref.ID] {
				continue
			}
			path, exists := mapping[jsonPathKey(ref.Path)]
			if !exists {
				return fmt.Errorf("missing media reference path in field %q", item.key)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO core.resource_media_references(resource_id,field_key,position,value_path,media_id) VALUES($1,$2,$3,$4,$5)`, id, item.key, item.position, path, ref.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func jsonPathKey(path []string) string { raw, _ := json.Marshal(path); return string(raw) }

func pruneReferenceValues(value any, oldPath, newPath []string, remove map[string]bool, mapping map[string][]string) (any, bool) {
	if remove[jsonPathKey(oldPath)] {
		return nil, false
	}
	mapping[jsonPathKey(oldPath)] = append([]string{}, newPath...)
	switch typed := value.(type) {
	case []any:
		result := make([]any, 0, len(typed))
		for i, item := range typed {
			old := append(append([]string{}, oldPath...), strconv.Itoa(i))
			next := append(append([]string{}, newPath...), strconv.Itoa(len(result)))
			if updated, keep := pruneReferenceValues(item, old, next, remove, mapping); keep {
				result = append(result, updated)
			}
		}
		return result, true
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			old := append(append([]string{}, oldPath...), key)
			next := append(append([]string{}, newPath...), key)
			if updated, keep := pruneReferenceValues(item, old, next, remove, mapping); keep {
				result[key] = updated
			}
		}
		return result, true
	default:
		return value, true
	}
}
