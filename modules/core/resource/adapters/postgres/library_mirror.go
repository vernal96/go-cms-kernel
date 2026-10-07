package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
)

// Tree mutations hold the exclusive topology lock. Item writes share it and
// retain the existing site lock, so independent source sites can still write.
func lockLibraryRouteNamespace(ctx context.Context, tx pgx.Tx, siteID site.ID) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended('core.route-topology', 0));`); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT site_id FROM (
 SELECT $1::bigint AS site_id UNION ALL
 SELECT m.site_id FROM core.resources m JOIN core.resources source ON source.id=m.source_library_id WHERE source.site_id=$1
) targets ORDER BY site_id`, siteID)
	if err != nil {
		return err
	}
	var sites []site.ID
	for rows.Next() {
		var id site.ID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		sites = append(sites, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range sites {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('core.routes:' || $1::bigint::text, 0));`, id); err != nil {
			return err
		}
	}
	return nil
}

func effectiveRouteLibrary(ctx context.Context, queryer routeQueryer, mount resource.Resource, override *resource.Resource) (resource.Resource, error) {
	if mount.Type != resourcetype.LibraryMirror {
		return mount, nil
	}
	sourceID := resource.ID(resourcetype.SourceLibraryID(mount.TypeSettings))
	source, err := routeResourceByID(ctx, queryer, sourceID)
	if err != nil {
		return resource.Resource{}, err
	}
	if override != nil && override.ID == sourceID {
		source = resource.Clone(*override)
	}
	if source.Type != resourcetype.Library {
		return resource.Resource{}, resource.ErrInvalidReference
	}
	source.Path = mount.Path
	return source, nil
}

func mirrorMounts(ctx context.Context, queryer routeQueryer, sourceID resource.ID) ([]resource.Resource, error) {
	rows, err := queryer.Query(ctx, `SELECT id FROM core.resources WHERE type='library_mirror' AND ($1::bigint=0 OR source_library_id=$1) ORDER BY id`, sourceID)
	if err != nil {
		return nil, err
	}
	var ids []resource.ID
	for rows.Next() {
		var id resource.ID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	mounts := make([]resource.Resource, 0, len(ids))
	for _, id := range ids {
		mount, err := routeResourceByID(ctx, queryer, id)
		if err != nil {
			return nil, err
		}
		mounts = append(mounts, mount)
	}
	return mounts, nil
}

func ensureMirroredItemRoutesAvailable(ctx context.Context, queryer routeQueryer, library resource.Resource, item resource.LibraryItem) error {
	mounts, err := mirrorMounts(ctx, queryer, library.ID)
	if err != nil {
		return err
	}
	for _, mount := range mounts {
		projection := resource.Clone(library)
		projection.Path = mount.Path
		path, err := resource.EffectiveLibraryItemURL(projection, item)
		if err != nil {
			return err
		}
		var conflict bool
		if err := queryer.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM core.resources WHERE site_id=$1 AND path=$2)`, mount.SiteID, path).Scan(&conflict); err != nil {
			return err
		}
		if conflict {
			return resource.ErrRouteConflict
		}
		conflict, err = libraryItemRouteExists(ctx, queryer, mount.SiteID, path, item.ID, nil)
		if err != nil {
			return err
		}
		if conflict {
			return resource.ErrRouteConflict
		}
	}
	return nil
}

// Validate tree paths against source indexes, never enumerate a collection.
// The exclusive topology lock also covers creates, moves, restores and transfers.
func validateMirrorNamespaces(ctx context.Context, queryer routeQueryer, topologyChanged bool) error {
	mounts, err := mirrorMounts(ctx, queryer, 0)
	if err != nil {
		return err
	}
	for _, mount := range mounts {
		if _, err := effectiveRouteLibrary(ctx, queryer, mount, nil); err != nil {
			return err
		}
		if mount.Path == nil {
			return resource.ErrInvalidReference
		}
		rows, err := queryer.Query(ctx, `SELECT path FROM core.resources WHERE site_id=$1 AND path IS NOT NULL AND ($2='/' OR path=$2 OR path LIKE $2||'/%')`, mount.SiteID, *mount.Path)
		if err != nil {
			return err
		}
		var paths []string
		for rows.Next() {
			var path string
			if err := rows.Scan(&path); err != nil {
				rows.Close()
				return err
			}
			paths = append(paths, path)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if err := ensureTreePathsAvailable(ctx, queryer, mount.SiteID, paths, nil); err != nil {
			return fmt.Errorf("mirror %d: %w", mount.ID, err)
		}
		if topologyChanged {
			if err := ensureProspectiveLibraryNamespaceAvailable(ctx, queryer, mount); err != nil {
				return fmt.Errorf("mirror %d: %w", mount.ID, err)
			}
		}
	}
	return nil
}
