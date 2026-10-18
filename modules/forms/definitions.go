package forms

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/permission"
)

// Definitions owns Forms' profile-scoped declarative registrations.
type Definitions struct {
	mu          sync.RWMutex
	sealed      bool
	fieldTypes  map[field.TypeCode]field.Type
	permissions map[permission.Code]permission.Definition
}

func newDefinitions() *Definitions {
	return &Definitions{
		fieldTypes:  make(map[field.TypeCode]field.Type),
		permissions: make(map[permission.Code]permission.Definition),
	}
}

func (*Definitions) ModuleCode() kernel.ModuleCode { return ModuleCode }

func (d *Definitions) RegisterFieldType(fieldType field.Type) error {
	if fieldType == nil || isNilDefinitionValue(fieldType) {
		return errors.New("Forms field type is nil")
	}
	code := fieldType.Code()
	if code == "" {
		return errors.New("Forms field type code is empty")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sealed {
		return errors.New("Forms definitions are sealed")
	}
	if _, exists := d.fieldTypes[code]; exists {
		return fmt.Errorf("Forms field type %q is already registered", code)
	}
	d.fieldTypes[code] = field.SnapshotType(fieldType)
	return nil
}

func (d *Definitions) RegisterPermissionEntity(entity permission.Entity) error {
	items, err := permission.Definitions(string(ModuleCode), []permission.Entity{entity})
	if err != nil {
		return fmt.Errorf("Forms permission entity: %w", err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sealed {
		return errors.New("Forms definitions are sealed")
	}
	for _, item := range items {
		if _, exists := d.permissions[item.Code]; exists {
			return fmt.Errorf("Forms permission %q is already registered", item.Code)
		}
	}
	for _, item := range items {
		d.permissions[item.Code] = item
	}
	return nil
}

func (d *Definitions) FinalizeDefinitionBuild(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sealed {
		return errors.New("Forms definitions are already sealed")
	}
	d.sealed = true
	return nil
}

// DefinitionCatalog exposes Forms registrations to the profile compiler after
// the definition build has finalized.
func (d *Definitions) DefinitionCatalog() kernel.DefinitionRegistry { return d }

func (d *Definitions) FieldType(code field.TypeCode) (field.Type, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	item, exists := d.fieldTypes[code]
	return item, exists
}

func (d *Definitions) FieldTypes() []field.TypeCode {
	d.mu.RLock()
	defer d.mu.RUnlock()
	result := make([]field.TypeCode, 0, len(d.fieldTypes))
	for code := range d.fieldTypes {
		result = append(result, code)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func (*Definitions) ValidatorType(field.ValidatorCode) (field.ValidatorType, bool) { return nil, false }
func (*Definitions) ValidatorTypes() []field.ValidatorCode                         { return nil }
func (*Definitions) ResourceType(resourcetype.Code) (resourcetype.Type, bool)      { return nil, false }
func (*Definitions) ResourceTypes() []resourcetype.Code                            { return nil }

func (d *Definitions) Permission(code permission.Code) (permission.Definition, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	item, exists := d.permissions[code]
	return item, exists
}

func (d *Definitions) Permissions() []permission.Code {
	d.mu.RLock()
	defer d.mu.RUnlock()
	result := make([]permission.Code, 0, len(d.permissions))
	for code := range d.permissions {
		result = append(result, code)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func isNilDefinitionValue(value any) bool {
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

var _ kernel.ModuleDefinitions = (*Definitions)(nil)
var _ kernel.DefinitionCatalogProvider = (*Definitions)(nil)
var _ kernel.DefinitionRegistry = (*Definitions)(nil)
