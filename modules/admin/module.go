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

type module struct{}

type coreDependency interface {
	kernel.ModuleRuntime
	Users() user.Service
	Authorization() security.Authorizer
}

func (module) Code() kernel.ModuleCode {
	return ModuleCode
}

func (module) Dependencies() []kernel.ModuleCode {
	return []kernel.ModuleCode{core.ModuleCode}
}

func (module) NewDefinitions(
	ctx context.Context,
	_ kernel.DefinitionContext,
) (kernel.ModuleDefinitions, error) {
	if ctx == nil {
		return nil, errors.New("admin definition context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	definitions := newDefinitions()
	if err := definitions.RegisterPermissionEntity(permission.Entity{Code: "panel"}); err != nil {
		return nil, err
	}
	return definitions, nil
}

func (m module) Build(
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

var _ kernel.Module = module{}
var _ kernel.DefinitionProvider = module{}
var _ kernel.DependencyProvider = module{}
var _ kernel.ModuleRuntime = (*Runtime)(nil)

// New declares the module, which has no configuration parameters.
func New() kernel.Module { return module{} }

func (m module) Validate(ctx context.Context, environment kernel.ModuleValidationContext) error {
	return ctx.Err()
}
