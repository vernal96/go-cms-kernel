package httpserver_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/eventbus"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
	httpserver "github.com/vernal96/go-cms-kernel/transport/httpserver"
)

type compilerResolver struct{}

type compilerEventBus struct{}

func (compilerEventBus) Publish(context.Context, eventbus.Message) error {
	return nil
}

func (compilerEventBus) Consume(
	context.Context,
	eventbus.Subscription,
	eventbus.Handler,
) error {
	return nil
}

func (compilerResolver) MainModuleDatabase(
	kernel.ModuleCode,
) (kernel.ModuleDatabase, bool) {
	return nil, false
}

func (compilerResolver) ModuleDatabase(
	kernel.ConnectionCode,
	kernel.ModuleCode,
) (kernel.ModuleDatabase, bool) {
	return nil, false
}

type compilerModule struct {
	code         kernel.ModuleCode
	registry     kernel.ModuleRegistry
	contribution httptransport.Contribution
	buildErr     error
}

func (m compilerModule) Code() kernel.ModuleCode {
	return m.code
}

func (m compilerModule) Registry() kernel.ModuleRegistry {
	return m.registry
}

func (m compilerModule) Build(
	context.Context,
	kernel.ModuleContext,
) (kernel.ModuleRuntime, error) {
	return &compilerRuntime{
		code:         m.code,
		contribution: m.contribution,
		buildErr:     m.buildErr,
	}, nil
}

type compilerRuntime struct {
	code         kernel.ModuleCode
	contribution httptransport.Contribution
	buildErr     error
}

func (r *compilerRuntime) ModuleCode() kernel.ModuleCode {
	return r.code
}

func (r *compilerRuntime) HTTP() httptransport.Builder {
	return httptransport.BuilderFunc(func(
		context.Context,
	) (httptransport.Contribution, error) {
		if r.buildErr != nil {
			return httptransport.Contribution{}, r.buildErr
		}
		return r.contribution, nil
	})
}

type compilerResourceType struct {
	code resourcetype.Code
}

func (t compilerResourceType) Code() resourcetype.Code {
	return t.code
}

func (compilerResourceType) PathMode() resourcetype.PathMode {
	return resourcetype.PathRoute
}

func (compilerResourceType) Metadata() resourcetype.Metadata {
	return resourcetype.Metadata{Label: "Compiler", Capabilities: resourcetype.Capabilities{MutableType: true}, SettingsDefaults: map[string]any{}}
}

func (compilerResourceType) Normalize(
	payload resourcetype.Payload,
) (resourcetype.Payload, error) {
	return payload, nil
}

func makeCompilerProfile(
	t *testing.T,
	code kernel.ProfileCode,
	modules ...compilerModule,
) *kernel.ProfileRuntime {
	t.Helper()
	factory, err := kernel.NewProfileRuntimeFactory(
		compilerResolver{},
		kernel.RuntimeServices{
			EventBus: compilerEventBus{},
			Logger:   slog.New(slog.NewJSONHandler(io.Discard, nil)),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	profileModules := make([]kernel.ProfileModule, len(modules))
	for index := range modules {
		profileModules[index] = kernel.ProfileModule{
			Module: modules[index],
		}
	}
	blueprint, err := factory.Compile(context.Background(), kernel.Profile{
		Code:    code,
		Modules: profileModules,
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := blueprint.Build(
		context.Background(),
		kernel.NewRuntimeScope("compiler-test", "", "", nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func compileProfileForTest(
	t *testing.T,
	runtime *kernel.ProfileRuntime,
) http.Handler {
	t.Helper()
	handler, err := httpserver.CompileProfile(
		context.Background(),
		runtime,
	)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func serveProfile(
	handler http.Handler,
	method string,
	path string,
) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func routeContribution(
	method string,
	pattern string,
	handler http.Handler,
) httptransport.Contribution {
	return httptransport.Contribution{
		Routes: func(registrar httptransport.Registrar) error {
			return registrar.Route(httptransport.Route{
				Method:  method,
				Pattern: pattern,
				Handler: handler,
			})
		},
	}
}

func terminalContribution(
	handler http.Handler,
	count *int,
) httptransport.Contribution {
	return httptransport.Contribution{
		TerminalResource: &httptransport.TerminalResourceHandler{
			Factory: func(
				httptransport.ResourceHandlers,
			) (http.Handler, error) {
				return http.HandlerFunc(func(
					response http.ResponseWriter,
					request *http.Request,
				) {
					if count != nil {
						*count++
					}
					handler.ServeHTTP(response, request)
				}), nil
			},
		},
	}
}

func TestProfileRouteExistsOnlyWhenModuleIsPresent(t *testing.T) {
	module := compilerModule{
		code: "forms",
		contribution: routeContribution(
			http.MethodGet,
			"/forms/{code}",
			http.HandlerFunc(func(
				response http.ResponseWriter,
				_ *http.Request,
			) {
				response.WriteHeader(http.StatusNoContent)
			}),
		),
	}
	withModule := compileProfileForTest(
		t,
		makeCompilerProfile(t, "with-forms", module),
	)
	withoutModule := compileProfileForTest(
		t,
		makeCompilerProfile(t, "without-forms"),
	)

	if response := serveProfile(
		withModule,
		http.MethodGet,
		"/forms/contact",
	); response.Code != http.StatusNoContent {
		t.Fatalf("route status = %d", response.Code)
	}
	if response := serveProfile(
		withoutModule,
		http.MethodGet,
		"/forms/contact",
	); response.Code != http.StatusNotFound {
		t.Fatalf("isolated profile status = %d", response.Code)
	}
}

func TestModuleMiddlewareIsScopedToOwningModuleRoutes(t *testing.T) {
	middleware := httptransport.MiddlewareDefinition{
		Code:  "first.module",
		Scope: httptransport.MiddlewareModule,
		Middleware: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(
				response http.ResponseWriter,
				request *http.Request,
			) {
				response.Header().Set("X-First-Module", "true")
				next.ServeHTTP(response, request)
			})
		},
	}
	first := compilerModule{
		code: "first",
		contribution: httptransport.Contribution{
			Middleware: []httptransport.MiddlewareDefinition{middleware},
			Routes: routeContribution(
				http.MethodGet,
				"/first",
				http.HandlerFunc(func(
					response http.ResponseWriter,
					_ *http.Request,
				) {
					response.WriteHeader(http.StatusNoContent)
				}),
			).Routes,
		},
	}
	second := compilerModule{
		code: "second",
		contribution: routeContribution(
			http.MethodGet,
			"/second",
			http.HandlerFunc(func(
				response http.ResponseWriter,
				_ *http.Request,
			) {
				response.WriteHeader(http.StatusNoContent)
			}),
		),
	}
	handler := compileProfileForTest(
		t,
		makeCompilerProfile(t, "scoped", first, second),
	)

	firstResponse := serveProfile(handler, http.MethodGet, "/first")
	if firstResponse.Header().Get("X-First-Module") != "true" {
		t.Fatal("owning module middleware did not run")
	}
	secondResponse := serveProfile(handler, http.MethodGet, "/second")
	if secondResponse.Header().Get("X-First-Module") != "" {
		t.Fatal("module middleware leaked to another module")
	}
}

func TestRouteGroupPrefixParametersAndMiddleware(t *testing.T) {
	module := compilerModule{
		code: "forms",
		contribution: httptransport.Contribution{
			Middleware: []httptransport.MiddlewareDefinition{{
				Code:  "forms.group",
				Scope: httptransport.MiddlewareLocal,
				Middleware: func(next http.Handler) http.Handler {
					return http.HandlerFunc(func(
						response http.ResponseWriter,
						request *http.Request,
					) {
						response.Header().Set("X-Forms-Group", "true")
						next.ServeHTTP(response, request)
					})
				},
			}},
			Routes: func(registrar httptransport.Registrar) error {
				return registrar.Group(
					"/forms",
					[]httptransport.MiddlewareCode{"forms.group"},
					func(group httptransport.Registrar) error {
						return group.Route(httptransport.Route{
							Method:  http.MethodGet,
							Pattern: "/{code}",
							Handler: http.HandlerFunc(func(
								response http.ResponseWriter,
								_ *http.Request,
							) {
								response.WriteHeader(http.StatusNoContent)
							}),
						})
					},
				)
			},
		},
	}
	handler := compileProfileForTest(
		t,
		makeCompilerProfile(t, "groups", module),
	)
	response := serveProfile(
		handler,
		http.MethodGet,
		"/forms/contact",
	)
	if response.Code != http.StatusNoContent ||
		response.Header().Get("X-Forms-Group") != "true" {
		t.Fatalf(
			"status = %d, header = %q",
			response.Code,
			response.Header().Get("X-Forms-Group"),
		)
	}
}

func TestCustomRouteWinsOverTerminalResourceFallback(t *testing.T) {
	fallbackCalls := 0
	handler := compileProfileForTest(
		t,
		makeCompilerProfile(
			t,
			"priority",
			compilerModule{
				code: "resource",
				contribution: terminalContribution(
					http.HandlerFunc(func(
						response http.ResponseWriter,
						_ *http.Request,
					) {
						_, _ = response.Write([]byte("resource"))
					}),
					&fallbackCalls,
				),
			},
			compilerModule{
				code: "custom",
				contribution: routeContribution(
					http.MethodGet,
					"/same",
					http.HandlerFunc(func(
						response http.ResponseWriter,
						_ *http.Request,
					) {
						_, _ = response.Write([]byte("custom"))
					}),
				),
			},
		),
	)

	response := serveProfile(handler, http.MethodGet, "/same")
	if response.Body.String() != "custom" || fallbackCalls != 0 {
		t.Fatalf(
			"response = %q, fallback calls = %d",
			response.Body.String(),
			fallbackCalls,
		)
	}
}

func TestUnknownCustomPathReachesTerminalFallback(t *testing.T) {
	fallbackCalls := 0
	handler := compileProfileForTest(
		t,
		makeCompilerProfile(
			t,
			"fallback",
			compilerModule{
				code: "custom",
				contribution: routeContribution(
					http.MethodGet,
					"/known",
					http.NotFoundHandler(),
				),
			},
			compilerModule{
				code: "resource",
				contribution: terminalContribution(
					http.HandlerFunc(func(
						response http.ResponseWriter,
						_ *http.Request,
					) {
						response.WriteHeader(http.StatusAccepted)
					}),
					&fallbackCalls,
				),
			},
		),
	)

	response := serveProfile(handler, http.MethodGet, "/unknown")
	if response.Code != http.StatusAccepted || fallbackCalls != 1 {
		t.Fatalf(
			"status = %d, fallback calls = %d",
			response.Code,
			fallbackCalls,
		)
	}
}

func TestCustomController404DoesNotRunResourceFallback(t *testing.T) {
	fallbackCalls := 0
	handler := compileProfileForTest(
		t,
		makeCompilerProfile(
			t,
			"controller-404",
			compilerModule{
				code: "custom",
				contribution: routeContribution(
					http.MethodGet,
					"/known",
					http.NotFoundHandler(),
				),
			},
			compilerModule{
				code: "resource",
				contribution: terminalContribution(
					http.NotFoundHandler(),
					&fallbackCalls,
				),
			},
		),
	)

	response := serveProfile(handler, http.MethodGet, "/known")
	if response.Code != http.StatusNotFound || fallbackCalls != 0 {
		t.Fatalf(
			"status = %d, fallback calls = %d",
			response.Code,
			fallbackCalls,
		)
	}
}

func TestCustomMethodMismatchReturns405WithoutFallback(t *testing.T) {
	fallbackCalls := 0
	handler := compileProfileForTest(
		t,
		makeCompilerProfile(
			t,
			"method",
			compilerModule{
				code: "custom",
				contribution: routeContribution(
					http.MethodGet,
					"/known",
					http.NotFoundHandler(),
				),
			},
			compilerModule{
				code: "resource",
				contribution: terminalContribution(
					http.NotFoundHandler(),
					&fallbackCalls,
				),
			},
		),
	)

	response := serveProfile(handler, http.MethodPost, "/known")
	if response.Code != http.StatusMethodNotAllowed ||
		!strings.Contains(response.Header().Get("Allow"), http.MethodGet) ||
		fallbackCalls != 0 {
		t.Fatalf(
			"status = %d, Allow = %q, fallback calls = %d",
			response.Code,
			response.Header().Get("Allow"),
			fallbackCalls,
		)
	}
}

func TestTerminalFallbackIsInstalledAfterEveryModuleRoute(t *testing.T) {
	fallbackCalls := 0
	handler := compileProfileForTest(
		t,
		makeCompilerProfile(
			t,
			"phases",
			compilerModule{
				code: "terminal-first",
				contribution: terminalContribution(
					http.NotFoundHandler(),
					&fallbackCalls,
				),
			},
			compilerModule{
				code: "route-last",
				contribution: routeContribution(
					http.MethodGet,
					"/late",
					http.HandlerFunc(func(
						response http.ResponseWriter,
						_ *http.Request,
					) {
						response.WriteHeader(http.StatusNoContent)
					}),
				),
			},
		),
	)

	response := serveProfile(handler, http.MethodGet, "/late")
	if response.Code != http.StatusNoContent || fallbackCalls != 0 {
		t.Fatalf(
			"status = %d, fallback calls = %d",
			response.Code,
			fallbackCalls,
		)
	}
}

func TestProfileRejectsMoreThanOneTerminalHandler(t *testing.T) {
	runtime := makeCompilerProfile(
		t,
		"duplicate-terminal",
		compilerModule{
			code: "first",
			contribution: terminalContribution(
				http.NotFoundHandler(),
				nil,
			),
		},
		compilerModule{
			code: "second",
			contribution: terminalContribution(
				http.NotFoundHandler(),
				nil,
			),
		},
	)
	_, err := httpserver.CompileProfile(context.Background(), runtime)
	if err == nil ||
		!strings.Contains(err.Error(), "duplicate-terminal") ||
		!strings.Contains(err.Error(), "second") {
		t.Fatalf("error = %v", err)
	}
}

func TestProfileRejectsConflictingRoutes(t *testing.T) {
	runtime := makeCompilerProfile(
		t,
		"route-conflict",
		compilerModule{
			code: "first",
			contribution: routeContribution(
				http.MethodPost,
				"/admin/resources",
				http.NotFoundHandler(),
			),
		},
		compilerModule{
			code: "second",
			contribution: routeContribution(
				http.MethodPost,
				"/admin/resources",
				http.NotFoundHandler(),
			),
		},
	)
	_, err := httpserver.CompileProfile(context.Background(), runtime)
	if err == nil ||
		!strings.Contains(err.Error(), "route-conflict") ||
		!strings.Contains(err.Error(), "second") ||
		!strings.Contains(err.Error(), http.MethodPost) ||
		!strings.Contains(err.Error(), "/admin/resources") {
		t.Fatalf("error = %v", err)
	}
}

func TestProfileRejectsDuplicateResourceHandler(t *testing.T) {
	resourceType := compilerResourceType{code: "article"}
	runtime := makeCompilerProfile(
		t,
		"resource-conflict",
		compilerModule{
			code: "first",
			registry: kernel.ModuleRegistry{
				ResourceTypes: []resourcetype.Type{resourceType},
			},
			contribution: httptransport.Contribution{
				ResourceHandlers: []httptransport.ResourceHandler{{
					Type: httptransport.ResourceHandlerCode(
						resourceType.Code(),
					),
					Handler: http.NotFoundHandler(),
				}},
			},
		},
		compilerModule{
			code: "second",
			contribution: httptransport.Contribution{
				ResourceHandlers: []httptransport.ResourceHandler{{
					Type: httptransport.ResourceHandlerCode(
						resourceType.Code(),
					),
					Handler: http.NotFoundHandler(),
				}},
			},
		},
	)
	_, err := httpserver.CompileProfile(context.Background(), runtime)
	if err == nil ||
		!strings.Contains(err.Error(), "resource-conflict") ||
		!strings.Contains(err.Error(), "article") {
		t.Fatalf("error = %v", err)
	}
}

func TestUnknownResourceEndsWith404(t *testing.T) {
	handler := compileProfileForTest(
		t,
		makeCompilerProfile(
			t,
			"final-404",
			compilerModule{
				code: "resource",
				contribution: terminalContribution(
					http.NotFoundHandler(),
					nil,
				),
			},
		),
	)
	response := serveProfile(handler, http.MethodGet, "/missing")
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d", response.Code)
	}
}

func TestCompilerValidatesMiddlewareNamesHandlersPatternsAndMounts(
	t *testing.T,
) {
	tests := []struct {
		name    string
		modules []compilerModule
		want    string
	}{
		{
			name: "unknown middleware",
			modules: []compilerModule{{
				code: "routes",
				contribution: httptransport.Contribution{
					Routes: func(registrar httptransport.Registrar) error {
						return registrar.Route(httptransport.Route{
							Method:     http.MethodGet,
							Pattern:    "/known",
							Handler:    http.NotFoundHandler(),
							Middleware: []httptransport.MiddlewareCode{"missing"},
						})
					},
				},
			}},
			want: "unknown middleware",
		},
		{
			name: "duplicate middleware code",
			modules: []compilerModule{
				{
					code: "first",
					contribution: httptransport.Contribution{
						Middleware: []httptransport.MiddlewareDefinition{{
							Code:       "same",
							Scope:      httptransport.MiddlewareLocal,
							Middleware: identityMiddleware,
						}},
					},
				},
				{
					code: "second",
					contribution: httptransport.Contribution{
						Middleware: []httptransport.MiddlewareDefinition{{
							Code:       "same",
							Scope:      httptransport.MiddlewareLocal,
							Middleware: identityMiddleware,
						}},
					},
				},
			},
			want: "duplicates module",
		},
		{
			name: "nil handler",
			modules: []compilerModule{{
				code: "routes",
				contribution: httptransport.Contribution{
					Routes: func(registrar httptransport.Registrar) error {
						return registrar.Route(httptransport.Route{
							Method:  http.MethodGet,
							Pattern: "/known",
						})
					},
				},
			}},
			want: "handler/controller is nil",
		},
		{
			name: "duplicate route name",
			modules: []compilerModule{{
				code: "routes",
				contribution: httptransport.Contribution{
					Routes: func(registrar httptransport.Registrar) error {
						if err := registrar.Route(httptransport.Route{
							Name:    "same",
							Method:  http.MethodGet,
							Pattern: "/first",
							Handler: http.NotFoundHandler(),
						}); err != nil {
							return err
						}
						return registrar.Route(httptransport.Route{
							Name:    "same",
							Method:  http.MethodGet,
							Pattern: "/second",
							Handler: http.NotFoundHandler(),
						})
					},
				},
			}},
			want: "name \"same\" duplicates",
		},
		{
			name: "invalid chi pattern",
			modules: []compilerModule{{
				code: "routes",
				contribution: routeContribution(
					http.MethodGet,
					"/broken/{id",
					http.NotFoundHandler(),
				),
			}},
			want: "closing delimiter",
		},
		{
			name: "mount conflict",
			modules: []compilerModule{{
				code: "routes",
				contribution: httptransport.Contribution{
					Routes: func(registrar httptransport.Registrar) error {
						if err := registrar.Mount(httptransport.Mount{
							Pattern: "/admin",
							Handler: http.NotFoundHandler(),
						}); err != nil {
							return err
						}
						return registrar.Route(httptransport.Route{
							Method:  http.MethodGet,
							Pattern: "/admin/resources",
							Handler: http.NotFoundHandler(),
						})
					},
				},
			}},
			want: "conflicts with",
		},
		{
			name: "missing routable handler",
			modules: []compilerModule{{
				code: "articles",
				registry: kernel.ModuleRegistry{
					ResourceTypes: []resourcetype.Type{
						compilerResourceType{code: "article"},
					},
				},
			}},
			want: "has no HTTP handler",
		},
		{
			name: "controller build error",
			modules: []compilerModule{{
				code:     "broken",
				buildErr: errors.New("dependency unavailable"),
			}},
			want: "dependency unavailable",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := makeCompilerProfile(
				t,
				"validation",
				test.modules...,
			)
			_, err := httpserver.CompileProfile(
				context.Background(),
				runtime,
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func identityMiddleware(next http.Handler) http.Handler {
	return next
}

func TestProfileRejectsEquivalentRouteShapes(t *testing.T) {
	for _, patterns := range [][2]string{
		{"/items/{id}", "/items/{slug}"},
		{"/items/{id:[0-9]{2}}", "/items/{slug:^[0-9]{2}$}"},
		{"/{site}/items/{id}", "/{domain}/items/{slug}"},
	} {
		t.Run(patterns[0], func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
			runtime := makeCompilerProfile(t, "shapes",
				compilerModule{code: "alpha", contribution: routeContribution("GET", patterns[0], handler)},
				compilerModule{code: "beta", contribution: routeContribution("GET", patterns[1], handler)},
			)
			_, err := httpserver.CompileProfile(context.Background(), runtime)
			if err == nil || !strings.Contains(err.Error(), "alpha") || !strings.Contains(err.Error(), "beta") {
				t.Fatalf("collision error=%v", err)
			}
		})
	}
}

func TestProfileKeepsDistinctRoutes(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	runtime := makeCompilerProfile(t, "distinct",
		compilerModule{code: "one", contribution: routeContribution("GET", "/items/{id:[0-9]+}", handler)},
		compilerModule{code: "two", contribution: routeContribution("GET", "/items/{name:[a-z]+}", handler)},
		compilerModule{code: "three", contribution: routeContribution("GET", "/items/new", handler)},
		compilerModule{code: "four", contribution: routeContribution("POST", "/items/{id:[0-9]+}", handler)},
	)
	compiled := compileProfileForTest(t, runtime)
	for _, path := range []string{"/items/12", "/items/abc", "/items/new"} {
		if got := serveProfile(compiled, "GET", path); got.Code != 204 {
			t.Fatalf("%s: %d", path, got.Code)
		}
	}
}

func TestPlatformNamespacesRejectProfileRoutesAndMounts(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	for _, pattern := range []string{"/api", "/api/custom", "/_cms", "/_cms/runtime"} {
		for _, mount := range []bool{false, true} {
			contribution := httptransport.Contribution{Routes: func(r httptransport.Registrar) error {
				return r.Group("/", nil, func(r httptransport.Registrar) error {
					if mount {
						return r.Mount(httptransport.Mount{Pattern: pattern, Handler: handler})
					}
					return r.Route(httptransport.Route{Method: "GET", Pattern: pattern, Handler: handler})
				})
			}}
			runtime := makeCompilerProfile(t, "reserved", compilerModule{code: "extension", contribution: contribution})
			_, err := httpserver.CompileProfile(context.Background(), runtime)
			if err == nil || !strings.Contains(err.Error(), "reserved by platform") || !strings.Contains(err.Error(), "extension") {
				t.Fatalf("%s mount=%t: %v", pattern, mount, err)
			}
		}
	}
	runtime := makeCompilerProfile(t, "allowed", compilerModule{code: "extension", contribution: httptransport.Contribution{Routes: func(r httptransport.Registrar) error {
		for _, pattern := range []string{"/apiary", "/_cms-other", "/custom/api"} {
			if err := r.Route(httptransport.Route{Method: "GET", Pattern: pattern, Handler: handler}); err != nil {
				return err
			}
		}
		return nil
	}}})
	compileProfileForTest(t, runtime)
}
