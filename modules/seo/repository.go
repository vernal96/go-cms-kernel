package seo

import (
	"context"
	"errors"

	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
)

var (
	ErrNotFound         = errors.New("SEO metadata not found")
	ErrInvalidReference = errors.New("SEO resource reference is invalid")
)

type Repository interface {
	ByResource(context.Context, site.ID, resource.ID) (Metadata, error)
	UsedByResources(context.Context, site.ID, []resource.ID) (bool, error)
	Save(context.Context, Metadata) (Metadata, error)
}
