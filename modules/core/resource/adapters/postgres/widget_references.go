package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/vernal96/go-cms-kernel/modules/core/adapters/postgres/mediaoccurrence"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
	"github.com/vernal96/go-cms-kernel/security"
)

func (r *Repository) replaceWidgetOccurrence(ctx context.Context, tx pgx.Tx, id resource.ID, binding widget.Binding) error {
	var sid site.ID
	if err := tx.QueryRow(ctx, `SELECT site_id FROM core.resource_entities WHERE id=$1`, id).Scan(&sid); err != nil {
		return err
	}
	refs, err := resource.WidgetOccurrenceReferences(ctx, sid, binding)
	if err != nil {
		return err
	}
	return mediaoccurrence.Replace(ctx, tx, media.FileOccurrence{OwnerKind: "resource", OwnerID: int64(id), SiteID: int64(sid), Container: fmt.Sprintf("widget:%d", binding.ID)}, refs)
}

func deleteWidgetOccurrence(ctx context.Context, tx pgx.Tx, id resource.ID, bindingID widget.BindingID) error {
	_, err := tx.Exec(ctx, `DELETE FROM core.media_field_occurrences WHERE owner_kind='resource' AND owner_id=$1 AND container=$2`, id, "widget:"+strconv.FormatInt(int64(bindingID), 10))
	return err
}

func deleteWidgetOccurrences(ctx context.Context, tx pgx.Tx, id resource.ID) error {
	_, err := tx.Exec(ctx, `DELETE FROM core.media_field_occurrences WHERE owner_kind='resource' AND owner_id=$1 AND container LIKE 'widget:%'`, id)
	return err
}

func (r *Repository) syncWidgetOccurrences(ctx context.Context, tx pgx.Tx, id resource.ID) error {
	var sid site.ID
	if err := tx.QueryRow(ctx, `SELECT site_id FROM core.resource_entities WHERE id=$1`, id).Scan(&sid); err != nil {
		return err
	}
	items := []resource.Resource{{ID: id}}
	if err := loadResourceWidgets(ctx, tx, items); err != nil {
		return err
	}
	for _, binding := range items[0].Widgets {
		refs, err := resource.WidgetOccurrenceReferences(ctx, sid, binding)
		if err != nil {
			return err
		}
		if err := mediaoccurrence.Replace(ctx, tx, media.FileOccurrence{OwnerKind: "resource", OwnerID: int64(id), SiteID: int64(sid), Container: "widget:" + strconv.FormatInt(int64(binding.ID), 10)}, refs); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) clearWidgetFileOccurrence(ctx context.Context, tx pgx.Tx, actor *security.UserID, ref media.FileOccurrence, bindingID int64) error {
	before, err := r.eventState(ctx, tx, resource.ID(ref.OwnerID))
	if err != nil {
		return err
	}
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT params FROM core.resource_widgets WHERE id=$1 AND resource_id=$2 FOR UPDATE`, bindingID, ref.OwnerID).Scan(&raw); err != nil {
		return err
	}
	var data any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&data); err != nil {
		return err
	}
	pruned, err := media.PruneFileOccurrence(data, ref.Path, ref.MediaID)
	if err != nil {
		return err
	}
	raw, err = json.Marshal(pruned)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE core.resource_widgets SET params=$2 WHERE id=$1`, bindingID, raw); err != nil {
		return err
	}
	if err := r.syncWidgetOccurrences(ctx, tx, resource.ID(ref.OwnerID)); err != nil {
		return err
	}
	after, err := r.eventState(ctx, tx, resource.ID(ref.OwnerID))
	if err != nil {
		return err
	}
	if _, err := resource.PrepareMutation(ctx, &before, after); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `UPDATE core.resource_entities SET version=version+1 WHERE id=$1 RETURNING version`, ref.OwnerID).Scan(&after.Version); err != nil {
		return err
	}
	if err := touchWidgetResource(ctx, tx, resource.ID(ref.OwnerID)); err != nil {
		return err
	}
	if resource.MediaCascadeRecordsRevision(ctx, after) {
		if err := r.appendCurrentRevision(ctx, tx, resource.ID(ref.OwnerID), after.Version, actor); err != nil {
			return err
		}
	}
	return r.appendStateEvent(ctx, tx, resource.EventUpdated, after, actor)
}

func loadWidgetOccurrences(ctx context.Context, query rowQueryer, items []resource.Resource) error {
	ids := make([]int64, len(items))
	for i, item := range items {
		ids[i] = int64(item.ID)
	}
	rows, err := query.Query(ctx, `SELECT owner_id,container,value_path,media_id,reference_target FROM core.media_field_occurrences WHERE owner_kind='resource' AND owner_id=ANY($1::bigint[])`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var container string
		var ref field.Reference
		if err := rows.Scan(&id, &container, &ref.Path, &ref.ID, &ref.Target); err != nil {
			return err
		}
		for i := range items {
			if int64(items[i].ID) == id {
				for j := range items[i].Widgets {
					if container == fmt.Sprintf("widget:%d", items[i].Widgets[j].ID) {
						items[i].Widgets[j].References = append(items[i].Widgets[j].References, ref)
					}
				}
			}
		}
	}
	return rows.Err()
}
