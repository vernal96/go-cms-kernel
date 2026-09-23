package app

import (
	"context"
	"errors"
	"log/slog"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/console"
	"github.com/vernal96/go-cms-kernel/eventbus"
	"github.com/vernal96/go-cms-kernel/modules/admin"
	"github.com/vernal96/go-cms-kernel/modules/core/group"
	coremanagement "github.com/vernal96/go-cms-kernel/modules/core/management"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/security"
)

func (a *App) Definition() Definition {
	if a == nil {
		return Definition{}
	}

	return cloneDefinition(a.definition)
}

func (a *App) Logger() *slog.Logger {
	if a == nil {
		return nil
	}

	return a.logger
}

func (a *App) EventBus() eventbus.Bus {
	if a == nil {
		return nil
	}

	return a.eventBus
}

func (a *App) Console() *console.Console {
	if a == nil {
		return nil
	}

	return a.console
}

func (a *App) ProfileBlueprint(
	code kernel.ProfileCode,
) (*kernel.ProfileBlueprint, bool) {
	if a == nil || a.closed.Load() || !a.booted.Load() {
		return nil, false
	}

	blueprint, exists := a.profileBlueprints[code]
	return blueprint, exists
}

func (a *App) AdminManagement() (*admin.Management, error) {
	if a == nil {
		return nil, errors.New("app is nil")
	}
	if a.closed.Load() {
		return nil, ErrClosed
	}
	if !a.booted.Load() || a.adminManagement == nil {
		return nil, ErrNotBooted
	}
	return a.adminManagement, nil
}

func (a *App) CMSManagement() (*coremanagement.Sites, *coremanagement.Resources, *coremanagement.Files, error) {
	if a == nil {
		return nil, nil, nil, errors.New("app is nil")
	}
	if a.closed.Load() {
		return nil, nil, nil, ErrClosed
	}
	if !a.booted.Load() || a.cmsSites == nil || a.cmsResources == nil || a.cmsFiles == nil {
		return nil, nil, nil, ErrNotBooted
	}
	return a.cmsSites, a.cmsResources, a.cmsFiles, nil
}

func (a *App) RuntimeByDomain(
	ctx context.Context,
	actor security.Actor,
	domain string,
) (*site.Runtime, error) {
	if a == nil || a.closed.Load() || !a.booted.Load() {
		if a != nil && a.closed.Load() {
			return nil, ErrClosed
		}
		return nil, ErrNotBooted
	}

	return a.sites.ResolveByDomain(ctx, actor, domain)
}

func (a *App) RuntimeBySiteID(
	ctx context.Context,
	actor security.Actor,
	id site.ID,
) (*site.Runtime, error) {
	if a == nil || a.closed.Load() || !a.booted.Load() {
		if a != nil && a.closed.Load() {
			return nil, ErrClosed
		}
		return nil, ErrNotBooted
	}
	runtime, exists := a.sites.RuntimeByID(id)
	if !exists {
		return nil, site.ErrNotFound
	}
	return a.sites.ResolveByDomain(ctx, actor, runtime.Site().Domain)
}

// ManagementSiteRuntime resolves the current immutable runtime for an
// authenticated site-management request after applying the shared site access
// policy. Optional module transports use this instead of feature-specific App
// state.
func (a *App) ManagementSiteRuntime(
	ctx context.Context,
	actor security.Actor,
	id site.ID,
	action group.SiteAccessAction,
) (*site.Runtime, error) {
	if a == nil {
		return nil, errors.New("app is nil")
	}
	if a.closed.Load() {
		return nil, ErrClosed
	}
	if !a.booted.Load() || a.sites == nil || a.siteAccessPolicy == nil {
		return nil, ErrNotBooted
	}
	if ctx == nil {
		return nil, errors.New("site management context is nil")
	}
	if id <= 0 {
		return nil, site.ErrNotFound
	}
	if err := a.siteAccessPolicy.Check(ctx, actor, id, action); err != nil {
		return nil, err
	}
	runtime, exists := a.sites.RuntimeByID(id)
	if !exists || runtime == nil {
		return nil, site.ErrNotFound
	}
	if err := a.sites.CheckCurrent(ctx, runtime); err != nil {
		return nil, err
	}
	return runtime, nil
}

func (a *App) ReloadSites(ctx context.Context) error {
	if a == nil {
		return errors.New("app is nil")
	}
	if ctx == nil {
		return errors.New("site reload context is nil")
	}
	if a.closed.Load() {
		return ErrClosed
	}
	if !a.booted.Load() {
		return ErrNotBooted
	}

	a.lifecycleMu.RLock()
	defer a.lifecycleMu.RUnlock()

	if a.closed.Load() {
		return ErrClosed
	}

	return a.sites.Reload(ctx)
}
