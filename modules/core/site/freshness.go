package site

import (
	"context"
	"errors"
)

// VersionRepository reads authoritative versions, never a cache. Adapters with
// distributed storage implement this to prevent stale runtimes granting access.
type VersionRepository interface {
	RuntimeVersions(context.Context) (map[ID]int64, error)
	RuntimeVersion(context.Context, ID) (int64, error)
}

func (c *Catalog) CheckCurrent(ctx context.Context, runtime *Runtime) error {
	if runtime == nil {
		return ErrNotFound
	}
	versions, ok := c.repository.(VersionRepository)
	if !ok {
		return nil
	}
	version, err := versions.RuntimeVersion(ctx, runtime.site.ID)
	if errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return errors.Join(ErrUnavailable, err)
	}
	if version != runtime.site.Version {
		return ErrUnavailable
	}
	return nil
}

// Synchronize rebuilds only when persisted versions differ; requests only check
// a version and never compile a SiteRuntime. Failed rebuilds keep the old snapshot.
func (c *Catalog) Synchronize(ctx context.Context) error {
	versions, ok := c.repository.(VersionRepository)
	if !ok {
		return nil
	}
	latest, err := versions.RuntimeVersions(ctx)
	if err != nil {
		return err
	}
	current := c.snapshot.Load()
	if current == nil {
		return ErrUnavailable
	}
	if len(latest) != len(current.byID) {
		return c.Reload(ctx)
	}
	for id, version := range latest {
		if runtime := current.byID[id]; runtime == nil || runtime.site.Version != version {
			return c.Reload(ctx)
		}
	}
	return nil
}
