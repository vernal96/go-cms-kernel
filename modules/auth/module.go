package auth

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/background"
	"github.com/vernal96/go-cms-kernel/modules/core"
	"github.com/vernal96/go-cms-kernel/modules/core/user"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

const ModuleCode kernel.ModuleCode = "auth"

type Config struct {
	Providers         []Provider
	AllowRegistration bool
	// EnabledProvidersSetting optionally selects provider codes from a site's
	// declared string-list setting. Missing or empty values disable all providers.
	EnabledProvidersSetting string
	// Scheme defaults to https. Select http only for a local development site.
	Scheme                 string
	FlowTTL                time.Duration
	ExchangeTimeout        time.Duration
	MaxConcurrentExchanges int
}

type Database interface {
	kernel.ModuleDatabase
	AuthenticationFlows() FlowRepository
}

type module struct{ config Config }

func New(config Config) kernel.Module {
	config.Providers = append([]Provider(nil), config.Providers...)
	return module{config: config}
}

func (module) Code() kernel.ModuleCode           { return ModuleCode }
func (module) Dependencies() []kernel.ModuleCode { return []kernel.ModuleCode{core.ModuleCode} }

func (m module) Validate(ctx context.Context, environment kernel.ModuleValidationContext) error {
	if _, err := normalizeConfig(m.config); err != nil {
		return err
	}
	database, err := kernel.ModuleDatabaseFrom[Database](environment, "", ModuleCode)
	if err != nil {
		return err
	}
	if database.AuthenticationFlows() == nil {
		return errors.New("authentication flow repository is nil")
	}
	return ctx.Err()
}

func (m module) Build(ctx context.Context, environment kernel.ModuleContext) (kernel.ModuleRuntime, error) {
	database, err := kernel.ModuleDatabaseFrom[Database](environment, "", ModuleCode)
	if err != nil {
		return nil, err
	}
	dependency, err := kernel.ModuleDependencyFrom[interface {
		kernel.ModuleRuntime
		Users() user.Service
	}](environment, core.ModuleCode)
	if err != nil {
		return nil, err
	}
	accounts, ok := dependency.Users().(Accounts)
	if !ok {
		return nil, errors.New("core external authentication service is unavailable")
	}
	siteID, err := strconv.ParseInt(environment.Scope().SiteID(), 10, 64)
	if err != nil {
		return nil, err
	}
	config, err := normalizeConfig(m.config)
	if err != nil {
		return nil, err
	}
	service, err := NewService(siteID, config.Scheme+"://"+environment.Scope().Domain(),
		database.AuthenticationFlows(), accounts, config)
	if err != nil {
		return nil, err
	}
	if config.EnabledProvidersSetting != "" {
		if err := service.selectProviders(environment.Scope().Settings()[config.EnabledProvidersSetting]); err != nil {
			return nil, err
		}
	}
	return &Runtime{service: service, logger: environment.Logger()}, ctx.Err()
}

type Runtime struct {
	service *Service
	logger  *slog.Logger
}

func (*Runtime) ModuleCode() kernel.ModuleCode { return ModuleCode }
func (r *Runtime) RegisterProvider(provider Provider) error {
	return r.service.RegisterProvider(provider)
}
func (r *Runtime) FinalizeRuntimeBuild(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.service.Seal()
}
func (r *Runtime) HTTP() httptransport.Builder { return publicHTTP(r.service) }

func (r *Runtime) BackgroundTasks() []background.Task {
	return []background.Task{{Name: "prune_authentication_flows", Run: r.pruneFlows}}
}

func (r *Runtime) pruneFlows(ctx context.Context) error {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			pruneContext, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := r.service.flows.Prune(pruneContext, r.service.siteID, r.service.now())
			cancel()
			if err != nil && ctx.Err() == nil {
				r.logger.ErrorContext(ctx, "prune authentication flows", "error", err)
			}
		}
	}
}

func normalizeConfig(config Config) (Config, error) {
	if config.Scheme == "" {
		config.Scheme = "https"
	}
	if config.FlowTTL == 0 {
		config.FlowTTL = 5 * time.Minute
	}
	if config.ExchangeTimeout == 0 {
		config.ExchangeTimeout = 10 * time.Second
	}
	if config.MaxConcurrentExchanges == 0 {
		config.MaxConcurrentExchanges = 8
	}
	if (config.Scheme != "https" && config.Scheme != "http") ||
		config.FlowTTL < time.Minute || config.FlowTTL > 10*time.Minute ||
		config.ExchangeTimeout <= 0 || config.ExchangeTimeout > 30*time.Second ||
		config.MaxConcurrentExchanges < 1 || config.MaxConcurrentExchanges > 1024 {
		return Config{}, errors.New("authentication configuration is invalid")
	}
	return config, nil
}

var _ ProviderRegistrar = (*Runtime)(nil)
var _ kernel.RuntimeBuildFinalizer = (*Runtime)(nil)
var _ httptransport.Provider = (*Runtime)(nil)

func selectedProviders(value any) ([]string, error) {
	switch value := value.(type) {
	case nil:
		return nil, nil
	case []string:
		return append([]string(nil), value...), nil
	case []any:
		codes := make([]string, 0, len(value))
		for _, item := range value {
			code, ok := item.(string)
			if !ok {
				return nil, errors.New("enabled authentication providers must be a string list")
			}
			codes = append(codes, code)
		}
		return codes, nil
	default:
		return nil, errors.New("enabled authentication providers must be a string list")
	}
}
