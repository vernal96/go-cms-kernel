package resourceextension

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/security"
)

type Code string

var ErrNotApplicable = errors.New("resource extension is not applicable")

type Field struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Control string `json:"control"`
	Rows    int    `json:"rows,omitempty"`
}

type Variable struct {
	Code  string `json:"code"`
	Label string `json:"label"`
}

type Metadata struct {
	Code      Code                `json:"code"`
	Title     string              `json:"title"`
	AppliesTo []resourcetype.Code `json:"applies_to"`
	Fields    []Field             `json:"fields"`
	Variables []Variable          `json:"variables,omitempty"`
}

type Request struct {
	Actor    security.Actor
	Site     site.Site
	Resource resource.Resource
}

type Editor interface {
	Metadata() Metadata
	AppliesTo(resourcetype.Code) bool
	Read(context.Context, Request) (any, error)
	Save(context.Context, Request, json.RawMessage) (any, error)
	Preview(context.Context, Request, json.RawMessage) (any, error)
}

type EditorProvider interface {
	ResourceEditorExtension() Editor
}

type TransferUsage interface {
	UsedByResources(context.Context, site.ID, []resource.ID) (bool, error)
}

type PublicRequest struct {
	Site     site.Site
	Resource resource.Resource
	Preview  bool
}

type PublicExtension struct {
	Code Code
	Data any
}

type PublicProvider interface {
	PublicResourceExtension(
		context.Context,
		PublicRequest,
	) (PublicExtension, error)
}

type FieldError struct {
	Key     string
	Message string
}

type ValidationError struct {
	Message string
	Fields  []FieldError
}

func (e ValidationError) Error() string {
	if e.Message == "" {
		return "resource extension validation failed"
	}
	return e.Message
}
