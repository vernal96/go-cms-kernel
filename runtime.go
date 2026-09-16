package kernel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"strings"

	"github.com/vernal96/go-cms-kernel/cache"
	"github.com/vernal96/go-cms-kernel/entityhooks"
	"github.com/vernal96/go-cms-kernel/eventbus"
	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
	"github.com/vernal96/go-cms-kernel/permission"
)

type ModuleCode string
type ProfileCode string

type Profile struct {
	Code        ProfileCode
	Name        string
	Modules     []ProfileModule
	Params      []field.Definition
	EditorTabs  []field.EditorTab
	Templates   []template.Definition
	WidgetViews []widget.View
}

type ProfileModule struct {
	Module      Module
	Config      any
	Caches      []cache.Binding
	Filesystems []filesystem.Binding
}

type Module interface {
	Code() ModuleCode
	Build(context.Context, ModuleContext) (ModuleRuntime, error)
}

// ModuleApplication is an application-scoped dependency owned by exactly one
// module. A module runtime receives only the dependency registered for its own
// module code, so this does not expose an unrestricted application service
// locator.
type ModuleApplication interface {
	ModuleCode() ModuleCode
}

type ModuleDescriptor struct {
	Label       string
	Description string
}

// ModuleDescriptorProvider is optional human metadata for admin/catalog UIs.
type ModuleDescriptorProvider interface {
	ModuleDescriptor() ModuleDescriptor
}

// DependencyProvider explicitly declares module runtime dependencies. A
// module can resolve only these dependencies from its ModuleContext.
type DependencyProvider interface {
	Dependencies() []ModuleCode
}

type ModuleRuntime interface {
	ModuleCode() ModuleCode
}

// RuntimeBuildFinalizer is an optional module-runtime capability invoked after
// every module in the profile has been built. It lets contributor modules
// register into an earlier dependency during Build while ensuring the
// dependency becomes immutable before the completed ProfileRuntime is used.
// Finalizers run deterministically in profile order.
type RuntimeBuildFinalizer interface {
	FinalizeRuntimeBuild(context.Context) error
}

type RuntimeTransitionReason string

var ErrRuntimeTransitionBlocked = errors.New("runtime transition is blocked")

const (
	RuntimeTransitionProfileChange RuntimeTransitionReason = "profile_change"
	RuntimeTransitionSiteDelete    RuntimeTransitionReason = "site_delete"
)

// RuntimeTransition describes a semantic change that will replace or remove
// the currently published site runtime. Ordinary same-profile site updates do
// not produce a transition.
type RuntimeTransition struct {
	Reason      RuntimeTransitionReason
	ScopeID     string
	FromProfile ProfileCode
	ToProfile   ProfileCode
}

// PreparedRuntimeTransition keeps a module's temporary transition state alive
// until the owning site mutation either commits or aborts. Commit must be a
// non-failing finalization step; fallible external cleanup belongs in prepare.
type PreparedRuntimeTransition interface {
	Commit()
	Abort()
}

// RuntimeTransitionParticipant is an optional module-runtime capability for
// safely draining runtime-owned work before a profile change or site deletion.
type RuntimeTransitionParticipant interface {
	PrepareRuntimeTransition(
		context.Context,
		RuntimeTransition,
	) (PreparedRuntimeTransition, error)
}

// RuntimeScope is the immutable site snapshot available while modules build
// their final site-scoped runtime state. Request-specific values do not belong
// here.
type RuntimeScope struct {
	siteID      string
	profileCode ProfileCode
	domain      string
	locale      string
	isPublic    bool
	settings    map[string]any
}

func NewSiteRuntimeScope(
	siteID string,
	profileCode ProfileCode,
	domain string,
	locale string,
	isPublic bool,
	settings map[string]any,
) RuntimeScope {
	return RuntimeScope{siteID: siteID, profileCode: profileCode, domain: domain, locale: locale, isPublic: isPublic, settings: cloneRuntimeSettings(settings)}
}

func NewRuntimeScope(
	siteID string,
	domain string,
	locale string,
	settings map[string]any,
) RuntimeScope {
	return RuntimeScope{
		siteID:   siteID,
		domain:   domain,
		locale:   locale,
		settings: cloneRuntimeSettings(settings),
	}
}

func (s RuntimeScope) SiteID() string { return s.siteID }

func (s RuntimeScope) ProfileCode() ProfileCode { return s.profileCode }

func (s RuntimeScope) Domain() string { return s.domain }

func (s RuntimeScope) Locale() string { return s.locale }

func (s RuntimeScope) IsPublic() bool { return s.isPublic }

func (s RuntimeScope) Settings() map[string]any {
	return cloneRuntimeSettings(s.settings)
}

func (s RuntimeScope) clone() RuntimeScope {
	return NewSiteRuntimeScope(s.siteID, s.profileCode, s.domain, s.locale, s.isPublic, s.settings)
}

func cloneRuntimeSettings(source map[string]any) map[string]any {
	if source == nil {
		return map[string]any{}
	}
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = cloneRuntimeSettingValue(value)
	}
	return result
}

func cloneRuntimeSettingValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneRuntimeSettings(typed)
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = cloneRuntimeSettingValue(item)
		}
		return result
	case []string:
		return append([]string(nil), typed...)
	default:
		return typed
	}
}

type DefinitionRegistry interface {
	FieldType(field.TypeCode) (field.Type, bool)
	FieldTypes() []field.TypeCode
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
	ResourceTypes      []resourcetype.Type
	PermissionEntities []permission.Entity
}

// ModuleConfigCloner protects mutable config declarations in profile snapshots.
type ModuleConfigCloner interface{ CloneModuleConfig() any }

// ConfiguredRegistryProvider declares profile-specific types before schemas compile.
// Implementations must return immutable declarations detached from config.
type ConfiguredRegistryProvider interface {
	RegistryForConfig(any) (ModuleRegistry, error)
}

// RegistryForModule resolves the same declarations for profile compilation and
// application-wide catalogs. Configured declarations take precedence.
func RegistryForModule(module ProfileModule) (ModuleRegistry, error) {
	if module.Module == nil || isNilValue(module.Module) {
		return ModuleRegistry{}, errors.New("module is nil")
	}
	if provider, ok := module.Module.(ConfiguredRegistryProvider); ok {
		registry, err := provider.RegistryForConfig(module.Config)
		if err != nil {
			return ModuleRegistry{}, fmt.Errorf("module %q registry: %w", module.Module.Code(), err)
		}
		return registry, nil
	}
	if provider, ok := module.Module.(RegistryProvider); ok {
		return provider.Registry(), nil
	}
	return ModuleRegistry{}, nil
}

type RegistryProvider interface {
	Registry() ModuleRegistry
}

type RuntimeRegistry struct {
	modules         map[ModuleCode]ModuleRuntime
	moduleRuntimes  []ModuleRuntime
	fieldTypes      map[field.TypeCode]field.Type
	resourceTypes   map[resourcetype.Code]resourcetype.Type
	permissions     map[permission.Code]permission.Definition
	permissionCodes []permission.Code
}

func newRuntimeRegistry() *RuntimeRegistry {
	return &RuntimeRegistry{
		modules:    make(map[ModuleCode]ModuleRuntime),
		fieldTypes: make(map[field.TypeCode]field.Type),
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

type ModuleContext struct {
	hooks        entityhooks.Registrar
	resolver     DatabaseResolver
	moduleCode   ModuleCode
	application  ModuleApplication
	profile      Profile
	registry     DefinitionRegistry
	config       any
	scope        RuntimeScope
	dependencies map[ModuleCode]ModuleRuntime
	caches       cache.ModuleManager
	filesystems  filesystem.ModuleManager
	eventBus     eventbus.Bus
	logger       *slog.Logger
}

func newModuleContext(
	resolver DatabaseResolver,
	moduleCode ModuleCode,
	application ModuleApplication,
	profile Profile,
	registry DefinitionRegistry,
	config any,
	scope RuntimeScope,
	services RuntimeServices,
	dependencies map[ModuleCode]ModuleRuntime,
	caches cache.ModuleManager,
	filesystems filesystem.ModuleManager,
) ModuleContext {
	return ModuleContext{
		resolver:     resolver,
		moduleCode:   moduleCode,
		application:  application,
		profile:      cloneProfile(profile),
		registry:     registry,
		config:       config,
		scope:        scope.clone(),
		dependencies: dependencies,
		caches:       caches,
		filesystems:  filesystems,
		eventBus:     services.EventBus,
		logger:       services.Logger,
	}
}

func (c ModuleContext) EntityHooks() entityhooks.Registrar { return c.hooks }

func (c ModuleContext) ModuleCode() ModuleCode {
	return c.moduleCode
}

func ModuleApplicationFrom[T ModuleApplication](ctx ModuleContext) (T, error) {
	var zero T
	if ctx.application == nil || isNilValue(ctx.application) {
		return zero, fmt.Errorf("module %q application dependency is unavailable", ctx.ModuleCode())
	}
	application, ok := ctx.application.(T)
	if !ok || isNilValue(application) {
		return zero, fmt.Errorf(
			"module %q application dependency has invalid type %T, expected %T",
			ctx.ModuleCode(),
			ctx.application,
			zero,
		)
	}
	return application, nil
}

func (c ModuleContext) Profile() Profile {
	return cloneProfile(c.profile)
}

func (c ModuleContext) Registry() DefinitionRegistry {
	return c.registry
}

func (c ModuleContext) Config() any {
	return c.config
}

func (c ModuleContext) Scope() RuntimeScope {
	return c.scope.clone()
}

func (c ModuleContext) Dependency(code ModuleCode) (ModuleRuntime, bool) {
	runtime, exists := c.dependencies[code]
	return runtime, exists
}

// Caches exposes only aliases explicitly bound to this module.
func (c ModuleContext) Caches() cache.ModuleManager {
	return c.caches
}

// Filesystems exposes only aliases explicitly bound to this module.
func (c ModuleContext) Filesystems() filesystem.ModuleManager {
	return c.filesystems
}

func (c ModuleContext) EventBus() eventbus.Bus {
	return c.eventBus
}

func (c ModuleContext) Logger() *slog.Logger {
	return c.logger
}

func ModuleConfigFrom[T any](ctx ModuleContext) (T, error) {
	var zero T

	if ctx.config == nil {
		return zero, nil
	}

	if config, ok := ctx.config.(T); ok {
		return config, nil
	}

	if config, ok := ctx.config.(*T); ok {
		if config == nil {
			return zero, errors.New("module config is nil")
		}

		return *config, nil
	}

	return zero, fmt.Errorf(
		"invalid module config type %T, expected %T",
		ctx.config,
		zero,
	)
}

func ModuleDatabaseFrom[T ModuleDatabase](
	ctx ModuleContext,
	connectionCode ConnectionCode,
	moduleCode ModuleCode,
) (T, error) {
	var zero T

	var (
		database ModuleDatabase
		exists   bool
	)

	if connectionCode == "" {
		database, exists = ctx.resolver.MainModuleDatabase(
			moduleCode,
		)
	} else {
		database, exists = ctx.resolver.ModuleDatabase(
			connectionCode,
			moduleCode,
		)
	}

	if !exists {
		return zero, fmt.Errorf(
			"database for module %q on connection %q not found",
			moduleCode,
			connectionCode,
		)
	}

	result, ok := database.(T)
	if !ok {
		return zero, fmt.Errorf(
			"database for module %q has invalid type %T",
			moduleCode,
			database,
		)
	}

	return result, nil
}

type ProfileRuntime struct {
	hooks     *entityhooks.Registry
	blueprint *ProfileBlueprint
	registry  Registry
	widgets   *widget.Catalog
	templates *template.Catalog
}

// ProfileBlueprint is the immutable, reusable part of a compiled profile.
// Final registries and module runtime instances are built from it per site.
type ProfileBlueprint struct {
	profile     Profile
	registry    *RuntimeRegistry
	paramSchema *field.Schema
	templates   *template.Catalog
	factory     *ProfileRuntimeFactory
}

func (b *ProfileBlueprint) Profile() Profile {
	if b == nil {
		return Profile{}
	}
	return cloneProfile(b.profile)
}

func (b *ProfileBlueprint) Registry() DefinitionRegistry {
	if b == nil {
		return nil
	}
	return b.registry
}

func (b *ProfileBlueprint) ParamSchema() *field.Schema {
	if b == nil {
		return nil
	}
	return b.paramSchema
}

func (b *ProfileBlueprint) Template(
	code template.Code,
) (*template.Runtime, bool) {
	if b == nil || b.templates == nil {
		return nil, false
	}
	return b.templates.Template(code)
}

func (b *ProfileBlueprint) Templates() []template.Definition {
	if b == nil || b.templates == nil {
		return nil
	}
	return b.templates.Definitions()
}

func (r *ProfileRuntime) Profile() Profile {
	if r == nil || r.blueprint == nil {
		return Profile{}
	}
	return r.blueprint.Profile()
}

func (r *ProfileRuntime) Blueprint() *ProfileBlueprint {
	if r == nil {
		return nil
	}
	return r.blueprint
}

func (r *ProfileRuntime) EntityHooks() *entityhooks.Registry { return r.hooks }

func (r *ProfileRuntime) Registry() Registry {
	return r.registry
}

func (r *ProfileRuntime) Modules() []ModuleRuntime {
	if r == nil || r.registry == nil {
		return nil
	}
	return r.registry.Modules()
}

func (r *ProfileRuntime) ParamSchema() *field.Schema {
	if r == nil || r.blueprint == nil {
		return nil
	}
	return r.blueprint.ParamSchema()
}

func (r *ProfileRuntime) Template(
	code template.Code,
) (*template.Runtime, bool) {
	if r == nil || r.templates == nil {
		return nil, false
	}
	return r.templates.Template(code)
}

func (r *ProfileRuntime) Templates() []template.Definition {
	if r == nil || r.templates == nil {
		return nil
	}
	return r.templates.Definitions()
}

func (r *ProfileRuntime) Widget(
	code widget.Code,
) (*widget.Runtime, bool) {
	if r == nil || r.widgets == nil {
		return nil, false
	}

	return r.widgets.Widget(code)
}

func (r *ProfileRuntime) Widgets() []widget.Definition {
	if r == nil || r.widgets == nil {
		return nil
	}

	return r.widgets.Definitions()
}

type ProfileRuntimeFactory struct {
	resolver     DatabaseResolver
	services     RuntimeServices
	applications map[ModuleCode]ModuleApplication
}

type RuntimeServices struct {
	Caches             cache.Resolver
	Filesystems        filesystem.Resolver
	EventBus           eventbus.Bus
	Logger             *slog.Logger
	ModuleApplications []ModuleApplication
}

func ModuleDependencyFrom[T ModuleRuntime](
	ctx ModuleContext,
	moduleCode ModuleCode,
) (T, error) {
	var zero T
	runtime, exists := ctx.Dependency(moduleCode)
	if !exists {
		return zero, fmt.Errorf(
			"module %q dependency %q is unavailable or undeclared",
			ctx.ModuleCode(),
			moduleCode,
		)
	}
	dependency, ok := runtime.(T)
	if !ok {
		return zero, fmt.Errorf(
			"module %q dependency %q has invalid runtime type %T",
			ctx.ModuleCode(),
			moduleCode,
			runtime,
		)
	}
	return dependency, nil
}

func NewProfileRuntimeFactory(
	resolver DatabaseResolver,
	services RuntimeServices,
) (*ProfileRuntimeFactory, error) {
	if resolver == nil {
		return nil, errors.New("database resolver is nil")
	}

	if services.Logger == nil {
		return nil, errors.New("runtime logger is nil")
	}
	if services.EventBus == nil || isNilValue(services.EventBus) {
		return nil, errors.New("runtime event bus is nil")
	}
	applications := make(map[ModuleCode]ModuleApplication, len(services.ModuleApplications))
	for index, application := range services.ModuleApplications {
		if application == nil || isNilValue(application) {
			return nil, fmt.Errorf("module application at index %d is nil", index)
		}
		moduleCode := application.ModuleCode()
		if moduleCode == "" {
			return nil, fmt.Errorf("module application at index %d has empty module code", index)
		}
		if _, exists := applications[moduleCode]; exists {
			return nil, fmt.Errorf("module application %q is defined more than once", moduleCode)
		}
		applications[moduleCode] = application
	}
	services.ModuleApplications = append([]ModuleApplication(nil), services.ModuleApplications...)
	return &ProfileRuntimeFactory{
		resolver:     resolver,
		services:     services,
		applications: applications,
	}, nil
}

func (f *ProfileRuntimeFactory) Compile(
	ctx context.Context,
	profile Profile,
) (*ProfileBlueprint, error) {
	if ctx == nil {
		return nil, errors.New("profile compile context is nil")
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if profile.Code == "" {
		return nil, errors.New("profile code is empty")
	}

	profile = cloneProfile(profile)

	moduleCodes := make(
		map[ModuleCode]struct{},
		len(profile.Modules),
	)

	for index, profileModule := range profile.Modules {
		if profileModule.Module == nil {
			return nil, fmt.Errorf(
				"profile %q module at index %d is nil",
				profile.Code,
				index,
			)
		}

		moduleCode := profileModule.Module.Code()
		if moduleCode == "" {
			return nil, fmt.Errorf(
				"profile %q module at index %d has empty code",
				profile.Code,
				index,
			)
		}

		if _, exists := moduleCodes[moduleCode]; exists {
			return nil, fmt.Errorf(
				"profile %q contains duplicate module %q",
				profile.Code,
				moduleCode,
			)
		}

		moduleCodes[moduleCode] = struct{}{}
	}
	if err := validateModuleDependencyOrder(profile); err != nil {
		return nil, err
	}

	registry := newRuntimeRegistry()

	for _, profileModule := range profile.Modules {
		moduleRegistry, err := RegistryForModule(profileModule)
		if err != nil {
			return nil, err
		}

		for index, fieldType := range moduleRegistry.FieldTypes {
			if err := registry.addFieldType(fieldType); err != nil {
				return nil, fmt.Errorf(
					"register field type at index %d from module %q: %w",
					index,
					profileModule.Module.Code(),
					err,
				)
			}
		}
		for index, resourceType := range moduleRegistry.ResourceTypes {
			if err := registry.addResourceType(resourceType); err != nil {
				return nil, fmt.Errorf(
					"register resource type at index %d from module %q: %w",
					index,
					profileModule.Module.Code(),
					err,
				)
			}
		}
		if len(moduleRegistry.PermissionEntities) == 0 {
			continue
		}
		permissionDefinitions, err := permission.Definitions(
			string(profileModule.Module.Code()),
			moduleRegistry.PermissionEntities,
		)
		if err != nil {
			return nil, fmt.Errorf(
				"register permissions from module %q: %w",
				profileModule.Module.Code(),
				err,
			)
		}
		for index, definition := range permissionDefinitions {
			if err := registry.addPermission(definition); err != nil {
				return nil, fmt.Errorf(
					"register permission at index %d from module %q: %w",
					index,
					profileModule.Module.Code(),
					err,
				)
			}
		}
	}

	paramSchema, err := field.Compile(profile.Params, registry)
	if err != nil {
		return nil, fmt.Errorf(
			"compile params for profile %q: %w",
			profile.Code,
			err,
		)
	}
	if err := field.ValidateEditorTabs(profile.Params, profile.EditorTabs); err != nil {
		return nil, fmt.Errorf(
			"compile editor tabs for profile %q: %w",
			profile.Code,
			err,
		)
	}

	templates, err := template.Compile(
		profile.Templates,
		registry,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"compile templates for profile %q: %w",
			profile.Code,
			err,
		)
	}

	return &ProfileBlueprint{
		profile:     profile,
		registry:    registry,
		paramSchema: paramSchema,
		templates:   templates,
		factory:     f,
	}, nil
}

func (b *ProfileBlueprint) Build(
	ctx context.Context,
	scope RuntimeScope,
) (*ProfileRuntime, error) {
	if b == nil || b.factory == nil {
		return nil, errors.New("profile blueprint is unavailable")
	}
	if ctx == nil {
		return nil, errors.New("profile runtime context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if scope.SiteID() == "" {
		return nil, errors.New("profile runtime site scope is empty")
	}

	profile := b.profile
	registry := b.registry.cloneDefinitions()
	hooks := entityhooks.NewRegistry(entityhooks.Site, scope.SiteID())
	widgetSources := make([]widget.Source, 0, len(profile.Modules))
	for _, profileModule := range profile.Modules {
		module := profileModule.Module
		dependencies, err := resolveModuleDependencies(module, registry)
		if err != nil {
			return nil, fmt.Errorf(
				"resolve dependencies for module %q in profile %q: %w",
				module.Code(),
				profile.Code,
				err,
			)
		}

		moduleCaches, err := cache.NewRuntimeModuleManager(
			b.factory.services.Caches,
			cache.RuntimeScope{
				Profile: string(profile.Code),
				Site:    scope.SiteID(),
			},
			string(module.Code()),
			profileModule.Caches,
		)
		if err != nil {
			return nil, fmt.Errorf(
				"configure caches for module %q in profile %q: %w",
				module.Code(),
				profile.Code,
				err,
			)
		}

		moduleFilesystems, err := filesystem.NewModuleManager(
			b.factory.services.Filesystems,
			profileModule.Filesystems,
		)
		if err != nil {
			return nil, fmt.Errorf(
				"configure filesystems for module %q in profile %q: %w",
				module.Code(),
				profile.Code,
				err,
			)
		}

		logger := b.factory.services.Logger.With(
			slog.String("profile.code", string(profile.Code)),
			slog.String("module.code", string(module.Code())),
		)
		if scope.SiteID() != "" {
			logger = logger.With(slog.String("site.id", scope.SiteID()))
		}
		moduleContext := newModuleContext(
			b.factory.resolver,
			module.Code(),
			b.factory.applications[module.Code()],
			profile,
			registry,
			profileModule.Config,
			scope,
			RuntimeServices{
				Caches:             b.factory.services.Caches,
				Filesystems:        b.factory.services.Filesystems,
				EventBus:           b.factory.services.EventBus,
				Logger:             logger,
				ModuleApplications: b.factory.services.ModuleApplications,
			},
			dependencies,
			moduleCaches,
			moduleFilesystems,
		)

		var hookDependencies []string
		if provider, ok := module.(DependencyProvider); ok {
			for _, code := range provider.Dependencies() {
				hookDependencies = append(hookDependencies, string(code))
			}
		}
		moduleContext.hooks = hooks.ForModule(string(module.Code()), hookDependencies)
		runtime, err := module.Build(ctx, moduleContext)
		if err != nil {
			return nil, fmt.Errorf(
				"build module %q for profile %q: %w",
				module.Code(),
				profile.Code,
				err,
			)
		}
		if runtime == nil || isNilValue(runtime) {
			return nil, fmt.Errorf("module %q returned nil runtime", module.Code())
		}
		if runtime.ModuleCode() != module.Code() {
			return nil, fmt.Errorf(
				"module %q returned runtime for module %q",
				module.Code(),
				runtime.ModuleCode(),
			)
		}
		if err := registry.add(runtime); err != nil {
			return nil, err
		}
		if provider, ok := runtime.(widget.Provider); ok {
			descriptor := ModuleDescriptor{Label: string(module.Code())}
			if provider, ok := module.(ModuleDescriptorProvider); ok {
				descriptor = provider.ModuleDescriptor()
			}
			widgetSources = append(widgetSources, widget.Source{
				Module: widget.ModuleDescriptor{
					Code: string(module.Code()), Label: descriptor.Label,
					Description: descriptor.Description,
				},
				Widgets: provider.Widgets(),
			})
		}
	}

	for _, moduleRuntime := range registry.Modules() {
		finalizer, ok := moduleRuntime.(RuntimeBuildFinalizer)
		if !ok {
			continue
		}
		if err := finalizer.FinalizeRuntimeBuild(ctx); err != nil {
			return nil, fmt.Errorf(
				"finalize module runtime %q for profile %q: %w",
				moduleRuntime.ModuleCode(),
				profile.Code,
				err,
			)
		}
	}

	if err := hooks.Seal(); err != nil {
		return nil, err
	}
	widgets, err := widget.Compile(widgetSources, profile.WidgetViews, registry)
	if err != nil {
		return nil, fmt.Errorf(
			"compile widgets for profile %q: %w",
			profile.Code,
			err,
		)
	}
	templates, err := b.templates.CompileWidgets(widgets)
	if err != nil {
		return nil, fmt.Errorf(
			"compile template widgets for profile %q: %w",
			profile.Code,
			err,
		)
	}
	return &ProfileRuntime{
		hooks:     hooks,
		blueprint: b,
		registry:  registry,
		widgets:   widgets,
		templates: templates,
	}, nil
}

func validateModuleDependencyOrder(profile Profile) error {
	available := make(map[ModuleCode]struct{}, len(profile.Modules))
	for _, profileModule := range profile.Modules {
		module := profileModule.Module
		provider, ok := module.(DependencyProvider)
		if ok {
			declared := make(map[ModuleCode]struct{})
			for index, dependency := range provider.Dependencies() {
				if dependency == "" {
					return fmt.Errorf(
						"profile %q module %q dependency at index %d has empty code",
						profile.Code,
						module.Code(),
						index,
					)
				}
				if dependency == module.Code() {
					return fmt.Errorf(
						"profile %q module %q declares itself as dependency",
						profile.Code,
						module.Code(),
					)
				}
				if _, exists := declared[dependency]; exists {
					return fmt.Errorf(
						"profile %q module %q declares dependency %q more than once",
						profile.Code,
						module.Code(),
						dependency,
					)
				}
				if _, exists := available[dependency]; !exists {
					return fmt.Errorf(
						"profile %q module %q dependency %q is unavailable; dependencies must be declared earlier in the profile",
						profile.Code,
						module.Code(),
						dependency,
					)
				}
				declared[dependency] = struct{}{}
			}
		}
		available[module.Code()] = struct{}{}
	}
	return nil
}

func resolveModuleDependencies(
	module Module,
	registry Registry,
) (map[ModuleCode]ModuleRuntime, error) {
	provider, ok := module.(DependencyProvider)
	if !ok {
		return nil, nil
	}
	result := make(map[ModuleCode]ModuleRuntime)
	for index, code := range provider.Dependencies() {
		if code == "" {
			return nil, fmt.Errorf("dependency at index %d has empty code", index)
		}
		if code == module.Code() {
			return nil, fmt.Errorf("module declares itself as dependency")
		}
		if _, exists := result[code]; exists {
			return nil, fmt.Errorf("dependency %q is declared more than once", code)
		}
		runtime, exists := registry.Module(code)
		if !exists {
			return nil, fmt.Errorf(
				"dependency %q is unavailable; dependencies must be declared earlier in the profile",
				code,
			)
		}
		result[code] = runtime
	}
	return result, nil
}

func cloneProfile(profile Profile) Profile {
	profile.Modules = append(
		[]ProfileModule(nil),
		profile.Modules...,
	)
	for index := range profile.Modules {
		if cloner, ok := profile.Modules[index].Config.(ModuleConfigCloner); ok {
			profile.Modules[index].Config = cloner.CloneModuleConfig()
		}
		profile.Modules[index].Caches = append(
			[]cache.Binding(nil),
			profile.Modules[index].Caches...,
		)
		profile.Modules[index].Filesystems = append(
			[]filesystem.Binding(nil),
			profile.Modules[index].Filesystems...,
		)
	}
	profile.Params = field.CloneDefinitions(profile.Params)
	profile.EditorTabs = field.CloneEditorTabs(profile.EditorTabs)
	profile.Templates = template.CloneDefinitions(profile.Templates)
	profile.WidgetViews = widget.CloneViews(profile.WidgetViews)

	return profile
}
