package app

import (
	"fmt"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/modules/core"
	coreaccess "github.com/vernal96/go-cms-kernel/modules/core/access"
	corefile "github.com/vernal96/go-cms-kernel/modules/core/file"
	coregroup "github.com/vernal96/go-cms-kernel/modules/core/group"
	coremedia "github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	coreuser "github.com/vernal96/go-cms-kernel/modules/core/user"
	"github.com/vernal96/go-cms-kernel/security"
)

// Services groups the assembled domain services without making App a facade
// for every domain operation.
type Services struct {
	Sessions      security.SessionStore
	Sites         *site.Catalog
	Resources     *resource.Service
	Files         corefile.ManagementService
	Media         coremedia.Service
	Users         coreuser.Service
	Groups        coregroup.Service
	Authorization coreaccess.Service
}

func servicesFromCore(services *core.Services) Services {
	if services == nil {
		return Services{}
	}
	return Services{
		Sessions:      services.Sessions,
		Sites:         services.Sites,
		Resources:     services.Resources,
		Files:         services.Files,
		Media:         services.Media,
		Users:         services.Users,
		Groups:        services.Groups,
		Authorization: services.Authorization,
	}
}

func (a *App) Services() Services {
	if a == nil || a.closed.Load() || !a.booted.Load() {
		return Services{}
	}
	return a.services
}

func (a *App) Sites() *site.Catalog              { return a.Services().Sites }
func (a *App) Resources() *resource.Service      { return a.Services().Resources }
func (a *App) Files() corefile.ManagementService { return a.Services().Files }
func (a *App) Media() coremedia.Service          { return a.Services().Media }
func (a *App) Users() coreuser.Service           { return a.Services().Users }
func (a *App) Groups() coregroup.Service         { return a.Services().Groups }
func (a *App) Authorization() coreaccess.Service {
	return a.Services().Authorization
}

func bindCoreServices(
	profiles []kernel.Profile,
	services *core.Services,
) ([]kernel.Profile, error) {
	result := make([]kernel.Profile, len(profiles))
	for index, profile := range profiles {
		result[index] = profile
		result[index].Modules = append(
			[]kernel.ProfileModule(nil),
			profile.Modules...,
		)
		for moduleIndex := range result[index].Modules {
			profileModule := &result[index].Modules[moduleIndex]
			if profileModule.Module.Code() != core.ModuleCode {
				continue
			}
			module, err := core.BindServices(profileModule.Module, services)
			if err != nil {
				return nil, fmt.Errorf(
					"bind core services for profile %q: %w",
					profile.Code,
					err,
				)
			}
			profileModule.Module = module
		}
	}
	return result, nil
}
