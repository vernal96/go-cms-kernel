package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/vernal96/go-cms-kernel/modules/core/adapters/postgres/medialock"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
)

func loadResourceFields(ctx context.Context, queryer rowQueryer, items []resource.Resource) error {
	if len(items) == 0 {
		return nil
	}
	indexes := make(map[resource.ID]int, len(items))
	ids := make([]int64, len(items))
	for index := range items {
		indexes[items[index].ID] = index
		ids[index] = int64(items[index].ID)
		items[index].Fields = map[string]any{}
		items[index].FieldValues = nil
	}
	rows, err := queryer.Query(ctx, `
SELECT resource_id, field_key, position, is_multi, value_kind,
       value_string, value_integer, value_float, value_boolean,
       value_timestamp, value_reference, value_json,
       CASE WHEN EXISTS (SELECT 1 FROM core.resource_media_references mr
                         WHERE mr.resource_id = fv.resource_id AND mr.field_key = fv.field_key AND mr.position = fv.position AND cardinality(mr.value_path)=0)
            THEN (SELECT mr.reference_target FROM core.resource_media_references mr WHERE mr.resource_id=fv.resource_id AND mr.field_key=fv.field_key AND mr.position=fv.position AND cardinality(mr.value_path)=0 LIMIT 1) ELSE '' END,
       COALESCE((SELECT jsonb_agg(jsonb_build_object('target',mr.reference_target,'id',mr.media_id,'path',mr.value_path) ORDER BY mr.value_path)
                 FROM core.resource_media_references mr WHERE mr.resource_id=fv.resource_id AND mr.field_key=fv.field_key AND mr.position=fv.position AND cardinality(mr.value_path)>0), '[]'::jsonb)
FROM core.resource_field_values fv
WHERE resource_id = ANY($1::bigint[])
ORDER BY resource_id, field_key, position;`, ids)
	if err != nil {
		return fmt.Errorf("query resource fields: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			resourceID     resource.ID
			stored         field.StoredValue
			stringValue    *string
			integerValue   *int64
			floatValue     *float64
			booleanValue   *bool
			timestampValue *time.Time
			referenceValue *int64
			rawJSON        []byte
			rawReferences  []byte
		)
		if err := rows.Scan(&resourceID, &stored.Key, &stored.Position, &stored.Multiple, &stored.Kind,
			&stringValue, &integerValue, &floatValue, &booleanValue,
			&timestampValue, &referenceValue, &rawJSON, &stored.ReferenceTarget, &rawReferences); err != nil {
			return fmt.Errorf("scan resource field: %w", err)
		}
		if err := json.Unmarshal(rawReferences, &stored.References); err != nil {
			return fmt.Errorf("decode field references: %w", err)
		}
		switch stored.Kind {
		case field.StorageString:
			if stringValue == nil {
				return errors.New("stored string resource field is empty")
			}
			stored.Value = *stringValue
		case field.StorageInteger:
			if integerValue == nil {
				return errors.New("stored integer resource field is empty")
			}
			stored.Value = *integerValue
		case field.StorageFloat:
			if floatValue == nil {
				return errors.New("stored float resource field is empty")
			}
			stored.Value = *floatValue
		case field.StorageBoolean:
			if booleanValue == nil {
				return errors.New("stored boolean resource field is empty")
			}
			stored.Value = *booleanValue
		case field.StorageTimestamp:
			if timestampValue == nil {
				return errors.New("stored timestamp resource field is empty")
			}
			stored.Value = *timestampValue
		case field.StorageReference:
			if referenceValue == nil {
				return errors.New("stored reference resource field is empty")
			}
			stored.Value = *referenceValue
		case field.StorageJSON:
			decoder := json.NewDecoder(bytes.NewReader(rawJSON))
			decoder.UseNumber()
			if err := decoder.Decode(&stored.Value); err != nil {
				return fmt.Errorf("decode resource field JSON: %w", err)
			}
		default:
			return fmt.Errorf("stored resource field has invalid kind %q", stored.Kind)
		}
		index, exists := indexes[resourceID]
		if !exists {
			return fmt.Errorf("resource field references unexpected resource %d", resourceID)
		}
		items[index].FieldValues = append(items[index].FieldValues, stored)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate resource fields: %w", err)
	}
	for index := range items {
		for _, stored := range items[index].FieldValues {
			if !stored.Multiple {
				items[index].Fields[stored.Key] = stored.Value
				continue
			}
			if stored.Kind == field.StorageString {
				values, _ := items[index].Fields[stored.Key].([]string)
				items[index].Fields[stored.Key] = append(values, stored.Value.(string))
			} else {
				values, _ := items[index].Fields[stored.Key].([]any)
				items[index].Fields[stored.Key] = append(values, stored.Value)
			}
		}
	}
	return nil
}

func scanResource(scanner rowScanner) (resource.Resource, error) {
	var (
		item             resource.Resource
		parentID         *int64
		templateCode     *string
		contentType      *string
		path             *string
		imageMediaID     *int64
		targetResourceID *int64
		externalURL      *string
		rawSettings      []byte
	)

	if err := scanner.Scan(
		&item.ID,
		&item.SiteID,
		&parentID,
		&item.Type,
		&templateCode,
		&contentType,
		&item.Title,
		&item.MenuTitle,
		&item.Slug,
		&path,
		&item.Annotation,
		&item.Content,
		&imageMediaID,
		&targetResourceID,
		&externalURL,
		&item.IsPublic,
		&item.IsSearchable,
		&item.InMenu,
		&item.InSitemap,
		&item.Sort,
		&item.PublishedAt,
		&item.UnpublishedAt,
		&rawSettings,
		&item.CreatedAt,
		&item.UpdatedAt,
		&item.CreatedBy,
		&item.UpdatedBy,
		&item.DeletedAt,
		&item.DeletedBy,
	); err != nil {
		return resource.Resource{}, err
	}

	if parentID != nil {
		value := resource.ID(*parentID)
		item.ParentID = &value
	}
	if templateCode != nil {
		value := template.Code(*templateCode)
		item.Template = &value
	}
	item.ContentType = contentType
	item.Path = path
	if imageMediaID != nil {
		value := media.ID(*imageMediaID)
		item.ImageMediaID = &value
	}
	if targetResourceID != nil {
		value := resource.ID(*targetResourceID)
		item.TargetResourceID = &value
	}
	item.ExternalURL = externalURL

	item.TypeSettings = make(map[string]any)
	if len(rawSettings) > 0 {
		decoder := json.NewDecoder(bytes.NewReader(rawSettings))
		decoder.UseNumber()
		if err := decoder.Decode(&item.TypeSettings); err != nil {
			return resource.Resource{}, fmt.Errorf(
				"decode type_settings for resource %d: %w",
				item.ID,
				err,
			)
		}
	}

	return item, nil
}

func ensureMediaAvailable(
	ctx context.Context,
	transaction pgx.Tx,
	id media.ID,
	exclude resource.ID,
) error {
	var attached bool
	if err := transaction.QueryRow(ctx, `
SELECT EXISTS
(
    SELECT 1 FROM core.resource_media_references WHERE media_id = $1 AND ($2 = 0 OR resource_id <> $2)
    UNION ALL

    SELECT 1
    FROM core.resources
    WHERE image_media_id = $1
      AND ($2 = 0 OR id <> $2)

    UNION ALL

    SELECT 1
	FROM core.library_items
	WHERE image_media_id = $1
	  AND ($2 = 0 OR id <> $2)

    UNION ALL

    SELECT 1
    FROM core.users
    WHERE avatar_media_id = $1
);
`, id, exclude).Scan(&attached); err != nil {
		return fmt.Errorf(
			"check media %d attachment: %w",
			id,
			err,
		)
	}
	if attached {
		return media.ErrAlreadyAttached
	}
	return nil
}

func treeMediaIDs(
	ctx context.Context,
	transaction pgx.Tx,
	id resource.ID,
	lock bool,
) ([]media.ID, bool, error) {
	query := `
WITH RECURSIVE tree AS
(
    SELECT id
    FROM core.resources
    WHERE id = $1

    UNION ALL

    SELECT child.id
    FROM core.resources AS child
    JOIN tree
      ON child.parent_id = tree.id
)
SELECT item.id, item.image_media_id
FROM core.resources AS item
JOIN tree
  ON tree.id = item.id
ORDER BY item.id`
	if lock {
		query += `
FOR UPDATE OF item`
	}
	query += ";"

	rows, err := transaction.Query(ctx, query, id)
	if err != nil {
		return nil, false, fmt.Errorf(
			"query resource %d delete tree: %w",
			id,
			err,
		)
	}
	defer rows.Close()

	seen := make(map[media.ID]struct{})
	result := make([]media.ID, 0)
	exists := false
	for rows.Next() {
		exists = true
		var (
			resourceID   resource.ID
			imageMediaID *int64
		)
		if err := rows.Scan(&resourceID, &imageMediaID); err != nil {
			return nil, false, fmt.Errorf(
				"scan resource delete tree: %w",
				err,
			)
		}
		if imageMediaID == nil {
			continue
		}
		mediaID := media.ID(*imageMediaID)
		if _, duplicate := seen[mediaID]; duplicate {
			continue
		}
		seen[mediaID] = struct{}{}
		result = append(result, mediaID)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf(
			"iterate resource delete tree: %w",
			err,
		)
	}
	return result, exists, nil
}

func treeLibraryItemMediaIDs(ctx context.Context, transaction pgx.Tx, id resource.ID, lock bool) ([]media.ID, error) {
	query := `
WITH RECURSIVE tree AS (
    SELECT id FROM core.resources WHERE id=$1
    UNION ALL
    SELECT child.id FROM core.resources child JOIN tree parent ON child.parent_id=parent.id
)
SELECT item.image_media_id
FROM core.library_items item
JOIN tree library ON library.id=item.library_id
WHERE item.image_media_id IS NOT NULL`
	if lock {
		query += ` FOR UPDATE OF item`
	}
	rows, err := transaction.Query(ctx, query+`;`, id)
	if err != nil {
		return nil, fmt.Errorf("query library item media for resource tree: %w", err)
	}
	defer rows.Close()
	seen := map[media.ID]struct{}{}
	result := make([]media.ID, 0)
	for rows.Next() {
		var raw int64
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		mediaID := media.ID(raw)
		if _, exists := seen[mediaID]; !exists {
			seen[mediaID] = struct{}{}
			result = append(result, mediaID)
		}
	}
	return result, rows.Err()
}

func mediaIDsContained(
	actual []media.ID,
	locked []media.ID,
) bool {
	lockedSet := make(map[media.ID]struct{}, len(locked))
	for _, id := range locked {
		lockedSet[id] = struct{}{}
	}
	for _, id := range actual {
		if _, exists := lockedSet[id]; !exists {
			return false
		}
	}
	return true
}

func equalMediaID(
	expected *media.ID,
	actual *int64,
) bool {
	if expected == nil || actual == nil {
		return expected == nil && actual == nil
	}
	return int64(*expected) == *actual
}

func sameMediaID(
	left *media.ID,
	right *media.ID,
) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func lockResource(
	ctx context.Context,
	transaction pgx.Tx,
	id resource.ID,
) (resource.Resource, error) {
	item, err := scanResource(transaction.QueryRow(ctx, `
SELECT
    id, site_id, parent_id, type, template, content_type,
	    title, menu_title, slug, path, annotation, content, image_media_id,
    target_resource_id,
    external_url, is_public, is_searchable, in_menu, in_sitemap,
    sort, published_at, unpublished_at, type_settings, created_at,
	    updated_at, created_by, updated_by, deleted_at, deleted_by
FROM core.resources
WHERE id = $1
FOR UPDATE;
`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return resource.Resource{}, resource.ErrInvalidReference
	}
	if err != nil {
		return resource.Resource{}, fmt.Errorf(
			"lock resource %d: %w",
			id,
			err,
		)
	}
	return item, nil
}

func translateError(err error) error {
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) {
		return err
	}

	switch postgresError.Code {
	case pgerrcode.UniqueViolation:
		if postgresError.ConstraintName == "uq_resources_site_path" || postgresError.ConstraintName == "uq_library_item_routes_slug" {
			return fmt.Errorf("%w: %s", resource.ErrRouteConflict, err)
		}
		return fmt.Errorf("%w: %s", resource.ErrConflict, err)
	case pgerrcode.ForeignKeyViolation:
		return fmt.Errorf("%w: %s", resource.ErrInvalidReference, err)
	default:
		return err
	}
}

func translateDeleteError(err error) error {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) &&
		postgresError.Code == pgerrcode.ForeignKeyViolation {
		return fmt.Errorf("%w: %s", resource.ErrReferenced, err)
	}
	return translateError(err)
}

func sameOptionalText(left, right *string) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func replaceFileReferences(
	ctx context.Context,
	tx pgx.Tx,
	ownerID resource.ID,
	references map[string]media.ID,
) error {
	if _, err := tx.Exec(ctx, `DELETE FROM core.file_field_references WHERE owner_kind = 'resource' AND owner_id = $1;`, ownerID); err != nil {
		return fmt.Errorf("delete resource file references: %w", err)
	}
	for key, id := range references {
		if _, err := tx.Exec(ctx, `
INSERT INTO core.file_field_references (owner_kind, owner_id, field_key, media_id)
VALUES ('resource', $1, $2, $3);`, ownerID, key, id); err != nil {
			return fmt.Errorf("insert resource file reference: %w", err)
		}
	}
	return nil
}

func replaceResourceFields(ctx context.Context, tx pgx.Tx, resourceID resource.ID, siteID site.ID, libraryID *resource.ID, values []field.StoredValue) error {
	oldMedia, err := fieldMediaIDs(ctx, tx, []resource.ID{resourceID})
	if err != nil {
		return err
	}
	mediaIDs := append([]media.ID(nil), oldMedia...)
	for _, stored := range values {
		refs, err := stored.MediaReferences()
		if err != nil {
			return fmt.Errorf("%w: %v", resource.ErrInvalidReference, err)
		}
		for _, ref := range refs {
			mediaIDs = append(mediaIDs, media.ID(ref.ID))
		}
	}
	if err := medialock.Lock(ctx, tx, mediaIDs...); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM core.resource_field_values WHERE resource_id = $1;`, resourceID); err != nil {
		return fmt.Errorf("delete resource fields: %w", err)
	}
	for _, stored := range values {
		var stringValue, integerValue, floatValue, booleanValue, timestampValue, referenceValue, jsonValue any
		switch stored.Kind {
		case field.StorageString:
			value, ok := stored.Value.(string)
			if !ok {
				return fmt.Errorf("resource field %q string value has type %T", stored.Key, stored.Value)
			}
			stringValue = value
		case field.StorageInteger:
			value, ok := stored.Value.(int64)
			if !ok {
				return fmt.Errorf("resource field %q integer value has type %T", stored.Key, stored.Value)
			}
			integerValue = value
		case field.StorageFloat:
			value, ok := stored.Value.(float64)
			if !ok {
				return fmt.Errorf("resource field %q float value has type %T", stored.Key, stored.Value)
			}
			floatValue = value
		case field.StorageBoolean:
			value, ok := stored.Value.(bool)
			if !ok {
				return fmt.Errorf("resource field %q boolean value has type %T", stored.Key, stored.Value)
			}
			booleanValue = value
		case field.StorageTimestamp:
			value, ok := stored.Value.(time.Time)
			if !ok {
				return fmt.Errorf("resource field %q timestamp value has type %T", stored.Key, stored.Value)
			}
			timestampValue = value
		case field.StorageReference:
			value, ok := stored.Value.(int64)
			if !ok {
				return fmt.Errorf("resource field %q reference value has type %T", stored.Key, stored.Value)
			}
			referenceValue = value
		case field.StorageJSON:
			raw, err := json.Marshal(stored.Value)
			if err != nil {
				return fmt.Errorf("encode resource field %q JSON: %w", stored.Key, err)
			}
			jsonValue = string(raw)
		default:
			return fmt.Errorf("resource field %q has invalid storage kind %q", stored.Key, stored.Kind)
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO core.resource_field_values (
    resource_id, site_id, library_id, field_key, position, is_multi, value_kind,
    value_string, value_integer, value_float, value_boolean,
    value_timestamp, value_reference, value_json
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14::jsonb);`,
			resourceID, siteID, libraryID, stored.Key, stored.Position, stored.Multiple, stored.Kind,
			stringValue, integerValue, floatValue, booleanValue, timestampValue, referenceValue, jsonValue); err != nil {
			return fmt.Errorf("insert resource field %q: %w", stored.Key, translateError(err))
		}
		refs, err := stored.MediaReferences()
		if err != nil {
			return err
		}
		for _, ref := range refs {
			if err := ensureMediaAvailable(ctx, tx, media.ID(ref.ID), resourceID); err != nil {
				return err
			}
			path := append([]string{}, ref.Path...)
			if _, err := tx.Exec(ctx, `INSERT INTO core.resource_media_references(resource_id,field_key,position,value_path,media_id,reference_target) VALUES($1,$2,$3,$4,$5,$6)`, resourceID, stored.Key, stored.Position, path, ref.ID, ref.Target); err != nil {
				return translateError(err)
			}
		}
	}
	return deleteUnusedMedia(ctx, tx, oldMedia)
}

func cloneFileReferences(source map[string]media.ID) map[string]media.ID {
	if source == nil {
		return nil
	}
	result := make(map[string]media.ID, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func cloneFieldMap(source map[string]any) map[string]any {
	return resource.Clone(resource.Resource{Fields: source}).Fields
}
