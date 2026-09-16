package seo

import (
	"context"
	"errors"

	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/security"
)

type Service struct {
	repository Repository
	renderer   *Renderer
	defaults   Settings
}

func NewService(
	repository Repository,
	renderer *Renderer,
	defaults Settings,
) (*Service, error) {
	if repository == nil {
		return nil, errors.New("SEO repository is nil")
	}
	if renderer == nil {
		return nil, errors.New("SEO renderer is nil")
	}
	if err := renderer.Validate(defaults); err != nil {
		return nil, err
	}
	return &Service{repository: repository, renderer: renderer, defaults: defaults}, nil
}

func (s *Service) Get(
	ctx context.Context,
	siteID site.ID,
	resourceID resource.ID,
) (Settings, error) {
	metadata, err := s.repository.ByResource(ctx, siteID, resourceID)
	if errors.Is(err, ErrNotFound) {
		return s.defaults, nil
	}
	if err != nil {
		return Settings{}, err
	}
	return s.normalize(settingsFromMetadata(metadata)), nil
}

func (s *Service) UsedByResources(
	ctx context.Context,
	siteID site.ID,
	resourceIDs []resource.ID,
) (bool, error) {
	return s.repository.UsedByResources(ctx, siteID, resourceIDs)
}

func (s *Service) Save(
	ctx context.Context,
	actor security.Actor,
	siteID site.ID,
	resourceID resource.ID,
	settings Settings,
) (Settings, error) {
	settings = s.normalize(settings)
	if err := s.renderer.Validate(settings); err != nil {
		return Settings{}, err
	}
	auditID := actor.AuditUserID()
	stored, err := s.repository.Save(ctx, Metadata{
		ResourceID:            resourceID,
		SiteID:                siteID,
		TitleTemplate:         settings.TitleTemplate,
		DescriptionTemplate:   settings.DescriptionTemplate,
		KeywordsTemplate:      settings.KeywordsTemplate,
		CanonicalTemplate:     settings.CanonicalTemplate,
		RobotsIndex:           settings.RobotsIndex,
		RobotsFollow:          settings.RobotsFollow,
		OGTitleTemplate:       settings.OGTitleTemplate,
		OGDescriptionTemplate: settings.OGDescriptionTemplate,
		CreatedBy:             auditID,
		UpdatedBy:             auditID,
	})
	if err != nil {
		return Settings{}, err
	}
	return settingsFromMetadata(stored), nil
}

func (s *Service) Preview(
	settings Settings,
	input RenderInput,
) (Preview, error) {
	settings = s.normalize(settings)
	input.Preview = true
	return s.renderer.Render(settings, input)
}

func (s *Service) Render(
	ctx context.Context,
	input RenderInput,
) (PublicData, error) {
	settings, err := s.Get(ctx, input.Site.ID, input.Resource.ID)
	if err != nil {
		return PublicData{}, err
	}
	preview, err := s.renderer.Render(settings, input)
	if err != nil {
		return PublicData{}, err
	}
	return preview.PublicData, nil
}

func (s *Service) normalize(settings Settings) Settings {
	if settings.TitleTemplate == "" {
		settings.TitleTemplate = s.defaults.TitleTemplate
	}
	if settings.DescriptionTemplate == "" {
		settings.DescriptionTemplate = s.defaults.DescriptionTemplate
	}
	return settings
}
