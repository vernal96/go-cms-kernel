package seo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/resourceextension"
)

const ModuleCode kernel.ModuleCode = "seo"

type Database interface {
	kernel.ModuleDatabase
	ResourceMetadata() Repository
}

type Module struct{}

func (Module) Code() kernel.ModuleCode { return ModuleCode }

func (Module) Build(
	_ context.Context,
	ctx kernel.ModuleContext,
) (kernel.ModuleRuntime, error) {
	database, err := kernel.ModuleDatabaseFrom[Database](ctx, "", ModuleCode)
	if err != nil {
		return nil, err
	}
	if database.ResourceMetadata() == nil {
		return nil, errors.New("SEO metadata repository is nil")
	}
	config, err := kernel.ModuleConfigFrom[Config](ctx)
	if err != nil {
		return nil, err
	}
	normalized := normalizeConfig(config)
	renderer, err := NewRenderer(
		ctx.Profile(),
		normalized.maxTemplateLength,
		normalized.maxResultLength,
	)
	if err != nil {
		return nil, err
	}
	service, err := NewService(
		database.ResourceMetadata(),
		renderer,
		normalized.defaults,
	)
	if err != nil {
		return nil, fmt.Errorf("build SEO service: %w", err)
	}
	return &Runtime{service: service, renderer: renderer}, nil
}

type Runtime struct {
	service  *Service
	renderer *Renderer
}

func (*Runtime) ModuleCode() kernel.ModuleCode { return ModuleCode }

func (r *Runtime) ResourceEditorExtension() resourceextension.Editor {
	return r
}

func (r *Runtime) UsedByResources(
	ctx context.Context,
	siteID site.ID,
	resourceIDs []resource.ID,
) (bool, error) {
	return r.service.UsedByResources(ctx, siteID, resourceIDs)
}

func (r *Runtime) Metadata() resourceextension.Metadata {
	variables := r.renderer.Variables()
	variableMetadata := make([]resourceextension.Variable, len(variables))
	for index, variable := range variables {
		variableMetadata[index] = resourceextension.Variable{
			Code:  "{{ " + variable + " }}",
			Label: variable,
		}
	}
	return resourceextension.Metadata{
		Code:      "seo",
		Title:     "SEO",
		AppliesTo: []resourcetype.Code{resourcetype.Page, resourcetype.Library},
		Fields: []resourceextension.Field{
			{Key: "title_template", Label: "Title", Control: "text"},
			{Key: "description_template", Label: "Description", Control: "textarea", Rows: 3},
			{Key: "keywords_template", Label: "Keywords", Control: "text"},
			{Key: "canonical_template", Label: "Canonical URL", Control: "text"},
			{Key: "robots_index", Label: "Разрешить индексацию", Control: "switch"},
			{Key: "robots_follow", Label: "Разрешить переходы", Control: "switch"},
			{Key: "og_title_template", Label: "Open Graph title", Control: "text"},
			{Key: "og_description_template", Label: "Open Graph description", Control: "textarea", Rows: 3},
		},
		Variables: variableMetadata,
	}
}

func (*Runtime) AppliesTo(code resourcetype.Code) bool {
	return code == resourcetype.Page || code == resourcetype.Library
}

func (r *Runtime) Read(
	ctx context.Context,
	request resourceextension.Request,
) (any, error) {
	if !r.AppliesTo(request.Resource.Type) {
		return nil, resourceextension.ErrNotApplicable
	}
	return r.service.Get(ctx, request.Site.ID, request.Resource.ID)
}

func (r *Runtime) Save(
	ctx context.Context,
	request resourceextension.Request,
	raw json.RawMessage,
) (any, error) {
	if !r.AppliesTo(request.Resource.Type) {
		return nil, resourceextension.ErrNotApplicable
	}
	settings, err := decodeSettings(raw)
	if err != nil {
		return nil, err
	}
	result, err := r.service.Save(
		ctx,
		request.Actor,
		request.Site.ID,
		request.Resource.ID,
		settings,
	)
	return result, extensionError(err)
}

func (r *Runtime) Preview(
	_ context.Context,
	request resourceextension.Request,
	raw json.RawMessage,
) (any, error) {
	if !r.AppliesTo(request.Resource.Type) {
		return nil, resourceextension.ErrNotApplicable
	}
	settings, err := decodeSettings(raw)
	if err != nil {
		return nil, err
	}
	result, err := r.service.Preview(settings, RenderInput{
		Site:     request.Site,
		Resource: request.Resource,
	})
	return result, extensionError(err)
}

func (r *Runtime) PublicResourceExtension(
	ctx context.Context,
	request resourceextension.PublicRequest,
) (resourceextension.PublicExtension, error) {
	if !r.AppliesTo(request.Resource.Type) {
		return resourceextension.PublicExtension{}, nil
	}
	data, err := r.service.Render(ctx, RenderInput{
		Site:     request.Site,
		Resource: request.Resource,
		Preview:  request.Preview,
	})
	if err != nil {
		return resourceextension.PublicExtension{}, err
	}
	return resourceextension.PublicExtension{Code: "seo", Data: data}, nil
}

func decodeSettings(raw json.RawMessage) (Settings, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var settings Settings
	if err := decoder.Decode(&settings); err != nil {
		return Settings{}, resourceextension.ValidationError{
			Message: "resource extension payload is invalid",
		}
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Settings{}, resourceextension.ValidationError{
			Message: "resource extension payload contains trailing data",
		}
	}
	return settings, nil
}

func extensionError(err error) error {
	if err == nil {
		return nil
	}
	var validation ValidationError
	if errors.As(err, &validation) {
		return resourceextension.ValidationError{
			Message: "SEO template validation failed",
			Fields: []resourceextension.FieldError{{
				Key:     validation.Field,
				Message: validation.Err.Error(),
			}},
		}
	}
	return err
}

var _ kernel.Module = Module{}
var _ kernel.ModuleRuntime = (*Runtime)(nil)
var _ resourceextension.EditorProvider = (*Runtime)(nil)
var _ resourceextension.Editor = (*Runtime)(nil)
var _ resourceextension.TransferUsage = (*Runtime)(nil)
var _ resourceextension.PublicProvider = (*Runtime)(nil)
