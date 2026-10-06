package kernel

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/vernal96/go-cms-kernel/cache"
	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/permission"
)

type DefinitionRegistry interface {
	FieldType(field.TypeCode) (field.Type, bool)
	FieldTypes() []field.TypeCode
	ValidatorType(field.ValidatorCode) (field.ValidatorType, bool)
	ValidatorTypes() []field.ValidatorCode
	ResourceType(resourcetype.Code) (resourcetype.Type, bool)
	ResourceTypes() []resourcetype.Code
	Permission(permission.Code) (permission.Definition, bool)
	Permissions() []permission.Code
}

type Registry interface {
	DefinitionRegistry
	Module(ModuleCode) (ModuleRuntime, bool)
	Modules() []ModuleRuntime
}

type ModuleRegistry struct {
	FieldTypes         []field.Type
	ValidatorTypes     []field.ValidatorType
	ResourceTypes      []resourcetype.Type
	PermissionEntities []permission.Entity
}

func RegistryForModule(module Module) (ModuleRegistry, error) {
	if module == nil || isNilValue(module) {
		return ModuleRegistry{}, errors.New("module is nil")
	}
	if provider, ok := module.(RegistryProvider); ok {
		registry, err := provider.Registry()
		if err != nil {
			return ModuleRegistry{}, fmt.Errorf("module %q registry: %w", module.Code(), err)
		}
		return registry, nil
	}
	return ModuleRegistry{}, nil
}

type RegistryProvider interface {
	Registry() (ModuleRegistry, error)
}

type CacheBindingsProvider interface{ CacheBindings() []cache.Binding }

type FilesystemBindingsProvider interface{ FilesystemBindings() []filesystem.Binding }

func moduleCacheBindings(module Module) []cache.Binding {
	if provider, ok := module.(CacheBindingsProvider); ok {
		return provider.CacheBindings()
	}
	return nil
}

func moduleFilesystemBindings(module Module) []filesystem.Binding {
	if provider, ok := module.(FilesystemBindingsProvider); ok {
		return provider.FilesystemBindings()
	}
	return nil
}

type RuntimeRegistry struct {
	modules         map[ModuleCode]ModuleRuntime
	moduleRuntimes  []ModuleRuntime
	fieldTypes      map[field.TypeCode]field.Type
	validatorTypes  map[field.ValidatorCode]field.ValidatorType
	resourceTypes   map[resourcetype.Code]resourcetype.Type
	permissions     map[permission.Code]permission.Definition
	permissionCodes []permission.Code
}

func newRuntimeRegistry() *RuntimeRegistry {
	return &RuntimeRegistry{
		modules:        make(map[ModuleCode]ModuleRuntime),
		fieldTypes:     make(map[field.TypeCode]field.Type),
		validatorTypes: make(map[field.ValidatorCode]field.ValidatorType),
		resourceTypes: make(
			map[resourcetype.Code]resourcetype.Type,
		),
		permissions: make(
			map[permission.Code]permission.Definition,
		),
	}
}

func (r *RuntimeRegistry) Module(
	code ModuleCode,
) (ModuleRuntime, bool) {
	runtime, exists := r.modules[code]
	return runtime, exists
}

func (r *RuntimeRegistry) Modules() []ModuleRuntime {
	return append([]ModuleRuntime(nil), r.moduleRuntimes...)
}

func (r *RuntimeRegistry) FieldType(
	code field.TypeCode,
) (field.Type, bool) {
	fieldType, exists := r.fieldTypes[code]
	return fieldType, exists
}

func (r *RuntimeRegistry) FieldTypes() []field.TypeCode {
	result := make([]field.TypeCode, 0, len(r.fieldTypes))
	for code := range r.fieldTypes {
		result = append(result, code)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func (r *RuntimeRegistry) ValidatorType(code field.ValidatorCode) (field.ValidatorType, bool) {
	item, exists := r.validatorTypes[code]
	return item, exists
}

func (r *RuntimeRegistry) ValidatorTypes() []field.ValidatorCode {
	result := make([]field.ValidatorCode, 0, len(r.validatorTypes))
	for code := range r.validatorTypes {
		result = append(result, code)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func (r *RuntimeRegistry) ResourceType(
	code resourcetype.Code,
) (resourcetype.Type, bool) {
	resourceType, exists := r.resourceTypes[code]
	return resourceType, exists
}

func (r *RuntimeRegistry) ResourceTypes() []resourcetype.Code {
	result := make([]resourcetype.Code, 0, len(r.resourceTypes))
	for code := range r.resourceTypes {
		result = append(result, code)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func (r *RuntimeRegistry) Permission(
	code permission.Code,
) (permission.Definition, bool) {
	definition, exists := r.permissions[code]
	return definition, exists
}

func (r *RuntimeRegistry) Permissions() []permission.Code {
	return append([]permission.Code(nil), r.permissionCodes...)
}

func (r *RuntimeRegistry) cloneDefinitions() *RuntimeRegistry {
	result := newRuntimeRegistry()
	for code, fieldType := range r.fieldTypes {
		result.fieldTypes[code] = fieldType
	}
	for code, validatorType := range r.validatorTypes {
		result.validatorTypes[code] = validatorType
	}
	for code, resourceType := range r.resourceTypes {
		result.resourceTypes[code] = resourceType
	}
	for code, definition := range r.permissions {
		result.permissions[code] = definition
	}
	result.permissionCodes = append(
		[]permission.Code(nil),
		r.permissionCodes...,
	)
	return result
}

func (r *RuntimeRegistry) add(runtime ModuleRuntime) error {
	if runtime == nil {
		return errors.New("module runtime is nil")
	}

	code := runtime.ModuleCode()
	if code == "" {
		return errors.New("module runtime code is empty")
	}

	if _, exists := r.modules[code]; exists {
		return fmt.Errorf(
			"module runtime %q already exists",
			code,
		)
	}

	r.modules[code] = runtime
	r.moduleRuntimes = append(r.moduleRuntimes, runtime)
	return nil
}

func (r *RuntimeRegistry) addFieldType(
	fieldType field.Type,
) error {
	if fieldType == nil || isNilValue(fieldType) {
		return errors.New("field type is nil")
	}

	code := fieldType.Code()
	if code == "" {
		return errors.New("field type code is empty")
	}
	if _, exists := r.fieldTypes[code]; exists {
		return fmt.Errorf("field type %q already exists", code)
	}

	r.fieldTypes[code] = field.SnapshotType(fieldType)
	return nil
}

func (r *RuntimeRegistry) addValidatorType(item field.ValidatorType) error {
	if item == nil || isNilValue(item) {
		return errors.New("validator type is nil")
	}
	code := item.Code()
	if err := field.ValidateValidatorCode(code); err != nil {
		return err
	}
	if _, exists := r.validatorTypes[code]; exists {
		return fmt.Errorf("validator type %q already exists", code)
	}
	r.validatorTypes[code] = field.SnapshotValidatorType(item)
	return nil
}

func (r *RuntimeRegistry) addResourceType(
	resourceType resourcetype.Type,
) error {
	if resourceType == nil || isNilValue(resourceType) {
		return errors.New("resource type is nil")
	}

	code := resourceType.Code()
	if code == "" {
		return errors.New("resource type code is empty")
	}
	if _, exists := r.resourceTypes[code]; exists {
		return fmt.Errorf("resource type %q already exists", code)
	}

	switch resourceType.PathMode() {
	case resourcetype.PathRoute, resourcetype.PathNone:
	default:
		return fmt.Errorf(
			"resource type %q has invalid path mode %q",
			code,
			resourceType.PathMode(),
		)
	}

	compiledResourceType, err := resourcetype.Compile(resourceType, r)
	if err != nil {
		return fmt.Errorf("resource type %q %w", code, err)
	}
	metadata := compiledResourceType.Metadata()
	if metadata.Label == "" || strings.TrimSpace(metadata.Label) != metadata.Label {
		return fmt.Errorf("resource type %q has invalid label %q", code, metadata.Label)
	}
	seenContentTypes := make(map[string]struct{}, len(metadata.ContentTypes))
	for index, option := range metadata.ContentTypes {
		if option.Code == "" || strings.TrimSpace(option.Code) != option.Code ||
			option.Label == "" || strings.TrimSpace(option.Label) != option.Label ||
			option.Editor == "" || strings.TrimSpace(string(option.Editor)) != string(option.Editor) {
			return fmt.Errorf("resource type %q content type at index %d is invalid", code, index)
		}
		if option.Editor != resourcetype.ContentEditorHTML &&
			option.Editor != resourcetype.ContentEditorTextarea {
			return fmt.Errorf(
				"resource type %q content type %q has unsupported editor %q",
				code,
				option.Code,
				option.Editor,
			)
		}
		if _, exists := seenContentTypes[option.Code]; exists {
			return fmt.Errorf("resource type %q content type %q is duplicated", code, option.Code)
		}
		seenContentTypes[option.Code] = struct{}{}
	}
	if metadata.Capabilities.SupportsContent != (len(metadata.ContentTypes) > 0) {
		return fmt.Errorf("resource type %q content capability and metadata disagree", code)
	}

	r.resourceTypes[code] = compiledResourceType
	return nil
}

func (r *RuntimeRegistry) addPermission(
	definition permission.Definition,
) error {
	if definition.Code == "" {
		return errors.New("permission code is empty")
	}
	if _, exists := r.permissions[definition.Code]; exists {
		return fmt.Errorf(
			"permission %q already exists",
			definition.Code,
		)
	}
	r.permissions[definition.Code] = definition
	r.permissionCodes = append(
		r.permissionCodes,
		definition.Code,
	)
	return nil
}

func isNilValue(value any) bool {
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map,
		reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
