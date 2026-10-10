package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/vernal96/go-cms-kernel/modules/core/adapters/postgres/mediaoccurrence"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/forms"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

func (r *Repository) Kind() string { return "forms.element" }

func (r *Repository) UpdatePermission() permission.Code { return forms.FormUpdatePermission }

func (r *Repository) ClearFileOccurrence(ctx context.Context, actorID *security.UserID, occurrence media.FileOccurrence) (int64, error) {
	if occurrence.OwnerKind != r.Kind() || occurrence.Container != "config" || occurrence.OwnerID <= 0 || occurrence.SiteID <= 0 || occurrence.Target != field.ReferenceFile || len(occurrence.Path) == 0 {
		return 0, media.ErrFileDeleteUnsupported
	}
	tx, err := mediaoccurrence.Transaction(ctx, r.connector.Pool())
	if err != nil {
		return 0, err
	}
	var actualSite int64
	var raw json.RawMessage
	err = tx.QueryRow(ctx, `SELECT f.site_id,e.config FROM forms.elements e JOIN forms.forms f ON f.id=e.form_id WHERE f.site_id=$1 AND e.id=$2 FOR UPDATE OF f,e;`, occurrence.SiteID, occurrence.OwnerID).Scan(&actualSite, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, media.ErrFileDeleteConflict
	}
	if err != nil {
		return 0, err
	}
	if actualSite != occurrence.SiteID {
		return 0, media.ErrFileDeleteConflict
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var config any
	if err := decoder.Decode(&config); err != nil {
		return 0, fmt.Errorf("decode Forms element config: %w", err)
	}
	updated, err := media.PruneFileOccurrence(config, occurrence.Path, occurrence.MediaID)
	if err != nil {
		return 0, err
	}
	updatedConfig, err := json.Marshal(updated)
	if err != nil {
		return 0, fmt.Errorf("encode Forms element config: %w", err)
	}
	rows, err := tx.Query(ctx, `SELECT value_path,media_id,reference_target FROM core.media_field_occurrences WHERE owner_kind=$1 AND owner_id=$2 AND site_id=$3 AND container=$4 ORDER BY value_path,media_id;`, occurrence.OwnerKind, occurrence.OwnerID, occurrence.SiteID, occurrence.Container)
	if err != nil {
		return 0, err
	}
	references := make([]field.Reference, 0)
	found := false
	for rows.Next() {
		var path []string
		var id int64
		var target string
		if err := rows.Scan(&path, &id, &target); err != nil {
			rows.Close()
			return 0, err
		}
		if equalOccurrencePath(path, occurrence.Path) && id == int64(occurrence.MediaID) && target == occurrence.Target {
			found = true
			continue
		}
		references = append(references, field.Reference{Path: shiftOccurrencePath(path, occurrence.Path), ID: id, Target: target})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	if !found {
		return 0, media.ErrFileDeleteConflict
	}
	if _, err := tx.Exec(ctx, `UPDATE forms.elements SET config=$2,updated_at=clock_timestamp() WHERE id=$1;`, occurrence.OwnerID, updatedConfig); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `UPDATE forms.forms SET updated_at=clock_timestamp(),updated_by=$3 WHERE site_id=$1 AND id=(SELECT form_id FROM forms.elements WHERE id=$2);`, site.ID(occurrence.SiteID), occurrence.OwnerID, actorID); err != nil {
		return 0, err
	}
	if err := mediaoccurrence.Replace(ctx, tx, occurrence, references); err != nil {
		return 0, err
	}
	return 0, nil
}

func equalOccurrencePath(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func shiftOccurrencePath(path, removed []string) []string {
	if len(removed) == 0 {
		return append([]string(nil), path...)
	}
	removedIndex, err := strconv.Atoi(removed[len(removed)-1])
	if err != nil {
		return append([]string(nil), path...)
	}
	parent := removed[:len(removed)-1]
	if len(path) <= len(parent) || !equalOccurrencePath(path[:len(parent)], parent) {
		return append([]string(nil), path...)
	}
	pathIndex, err := strconv.Atoi(path[len(parent)])
	if err != nil || pathIndex <= removedIndex {
		return append([]string(nil), path...)
	}
	result := append([]string(nil), path...)
	result[len(parent)] = strconv.Itoa(pathIndex - 1)
	return result
}

var _ media.FileOccurrenceOwner = (*Repository)(nil)
