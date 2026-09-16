package search

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

var ErrInvalid = errors.New("invalid search query")

type Input struct {
	Text    string
	Page    int
	PerPage int
}

// Query is an engine request assembled by the site-owned service. Engines must
// filter by SiteID and public visibility before counting or paginating results.
type Query struct {
	Input
	SiteID     site.ID
	RouteTypes []resourcetype.Code
}

type Item struct {
	ID          resource.ID          `json:"id"`
	StorageKind resource.StorageKind `json:"storage_kind"`
	Title       string               `json:"title"`
	URL         string               `json:"url"`
	Annotation  string               `json:"annotation"`
	Score       float64              `json:"score"`
}

type Pagination struct {
	Page    int   `json:"page"`
	PerPage int   `json:"per_page"`
	Total   int64 `json:"total"`
}

type Page struct {
	Items      []Item     `json:"items"`
	Pagination Pagination `json:"pagination"`
}

type Engine interface {
	Search(context.Context, Query) (Page, error)
}

type Service struct {
	siteID     site.ID
	engine     Engine
	authorizer security.Authorizer
	routeTypes []resourcetype.Code
}

func NewService(siteID site.ID, engine Engine, authorizer security.Authorizer, routeTypes []resourcetype.Code) (*Service, error) {
	if siteID <= 0 || engine == nil || authorizer == nil {
		return nil, errors.New("search requires a site, engine and authorizer")
	}
	return &Service{siteID: siteID, engine: engine, authorizer: authorizer, routeTypes: append([]resourcetype.Code{}, routeTypes...)}, nil
}

func (s *Service) Search(ctx context.Context, actor security.Actor, input Input) (Page, error) {
	if ctx == nil {
		return Page{}, errors.New("search context is nil")
	}
	if err := ctx.Err(); err != nil {
		return Page{}, err
	}
	if err := s.authorizer.Check(ctx, actor, permission.MustCode("core", "resource", permission.Read)); err != nil {
		return Page{}, err
	}
	input, err := NormalizeInput(input)
	if err != nil {
		return Page{}, err
	}
	result, err := s.engine.Search(ctx, Query{Input: input, SiteID: s.siteID, RouteTypes: append([]resourcetype.Code{}, s.routeTypes...)})
	if err != nil {
		return Page{}, fmt.Errorf("search resources: %w", err)
	}
	if result.Items == nil {
		result.Items = []Item{}
	}
	result.Pagination.Page, result.Pagination.PerPage = input.Page, input.PerPage
	return result, nil
}

func NormalizeInput(input Input) (Input, error) {
	input.Text = strings.TrimSpace(input.Text)
	length := utf8.RuneCountInString(input.Text)
	if !utf8.ValidString(input.Text) || strings.ContainsRune(input.Text, 0) || length < 3 || length > 200 {
		return Input{}, fmt.Errorf("%w: q must contain 3 to 200 characters", ErrInvalid)
	}
	if input.Page == 0 {
		input.Page = 1
	}
	if input.PerPage == 0 {
		input.PerPage = 20
	}
	if input.Page < 1 || input.PerPage < 1 || input.PerPage > 50 || int64(input.Page-1) > math.MaxInt64/int64(input.PerPage) {
		return Input{}, fmt.Errorf("%w: page must be positive and per_page between 1 and 50", ErrInvalid)
	}
	return input, nil
}
