package core

import (
	"context"

	"github.com/vernal96/go-cms-kernel/modules/core/site"
)

func siteVersions(ctx context.Context, base site.Repository) (map[site.ID]int64, error) {
	if r, ok := base.(site.VersionRepository); ok {
		return r.RuntimeVersions(ctx)
	}
	items, err := base.List(ctx)
	if err != nil {
		return nil, err
	}
	result := map[site.ID]int64{}
	for _, item := range items {
		result[item.ID] = item.Version
	}
	return result, nil
}
func siteVersion(ctx context.Context, base site.Repository, id site.ID) (int64, error) {
	if r, ok := base.(site.VersionRepository); ok {
		return r.RuntimeVersion(ctx, id)
	}
	versions, err := siteVersions(ctx, base)
	if err != nil {
		return 0, err
	}
	version, ok := versions[id]
	if !ok {
		return 0, site.ErrNotFound
	}
	return version, nil
}
func (r *invalidatingSiteRepository) RuntimeVersion(ctx context.Context, id site.ID) (int64, error) {
	return siteVersion(ctx, r.base, id)
}
func (r *invalidatingSiteRepository) RuntimeVersions(ctx context.Context) (map[site.ID]int64, error) {
	return siteVersions(ctx, r.base)
}
func (r *cachedSiteRepository) RuntimeVersion(ctx context.Context, id site.ID) (int64, error) {
	return siteVersion(ctx, r.base, id)
}
func (r *cachedSiteRepository) RuntimeVersions(ctx context.Context) (map[site.ID]int64, error) {
	return siteVersions(ctx, r.base)
}
