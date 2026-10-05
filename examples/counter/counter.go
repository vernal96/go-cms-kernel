// Package counter demonstrates a module with one typed parameter and no storage bindings.
package counter

import (
	"context"
	"errors"

	"github.com/vernal96/go-cms-kernel"
)

type module struct{ limit int }

// New declares a counter limit. Each site receives its own runtime.
func New(limit int) kernel.Module      { return module{limit: limit} }
func (module) Code() kernel.ModuleCode { return "example.counter" }
func (m module) Validate(ctx context.Context, _ kernel.ModuleValidationContext) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return m.validate()
}
func (m module) validate() error {
	if m.limit <= 0 {
		return errors.New("counter limit must be positive")
	}
	return nil
}
func (m module) Build(ctx context.Context, _ kernel.ModuleContext) (kernel.ModuleRuntime, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := m.validate(); err != nil {
		return nil, err
	}
	return &Runtime{limit: m.limit}, nil
}

// Runtime is the counter configuration scoped to one site.
type Runtime struct{ limit int }

func (*Runtime) ModuleCode() kernel.ModuleCode { return "example.counter" }
func (r *Runtime) Limit() int                  { return r.limit }
