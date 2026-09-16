package admin

import (
	"context"
	"errors"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/modules/core"
	"github.com/vernal96/go-cms-kernel/modules/core/user"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

const ModuleCode kernel.ModuleCode = "admin"

const AccessPermission permission.Code = "admin.panel.read"

type Module struct{}

type coreDependency interface {
	kernel.ModuleRuntime
	Users() user.Service
	Authorization() security.Authorizer
}

func (Module) Code() kernel.ModuleCode {
	return ModuleCode
}

func (Module) Dependencies() []kernel.ModuleCode {
	return []kernel.ModuleCode{core.ModuleCode}
}

func (Module) Registry() kernel.ModuleRegistry {
	return kernel.ModuleRegistry{
		PermissionEntities: []permission.Entity{{Code: "panel"}},
	}
}

func (Module) Build(
	_ context.Context,
	ctx kernel.ModuleContext,
) (kernel.ModuleRuntime, error) {
	coreRuntime, err := kernel.ModuleDependencyFrom[coreDependency](
		ctx,
		core.ModuleCode,
	)
	if err != nil {
		return nil, err
	}
	return NewRuntime(coreRuntime.Users(), coreRuntime.Authorization())
}

type Runtime struct {
	users         user.Service
	authorization security.Authorizer
}

func NewRuntime(
	users user.Service,
	authorization security.Authorizer,
) (*Runtime, error) {
	if users == nil {
		return nil, errors.New("admin user service is nil")
	}
	if authorization == nil {
		return nil, errors.New("admin authorizer is nil")
	}
	return &Runtime{users: users, authorization: authorization}, nil
}

func (*Runtime) ModuleCode() kernel.ModuleCode {
	return ModuleCode
}

var _ kernel.Module = Module{}
var _ kernel.RegistryProvider = Module{}
var _ kernel.DependencyProvider = Module{}
var _ kernel.ModuleRuntime = (*Runtime)(nil)
