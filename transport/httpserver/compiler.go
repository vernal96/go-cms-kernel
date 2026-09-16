package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

var (
	middlewareCodePattern = regexp.MustCompile(
		`^[a-z][a-z0-9._-]*$`,
	)
	methodPattern = regexp.MustCompile(`^[A-Z][A-Z0-9!#$%&'*+.^_` +
		"`" + `|~-]*$`)
)

type moduleContribution struct {
	module       kernel.ModuleCode
	contribution httptransport.Contribution
}

type ownedMiddleware struct {
	module     kernel.ModuleCode
	definition httptransport.MiddlewareDefinition
}

type registration struct {
	module  kernel.ModuleCode
	method  string
	pattern string
	name    string
	shape   string
}

type profileCompiler struct {
	profile kernel.ProfileCode
	runtime *kernel.ProfileRuntime
	router  chi.Router

	middleware       map[httptransport.MiddlewareCode]ownedMiddleware
	moduleMiddleware map[kernel.ModuleCode][]ownedMiddleware
	routes           map[string]registration
	names            map[string]registration
	mounts           map[string]registration
}

func CompileProfile(
	ctx context.Context,
	runtime *kernel.ProfileRuntime,
) (http.Handler, error) {
	if ctx == nil {
		return nil, errors.New("HTTP profile compile context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if runtime == nil {
		return nil, errors.New("HTTP profile runtime is nil")
	}

	profile := runtime.Profile()
	if profile.Code == "" {
		return nil, errors.New("HTTP profile code is empty")
	}

	contributions, err := collectHTTPContributions(ctx, runtime)
	if err != nil {
		return nil, err
	}

	compiler := &profileCompiler{
		profile:          profile.Code,
		runtime:          runtime,
		router:           chi.NewRouter(),
		middleware:       make(map[httptransport.MiddlewareCode]ownedMiddleware),
		moduleMiddleware: make(map[kernel.ModuleCode][]ownedMiddleware),
		routes:           make(map[string]registration),
		names:            make(map[string]registration),
		mounts:           make(map[string]registration),
	}

	profileMiddleware, err := compiler.collectMiddleware(contributions)
	if err != nil {
		return nil, err
	}
	for _, middleware := range profileMiddleware {
		compiler.router.Use(middleware.definition.Middleware)
	}

	for _, contribution := range contributions {
		if contribution.contribution.Routes == nil {
			continue
		}
		registrar := &scopedRegistrar{
			compiler: compiler,
			module:   contribution.module,
		}
		if err := contribution.contribution.Routes(registrar); err != nil {
			return nil, fmt.Errorf(
				"profile %q module %q register routes: %w",
				compiler.profile,
				contribution.module,
				err,
			)
		}
	}

	resourceHandlers, err := compiler.compileResourceHandlers(
		contributions,
	)
	if err != nil {
		return nil, err
	}
	if err := compiler.validateRoutableResourceHandlers(
		resourceHandlers,
	); err != nil {
		return nil, err
	}

	terminal, err := compiler.compileTerminal(
		contributions,
		resourceHandlers,
	)
	if err != nil {
		return nil, err
	}
	compiler.router.NotFound(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		terminal.ServeHTTP(response, request)
	})

	return compiler.router, nil
}

func CompileSite(
	ctx context.Context,
	runtime *site.Runtime,
) (http.Handler, error) {
	if runtime == nil {
		return nil, errors.New("HTTP site runtime is nil")
	}
	return CompileProfile(ctx, runtime.Profile())
}

func collectHTTPContributions(
	ctx context.Context,
	runtime *kernel.ProfileRuntime,
) ([]moduleContribution, error) {
	profile := runtime.Profile()
	result := make([]moduleContribution, 0, len(profile.Modules))
	for _, profileModule := range profile.Modules {
		moduleCode := profileModule.Module.Code()
		moduleRuntime, exists := runtime.Registry().Module(moduleCode)
		if !exists {
			return nil, fmt.Errorf(
				"profile %q module runtime %q is unavailable",
				profile.Code,
				moduleCode,
			)
		}

		provider, ok := moduleRuntime.(httptransport.Provider)
		if !ok {
			continue
		}
		builder := provider.HTTP()
		if isNilHTTPValue(builder) {
			return nil, fmt.Errorf(
				"profile %q module %q returned nil HTTP builder",
				profile.Code,
				moduleCode,
			)
		}
		contribution, err := builder.Build(ctx)
		if err != nil {
			return nil, fmt.Errorf(
				"profile %q module %q build HTTP controllers: %w",
				profile.Code,
				moduleCode,
				err,
			)
		}
		result = append(result, moduleContribution{
			module:       moduleCode,
			contribution: contribution,
		})
	}
	return result, nil
}

func (c *profileCompiler) collectMiddleware(
	contributions []moduleContribution,
) ([]ownedMiddleware, error) {
	var profileMiddleware []ownedMiddleware
	for _, contribution := range contributions {
		for _, definition := range contribution.contribution.Middleware {
			if !middlewareCodePattern.MatchString(
				string(definition.Code),
			) {
				return nil, fmt.Errorf(
					"profile %q module %q has invalid middleware code %q",
					c.profile,
					contribution.module,
					definition.Code,
				)
			}
			if definition.Middleware == nil {
				return nil, fmt.Errorf(
					"profile %q module %q middleware %q is nil",
					c.profile,
					contribution.module,
					definition.Code,
				)
			}
			switch definition.Scope {
			case httptransport.MiddlewareProfile,
				httptransport.MiddlewareModule,
				httptransport.MiddlewareLocal:
			default:
				return nil, fmt.Errorf(
					"profile %q module %q middleware %q has invalid scope %d",
					c.profile,
					contribution.module,
					definition.Code,
					definition.Scope,
				)
			}
			if previous, exists := c.middleware[definition.Code]; exists {
				return nil, fmt.Errorf(
					"profile %q module %q middleware %q duplicates module %q",
					c.profile,
					contribution.module,
					definition.Code,
					previous.module,
				)
			}

			owned := ownedMiddleware{
				module:     contribution.module,
				definition: definition,
			}
			c.middleware[definition.Code] = owned
			switch definition.Scope {
			case httptransport.MiddlewareProfile:
				profileMiddleware = append(profileMiddleware, owned)
			case httptransport.MiddlewareModule:
				c.moduleMiddleware[contribution.module] = append(
					c.moduleMiddleware[contribution.module],
					owned,
				)
			}
		}
	}
	return profileMiddleware, nil
}

func (c *profileCompiler) compileResourceHandlers(
	contributions []moduleContribution,
) (*compiledResourceHandlers, error) {
	handlers := &compiledResourceHandlers{
		handlers: make(map[resourcetype.Code]http.Handler),
		owners:   make(map[resourcetype.Code]kernel.ModuleCode),
	}

	for _, contribution := range contributions {
		for _, definition := range contribution.contribution.ResourceHandlers {
			resourceTypeCode := resourcetype.Code(definition.Type)
			if definition.Type == "" {
				return nil, fmt.Errorf(
					"profile %q module %q has empty resource handler type",
					c.profile,
					contribution.module,
				)
			}
			if isNilHTTPValue(definition.Handler) {
				return nil, fmt.Errorf(
					"profile %q module %q resource handler %q is nil",
					c.profile,
					contribution.module,
					definition.Type,
				)
			}
			if _, exists := c.runtime.Registry().ResourceType(
				resourceTypeCode,
			); !exists {
				return nil, fmt.Errorf(
					"profile %q module %q resource handler %q has no registered resource type",
					c.profile,
					contribution.module,
					definition.Type,
				)
			}
			if previous, exists := handlers.owners[resourceTypeCode]; exists {
				return nil, fmt.Errorf(
					"profile %q module %q resource handler %q duplicates module %q",
					c.profile,
					contribution.module,
					definition.Type,
					previous,
				)
			}

			explicit, err := c.resolveMiddleware(
				contribution.module,
				definition.Middleware,
			)
			if err != nil {
				return nil, fmt.Errorf(
					"profile %q module %q resource handler %q: %w",
					c.profile,
					contribution.module,
					definition.Type,
					err,
				)
			}
			chain := append(
				append(
					[]ownedMiddleware(nil),
					c.moduleMiddleware[contribution.module]...,
				),
				explicit...,
			)
			handler, err := wrapMiddleware(definition.Handler, chain)
			if err != nil {
				return nil, fmt.Errorf(
					"profile %q module %q resource handler %q: %w",
					c.profile,
					contribution.module,
					definition.Type,
					err,
				)
			}

			handlers.handlers[resourceTypeCode] = handler
			handlers.owners[resourceTypeCode] = contribution.module
		}
	}
	return handlers, nil
}

func (c *profileCompiler) validateRoutableResourceHandlers(
	handlers *compiledResourceHandlers,
) error {
	profile := c.runtime.Profile()
	for _, profileModule := range profile.Modules {
		provider, ok := profileModule.Module.(kernel.RegistryProvider)
		if !ok {
			continue
		}
		for _, resourceType := range provider.Registry().ResourceTypes {
			if resourceType.PathMode() != resourcetype.PathRoute {
				continue
			}
			if _, exists := handlers.handlers[resourceType.Code()]; !exists {
				return fmt.Errorf(
					"profile %q module %q routable resource type %q has no HTTP handler",
					c.profile,
					profileModule.Module.Code(),
					resourceType.Code(),
				)
			}
		}
	}
	return nil
}

func (c *profileCompiler) compileTerminal(
	contributions []moduleContribution,
	handlers *compiledResourceHandlers,
) (http.Handler, error) {
	var (
		terminal       *httptransport.TerminalResourceHandler
		terminalModule kernel.ModuleCode
	)
	for index := range contributions {
		candidate := contributions[index].contribution.TerminalResource
		if candidate == nil {
			continue
		}
		if terminal != nil {
			return nil, fmt.Errorf(
				"profile %q module %q terminal resource handler duplicates module %q",
				c.profile,
				contributions[index].module,
				terminalModule,
			)
		}
		terminal = candidate
		terminalModule = contributions[index].module
	}
	if terminal == nil {
		return http.NotFoundHandler(), nil
	}
	if terminal.Factory == nil {
		return nil, fmt.Errorf(
			"profile %q module %q terminal resource handler factory is nil",
			c.profile,
			terminalModule,
		)
	}

	explicit, err := c.resolveMiddleware(
		terminalModule,
		terminal.Middleware,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"profile %q module %q terminal resource handler: %w",
			c.profile,
			terminalModule,
			err,
		)
	}
	handler, err := terminal.Factory(handlers)
	if err != nil {
		return nil, fmt.Errorf(
			"profile %q module %q build terminal resource handler: %w",
			c.profile,
			terminalModule,
			err,
		)
	}
	if isNilHTTPValue(handler) {
		return nil, fmt.Errorf(
			"profile %q module %q terminal resource handler is nil",
			c.profile,
			terminalModule,
		)
	}
	handler, err = wrapMiddleware(handler, explicit)
	if err != nil {
		return nil, fmt.Errorf(
			"profile %q module %q terminal resource handler: %w",
			c.profile,
			terminalModule,
			err,
		)
	}
	return handler, nil
}

func (c *profileCompiler) resolveMiddleware(
	module kernel.ModuleCode,
	codes []httptransport.MiddlewareCode,
) ([]ownedMiddleware, error) {
	result := make([]ownedMiddleware, 0, len(codes))
	used := make(map[httptransport.MiddlewareCode]struct{}, len(codes))
	for _, code := range codes {
		if _, exists := used[code]; exists {
			return nil, fmt.Errorf(
				"middleware %q is referenced more than once",
				code,
			)
		}
		used[code] = struct{}{}

		middleware, exists := c.middleware[code]
		if !exists {
			return nil, fmt.Errorf("unknown middleware %q", code)
		}
		if middleware.definition.Scope == httptransport.MiddlewareProfile ||
			middleware.definition.Scope == httptransport.MiddlewareModule {
			return nil, fmt.Errorf(
				"middleware %q is applied automatically for scope %d",
				code,
				middleware.definition.Scope,
			)
		}
		if middleware.module != module {
			return nil, fmt.Errorf(
				"middleware %q belongs to module %q",
				code,
				middleware.module,
			)
		}
		result = append(result, middleware)
	}
	return result, nil
}

type scopedRegistrar struct {
	compiler   *profileCompiler
	module     kernel.ModuleCode
	prefix     string
	middleware []ownedMiddleware
}

func (r *scopedRegistrar) Route(route httptransport.Route) error {
	method := strings.TrimSpace(route.Method)
	pattern, err := joinRoutePattern(r.prefix, route.Pattern)
	if err != nil {
		return r.routeError(method, route.Pattern, err)
	}
	if !methodPattern.MatchString(method) {
		return r.routeError(
			method,
			pattern,
			errors.New("invalid HTTP method"),
		)
	}
	if isNilHTTPValue(route.Handler) {
		return r.routeError(
			method,
			pattern,
			errors.New("handler/controller is nil"),
		)
	}

	if err := validatePlatformRoute(pattern); err != nil {
		return r.routeError("REGISTER", pattern, err)
	}
	shape, err := routeShape(pattern)
	if err != nil {
		return r.routeError(method, pattern, err)
	}
	key := method + "\x00" + shape
	if previous, exists := r.compiler.routes[key]; exists {
		return r.routeError(
			method,
			pattern,
			fmt.Errorf(
				"duplicates module %q route %s %q",
				previous.module,
				previous.method,
				previous.pattern,
			),
		)
	}
	if conflict, exists := r.compiler.mountConflict(shape); exists {
		return r.routeError(
			method,
			pattern,
			fmt.Errorf(
				"conflicts with module %q mount %q",
				conflict.module,
				conflict.pattern,
			),
		)
	}
	if err := r.compiler.reserveName(
		r.module,
		method,
		pattern,
		route.Name,
	); err != nil {
		return err
	}

	explicit, err := r.compiler.resolveMiddleware(
		r.module,
		route.Middleware,
	)
	if err != nil {
		return r.routeError(method, pattern, err)
	}
	chain := r.middlewareChain(explicit)
	handler, err := wrapMiddleware(route.Handler, chain)
	if err != nil {
		return r.routeError(method, pattern, err)
	}
	if err := registerChi(func() {
		r.compiler.router.Method(method, pattern, handler)
	}); err != nil {
		return r.routeError(method, pattern, err)
	}

	r.compiler.routes[key] = registration{
		module:  r.module,
		method:  method,
		pattern: pattern,
		shape:   shape,
		name:    route.Name,
	}
	return nil
}

func (r *scopedRegistrar) Mount(mount httptransport.Mount) error {
	pattern, err := joinRoutePattern(r.prefix, mount.Pattern)
	if err != nil {
		return r.routeError("MOUNT", mount.Pattern, err)
	}
	if err := validatePlatformRoute(pattern); err != nil {
		return r.routeError("REGISTER", pattern, err)
	}
	shape, err := routeShape(pattern)
	if err != nil {
		return r.routeError("MOUNT", pattern, err)
	}
	if isNilHTTPValue(mount.Handler) {
		return r.routeError(
			"MOUNT",
			pattern,
			errors.New("handler/controller is nil"),
		)
	}
	if conflict, exists := r.compiler.anyMountConflict(shape); exists {
		return r.routeError(
			"MOUNT",
			pattern,
			fmt.Errorf(
				"conflicts with module %q %s %q",
				conflict.module,
				conflict.method,
				conflict.pattern,
			),
		)
	}
	if err := r.compiler.reserveName(
		r.module,
		"MOUNT",
		pattern,
		mount.Name,
	); err != nil {
		return err
	}

	explicit, err := r.compiler.resolveMiddleware(
		r.module,
		mount.Middleware,
	)
	if err != nil {
		return r.routeError("MOUNT", pattern, err)
	}
	handler, err := wrapMiddleware(
		mount.Handler,
		r.middlewareChain(explicit),
	)
	if err != nil {
		return r.routeError("MOUNT", pattern, err)
	}
	if err := registerChi(func() {
		r.compiler.router.Mount(pattern, handler)
	}); err != nil {
		return r.routeError("MOUNT", pattern, err)
	}
	r.compiler.mounts[shape] = registration{
		module:  r.module,
		method:  "MOUNT",
		pattern: pattern,
		shape:   shape,
		name:    mount.Name,
	}
	return nil
}

func (r *scopedRegistrar) Group(
	prefix string,
	middleware []httptransport.MiddlewareCode,
	register httptransport.RegisterRoutes,
) error {
	if register == nil {
		return fmt.Errorf(
			"profile %q module %q route group %q register function is nil",
			r.compiler.profile,
			r.module,
			prefix,
		)
	}
	pattern, err := joinRoutePattern(r.prefix, prefix)
	if err != nil {
		return fmt.Errorf(
			"profile %q module %q route group %q: %w",
			r.compiler.profile,
			r.module,
			prefix,
			err,
		)
	}
	groupMiddleware, err := r.compiler.resolveMiddleware(
		r.module,
		middleware,
	)
	if err != nil {
		return fmt.Errorf(
			"profile %q module %q route group %q: %w",
			r.compiler.profile,
			r.module,
			pattern,
			err,
		)
	}
	child := &scopedRegistrar{
		compiler: r.compiler,
		module:   r.module,
		prefix:   pattern,
		middleware: append(
			append([]ownedMiddleware(nil), r.middleware...),
			groupMiddleware...,
		),
	}
	if err := register(child); err != nil {
		return fmt.Errorf("group %q: %w", pattern, err)
	}
	return nil
}

func (r *scopedRegistrar) middlewareChain(
	explicit []ownedMiddleware,
) []ownedMiddleware {
	result := make(
		[]ownedMiddleware,
		0,
		len(r.compiler.moduleMiddleware[r.module])+
			len(r.middleware)+len(explicit),
	)
	result = append(result, r.compiler.moduleMiddleware[r.module]...)
	result = append(result, r.middleware...)
	result = append(result, explicit...)
	return result
}

func (r *scopedRegistrar) routeError(
	method string,
	pattern string,
	err error,
) error {
	return fmt.Errorf(
		"profile %q module %q route %s %q: %w",
		r.compiler.profile,
		r.module,
		method,
		pattern,
		err,
	)
}

func (c *profileCompiler) reserveName(
	module kernel.ModuleCode,
	method string,
	pattern string,
	name string,
) error {
	if name == "" {
		return nil
	}
	if previous, exists := c.names[name]; exists {
		return fmt.Errorf(
			"profile %q module %q route %s %q name %q duplicates module %q route %s %q",
			c.profile,
			module,
			method,
			pattern,
			name,
			previous.module,
			previous.method,
			previous.pattern,
		)
	}
	c.names[name] = registration{
		module:  module,
		method:  method,
		pattern: pattern,
		name:    name,
	}
	return nil
}

func (c *profileCompiler) mountConflict(
	pattern string,
) (registration, bool) {
	for mountPattern, mount := range c.mounts {
		if pathWithinMount(pattern, mountPattern) {
			return mount, true
		}
	}
	return registration{}, false
}

func (c *profileCompiler) anyMountConflict(
	pattern string,
) (registration, bool) {
	for mountPattern, mount := range c.mounts {
		if pathWithinMount(pattern, mountPattern) ||
			pathWithinMount(mountPattern, pattern) {
			return mount, true
		}
	}
	for _, route := range c.routes {
		if pathWithinMount(route.shape, pattern) {
			return route, true
		}
	}
	return registration{}, false
}

func pathWithinMount(path string, mount string) bool {
	mount = strings.TrimSuffix(mount, "/")
	return path == mount || strings.HasPrefix(path, mount+"/")
}

func joinRoutePattern(prefix string, pattern string) (string, error) {
	if pattern == "" || !strings.HasPrefix(pattern, "/") {
		return "", fmt.Errorf("invalid route pattern %q", pattern)
	}
	if prefix == "" || prefix == "/" {
		return pattern, nil
	}
	prefix = strings.TrimSuffix(prefix, "/")
	if pattern == "/" {
		return prefix + "/", nil
	}
	return prefix + pattern, nil
}

func wrapMiddleware(
	handler http.Handler,
	middleware []ownedMiddleware,
) (http.Handler, error) {
	if isNilHTTPValue(handler) {
		return nil, errors.New("handler is nil")
	}
	result := handler
	for index := len(middleware) - 1; index >= 0; index-- {
		result = middleware[index].definition.Middleware(result)
		if isNilHTTPValue(result) {
			return nil, fmt.Errorf(
				"middleware %q returned nil handler",
				middleware[index].definition.Code,
			)
		}
	}
	return result, nil
}

func registerChi(register func()) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("chi registration panic: %v", recovered)
		}
	}()
	register()
	return nil
}

func isNilHTTPValue(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map,
		reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

type compiledResourceHandlers struct {
	handlers map[resourcetype.Code]http.Handler
	owners   map[resourcetype.Code]kernel.ModuleCode
}

func (r *compiledResourceHandlers) Handler(
	code httptransport.ResourceHandlerCode,
) (http.Handler, bool) {
	if r == nil {
		return nil, false
	}
	handler, exists := r.handlers[resourcetype.Code(code)]
	return handler, exists
}

var _ httptransport.Registrar = (*scopedRegistrar)(nil)
var _ httptransport.ResourceHandlers = (*compiledResourceHandlers)(nil)

// Platform namespaces are handled before profile dispatch. Registering a
// literal route below them would succeed in chi but never reach the module.
func validatePlatformRoute(pattern string) error {
	for _, prefix := range []string{"/api", "/_cms"} {
		if pattern == prefix || strings.HasPrefix(pattern, prefix+"/") {
			return fmt.Errorf("path is reserved by platform mount %q", prefix)
		}
	}
	return nil
}
