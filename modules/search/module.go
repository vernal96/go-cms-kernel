package search

import (
	"context"
	"errors"
	"strconv"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/modules/core"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/security"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

const ModuleCode kernel.ModuleCode = "search"

type Database interface {
	kernel.ModuleDatabase
	Search() Engine
}

type coreDependency interface {
	kernel.ModuleRuntime
	Authorization() security.Authorizer
}

type module struct{}

func (module) Code() kernel.ModuleCode           { return ModuleCode }
func (module) Dependencies() []kernel.ModuleCode { return []kernel.ModuleCode{core.ModuleCode} }
func (module) ModuleDescriptor() kernel.ModuleDescriptor {
	return kernel.ModuleDescriptor{Label: "Поиск", Description: "Поиск по публичным ресурсам сайта"}
}
func (m module) Build(ctx context.Context, moduleContext kernel.ModuleContext) (kernel.ModuleRuntime, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	database, err := kernel.ModuleDatabaseFrom[Database](moduleContext, "", ModuleCode)
	if err != nil {
		return nil, err
	}
	if err := validateModuleDatabase(database); err != nil {
		return nil, err
	}
	dependency, err := kernel.ModuleDependencyFrom[coreDependency](moduleContext, core.ModuleCode)
	if err != nil {
		return nil, err
	}
	id, err := strconv.ParseInt(moduleContext.Scope().SiteID(), 10, 64)
	if err != nil || id <= 0 {
		return nil, errors.New("search runtime site scope is invalid")
	}
	var routeTypes []resourcetype.Code
	for _, code := range moduleContext.Registry().ResourceTypes() {
		definition, exists := moduleContext.Registry().ResourceType(code)
		if exists && definition.PathMode() == resourcetype.PathRoute {
			routeTypes = append(routeTypes, code)
		}
	}
	service, err := NewService(site.ID(id), database.Search(), dependency.Authorization(), routeTypes)
	if err != nil {
		return nil, err
	}
	return &Runtime{service: service}, nil
}

type Runtime struct{ service *Service }

func (*Runtime) ModuleCode() kernel.ModuleCode { return ModuleCode }
func (r *Runtime) Search() *Service            { return r.service }
func (r *Runtime) HTTP() httptransport.Builder { return publicHTTP(r.service) }

var _ kernel.Module = module{}
var _ httptransport.Provider = (*Runtime)(nil)

// New declares the module, which has no configuration parameters.
func New() kernel.Module { return module{} }

func (m module) Validate(ctx context.Context, environment kernel.ModuleValidationContext) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	database, err := kernel.ModuleDatabaseFrom[Database](environment, "", ModuleCode)
	if err != nil {
		return err
	}
	if err := validateModuleDatabase(database); err != nil {
		return err
	}
	return nil
}

func validateModuleDatabase(database Database) error {
	if database.Search() == nil {
		return errors.New("search engine is nil")
	}
	return nil
}
