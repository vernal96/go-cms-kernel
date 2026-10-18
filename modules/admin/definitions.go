package admin

import (
	"context"
	"errors"
	"fmt"
	"sort"

	kernel "github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/permission"
)

type Definitions struct {
	permissions map[permission.Code]permission.Definition
	codes       []permission.Code
	entities    map[string]struct{}
	sealed      bool
}

func newDefinitions() *Definitions {
	return &Definitions{permissions: make(map[permission.Code]permission.Definition), entities: make(map[string]struct{})}
}

func (d *Definitions) ModuleCode() kernel.ModuleCode { return ModuleCode }

func (d *Definitions) DefinitionCatalog() kernel.DefinitionRegistry { return d }

func (d *Definitions) RegisterPermissionEntity(entity permission.Entity) error {
	if d == nil {
		return errors.New("admin definitions are unavailable")
	}
	if d.sealed {
		return errors.New("admin definitions are finalized")
	}
	if _, exists := d.entities[entity.Code]; exists {
		return fmt.Errorf("permission entity %q is registered more than once", entity.Code)
	}
	items, err := permission.Definitions(string(ModuleCode), []permission.Entity{entity})
	if err != nil {
		return err
	}
	for _, item := range items {
		if _, exists := d.permissions[item.Code]; exists {
			return fmt.Errorf("permission %q already exists", item.Code)
		}
		d.permissions[item.Code] = item
		d.codes = append(d.codes, item.Code)
	}
	d.entities[entity.Code] = struct{}{}
	sort.Slice(d.codes, func(i, j int) bool { return d.codes[i] < d.codes[j] })
	return nil
}

func (d *Definitions) FinalizeDefinitionBuild(ctx context.Context) error {
	if d == nil {
		return errors.New("admin definitions are unavailable")
	}
	if d.sealed {
		return errors.New("admin definitions are already finalized")
	}
	if ctx == nil {
		return errors.New("admin definition context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	d.sealed = true
	return nil
}

func (d *Definitions) FieldType(field.TypeCode) (field.Type, bool) { return nil, false }
func (d *Definitions) FieldTypes() []field.TypeCode                { return nil }
func (d *Definitions) ValidatorType(field.ValidatorCode) (field.ValidatorType, bool) {
	return nil, false
}
func (d *Definitions) ValidatorTypes() []field.ValidatorCode                    { return nil }
func (d *Definitions) ResourceType(resourcetype.Code) (resourcetype.Type, bool) { return nil, false }
func (d *Definitions) ResourceTypes() []resourcetype.Code                       { return nil }
func (d *Definitions) Permission(code permission.Code) (permission.Definition, bool) {
	if d == nil {
		return permission.Definition{}, false
	}
	item, ok := d.permissions[code]
	return item, ok
}
func (d *Definitions) Permissions() []permission.Code {
	if d == nil {
		return nil
	}
	return append([]permission.Code(nil), d.codes...)
}

var _ kernel.ModuleDefinitions = (*Definitions)(nil)
var _ kernel.DefinitionCatalogProvider = (*Definitions)(nil)
var _ kernel.DefinitionRegistry = (*Definitions)(nil)
