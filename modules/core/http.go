package core

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/cache"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
	"github.com/vernal96/go-cms-kernel/modules/resourceextension"
	"github.com/vernal96/go-cms-kernel/security"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

func (r *Runtime) HTTP() httptransport.Builder {
	return httptransport.BuilderFunc(func(
		context.Context,
	) (httptransport.Contribution, error) {
		if r == nil {
			return httptransport.Contribution{}, errors.New(
				"core runtime is nil",
			)
		}

		resolverOptions := make([]resource.RouteResolverOption, 0, 1)
		if r.resourcePreview != nil {
			resolverOptions = append(
				resolverOptions,
				resource.WithPreviewPolicy(r.resourcePreview),
			)
		}
		resolver, err := resource.NewRouteResolver(
			r.database.Resources(),
			r.authorization,
			resolverOptions...,
		)
		if err != nil {
			return httptransport.Contribution{}, err
		}

		libraryRepository, ok := r.database.Resources().(resource.LibraryItemRepository)
		if !ok {
			return httptransport.Contribution{}, errors.New("resource library item repository is unavailable")
		}
		libraryItems, err := resource.NewLibraryService(libraryRepository, r.services.Resources)
		if err != nil {
			return httptransport.Contribution{}, err
		}
		generation := rand.Text()
		return httptransport.Contribution{
			Routes: func(registrar httptransport.Registrar) error {
				if err := registrar.Route(httptransport.Route{Name: "core.menu", Method: http.MethodGet, Pattern: "/menu", Handler: http.HandlerFunc(r.serveMenu)}); err != nil {
					return err
				}
				return registrar.Route(httptransport.Route{Name: "core.site", Method: http.MethodGet, Pattern: "/site", Handler: http.HandlerFunc(r.serveSite)})
			},
			ResourceHandlers: []httptransport.ResourceHandler{
				{
					Type:    httptransport.ResourceHandlerCode(resourcetype.Page),
					Handler: pageResourceHandler{logger: r.logger, files: r.Files(), resultStore: r.resultStore, generation: generation},
				},
				{
					Type:    httptransport.ResourceHandlerCode(resourcetype.Library),
					Handler: pageResourceHandler{logger: r.logger, files: r.Files(), resultStore: r.resultStore, generation: generation},
				},
				{
					Type:    httptransport.ResourceHandlerCode(resourcetype.Link),
					Handler: externalLinkHandler{},
				},
				{
					Type: httptransport.ResourceHandlerCode(resourcetype.ResourceLink),
					Handler: resourceLinkHandler{
						resolver: resolver,
					},
				},
			},
			TerminalResource: &httptransport.TerminalResourceHandler{
				Factory: func(
					handlers httptransport.ResourceHandlers,
				) (http.Handler, error) {
					if handlers == nil {
						return nil, errors.New(
							"core resource handlers are nil",
						)
					}
					return &terminalResourceHandler{
						resolver: resolver, handlers: handlers,
						libraryItems: libraryItems,
					}, nil
				},
			},
		}, nil
	})
}

type terminalResourceHandler struct {
	resolver     *resource.RouteResolver
	handlers     httptransport.ResourceHandlers
	libraryItems *resource.LibraryService
}

func (h *terminalResourceHandler) ServeHTTP(
	response http.ResponseWriter,
	request *http.Request,
) {
	siteRuntime, siteExists := SiteRuntimeFromContext(
		request.Context(),
	)
	actor, actorExists := httptransport.ActorFromContext(request.Context())
	if !siteExists || !actorExists {
		http.Error(
			response,
			"resource request context is incomplete",
			http.StatusInternalServerError,
		)
		return
	}

	path, err := resource.NormalizeLookupPath(request.URL.Path)
	if err != nil {
		http.NotFound(response, request)
		return
	}

	item, err := h.resolver.ResolvePublishedByPath(
		request.Context(),
		actor,
		siteRuntime,
		path,
		resource.ResolveRouteOptions{
			Preview: httptransport.PreviewFromContext(request.Context()),
		},
	)
	if errors.Is(err, resource.ErrNotFound) && h.libraryItems != nil {
		libraryItem, _, itemErr := h.libraryItems.ResolvePublished(request.Context(), actor, site.ID(siteRuntime.Site().ID), path)
		if itemErr == nil {
			itemPath := path
			item = resource.Resource{ID: libraryItem.ID, SiteID: libraryItem.SiteID, Type: resourcetype.Page, Template: libraryItem.Template, ContentType: libraryItem.ContentType, Title: libraryItem.Title, Slug: libraryItem.Slug, Path: &itemPath, Annotation: libraryItem.Annotation, Content: libraryItem.Content, ImageMediaID: libraryItem.ImageMediaID, IsPublic: libraryItem.IsPublic, IsSearchable: libraryItem.IsSearchable, PublishedAt: libraryItem.PublishedAt, UnpublishedAt: libraryItem.UnpublishedAt, Fields: libraryItem.Fields, Widgets: libraryItem.Widgets, CreatedAt: libraryItem.CreatedAt, UpdatedAt: libraryItem.UpdatedAt}
			err = nil
		} else {
			err = itemErr
		}
	}
	if err != nil {
		writeResourceRouteError(response, request, err)
		return
	}

	handler, exists := h.handlers.Handler(
		httptransport.ResourceHandlerCode(item.Type),
	)
	if !exists {
		http.NotFound(response, request)
		return
	}

	handler.ServeHTTP(
		response,
		request.WithContext(
			WithResource(request.Context(), item),
		),
	)
}

const (
	widgetUnavailableError = "widget_unavailable"
	invalidParamsError     = "invalid_params"
	instanceFailedError    = "instance_failed"
	renderFailedError      = "render_failed"
	invalidResultError     = "invalid_result"
)

type pageResourceResponse struct {
	Resource   pageResourcePayload `json:"resource"`
	Widgets    pageWidgetsResponse `json:"widgets"`
	Extensions map[string]any      `json:"extensions,omitempty"`
}

type pageWidgetsResponse struct {
	Body    []pageWidgetResponse `json:"body"`
	Sidebar []pageWidgetResponse `json:"sidebar"`
}

type pageResourcePayload struct {
	ID          resource.ID       `json:"id"`
	Type        resourcetype.Code `json:"type"`
	Template    *template.Code    `json:"template"`
	Title       string            `json:"title"`
	Path        *string           `json:"path"`
	Annotation  string            `json:"annotation"`
	ContentType *string           `json:"content_type"`
}

type pageWidgetResponse struct {
	Key          string           `json:"key"`
	Code         widget.Code      `json:"code"`
	View         widget.ViewCode  `json:"view"`
	Columns      int              `json:"columns"`
	MarginTop    int              `json:"margin_top"`
	MarginBottom int              `json:"margin_bottom"`
	Data         json.RawMessage  `json:"data,omitempty"`
	Error        *pageWidgetError `json:"error,omitempty"`
}

type pageWidgetError struct {
	Code string `json:"code"`
}

type pageResourceHandler struct {
	resultStore cache.Store
	generation  string
	logger      *slog.Logger
	files       file.Service
}

func (h pageResourceHandler) ServeHTTP(
	response http.ResponseWriter,
	request *http.Request,
) {
	ctx := request.Context()
	item, resourceExists := ResourceFromContext(ctx)
	siteRuntime, siteExists := SiteRuntimeFromContext(ctx)
	if !resourceExists || !siteExists {
		http.Error(
			response,
			"resource request context is incomplete",
			http.StatusInternalServerError,
		)
		return
	}

	result := pageResourceResponse{
		Resource: pageResourcePayload{
			ID:          item.ID,
			Type:        item.Type,
			Template:    item.Template,
			Title:       item.Title,
			Path:        item.Path,
			Annotation:  item.Annotation,
			ContentType: item.ContentType,
		},
		Widgets: pageWidgetsResponse{
			Body: []pageWidgetResponse{}, Sidebar: []pageWidgetResponse{},
		},
	}
	if item.Template != nil {
		templateRuntime, exists := siteRuntime.Profile().Template(*item.Template)
		if !exists {
			http.Error(response, "resource template is unavailable", http.StatusInternalServerError)
			return
		}
		placements, err := template.Compose(templateRuntime, item.Widgets)
		if err != nil {
			h.logError(ctx, "resource widget composition failed", item, widget.Binding{}, err)
			http.Error(response, "resource response failed", http.StatusInternalServerError)
			return
		}
		result.Widgets.Body = h.renderWidgets(ctx, siteRuntime, item, placements.Body)
		if ctx.Err() != nil {
			return
		}
		result.Widgets.Sidebar = h.renderWidgets(ctx, siteRuntime, item, placements.Sidebar)
	}

	result.Extensions = h.publicExtensions(
		ctx,
		siteRuntime.Site(),
		siteRuntime.Profile().Modules(),
		item,
		httptransport.PreviewFromContext(ctx),
	)

	raw, err := json.Marshal(result)
	if err != nil {
		h.logError(ctx, "resource response encoding failed", item, widget.Binding{}, err)
		http.Error(response, "resource response failed", http.StatusInternalServerError)
		return
	}

	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(http.StatusOK)
	_, _ = response.Write(append(raw, '\n'))
}

func (h pageResourceHandler) renderWidgets(ctx context.Context, siteRuntime *site.Runtime, item resource.Resource, placements []widget.Placement) []pageWidgetResponse {
	result := make([]pageWidgetResponse, 0, len(placements))
	jobs := make([]widget.RenderJob, 0, len(placements))
	indexes := make([]int, 0, len(placements))
	bindings := make([]widget.Binding, 0, len(placements))
	templateRuntime, _ := siteRuntime.Profile().Template(*item.Template)
	for _, placement := range placements {
		if ctx.Err() != nil {
			return result
		}
		if !placement.Presentation.Enabled {
			continue
		}
		binding := widget.Binding{ID: placement.BindingID, Code: placement.Code, Area: placement.Area, Position: placement.Position, Presentation: placement.Presentation, Params: placement.Params}
		rendered := pageWidgetResponse{Key: placement.Key, Code: placement.Code, View: widget.PublicView(placement.Presentation.View), Columns: placement.Presentation.Columns, MarginTop: placement.Presentation.MarginTop, MarginBottom: placement.Presentation.MarginBottom}
		runtime, exists := siteRuntime.Profile().Widget(placement.Code)
		if !exists {
			rendered.Error = h.widgetError(ctx, item, binding, widgetUnavailableError, fmt.Errorf("widget %q is unavailable", binding.Code))
			result = append(result, rendered)
			continue
		}
		instance, err := h.newWidgetInstance(ctx, runtime, placement, templateRuntime.FieldSchema(), item.WidgetValues())
		if err != nil {
			code := instanceFailedError
			if errors.Is(err, widget.ErrInvalidParams) {
				code = invalidParamsError
			}
			rendered.Error = h.widgetError(ctx, item, binding, code, err)
			result = append(result, rendered)
			continue
		}
		identity := fmt.Sprintf("%d:%d:%s:%d", item.SiteID, item.ID, placement.Code, placement.BindingID)
		if placement.BindingID == 0 {
			identity += ":" + placement.Key
		}
		jobs = append(jobs, widget.RenderJob{Identity: identity, Instance: instance,
			Fingerprint: struct {
				Params   map[string]any
				Bindings widget.ParamBindings
				Values   widget.ResourceValues
			}{placement.Params, placement.ParamBindings, item.WidgetValues()},
			Input: widget.RenderInput{Site: widget.SiteSnapshot{ID: int64(item.SiteID), Domain: siteRuntime.Site().Domain, Locale: siteRuntime.Site().Locale}, Resource: widget.ResourceSnapshot{ID: int64(item.ID), Title: item.Title, Content: item.Content}},
		})
		indexes = append(indexes, len(result))
		bindings = append(bindings, binding)
		result = append(result, rendered)
	}
	store := h.resultStore
	if httptransport.PreviewFromContext(ctx) {
		store = nil
	}
	outputs := widget.RenderBatch(ctx, store, h.generation, jobs)
	for index, output := range outputs {
		rendered := &result[indexes[index]]
		binding := bindings[index]
		if output.Err != nil {
			rendered.Error = h.widgetError(ctx, item, binding, renderFailedError, output.Err)
			continue
		}
		if output.Data == nil {
			rendered.Error = h.widgetError(ctx, item, binding, invalidResultError, errors.New("widget returned nil data"))
			continue
		}
		raw, err := json.Marshal(output.Data)
		if err != nil {
			rendered.Error = h.widgetError(ctx, item, binding, invalidResultError, err)
			continue
		}
		rendered.Data = raw
	}
	return result
}

func (h pageResourceHandler) publicExtensions(
	ctx context.Context,
	siteItem site.Site,
	modules []kernel.ModuleRuntime,
	item resource.Resource,
	preview bool,
) map[string]any {
	result := make(map[string]any)
	for _, moduleRuntime := range modules {
		provider, ok := moduleRuntime.(resourceextension.PublicProvider)
		if !ok {
			continue
		}
		extension, err := provider.PublicResourceExtension(
			ctx,
			resourceextension.PublicRequest{
				Site: siteItem, Resource: item, Preview: preview,
			},
		)
		if err != nil {
			h.logExtensionError(ctx, item, moduleRuntime.ModuleCode(), err)
			continue
		}
		if extension.Code == "" {
			continue
		}
		code := string(extension.Code)
		if extension.Data == nil {
			h.logExtensionError(
				ctx,
				item,
				moduleRuntime.ModuleCode(),
				errors.New("public resource extension returned nil data"),
			)
			continue
		}
		if _, exists := result[code]; exists {
			h.logExtensionError(
				ctx,
				item,
				moduleRuntime.ModuleCode(),
				fmt.Errorf("public resource extension %q is duplicated", code),
			)
			continue
		}
		result[code] = extension.Data
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func (h pageResourceHandler) logExtensionError(
	ctx context.Context,
	item resource.Resource,
	moduleCode kernel.ModuleCode,
	err error,
) {
	if h.logger == nil {
		return
	}
	h.logger.ErrorContext(
		ctx,
		"public resource extension failed",
		slog.String("event", "resource.extension.failed"),
		slog.Int64("resource.id", int64(item.ID)),
		slog.String("module.code", string(moduleCode)),
		slog.Any("error", err),
	)
}

func (h pageResourceHandler) widgetError(
	ctx context.Context,
	item resource.Resource,
	binding widget.Binding,
	code string,
	err error,
) *pageWidgetError {
	h.logError(ctx, "resource widget failed", item, binding, err)
	return &pageWidgetError{Code: code}
}

func (h pageResourceHandler) logError(
	ctx context.Context,
	message string,
	item resource.Resource,
	binding widget.Binding,
	err error,
) {
	if h.logger == nil {
		return
	}

	attributes := []any{
		slog.String("event", "resource.widget.failed"),
		slog.Int64("site.id", int64(item.SiteID)),
		slog.Int64("resource.id", int64(item.ID)),
		slog.String("widget.code", string(binding.Code)),
		slog.Int64("widget.id", int64(binding.ID)),
		slog.String("widget.area", string(binding.Area)),
		slog.Int("widget.position", binding.Position),
		slog.Any("error", err),
	}
	h.logger.ErrorContext(ctx, message, attributes...)
}

type externalLinkHandler struct{}

func (externalLinkHandler) ServeHTTP(
	response http.ResponseWriter,
	request *http.Request,
) {
	item, exists := ResourceFromContext(request.Context())
	if !exists || item.ExternalURL == nil {
		http.NotFound(response, request)
		return
	}
	http.Redirect(
		response,
		request,
		*item.ExternalURL,
		http.StatusFound,
	)
}

type resourceLinkHandler struct {
	resolver *resource.RouteResolver
}

func (h resourceLinkHandler) ServeHTTP(
	response http.ResponseWriter,
	request *http.Request,
) {
	item, resourceExists := ResourceFromContext(
		request.Context(),
	)
	siteRuntime, siteExists := SiteRuntimeFromContext(
		request.Context(),
	)
	actor, actorExists := httptransport.ActorFromContext(request.Context())
	if !resourceExists ||
		!siteExists ||
		!actorExists ||
		item.TargetResourceID == nil {
		http.NotFound(response, request)
		return
	}

	target, err := h.resolver.ResolvePublishedByID(
		request.Context(),
		actor,
		siteRuntime,
		*item.TargetResourceID,
		resource.ResolveRouteOptions{
			Preview: httptransport.PreviewFromContext(request.Context()),
		},
	)
	if err != nil {
		writeResourceRouteError(response, request, err)
		return
	}
	if target.Path == nil {
		http.NotFound(response, request)
		return
	}
	http.Redirect(
		response,
		request,
		*target.Path,
		http.StatusFound,
	)
}

func writeResourceRouteError(
	response http.ResponseWriter,
	request *http.Request,
	err error,
) {
	switch {
	case errors.Is(err, resource.ErrNotFound),
		errors.Is(err, security.ErrForbidden),
		errors.Is(err, security.ErrUnauthenticated):
		http.NotFound(response, request)
	default:
		http.Error(
			response,
			"resource route failed",
			http.StatusInternalServerError,
		)
	}
}

var _ httptransport.Provider = (*Runtime)(nil)

// Bound file values inherit the target field's disk/MIME restrictions, including
// references nested in repeaters. The source resource has already been authorized.
func (h pageResourceHandler) newWidgetInstance(ctx context.Context, runtime *widget.Runtime, placement widget.Placement, schema *field.Schema, values widget.ResourceValues) (widget.Instance, error) {
	params, err := runtime.ResolveParams(placement.Params, placement.ParamBindings, schema, values)
	if err != nil {
		return nil, err
	}
	bound := make(map[string]any, len(placement.ParamBindings))
	for key := range placement.ParamBindings {
		if value, exists := params[key]; exists {
			bound[key] = value
		}
	}
	refs, err := runtime.FieldSchema().FileReferences(bound)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", widget.ErrInvalidParams, err)
	}
	for _, ref := range refs {
		if h.files == nil {
			return nil, fmt.Errorf("%w: file service is unavailable", widget.ErrInvalidParams)
		}
		item, err := h.files.GetFile(ctx, security.System(), file.ID(ref.ID))
		if err != nil {
			return nil, fmt.Errorf("%w: file parameter %q: %v", widget.ErrInvalidParams, ref.Key, err)
		}
		if !field.FileMatches(ref.Options, item.Storage, item.MIMEType) {
			return nil, fmt.Errorf("%w: file parameter %q rejects selected file", widget.ErrInvalidParams, ref.Key)
		}
	}
	return runtime.New(params)
}
