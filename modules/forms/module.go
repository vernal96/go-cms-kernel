package forms

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/adminui"
	"github.com/vernal96/go-cms-kernel/background"
	"github.com/vernal96/go-cms-kernel/job"
	"github.com/vernal96/go-cms-kernel/modules/core"
	corefile "github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/mail"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

type Config struct {
	ActionMaxAttempts      int
	Public                 PublicLimits
	SpoolEnabled           bool
	SpoolTTL               time.Duration
	SpoolCleanupInterval   time.Duration
	SpoolCleanupBatch      int
	DefaultCaptchaProvider string
}

type Database interface {
	kernel.ModuleDatabase
	Forms() Repository
}

type coreDependency interface {
	kernel.ModuleRuntime
	Authorization() security.Authorizer
	Files() corefile.ManagementService
}

type mailDependency interface {
	kernel.ModuleRuntime
	Mail() *mail.Service
}

type Module struct{}

func (Module) Code() kernel.ModuleCode { return ModuleCode }
func (Module) Dependencies() []kernel.ModuleCode {
	return []kernel.ModuleCode{core.ModuleCode, mail.ModuleCode}
}
func (Module) ModuleDescriptor() kernel.ModuleDescriptor {
	return kernel.ModuleDescriptor{Label: "Формы", Description: "Конструктор форм и результаты отправок"}
}
func (Module) Registry() kernel.ModuleRegistry {
	return kernel.ModuleRegistry{FieldTypes: fieldTypes(), PermissionEntities: []permission.Entity{{Code: "form"}, {Code: "result"}, {Code: "action"}, {Code: "status"}}}
}
func (Module) JobNames() []string { return []string{ExecuteActionJobName} }

func (Module) Build(ctx context.Context, moduleContext kernel.ModuleContext) (kernel.ModuleRuntime, error) {
	database, err := kernel.ModuleDatabaseFrom[Database](moduleContext, "", ModuleCode)
	if err != nil {
		return nil, err
	}
	if database.Forms() == nil {
		return nil, errors.New("Forms repository is nil")
	}
	coreRuntime, err := kernel.ModuleDependencyFrom[coreDependency](moduleContext, core.ModuleCode)
	if err != nil {
		return nil, err
	}
	mailRuntime, err := kernel.ModuleDependencyFrom[mailDependency](moduleContext, mail.ModuleCode)
	if err != nil {
		return nil, err
	}
	if mailRuntime.Mail() == nil {
		return nil, errors.New("Forms Mail dependency is unavailable")
	}
	config, err := kernel.ModuleConfigFrom[Config](moduleContext)
	if err != nil {
		return nil, err
	}
	config, err = normalizeConfig(config)
	if err != nil {
		return nil, err
	}
	application, err := kernel.ModuleApplicationFrom[Application](moduleContext)
	if err != nil {
		return nil, err
	}
	providers := make(map[string]CaptchaProvider, len(application.Providers))
	for index, provider := range application.Providers {
		if provider == nil || strings.TrimSpace(provider.Code()) == "" {
			return nil, fmt.Errorf("Forms CAPTCHA provider at index %d is invalid", index)
		}
		if _, exists := providers[provider.Code()]; exists {
			return nil, fmt.Errorf("Forms CAPTCHA provider %q is duplicated", provider.Code())
		}
		providers[provider.Code()] = provider
	}
	if _, exists := providers[config.DefaultCaptchaProvider]; !exists {
		return nil, fmt.Errorf("default Forms CAPTCHA provider %q is unavailable", config.DefaultCaptchaProvider)
	}
	siteIDValue, err := strconv.ParseInt(moduleContext.Scope().SiteID(), 10, 64)
	if err != nil || siteIDValue <= 0 {
		return nil, errors.New("Forms runtime site scope is invalid")
	}
	var spool *UploadSpool
	if config.SpoolEnabled {
		disk, exists := moduleContext.Filesystems().Disk(SpoolFilesystemAlias)
		if !exists {
			return nil, errors.New("Forms spool filesystem binding is unavailable")
		}
		spool, err = NewUploadSpool(site.ID(siteIDValue), disk)
		if err != nil {
			return nil, err
		}
	}
	elements, err := newElementCatalog()
	if err != nil {
		return nil, err
	}
	actions := newActionRegistry()
	if err := actions.Register(mailActionType{mail: mailRuntime.Mail(), fieldTypes: moduleContext.Registry()}); err != nil {
		return nil, err
	}
	service, err := NewService(site.ID(siteIDValue), database.Forms(), moduleContext.Registry(), elements, actions, providers, config.DefaultCaptchaProvider, coreRuntime.Authorization(), coreRuntime.Files(), spool, config.Public, moduleContext.Logger())
	if err != nil {
		return nil, err
	}
	worker, err := newWorker(site.ID(siteIDValue), database.Forms(), actions, spool, service.lifecycle, config.ActionMaxAttempts, moduleContext.Logger())
	if err != nil {
		return nil, err
	}
	management, err := NewManagementHTTPHandler(service)
	if err != nil {
		return nil, err
	}
	return &Runtime{service: service, worker: worker, actions: actions, managementHTTP: management, spool: spool, config: config, logger: moduleContext.Logger()}, nil
}

type Runtime struct {
	service        *Service
	worker         *Worker
	actions        *actionRegistry
	managementHTTP http.Handler
	spool          *UploadSpool
	config         Config
	logger         *slog.Logger
}

func (*Runtime) ModuleCode() kernel.ModuleCode { return ModuleCode }
func (r *Runtime) Forms() *Service             { return r.service }
func (r *Runtime) RegisterActionType(actionType ActionType) error {
	return r.actions.Register(actionType)
}
func (r *Runtime) RegisterElementType(element ElementType) error {
	return r.service.elements.Register(element)
}
func (r *Runtime) FinalizeRuntimeBuild(context.Context) error {
	if err := r.actions.Seal(); err != nil {
		return err
	}
	return r.service.elements.Seal()
}
func (r *Runtime) SiteManagementHTTP() httptransport.SiteManagementContribution {
	return httptransport.SiteManagementContribution{Path: "forms", Handler: r.managementHTTP}
}
func (r *Runtime) HTTP() httptransport.Builder { return newPublicHTTPBuilder(r.service) }
func (r *Runtime) Jobs() []job.Definition {
	return []job.Definition{{Name: ExecuteActionJobName, ScopeID: fmt.Sprint(r.service.siteID), Handler: r.worker.Handle}}
}
func (r *Runtime) BackgroundTasks() []background.Task {
	if r.spool == nil {
		return nil
	}
	return []background.Task{{Name: "forms.spool_cleanup", Run: r.runSpoolCleanup}}
}

func (r *Runtime) PrepareRuntimeTransition(ctx context.Context, transition kernel.RuntimeTransition) (kernel.PreparedRuntimeTransition, error) {
	if transition.Reason != kernel.RuntimeTransitionProfileChange && transition.Reason != kernel.RuntimeTransitionSiteDelete {
		return nil, fmt.Errorf("unsupported Forms runtime transition reason %q", transition.Reason)
	}
	if transition.ScopeID != fmt.Sprint(r.service.siteID) {
		return nil, errors.New("Forms runtime transition scope is invalid")
	}
	prepared := &preparedRuntimeTransition{lifecycle: r.service.lifecycle}
	if err := r.service.lifecycle.beginDrain(); err != nil {
		return nil, err
	}
	active, err := r.service.repository.HasActiveExecutions(ctx, r.service.siteID)
	if err != nil {
		prepared.Abort()
		return nil, err
	}
	if active {
		if r.logger != nil {
			r.logger.WarnContext(ctx, "Forms runtime transition blocked",
				slog.String("event", "forms.runtime.transition.blocked"),
				slog.Int64("site_id", int64(r.service.siteID)),
				slog.String("reason", string(transition.Reason)),
			)
		}
		prepared.Abort()
		return nil, fmt.Errorf("%w: %w for site %d", kernel.ErrRuntimeTransitionBlocked, ErrActiveExecutions, r.service.siteID)
	}
	if r.spool != nil {
		if _, err := r.spool.Purge(ctx, r.config.SpoolCleanupBatch); err != nil {
			prepared.Abort()
			return nil, fmt.Errorf("purge Forms spool before runtime transition: %w", err)
		}
		if err := r.service.repository.MarkAllUploadSpoolDeleted(ctx, r.service.siteID); err != nil {
			prepared.Abort()
			return nil, fmt.Errorf("mark purged Forms uploads before runtime transition: %w", err)
		}
	}
	return prepared, nil
}

func (r *Runtime) runSpoolCleanup(ctx context.Context) (resultErr error) {
	defer func() { resultErr = errors.Join(resultErr, r.spool.CloseCleanupScan()) }()
	cleanup := func() error {
		deleted, err := r.spool.Cleanup(ctx, time.Now().UTC().Add(-r.config.SpoolTTL), r.config.SpoolCleanupBatch, func(ctx context.Context, keys []string) (map[string]struct{}, error) {
			return r.service.repository.ActiveSpoolReferences(ctx, r.service.siteID, keys)
		})
		if err == nil && len(deleted) > 0 {
			err = r.service.repository.MarkUploadSpoolReferencesDeleted(ctx, r.service.siteID, deleted)
		}
		if err != nil && r.logger != nil {
			r.logger.ErrorContext(ctx, "Forms spool cleanup failed", slog.String("event", "forms.spool.cleanup.failed"), slog.Any("error", err))
		}
		return err
	}
	return background.RunPeriodic(ctx, r.config.SpoolCleanupInterval, func(context.Context) error { return cleanup() })
}

func (r *Runtime) AdminNavigation() []adminui.NavigationItem {
	return []adminui.NavigationItem{{Code: "forms", Parent: "tools", Label: "Формы", Icon: "forms", Order: 60, Scope: adminui.NavigationSite, Children: []adminui.NavigationItem{
		{Code: "forms.list", Icon: "forms.list", Label: "Формы", Route: "forms.list", Order: 10, Permission: FormReadPermission, Scope: adminui.NavigationSite},
		{Code: "forms.results", Icon: "forms.results", Label: "Результаты", Route: "forms.results", Order: 20, Permission: ResultReadPermission, Scope: adminui.NavigationSite},
	}}}
}

func normalizeConfig(config Config) (Config, error) {
	if config.ActionMaxAttempts < 1 {
		return Config{}, errors.New("Forms action maximum attempts is invalid")
	}
	if err := config.Public.Validate(); err != nil {
		return Config{}, err
	}
	config.DefaultCaptchaProvider = strings.TrimSpace(config.DefaultCaptchaProvider)
	if config.DefaultCaptchaProvider == "" {
		return Config{}, errors.New("Forms default CAPTCHA provider is empty")
	}
	if config.SpoolEnabled && (config.SpoolTTL < time.Minute || config.SpoolCleanupInterval < time.Second || config.SpoolCleanupBatch < 1) {
		return Config{}, errors.New("Forms spool configuration is invalid")
	}
	return config, nil
}

var _ kernel.Module = Module{}
var _ kernel.DependencyProvider = Module{}
var _ kernel.ModuleDescriptorProvider = Module{}
var _ kernel.RegistryProvider = Module{}
var _ job.NamesProvider = Module{}
var _ kernel.ModuleRuntime = (*Runtime)(nil)
var _ kernel.RuntimeBuildFinalizer = (*Runtime)(nil)
var _ kernel.RuntimeTransitionParticipant = (*Runtime)(nil)
var _ ActionRegistrar = (*Runtime)(nil)
var _ job.Provider = (*Runtime)(nil)
var _ background.Provider = (*Runtime)(nil)
var _ adminui.NavigationProvider = (*Runtime)(nil)
var _ httptransport.Provider = (*Runtime)(nil)
var _ httptransport.SiteManagementProvider = (*Runtime)(nil)
