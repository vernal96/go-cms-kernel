package postgres

import (
	"context"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	hookspostgres "github.com/vernal96/go-cms-kernel/entityhooks/adapters/postgres"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/security"
)

func (r *Repository) hookSource() *hookspostgres.Source {
	return hookspostgres.NewSource(r.connector.Pool(), "core:"+string(r.connector.Code()), "core")
}
func (r *Repository) appendResourceEvent(ctx context.Context, tx pgx.Tx, name string, resourceID resource.ID, siteID site.ID, storage resource.StorageKind, version int64, actorID *security.UserID) error {
	state, err := r.eventState(ctx, tx, resourceID)
	if err != nil {
		return err
	}
	return r.appendStateEvent(ctx, tx, name, state, actorID)
}
func (r *Repository) appendStateEvent(ctx context.Context, tx pgx.Tx, name string, state resource.EventState, actorID *security.UserID) error {
	payload, targets, err := resource.MutationEvent(ctx, name, state)
	if err != nil {
		return err
	}
	if actorID != nil {
		payload.ActorID = actorID
	}
	event, err := resource.NewEvent(name, time.Now().UTC(), payload)
	if err != nil {
		return err
	}
	return r.hookSource().Append(ctx, tx, event, targets, []byte(strconv.FormatInt(int64(state.ID), 10)))
}
func (r *Repository) appendWidgetResourceEvent(ctx context.Context, tx pgx.Tx, id resource.ID, version int64, actorID *security.UserID) error {
	state, err := r.eventState(ctx, tx, id)
	if err != nil {
		return err
	}
	return r.appendStateEvent(ctx, tx, resource.EventUpdated, state, actorID)
}

func (r *Repository) treeInTransaction(ctx context.Context, tx pgx.Tx, id resource.ID) (resource.Resource, error) {
	result, err := scanResource(tx.QueryRow(ctx, `SELECT id,site_id,parent_id,type,template,content_type,title,menu_title,slug,path,annotation,content,image_media_id,target_resource_id,external_url,is_public,is_searchable,in_menu,in_sitemap,sort,published_at,unpublished_at,type_settings,created_at,updated_at,created_by,updated_by,deleted_at,deleted_by FROM core.resources WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return resource.Resource{}, translateError(err)
	}
	items := []resource.Resource{result}
	if err := loadResourceWidgets(ctx, tx, items); err != nil {
		return resource.Resource{}, err
	}
	if err := loadResourceFields(ctx, tx, items); err != nil {
		return resource.Resource{}, err
	}
	if err := tx.QueryRow(ctx, `SELECT version FROM core.resource_entities WHERE id=$1 FOR UPDATE`, id).Scan(&items[0].Version); err != nil {
		return resource.Resource{}, translateError(err)
	}
	return items[0], nil
}
func (r *Repository) libraryInTransaction(ctx context.Context, tx pgx.Tx, id resource.ID) (resource.LibraryItem, error) {
	item, err := r.libraryItemByID(ctx, tx, id, true)
	if err != nil {
		return resource.LibraryItem{}, err
	}
	items := []resource.Resource{{ID: id, SiteID: item.SiteID}}
	if err := loadResourceWidgets(ctx, tx, items); err != nil {
		return resource.LibraryItem{}, err
	}
	if err := loadResourceFields(ctx, tx, items); err != nil {
		return resource.LibraryItem{}, err
	}
	item.Fields = items[0].Fields
	item.FieldValues = items[0].FieldValues
	item.FileReferences = items[0].FileReferences
	item.Widgets = items[0].Widgets
	return item, nil
}
func (r *Repository) eventState(ctx context.Context, tx pgx.Tx, id resource.ID) (resource.EventState, error) {
	var storage resource.StorageKind
	if err := tx.QueryRow(ctx, `SELECT storage_kind FROM core.resource_entities WHERE id=$1`, id).Scan(&storage); err != nil {
		return resource.EventState{}, translateError(err)
	}
	if storage == resource.StorageLibraryItem {
		item, err := r.libraryInTransaction(ctx, tx, id)
		if err != nil {
			return resource.EventState{}, err
		}
		return r.libraryHookState(ctx, tx, item)
	}
	item, err := r.treeInTransaction(ctx, tx, id)
	return resource.StateFromResource(item), err
}
