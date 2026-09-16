package seo

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/eventbus"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/resourceextension"
	"github.com/vernal96/go-cms-kernel/security"
)

type moduleTestDatabase struct{ repository Repository }

func (*moduleTestDatabase) ModuleCode() kernel.ModuleCode { return ModuleCode }
func (d *moduleTestDatabase) ResourceMetadata() Repository {
	return d.repository
}

type moduleTestResolver struct{ database kernel.ModuleDatabase }

func (r moduleTestResolver) MainModuleDatabase(
	code kernel.ModuleCode,
) (kernel.ModuleDatabase, bool) {
	return r.database, code == ModuleCode
}
func (moduleTestResolver) ModuleDatabase(
	kernel.ConnectionCode,
	kernel.ModuleCode,
) (kernel.ModuleDatabase, bool) {
	return nil, false
}

type moduleTestBus struct{}

func (moduleTestBus) Publish(context.Context, eventbus.Message) error { return nil }
func (moduleTestBus) Consume(
	context.Context,
	eventbus.Subscription,
	eventbus.Handler,
) error {
	return nil
}

func TestModuleBuildsProfileScopedRuntime(t *testing.T) {
	t.Parallel()
	factory, err := kernel.NewProfileRuntimeFactory(
		moduleTestResolver{database: &moduleTestDatabase{
			repository: &memoryRepository{},
		}},
		kernel.RuntimeServices{
			Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
			EventBus: moduleTestBus{},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	blueprint, err := factory.Compile(context.Background(), kernel.Profile{
		Code: "seo-test",
		Modules: []kernel.ProfileModule{{
			Module: Module{},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	profileRuntime, err := blueprint.Build(
		context.Background(),
		kernel.NewRuntimeScope("seo-test", "", "", nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	modules := profileRuntime.Modules()
	if len(modules) != 1 || modules[0].ModuleCode() != ModuleCode {
		t.Fatalf("module runtimes = %#v", modules)
	}
	runtime, ok := modules[0].(*Runtime)
	if !ok || runtime.Metadata().Code != "seo" {
		t.Fatalf("SEO runtime = %#v", modules[0])
	}
}

func TestRuntimeEditorAndPublicContracts(t *testing.T) {
	t.Parallel()
	renderer := testRenderer(t, 1000)
	service, err := NewService(&memoryRepository{}, renderer, Settings{
		TitleTemplate:       "{{ resource.title }}",
		DescriptionTemplate: "{{ resource.annotation }}",
		RobotsIndex:         true,
		RobotsFollow:        true,
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{service: service, renderer: renderer}
	metadata := runtime.Metadata()
	if metadata.Code != "seo" || metadata.Title != "SEO" ||
		len(metadata.AppliesTo) != 2 || metadata.AppliesTo[0] != resourcetype.Page || metadata.AppliesTo[1] != resourcetype.Library ||
		len(metadata.Fields) != 8 || len(metadata.Variables) == 0 {
		t.Fatalf("metadata = %#v", metadata)
	}
	request := resourceextension.Request{
		Actor: security.User(1),
		Site:  site.Site{ID: 7, Domain: "example.com"},
		Resource: resource.Resource{
			ID: 9, SiteID: 7, Type: resourcetype.Page,
			Title: "Контакты", Annotation: "Напишите нам",
		},
	}
	raw, _ := json.Marshal(Settings{
		TitleTemplate:       "{{ resource.title }} — {{ site.domain }}",
		DescriptionTemplate: "{{ resource.annotation }}",
		RobotsIndex:         true,
		RobotsFollow:        true,
	})
	previewValue, err := runtime.Preview(context.Background(), request, raw)
	if err != nil {
		t.Fatal(err)
	}
	preview := previewValue.(Preview)
	if preview.Title != "Контакты — example.com" || preview.Robots.Index {
		t.Fatalf("preview = %#v", preview)
	}
	if _, err := runtime.Save(context.Background(), request, raw); err != nil {
		t.Fatal(err)
	}
	public, err := runtime.PublicResourceExtension(
		context.Background(),
		resourceextension.PublicRequest{Site: request.Site, Resource: request.Resource},
	)
	if err != nil || public.Code != "seo" ||
		public.Data.(PublicData).Title != "Контакты — example.com" {
		t.Fatalf("public extension = %#v, %v", public, err)
	}
	request.Resource.Type = resourcetype.Link
	if _, err := runtime.Read(context.Background(), request); err != resourceextension.ErrNotApplicable {
		t.Fatalf("link error = %v", err)
	}
}

var _ Database = (*moduleTestDatabase)(nil)
var _ eventbus.Bus = moduleTestBus{}
