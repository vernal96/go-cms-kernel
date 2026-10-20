package core

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	kernel "github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/permission"
)

// Definitions owns Core's field, validator, resource type and permission
// definitions for one profile build.
type Definitions struct {
	moduleCode         kernel.ModuleCode
	registry           kernel.DefinitionRegistry
	fieldTypes         map[field.TypeCode]field.Type
	validators         map[field.ValidatorCode]field.ValidatorType
	resources          map[resourcetype.Code]resourcetype.Type
	pending            []resourcetype.Type
	permissions        map[permission.Code]permission.Definition
	permissionCodes    []permission.Code
	permissionEntities map[string]struct{}
	sealed             bool
}

func newDefinitions(moduleCode kernel.ModuleCode, registry kernel.DefinitionRegistry) *Definitions {
	return &Definitions{
		moduleCode:         moduleCode,
		registry:           registry,
		fieldTypes:         make(map[field.TypeCode]field.Type),
		validators:         make(map[field.ValidatorCode]field.ValidatorType),
		resources:          make(map[resourcetype.Code]resourcetype.Type),
		permissions:        make(map[permission.Code]permission.Definition),
		permissionEntities: make(map[string]struct{}),
	}
}

func (d *Definitions) ModuleCode() kernel.ModuleCode {
	if d == nil {
		return ""
	}
	return d.moduleCode
}

// DefinitionCatalog exposes Core's read-only definition contract to the
// profile compiler. Registration closes when FinalizeDefinitionBuild returns.
func (d *Definitions) DefinitionCatalog() kernel.DefinitionRegistry { return d }

func (d *Definitions) FieldType(code field.TypeCode) (field.Type, bool) {
	if d == nil {
		return nil, false
	}
	value, ok := d.fieldTypes[code]
	return value, ok
}

func (d *Definitions) FieldTypes() []field.TypeCode {
	if d == nil {
		return nil
	}
	result := make([]field.TypeCode, 0, len(d.fieldTypes))
	for code := range d.fieldTypes {
		result = append(result, code)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func (d *Definitions) ValidatorType(code field.ValidatorCode) (field.ValidatorType, bool) {
	if d == nil {
		return nil, false
	}
	value, ok := d.validators[code]
	return value, ok
}

func (d *Definitions) ValidatorTypes() []field.ValidatorCode {
	if d == nil {
		return nil
	}
	result := make([]field.ValidatorCode, 0, len(d.validators))
	for code := range d.validators {
		result = append(result, code)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func (d *Definitions) ResourceType(code resourcetype.Code) (resourcetype.Type, bool) {
	if d == nil {
		return nil, false
	}
	value, ok := d.resources[code]
	return value, ok
}

func (d *Definitions) ResourceTypes() []resourcetype.Code {
	if d == nil {
		return nil
	}
	result := make([]resourcetype.Code, 0, len(d.resources))
	for code := range d.resources {
		result = append(result, code)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func (d *Definitions) Permission(code permission.Code) (permission.Definition, bool) {
	if d == nil {
		return permission.Definition{}, false
	}
	value, ok := d.permissions[code]
	return value, ok
}

func (d *Definitions) Permissions() []permission.Code {
	if d == nil {
		return nil
	}
	return append([]permission.Code(nil), d.permissionCodes...)
}

func (d *Definitions) RegisterFieldType(value field.Type) error {
	if err := d.ensureOpen(); err != nil {
		return err
	}
	if nilValue(value) {
		return errors.New("field type is nil")
	}
	code := value.Code()
	if code == "" {
		return errors.New("field type code is empty")
	}
	if _, exists := d.fieldTypes[code]; exists {
		return fmt.Errorf("field type %q already exists", code)
	}
	d.fieldTypes[code] = field.SnapshotType(value)
	return nil
}

func (d *Definitions) RegisterValidatorType(value field.ValidatorType) error {
	if err := d.ensureOpen(); err != nil {
		return err
	}
	if nilValue(value) {
		return errors.New("validator type is nil")
	}
	code := value.Code()
	if err := field.ValidateValidatorCode(code); err != nil {
		return err
	}
	if _, exists := d.validators[code]; exists {
		return fmt.Errorf("validator type %q already exists", code)
	}
	d.validators[code] = field.SnapshotValidatorType(value)
	return nil
}

func (d *Definitions) RegisterResourceType(value resourcetype.Type) error {
	if err := d.ensureOpen(); err != nil {
		return err
	}
	if nilValue(value) {
		return errors.New("resource type is nil")
	}
	if value.Code() == "" {
		return errors.New("resource type code is empty")
	}
	if _, exists := d.resources[value.Code()]; exists {
		return fmt.Errorf("resource type %q already exists", value.Code())
	}
	for _, current := range d.pending {
		if current.Code() == value.Code() {
			return fmt.Errorf("resource type %q already exists", value.Code())
		}
	}
	d.pending = append(d.pending, value)
	return nil
}

func (d *Definitions) RegisterPermissionEntity(entity permission.Entity) error {
	if err := d.ensureOpen(); err != nil {
		return err
	}
	if _, exists := d.permissionEntities[entity.Code]; exists {
		return fmt.Errorf("permission entity %q is registered more than once", entity.Code)
	}
	definitions, err := permission.Definitions(string(d.moduleCode), []permission.Entity{entity})
	if err != nil {
		return err
	}
	for _, definition := range definitions {
		if _, exists := d.permissions[definition.Code]; exists {
			return fmt.Errorf("permission %q already exists", definition.Code)
		}
		d.permissions[definition.Code] = definition
		d.permissionCodes = append(d.permissionCodes, definition.Code)
	}
	d.permissionEntities[entity.Code] = struct{}{}
	sort.Slice(d.permissionCodes, func(i, j int) bool { return d.permissionCodes[i] < d.permissionCodes[j] })
	return nil
}

func (d *Definitions) FinalizeDefinitionBuild(ctx context.Context) error {
	if err := d.ensureOpen(); err != nil {
		return err
	}
	if ctx == nil {
		return errors.New("core definition context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if d.registry == nil {
		return errors.New("core definition registry is nil")
	}
	for _, value := range d.pending {
		if err := ctx.Err(); err != nil {
			return err
		}
		compiled, err := compileResourceType(value, d.registry)
		if err != nil {
			return fmt.Errorf("resource type %q: %w", value.Code(), err)
		}
		d.resources[value.Code()] = compiled
	}
	d.pending = nil
	d.sealed = true
	return nil
}

func (d *Definitions) ensureOpen() error {
	if d == nil {
		return errors.New("core definitions are unavailable")
	}
	if d.sealed {
		return errors.New("core definitions are finalized")
	}
	return nil
}

func compileResourceType(value resourcetype.Type, resolver field.TypeResolver) (resourcetype.Type, error) {
	switch value.PathMode() {
	case resourcetype.PathRoute, resourcetype.PathNone:
	default:
		return nil, fmt.Errorf("has invalid path mode %q", value.PathMode())
	}
	compiled, err := resourcetype.Compile(value, resolver)
	if err != nil {
		return nil, err
	}
	metadata := compiled.Metadata()
	if metadata.Label == "" || strings.TrimSpace(metadata.Label) != metadata.Label {
		return nil, fmt.Errorf("has invalid label %q", metadata.Label)
	}
	seen := make(map[string]struct{}, len(metadata.ContentTypes))
	for index, option := range metadata.ContentTypes {
		if option.Code == "" || strings.TrimSpace(option.Code) != option.Code ||
			option.Label == "" || strings.TrimSpace(option.Label) != option.Label ||
			option.Editor == "" || strings.TrimSpace(string(option.Editor)) != string(option.Editor) {
			return nil, fmt.Errorf("content type at index %d is invalid", index)
		}
		if option.Editor != resourcetype.ContentEditorHTML && option.Editor != resourcetype.ContentEditorTextarea {
			return nil, fmt.Errorf("content type %q has unsupported editor %q", option.Code, option.Editor)
		}
		if _, exists := seen[option.Code]; exists {
			return nil, fmt.Errorf("content type %q is duplicated", option.Code)
		}
		seen[option.Code] = struct{}{}
	}
	if metadata.Capabilities.SupportsContent != (len(metadata.ContentTypes) > 0) {
		return nil, errors.New("content capability and metadata disagree")
	}
	return compiled, nil
}

func nilValue(value any) bool {
	if value == nil {
		return true
	}
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
