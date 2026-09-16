package mail

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/adminui"
	"github.com/vernal96/go-cms-kernel/background"
	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/job"
	"github.com/vernal96/go-cms-kernel/modules/core"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/user"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

const ModuleCode kernel.ModuleCode = "mail"

type Config struct {
	Renderer             RendererConfig
	MessageIDDomain      string
	HistoryRetention     time.Duration
	CleanupInterval      time.Duration
	CleanupBatchSize     int
	SendMaxAttempts      int
	MaxRecipients        int
	MaxMessageSize       int64
	MaxAttachmentSize    int64
	UploadStorage        filesystem.Code
	UploadPath           string
	SpoolEnabled         bool
	SpoolTTL             time.Duration
	SpoolCleanupInterval time.Duration
	SpoolCleanupBatch    int
}

type Database interface {
	kernel.ModuleDatabase
	Mail() Repository
}

type coreDependency interface {
	kernel.ModuleRuntime
	Files() file.ManagementService
	Authorization() security.Authorizer
	Users() user.Service
}

type Module struct{}

func (Module) Code() kernel.ModuleCode { return ModuleCode }

func (Module) Dependencies() []kernel.ModuleCode { return []kernel.ModuleCode{core.ModuleCode} }

func (Module) ModuleDescriptor() kernel.ModuleDescriptor {
	return kernel.ModuleDescriptor{Label: "Mail", Description: "Шаблоны и асинхронная отправка почты"}
}

func (Module) Registry() kernel.ModuleRegistry {
	return kernel.ModuleRegistry{PermissionEntities: []permission.Entity{
		{Code: "template"},
		{Code: "message", Actions: []permission.Action{permission.Read, permission.Create, permission.Delete}},
	}}
}

func (Module) JobNames() []string { return []string{SendJobName} }

func (Module) Build(buildCtx context.Context, ctx kernel.ModuleContext) (kernel.ModuleRuntime, error) {
	database, err := kernel.ModuleDatabaseFrom[Database](ctx, "", ModuleCode)
	if err != nil {
		return nil, err
	}
	if database.Mail() == nil {
		return nil, errors.New("mail repository is nil")
	}
	coreRuntime, err := kernel.ModuleDependencyFrom[coreDependency](ctx, core.ModuleCode)
	if err != nil {
		return nil, err
	}
	config, err := kernel.ModuleConfigFrom[Config](ctx)
	if err != nil {
		return nil, err
	}
	config, err = normalizeConfig(config)
	if err != nil {
		return nil, err
	}
	if err := validateUploadStorage(buildCtx, coreRuntime.Files(), config.UploadStorage); err != nil {
		return nil, err
	}
	application, err := kernel.ModuleApplicationFrom[Application](ctx)
	if err != nil {
		return nil, err
	}
	if err := validateTransport(application.Transport); err != nil {
		return nil, err
	}
	siteIDValue, err := strconv.ParseInt(ctx.Scope().SiteID(), 10, 64)
	if err != nil || siteIDValue <= 0 {
		return nil, errors.New("mail runtime site scope is invalid")
	}
	var spool *AttachmentSpool
	if config.SpoolEnabled {
		disk, exists := ctx.Filesystems().Disk(SpoolFilesystemAlias)
		if !exists {
			return nil, errors.New("mail spool filesystem binding is unavailable")
		}
		spool, err = NewAttachmentSpool(site.ID(siteIDValue), disk)
		if err != nil {
			return nil, err
		}
	}
	scope := ctx.Scope()
	renderer, err := NewRenderer(ctx.Registry(), coreRuntime.Files(), site.Site{
		ID: site.ID(siteIDValue), ProfileCode: ctx.Profile().Code, Domain: scope.Domain(),
		Locale: scope.Locale(), IsPublic: scope.IsPublic(), Settings: scope.Settings(),
	}, ctx.Profile().Params, config.Renderer)
	if err != nil {
		return nil, err
	}
	service, err := NewService(
		site.ID(siteIDValue), database.Mail(), renderer, coreRuntime.Authorization(),
		coreRuntime.Users(), spool, Limits{MaxRecipients: config.MaxRecipients, MaxMessageSize: config.MaxMessageSize, MaxAttachmentSize: config.MaxAttachmentSize}, config.MessageIDDomain,
	)
	if err != nil {
		return nil, err
	}
	service.logger = ctx.Logger()
	service.uploadStorage = config.UploadStorage
	service.uploadPath = config.UploadPath
	worker, err := newWorker(site.ID(siteIDValue), database.Mail(), coreRuntime.Files(), spool, application.Transport, service.lifecycle, config.SendMaxAttempts, ctx.Logger())
	if err != nil {
		return nil, err
	}
	handler, err := NewHTTPHandler(service)
	if err != nil {
		return nil, err
	}
	return &Runtime{service: service, worker: worker, managementHTTP: handler, spool: spool, logger: ctx.Logger(), retention: config.HistoryRetention, cleanupInterval: config.CleanupInterval, cleanupBatchSize: config.CleanupBatchSize, spoolTTL: config.SpoolTTL, spoolCleanupInterval: config.SpoolCleanupInterval, spoolCleanupBatch: config.SpoolCleanupBatch}, nil
}

type Runtime struct {
	service              *Service
	worker               *Worker
	managementHTTP       http.Handler
	spool                *AttachmentSpool
	logger               *slog.Logger
	retention            time.Duration
	cleanupInterval      time.Duration
	cleanupBatchSize     int
	spoolTTL             time.Duration
	spoolCleanupInterval time.Duration
	spoolCleanupBatch    int
}

func (*Runtime) ModuleCode() kernel.ModuleCode { return ModuleCode }
func (r *Runtime) Mail() *Service              { return r.service }

func (r *Runtime) SiteManagementHTTP() httptransport.SiteManagementContribution {
	return httptransport.SiteManagementContribution{Path: "mail", Handler: r.managementHTTP}
}

func (r *Runtime) Jobs() []job.Definition {
	return []job.Definition{{Name: SendJobName, ScopeID: fmt.Sprint(r.service.siteID), Handler: r.worker.Handle}}
}

func (r *Runtime) BackgroundTasks() []background.Task {
	result := make([]background.Task, 0, 2)
	if r.retention > 0 {
		result = append(result, background.Task{Name: "mail.history_retention", Run: r.runRetention})
	}
	if r.spool != nil {
		result = append(result, background.Task{Name: "mail.spool_cleanup", Run: r.runSpoolCleanup})
	}
	return result
}

func (r *Runtime) PrepareRuntimeTransition(ctx context.Context, transition kernel.RuntimeTransition) (kernel.PreparedRuntimeTransition, error) {
	if transition.Reason != kernel.RuntimeTransitionProfileChange && transition.Reason != kernel.RuntimeTransitionSiteDelete {
		return nil, fmt.Errorf("unsupported Mail runtime transition reason %q", transition.Reason)
	}
	if transition.ScopeID != fmt.Sprint(r.service.siteID) {
		return nil, errors.New("mail runtime transition scope is invalid")
	}
	prepared := &preparedRuntimeTransition{lifecycle: r.service.lifecycle}
	if err := r.service.lifecycle.beginDrain(); err != nil {
		return nil, err
	}
	active, err := r.service.repository.HasActiveMessages(ctx, r.service.siteID)
	if err != nil {
		prepared.Abort()
		return nil, err
	}
	if active {
		prepared.Abort()
		return nil, fmt.Errorf("%w: %w for site %d", kernel.ErrRuntimeTransitionBlocked, ErrActiveMessages, r.service.siteID)
	}
	if r.spool != nil {
		if _, err := r.spool.Purge(ctx, r.spoolCleanupBatch); err != nil {
			prepared.Abort()
			return nil, fmt.Errorf("purge mail spool before runtime transition: %w", err)
		}
	}
	return prepared, nil
}

func (r *Runtime) runSpoolCleanup(ctx context.Context) (resultErr error) {
	defer func() { resultErr = errors.Join(resultErr, r.spool.CloseCleanupScan()) }()
	cleanup := func() error {
		_, err := r.spool.Cleanup(ctx, time.Now().UTC().Add(-r.spoolTTL), r.spoolCleanupBatch, func(ctx context.Context, keys []string) (map[string]struct{}, error) {
			return r.service.repository.ActiveSpoolKeys(ctx, r.service.siteID, keys)
		})
		if err != nil && r.logger != nil {
			r.logger.ErrorContext(ctx, "mail spool cleanup failed", slog.String("event", "mail.spool.cleanup.failed"), slog.Any("error", err))
		}
		return err
	}
	return background.RunPeriodic(ctx, r.spoolCleanupInterval, func(context.Context) error { return cleanup() })
}

type uploadDiskCatalog interface {
	Disks(context.Context, security.Actor) ([]filesystem.DiskInfo, error)
}

func validateUploadStorage(ctx context.Context, catalog uploadDiskCatalog, code filesystem.Code) error {
	if catalog == nil {
		return errors.New("mail upload filesystem catalog is unavailable")
	}
	disks, err := catalog.Disks(ctx, security.System())
	if err != nil {
		return fmt.Errorf("list filesystems for Mail uploads: %w", err)
	}
	for _, disk := range disks {
		if disk.Code == code {
			return nil
		}
	}
	return fmt.Errorf("mail upload storage %q is unavailable", code)
}

func (r *Runtime) runRetention(ctx context.Context) error {
	cleanup := func() error {
		_, err := r.service.repository.Cleanup(ctx, r.service.siteID, r.retention, r.cleanupBatchSize)
		return err
	}
	return background.RunPeriodic(ctx, r.cleanupInterval, func(context.Context) error { return cleanup() })
}

func (r *Runtime) AdminNavigation() []adminui.NavigationItem {
	return []adminui.NavigationItem{{
		Code: "mail", Parent: "tools", Label: "Почта", Icon: "mail", Order: 50, Scope: adminui.NavigationSite,
		Children: []adminui.NavigationItem{
			{Code: "mail.templates", Icon: "mail.templates", Label: "Шаблоны", Route: "mail.templates", Order: 10, Permission: TemplateReadPermission, Scope: adminui.NavigationSite},
			{Code: "mail.send", Icon: "mail.send", Label: "Отправить", Route: "mail.send", Order: 20, Permission: MessageCreatePermission, Scope: adminui.NavigationSite},
			{Code: "mail.history", Icon: "mail.history", Label: "История", Route: "mail.history", Order: 30, Permission: MessageReadPermission, Scope: adminui.NavigationSite},
		},
	}}
}

func normalizeConfig(config Config) (Config, error) {
	if config.HistoryRetention < 0 || config.CleanupInterval < 0 || config.CleanupBatchSize < 0 {
		return Config{}, errors.New("mail history configuration is invalid")
	}
	if config.CleanupInterval == 0 {
		config.CleanupInterval = time.Hour
	}
	if config.CleanupBatchSize == 0 {
		config.CleanupBatchSize = 100
	}
	if config.SendMaxAttempts < 1 {
		return Config{}, errors.New("mail send maximum attempts is invalid")
	}
	if config.MaxRecipients < 1 || config.MaxMessageSize < 1 || config.MaxAttachmentSize < 1 {
		return Config{}, errors.New("mail delivery limits are invalid")
	}
	if strings.TrimSpace(string(config.UploadStorage)) == "" {
		config.UploadStorage = "private"
	}
	rawUploadPath := strings.TrimSpace(config.UploadPath)
	if path.IsAbs(rawUploadPath) || strings.Contains(rawUploadPath, "\\") {
		return Config{}, errors.New("mail upload path is invalid")
	}
	config.UploadPath = strings.Trim(rawUploadPath, "/")
	if config.UploadPath == "" {
		config.UploadPath = "mail"
	}
	if path.Clean(config.UploadPath) != config.UploadPath || config.UploadPath == ".." || strings.HasPrefix(config.UploadPath, "../") {
		return Config{}, errors.New("mail upload path is invalid")
	}
	if config.SpoolEnabled {
		if config.SpoolTTL < time.Minute || config.SpoolCleanupInterval < time.Second || config.SpoolCleanupBatch < 1 {
			return Config{}, errors.New("mail spool configuration is invalid")
		}
	}
	return config, nil
}

var _ kernel.Module = Module{}
var _ kernel.DependencyProvider = Module{}
var _ kernel.ModuleDescriptorProvider = Module{}
var _ kernel.RegistryProvider = Module{}
var _ job.NamesProvider = Module{}
var _ kernel.ModuleRuntime = (*Runtime)(nil)
var _ kernel.RuntimeTransitionParticipant = (*Runtime)(nil)
var _ job.Provider = (*Runtime)(nil)
var _ background.Provider = (*Runtime)(nil)
var _ adminui.NavigationProvider = (*Runtime)(nil)
var _ httptransport.SiteManagementProvider = (*Runtime)(nil)
