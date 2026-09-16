package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/job"
	"github.com/vernal96/go-cms-kernel/migrations"
	"github.com/vernal96/go-cms-kernel/modules/admin"
	"github.com/vernal96/go-cms-kernel/modules/core"
	coregroup "github.com/vernal96/go-cms-kernel/modules/core/group"
	coremanagement "github.com/vernal96/go-cms-kernel/modules/core/management"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	coreuser "github.com/vernal96/go-cms-kernel/modules/core/user"
	"github.com/vernal96/go-cms-kernel/outbox"
)

func (a *App) Boot(ctx context.Context) error {
	if a == nil {
		return errors.New("app is nil")
	}

	a.bootOnce.Do(func() {
		logContext := ctx
		if logContext == nil {
			logContext = context.Background()
		}
		if a.logger != nil {
			a.logger.InfoContext(
				logContext,
				"application boot started",
				slog.String("event", "app.boot.started"),
			)
		}
		a.bootErr = a.boot(ctx)
		if a.logger == nil {
			return
		}
		if a.bootErr != nil {
			a.logger.ErrorContext(
				logContext,
				"application boot failed",
				slog.String("event", "app.boot.failed"),
				slog.Any("error", a.bootErr),
			)
			return
		}
		a.logger.InfoContext(
			logContext,
			"application boot completed",
			slog.String("event", "app.boot.completed"),
		)
	})

	return a.bootErr
}

func (a *App) boot(ctx context.Context) error {
	if ctx == nil {
		return errors.New("app boot context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	a.lifecycleMu.RLock()
	defer a.lifecycleMu.RUnlock()

	if a.closed.Load() {
		return ErrClosed
	}

	if err := migrations.NewManager().UpAll(ctx, a.migrationPlan); err != nil {
		return err
	}

	var knownHookModules []kernel.ModuleCode
	for _, profile := range a.definition.Profiles {
		for _, item := range profile.Modules {
			knownHookModules = append(knownHookModules, item.Module.Code())
		}
	}
	applicationHooks, err := kernel.BuildApplicationEntityHooks(ctx, a.definition.ModuleApplications, knownHookModules)
	if err != nil {
		return err
	}
	a.applicationHooks = applicationHooks

	coreServices, err := core.NewServices(
		a.coreDatabase,
		a.permissions,
		a.filesystems,
		a.definition.PasswordHasher,
		a.caches,
		applicationHooks,
	)
	if err != nil {
		return err
	}
	a.coreDatabase = coreServices.Database()
	a.main.adapters[core.ModuleCode] = a.coreDatabase
	profiles, err := bindCoreServices(a.definition.Profiles, coreServices)
	if err != nil {
		return err
	}

	factory, err := kernel.NewProfileRuntimeFactory(
		a,
		kernel.RuntimeServices{
			Caches:             a.caches,
			Filesystems:        a.filesystems,
			EventBus:           a.eventBus,
			Logger:             a.logger,
			ModuleApplications: a.definition.ModuleApplications,
		},
	)
	if err != nil {
		return err
	}

	profileBlueprints := make(
		map[kernel.ProfileCode]*kernel.ProfileBlueprint,
		len(a.definition.Profiles),
	)

	for _, profile := range profiles {
		blueprint, err := factory.Compile(ctx, profile)
		if err != nil {
			return fmt.Errorf(
				"compile profile blueprint %q: %w",
				profile.Code,
				err,
			)
		}

		profileBlueprints[profile.Code] = blueprint
	}

	if err := coreServices.BuildContent(
		ctx,
		profileResolver(profileBlueprints),
	); err != nil {
		return err
	}
	catalog := coreServices.Sites
	resourceService := coreServices.Resources
	siteManagementRepository, ok := a.coreDatabase.Sites().(site.ManagementRepository)
	if !ok {
		return errors.New("site management repository is unavailable")
	}
	resourceManagementRepository, ok := a.coreDatabase.Resources().(resource.ManagementRepository)
	if !ok {
		return errors.New("resource management repository is unavailable")
	}
	userManagementRepository, ok := a.coreDatabase.Users().(coreuser.ManagementRepository)
	if !ok {
		return errors.New("user management repository is unavailable")
	}
	groupManagementRepository, ok := a.coreDatabase.Groups().(coregroup.ManagementRepository)
	if !ok {
		return errors.New("group management repository is unavailable")
	}
	siteAccessPolicy := a.definition.SiteAccessPolicy
	if siteAccessPolicy == nil {
		siteAccessPolicy, err = coremanagement.NewGroupSiteAccessPolicy(
			groupManagementRepository,
			coreServices.Authorization,
		)
		if err != nil {
			return err
		}
	}
	cmsSites, err := coremanagement.NewSites(coremanagement.SiteDependencies{
		Profiles:         a.definition.Profiles,
		ProfileSource:    profileResolver(profileBlueprints),
		SiteRepository:   siteManagementRepository,
		Sites:            catalog,
		Resources:        resourceService,
		Authorizer:       coreServices.Authorization,
		SiteAccessPolicy: siteAccessPolicy,
	})
	if err != nil {
		return err
	}
	cmsResources, err := coremanagement.NewResources(coremanagement.ResourceDependencies{
		Sites: catalog, Resources: resourceService, LibraryItems: coreServices.LibraryItems,
		Revisions:          coreServices.Revisions,
		Administrator:      coreServices.Authorization,
		ResourceRepository: resourceManagementRepository,
		Authorizer:         coreServices.Authorization, SiteAccessPolicy: siteAccessPolicy,
	})
	if err != nil {
		return err
	}
	cmsFiles, err := coremanagement.NewFiles(coreServices.Files, coreServices.Authorization)
	if err != nil {
		return err
	}
	if err := coreServices.ConfigureManagementImages(cmsFiles, a.definition.Profiles, a.caches, a.logger); err != nil {
		return err
	}

	adminManagement, err := admin.NewManagement(admin.ManagementDependencies{
		Images: coreServices.Images, ImageFiles: cmsFiles,
		Profiles:           a.definition.Profiles,
		SiteRepository:     siteManagementRepository,
		Sites:              catalog,
		ResourceRepository: resourceManagementRepository,
		Authorizer:         coreServices.Authorization,
		Permissions:        a.permissions,
		SiteAccessPolicy:   siteAccessPolicy,
		Users:              coreServices.Users,
		UserRepository:     userManagementRepository,
		Groups:             coreServices.Groups,
		GroupRepository:    groupManagementRepository,
		Access:             coreServices.Authorization,
		Files:              coreServices.Files,
		Media:              coreServices.Media,
		UploadTimeout:      a.definition.UploadTimeout,
		AvatarStorage:      a.definition.AvatarStorage,
		AvatarMaxSize:      a.definition.AvatarMaxSize,
	})
	if err != nil {
		return err
	}

	if err := catalog.AddRuntimePreparer(ctx, adminManagement.PrepareRuntimes); err != nil {
		return fmt.Errorf("prepare admin navigation: %w", err)
	}

	a.profileBlueprints = profileBlueprints
	a.sites = catalog
	a.services = servicesFromCore(coreServices)
	a.cmsSites = cmsSites
	a.cmsResources = cmsResources
	a.cmsFiles = cmsFiles
	a.adminManagement = adminManagement
	a.siteAccessPolicy = siteAccessPolicy
	jobRunner, err := jobRunnerFromProfiles(a.definition.Profiles, catalog)
	if err != nil {
		return err
	}
	hookRunner, hookTopics, err := a.prepareEntityHooks(ctx, catalog)
	if err != nil {
		return err
	}
	var publisher *outbox.Publisher
	if len(a.outboxSources) > 0 {
		publisher, err = outbox.NewPublisher(a.eventBus, a.outboxSources, a.logger, a.definition.OutboxPublisher)
		if err != nil {
			return err
		}
	}
	workerContext, cancelWorkers := context.WithCancel(context.Background())
	a.workerCancel = cancelWorkers
	backgroundTasks := newRuntimeBackgroundTasks(workerContext, a.logger)
	a.workers.Add(1)
	go func() {
		defer a.workers.Done()
		backgroundTasks.run()
	}()
	if err := catalog.AddRuntimePreparer(ctx, backgroundTasks.prepare); err != nil {
		cancelWorkers()
		a.workers.Wait()
		return fmt.Errorf("prepare runtime background tasks: %w", err)
	}
	a.startEntityHooks(workerContext, hookRunner, hookTopics)
	if len(a.outboxSources) > 0 {
		a.outboxPublisher = publisher
		a.workers.Add(1)
		go func() {
			defer a.workers.Done()
			if err := publisher.Run(workerContext); err != nil && a.logger != nil {
				a.logger.Error("outbox publisher exited", slog.String("event", "outbox.publisher.failed"), slog.Any("error", err))
			}
		}()
	}
	if jobRunner != nil {
		a.workers.Add(1)
		go func() {
			defer a.workers.Done()
			if err := jobRunner.Run(workerContext, a.eventBus, "go-cms"); err != nil && a.logger != nil && workerContext.Err() == nil {
				a.logger.Error("job runner exited", slog.String("event", "job.runner.failed"), slog.Any("error", err))
			}
		}()
	}
	a.booted.Store(true)
	return nil
}

func jobRunnerFromProfiles(profiles []kernel.Profile, catalog *site.Catalog) (*job.Runner, error) {
	owners, err := declaredNames(profiles, func(module kernel.Module) ([]string, bool) {
		provider, ok := module.(job.NamesProvider)
		if !ok {
			return nil, false
		}
		return provider.JobNames(), true
	})
	if err != nil {
		return nil, fmt.Errorf("collect job declarations: %w", err)
	}
	registry := job.NewRegistry()
	if len(owners) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(owners))
	for name := range owners {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		name := name
		owner := owners[name]
		if err := registry.Register(name, func(ctx context.Context, item job.Envelope) error {
			return dispatchRuntimeJob(ctx, catalog, owner, name, item)
		}); err != nil {
			return nil, err
		}
	}
	return job.NewRunner(registry)
}

func dispatchRuntimeJob(ctx context.Context, catalog *site.Catalog, owner kernel.ModuleCode, name string, item job.Envelope) error {
	runtimes := catalog.Runtimes()
	if item.ScopeID != "" {
		var scoped *site.Runtime
		for _, runtime := range runtimes {
			if item.ScopeID == fmt.Sprint(runtime.Site().ID) {
				scoped = runtime
				break
			}
		}
		if scoped == nil {
			return fmt.Errorf("%w: scoped job %q site %q no longer exists", job.ErrObsolete, name, item.ScopeID)
		}
		moduleRuntime, exists := scoped.Profile().Registry().Module(owner)
		if !exists {
			return fmt.Errorf("%w: scoped job %q module %q is absent from site %q", job.ErrObsolete, name, owner, item.ScopeID)
		}
		return dispatchModuleJob(ctx, moduleRuntime, name, item)
	}
	candidates := make([]job.Definition, 0, 1)
	for _, runtime := range runtimes {
		moduleRuntime, exists := runtime.Profile().Registry().Module(owner)
		if !exists {
			continue
		}
		provider, ok := moduleRuntime.(job.Provider)
		if !ok {
			return fmt.Errorf("job owner module %q does not provide runtime jobs", owner)
		}
		candidates = append(candidates, matchingJobDefinitions(provider, name, item.ScopeID)...)
	}
	return runJobCandidate(ctx, item, candidates)
}

func dispatchModuleJob(ctx context.Context, moduleRuntime kernel.ModuleRuntime, name string, item job.Envelope) error {
	provider, ok := moduleRuntime.(job.Provider)
	if !ok {
		return fmt.Errorf("job owner module %q does not provide runtime jobs", moduleRuntime.ModuleCode())
	}
	return runJobCandidate(ctx, item, matchingJobDefinitions(provider, name, item.ScopeID))
}

func matchingJobDefinitions(provider job.Provider, name, scopeID string) []job.Definition {
	result := make([]job.Definition, 0, 1)
	for _, definition := range provider.Jobs() {
		if definition.Name == name && definition.Handler != nil && (definition.ScopeID == "" || definition.ScopeID == scopeID) {
			result = append(result, definition)
		}
	}
	return result
}

func runJobCandidate(ctx context.Context, item job.Envelope, candidates []job.Definition) error {
	if len(candidates) == 0 {
		return fmt.Errorf("job handler %q scope %q is missing from its current module runtime", item.Name, item.ScopeID)
	}
	if len(candidates) > 1 {
		return fmt.Errorf("job handler %q scope %q is ambiguous", item.Name, item.ScopeID)
	}
	return candidates[0].Handler(ctx, item)
}

func declaredNames(
	profiles []kernel.Profile,
	resolve func(kernel.Module) ([]string, bool),
) (map[string]kernel.ModuleCode, error) {
	owners := make(map[string]kernel.ModuleCode)
	modules := make(map[kernel.ModuleCode][]string)
	for _, profile := range profiles {
		for _, profileModule := range profile.Modules {
			names, ok := resolve(profileModule.Module)
			if !ok {
				continue
			}
			names = append([]string(nil), names...)
			sort.Strings(names)
			if previous, exists := modules[profileModule.Module.Code()]; exists {
				if !slices.Equal(previous, names) {
					return nil, fmt.Errorf("module %q has inconsistent declarations", profileModule.Module.Code())
				}
				continue
			}
			modules[profileModule.Module.Code()] = names
			for _, name := range names {
				if name == "" {
					return nil, fmt.Errorf("module %q declares an empty name", profileModule.Module.Code())
				}
				if owner, exists := owners[name]; exists && owner != profileModule.Module.Code() {
					return nil, fmt.Errorf("name %q is declared by modules %q and %q", name, owner, profileModule.Module.Code())
				}
				owners[name] = profileModule.Module.Code()
			}
		}
	}
	return owners, nil
}
