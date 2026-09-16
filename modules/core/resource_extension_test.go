package core

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/resourceextension"
)

type publicExtensionRuntime struct {
	code resourceextension.Code
	data any
	err  error
}

func (publicExtensionRuntime) ModuleCode() kernel.ModuleCode { return "feature" }
func (r publicExtensionRuntime) PublicResourceExtension(
	context.Context,
	resourceextension.PublicRequest,
) (resourceextension.PublicExtension, error) {
	return resourceextension.PublicExtension{Code: r.code, Data: r.data}, r.err
}

func TestPublicResourceExtensionsAreOptionalAndFailureIsIsolated(t *testing.T) {
	t.Parallel()
	handler := pageResourceHandler{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	item := resource.Resource{ID: 9}
	if result := handler.publicExtensions(
		context.Background(), site.Site{ID: 7}, nil, item, false,
	); result != nil {
		t.Fatalf("extensions without providers = %#v", result)
	}
	result := handler.publicExtensions(
		context.Background(),
		site.Site{ID: 7},
		[]kernel.ModuleRuntime{
			publicExtensionRuntime{err: errors.New("provider failed")},
			publicExtensionRuntime{code: "seo", data: map[string]any{"title": "Page"}},
		},
		item,
		false,
	)
	seo, exists := result["seo"]
	if !exists || seo.(map[string]any)["title"] != "Page" || len(result) != 1 {
		t.Fatalf("extensions = %#v", result)
	}
}

var _ resourceextension.PublicProvider = publicExtensionRuntime{}
