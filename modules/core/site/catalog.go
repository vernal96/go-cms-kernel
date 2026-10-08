package site

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/security"
)

func NewCatalog(
	repository Repository,
	profiles ProfileResolver,
	access Access,
	fileServices ...file.Service,
) (*Catalog, error) {
	if repository == nil {
		return nil, errors.New("site repository is nil")
	}

	if profiles == nil {
		return nil, errors.New("profile resolver is nil")
	}
	if access == nil {
		return nil, errors.New("site access service is nil")
	}

	catalog := &Catalog{
		repository: repository,
		profiles:   profiles,
		access:     access,
	}
	if len(fileServices) > 0 {
		catalog.files = fileServices[0]
	}

	catalog.snapshot.Store(&runtimeSnapshot{
		byDomain: make(map[string]*Runtime),
		byID:     make(map[ID]*Runtime),
	})

	return catalog, nil
}

func (c *Catalog) RuntimeByDomain(
	domain string,
) (*Runtime, bool) {
	domain, err := NormalizeDomain(domain)
	if err != nil {
		return nil, false
	}

	snapshot := c.snapshot.Load()
	if snapshot == nil {
		return nil, false
	}

	runtime, exists := snapshot.byDomain[domain]
	return runtime, exists
}

func (c *Catalog) RuntimeByID(
	id ID,
) (*Runtime, bool) {
	if id <= 0 {
		return nil, false
	}

	snapshot := c.snapshot.Load()
	if snapshot == nil {
		return nil, false
	}

	runtime, exists := snapshot.byID[id]
	return runtime, exists
}

func (c *Catalog) Runtimes() []*Runtime {
	if c == nil {
		return nil
	}
	snapshot := c.snapshot.Load()
	if snapshot == nil {
		return nil
	}
	return snapshotRuntimes(snapshot)
}

func snapshotRuntimes(snapshot *runtimeSnapshot) []*Runtime {
	if snapshot == nil {
		return nil
	}
	ids := make([]ID, 0, len(snapshot.byID))
	for id := range snapshot.byID {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	result := make([]*Runtime, 0, len(ids))
	for _, id := range ids {
		result = append(result, snapshot.byID[id])
	}
	return result
}

func (c *Catalog) ResolveByDomain(
	ctx context.Context,
	actor security.Actor,
	domain string,
) (*Runtime, error) {
	if ctx == nil {
		return nil, errors.New("site resolve context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	runtime, exists := c.RuntimeByDomain(domain)
	if !exists {
		return nil, ErrNotFound
	}
	if err := c.CheckReadAccess(ctx, actor, runtime); err != nil {
		return nil, err
	}
	return runtime, nil
}

func (c *Catalog) CheckReadAccess(
	ctx context.Context,
	actor security.Actor,
	runtime *Runtime,
) error {
	if ctx == nil {
		return errors.New("site access context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if runtime == nil {
		return ErrNotFound
	}
	if err := c.CheckCurrent(ctx, runtime); err != nil {
		return err
	}
	if err := c.access.Check(ctx, actor, readPermission); err != nil {
		return err
	}
	guest, err := c.access.IsGuestSubject(ctx, actor)
	if err != nil {
		return err
	}
	if guest && !runtime.site.IsPublic {
		return security.ErrForbidden
	}
	return nil
}

func (c *Catalog) Reload(ctx context.Context) error {
	if ctx == nil {
		return errors.New("site reload context is nil")
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	c.mutationMu.Lock()
	defer c.mutationMu.Unlock()

	sites, err := c.repository.List(ctx)
	if err != nil {
		return fmt.Errorf("list sites: %w", err)
	}

	next := &runtimeSnapshot{
		byDomain: make(map[string]*Runtime, len(sites)),
		byID:     make(map[ID]*Runtime, len(sites)),
	}

	for index, item := range sites {
		blueprint, exists := c.profiles.ProfileBlueprint(
			item.ProfileCode,
		)
		if !exists {
			return fmt.Errorf(
				"site at index %d references unknown profile %q",
				index,
				item.ProfileCode,
			)
		}

		runtime, err := NewRuntimeFromBlueprint(ctx, item, blueprint)
		if err != nil {
			return fmt.Errorf(
				"build site runtime at index %d with id %d: %w",
				index,
				item.ID,
				err,
			)
		}
		if err := c.validateFileReferences(ctx, security.System(), runtime.fileReferences, nil); err != nil {
			return fmt.Errorf("validate site file references at index %d: %w", index, err)
		}
		domain := runtime.site.Domain

		if _, exists := next.byDomain[domain]; exists {
			return fmt.Errorf(
				"duplicate normalized site domain %q",
				domain,
			)
		}
		if _, exists := next.byID[item.ID]; exists {
			return fmt.Errorf(
				"duplicate site id %d",
				item.ID,
			)
		}

		next.byDomain[domain] = runtime
		next.byID[item.ID] = runtime
	}

	current := c.snapshot.Load()
	preparations, err := c.prepareRuntimePlan(ctx, current, next)
	if err != nil {
		return fmt.Errorf("prepare reloaded site runtimes: %w", err)
	}
	c.publishRuntimeSnapshot(next, preparations)
	return nil
}

func (c *Catalog) Create(
	ctx context.Context,
	actor security.Actor,
	input CreateInput,
) (*Runtime, error) {
	if ctx == nil {
		return nil, errors.New("site create context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := c.access.Check(ctx, actor, createPermission); err != nil {
		return nil, err
	}
	blueprint, exists := c.profiles.ProfileBlueprint(input.ProfileCode)
	if !exists {
		return nil, fmt.Errorf("%w: profile %q is unknown", ErrInvalid, input.ProfileCode)
	}

	c.mutationMu.Lock()
	defer c.mutationMu.Unlock()
	currentSnapshot := c.snapshot.Load()
	if currentSnapshot == nil {
		return nil, errors.New("site runtime snapshot is nil")
	}

	candidate, fileReferences, err := normalizeRuntimeSite(Site{
		ID:          1,
		ProfileCode: input.ProfileCode,
		Name:        input.Name,
		Domain:      input.Domain,
		Locale:      input.Locale,
		Settings:    cloneSettings(input.Settings),
		IsPublic:    input.IsPublic,
	}, blueprint.Profile().Code, blueprint.ParamSchema())
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if err := c.validateFileReferences(ctx, actor, fileReferences, nil); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if _, exists := currentSnapshot.byDomain[candidate.Domain]; exists {
		return nil, ErrConflict
	}
	management, ok := c.repository.(ManagementRepository)
	if !ok {
		return nil, errors.New("site management repository is unavailable")
	}
	candidate.ID = 0
	stored, err := management.Create(
		ctx,
		actor.AuditUserID(),
		candidate,
	)
	if err != nil {
		return nil, fmt.Errorf("create site: %w", err)
	}
	nextRuntime, err := NewRuntimeFromBlueprint(ctx, stored, blueprint)
	if err != nil {
		return nil, rollbackCreatedSite(
			ctx,
			management,
			stored.ID,
			fmt.Errorf("build created site runtime: %w", err),
		)
	}
	if _, exists := currentSnapshot.byID[nextRuntime.site.ID]; exists {
		return nil, rollbackCreatedSite(
			ctx,
			management,
			stored.ID,
			fmt.Errorf("%w: site id %d already exists", ErrConflict, stored.ID),
		)
	}
	if _, exists := currentSnapshot.byDomain[nextRuntime.site.Domain]; exists {
		return nil, rollbackCreatedSite(
			ctx,
			management,
			stored.ID,
			fmt.Errorf(
				"%w: site domain %q already exists",
				ErrConflict,
				nextRuntime.site.Domain,
			),
		)
	}
	nextSnapshot := cloneSnapshot(currentSnapshot, 1)
	nextSnapshot.byDomain[nextRuntime.site.Domain] = nextRuntime
	nextSnapshot.byID[nextRuntime.site.ID] = nextRuntime
	preparations, err := c.prepareRuntimePlan(ctx, currentSnapshot, nextSnapshot)
	if err != nil {
		return nil, rollbackCreatedSite(
			ctx,
			management,
			stored.ID,
			fmt.Errorf("prepare created site runtime: %w", err),
		)
	}
	c.publishRuntimeSnapshot(nextSnapshot, preparations)
	return nextRuntime, nil
}

func rollbackCreatedSite(
	ctx context.Context,
	repository ManagementRepository,
	id ID,
	cause error,
) error {
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := repository.Delete(rollbackCtx, id); err != nil {
		return errors.Join(
			cause,
			fmt.Errorf("rollback created site %d: %w", id, err),
		)
	}
	return cause
}

func (c *Catalog) Update(
	ctx context.Context,
	actor security.Actor,
	input UpdateInput,
) (*Runtime, error) {
	if ctx == nil {
		return nil, errors.New("site settings update context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := c.access.Check(ctx, actor, updatePermission); err != nil {
		return nil, err
	}
	if input.ID <= 0 {
		return nil, fmt.Errorf("%w: invalid site id", ErrInvalid)
	}

	c.mutationMu.Lock()
	defer c.mutationMu.Unlock()

	currentSnapshot := c.snapshot.Load()
	if currentSnapshot == nil {
		return nil, errors.New("site runtime snapshot is nil")
	}

	current, exists := currentSnapshot.byID[input.ID]
	if !exists {
		return nil, ErrNotFound
	}

	blueprint, exists := c.profiles.ProfileBlueprint(input.ProfileCode)
	if !exists {
		return nil, fmt.Errorf("%w: profile %q is unknown", ErrInvalid, input.ProfileCode)
	}
	settings, err := blueprint.ParamSchema().Validate(input.Settings)
	if err != nil {
		return nil, fmt.Errorf("%w: validate site settings: %w", ErrInvalid, err)
	}
	item := current.Site()
	item.ProfileCode = input.ProfileCode
	item.Name = input.Name
	item.Domain = input.Domain
	item.Locale = strings.TrimSpace(input.Locale)
	item.Settings = settings
	item.IsPublic = input.IsPublic

	nextRuntime, err := NewRuntimeFromBlueprint(ctx, item, blueprint)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if err := c.validateFileReferences(ctx, actor, nextRuntime.fileReferences, current.site.FileReferences); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if existing, exists := currentSnapshot.byDomain[nextRuntime.site.Domain]; exists && existing.site.ID != input.ID {
		return nil, ErrConflict
	}
	nextSnapshot := cloneSnapshot(currentSnapshot, 0)
	delete(nextSnapshot.byDomain, current.site.Domain)
	nextSnapshot.byDomain[nextRuntime.site.Domain] = nextRuntime
	nextSnapshot.byID[input.ID] = nextRuntime
	preparations, err := c.prepareRuntimePlan(ctx, currentSnapshot, nextSnapshot)
	if err != nil {
		return nil, runtimePreparationError("prepare updated site runtime", err)
	}
	stored, err := c.repository.Update(
		ctx,
		actor.AuditUserID(),
		nextRuntime.Site(),
	)
	if err != nil {
		abortRuntimePreparations(preparations)
		return nil, fmt.Errorf("update site: %w", err)
	}
	nextRuntime.site.Version = stored.Version
	nextRuntime.site.CreatedAt = stored.CreatedAt
	nextRuntime.site.UpdatedAt = stored.UpdatedAt
	nextRuntime.site.CreatedBy = cloneUserID(stored.CreatedBy)
	nextRuntime.site.UpdatedBy = cloneUserID(stored.UpdatedBy)

	c.publishRuntimeSnapshot(nextSnapshot, preparations)

	return nextRuntime, nil
}

func (c *Catalog) Delete(
	ctx context.Context,
	actor security.Actor,
	id ID,
) error {
	if ctx == nil {
		return errors.New("site delete context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.access.Check(ctx, actor, deletePermission); err != nil {
		return err
	}
	if id <= 0 {
		return errors.New("invalid site id")
	}
	c.mutationMu.Lock()
	defer c.mutationMu.Unlock()
	currentSnapshot := c.snapshot.Load()
	if currentSnapshot == nil {
		return errors.New("site runtime snapshot is nil")
	}
	current, exists := currentSnapshot.byID[id]
	if !exists {
		return ErrNotFound
	}
	management, ok := c.repository.(ManagementRepository)
	if !ok {
		return errors.New("site management repository is unavailable")
	}
	nextSnapshot := cloneSnapshot(currentSnapshot, -1)
	delete(nextSnapshot.byDomain, current.site.Domain)
	delete(nextSnapshot.byID, id)
	preparations, err := c.prepareRuntimePlan(ctx, currentSnapshot, nextSnapshot)
	if err != nil {
		return runtimePreparationError("prepare deleted site runtime", err)
	}
	if err := management.Delete(ctx, id); err != nil {
		abortRuntimePreparations(preparations)
		return fmt.Errorf("delete site: %w", err)
	}
	c.publishRuntimeSnapshot(nextSnapshot, preparations)
	return nil
}
