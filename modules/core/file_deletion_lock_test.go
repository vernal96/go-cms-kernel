package core

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	kernel "github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/cache"
	"github.com/vernal96/go-cms-kernel/eventbus"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

type deletionCallKey struct{}
type updateCallKey struct{}

type deletionSiteAccess struct {
	requested chan struct{}
	once      sync.Once
}

func (a *deletionSiteAccess) Check(ctx context.Context, _ security.Actor, code permission.Code) error {
	if ctx.Value(deletionCallKey{}) != nil && code == "core.site.update" && a.requested != nil {
		a.once.Do(func() { close(a.requested) })
	}
	return nil
}
func (*deletionSiteAccess) Allowed(_ context.Context, _ security.Actor, codes []permission.Code) ([]permission.Code, error) {
	return append([]permission.Code(nil), codes...), nil
}
func (*deletionSiteAccess) IsGuestSubject(_ context.Context, actor security.Actor) (bool, error) {
	return actor.IsGuest(), nil
}

type deletionFieldsModule struct{}

func (deletionFieldsModule) Code() kernel.ModuleCode       { return "fields" }
func (deletionFieldsModule) ModuleCode() kernel.ModuleCode { return "fields" }
func (deletionFieldsModule) Validate(context.Context, kernel.ModuleValidationContext) error {
	return nil
}
func (deletionFieldsModule) Registry() (kernel.ModuleRegistry, error) {
	return kernel.ModuleRegistry{FieldTypes: field.StandardTypes(), ValidatorTypes: field.StandardValidatorTypes()}, nil
}
func (m deletionFieldsModule) Build(context.Context, kernel.ModuleContext) (kernel.ModuleRuntime, error) {
	return m, nil
}

type deletionResolver struct{}

func (deletionResolver) MainModuleDatabase(kernel.ModuleCode) (kernel.ModuleDatabase, bool) {
	return nil, false
}
func (deletionResolver) ModuleDatabase(kernel.ConnectionCode, kernel.ModuleCode) (kernel.ModuleDatabase, bool) {
	return nil, false
}

type deletionEventBus struct{ eventbus.Bus }
type deletionProfiles struct{ blueprint *kernel.ProfileBlueprint }

func (p deletionProfiles) ProfileBlueprint(kernel.ProfileCode) (*kernel.ProfileBlueprint, bool) {
	return p.blueprint, true
}

type deletionSiteMedia struct{ media.Service }

func (deletionSiteMedia) Resolve(_ context.Context, _ security.Actor, id media.ID) (media.ResolvedMedia, error) {
	return media.ResolvedMedia{Media: media.Media{ID: id}, File: file.File{Storage: "public", MIMEType: "image/png"}}, nil
}

type deletionSiteRepository struct{ *siteRepositoryStub }

func (r deletionSiteRepository) ApplyFileDeletionSettings(ctx context.Context, actor *security.UserID, item site.Site) (site.Site, error) {
	item.Version++
	return r.Update(ctx, actor, item)
}

type deletionOwnerRepository struct {
	media.FileDeletionRepository
	refs          []media.FileOccurrence
	current       *media.FileOccurrence
	failure       error
	abortPrepared bool
}

func (r *deletionOwnerRepository) FileOccurrences(context.Context, media.ID) ([]media.FileOccurrence, error) {
	return r.refs, nil
}
func (r *deletionOwnerRepository) DeleteMediaFile(ctx context.Context, _ *security.UserID, input media.DeleteFileInput, prepare media.PrepareFileOwner) (media.FileDeletion, error) {
	if r.failure != nil {
		return media.FileDeletion{}, r.failure
	}
	owner, err := prepare(ctx, r.current)
	if err != nil {
		return media.FileDeletion{}, err
	}
	if r.abortPrepared {
		if owner.Abort != nil {
			owner.Abort()
		}
		return media.FileDeletion{}, media.ErrFileDeleteConflict
	}
	result := media.FileDeletion{OperationID: "test-operation", SiteID: input.SiteID, MediaID: input.MediaID, Status: "pending"}
	if owner.Apply != nil {
		result.ClearedReference, err = owner.Apply(ctx)
		if err != nil {
			if owner.Abort != nil {
				owner.Abort()
			}
			return media.FileDeletion{}, err
		}
	}
	if owner.Publish != nil {
		owner.Publish()
	}
	return result, nil
}
func (*deletionOwnerRepository) CleanFileDeletion(_ context.Context, sid int64, operation string, _ file.DeletePhysical) (media.FileDeletion, error) {
	return media.FileDeletion{OperationID: operation, SiteID: sid, Status: "completed"}, nil
}

func newSiteDeletionTest(t *testing.T, access *deletionSiteAccess) (*MediaFileDeletions, *site.Catalog, *deletionOwnerRepository) {
	t.Helper()
	ctx := context.Background()
	factory, err := kernel.NewProfileRuntimeFactory(deletionResolver{}, kernel.RuntimeServices{EventBus: deletionEventBus{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	blueprint, err := factory.Compile(ctx, kernel.Profile{Code: "delete", Modules: []kernel.Module{deletionFieldsModule{}}, Params: []field.Definition{
		{Key: "icon", Label: "Icon", Type: field.TypeFile, Required: true, Options: field.FileOptions{Disk: "public", VirtualPath: "icons", SettingsCode: "icon"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	base := deletionSiteRepository{&siteRepositoryStub{items: []site.Site{{ID: 1, Version: 1, ProfileCode: "delete", Name: "Site", Domain: "delete.test", Locale: "en", Settings: map[string]any{"icon": int64(1)}}}}}
	policy := newRepositoryCachePolicy(nil)
	catalog, err := site.NewCatalog(&invalidatingSiteRepository{base: base, policy: policy}, deletionProfiles{blueprint}, access)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.SetMediaService(deletionSiteMedia{}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	ref := media.FileOccurrence{OwnerKind: "site", OwnerID: 1, SiteID: 1, Container: "settings", Path: []string{"icon"}, MediaID: 1, Target: field.ReferenceFile}
	repository := &deletionOwnerRepository{refs: []media.FileOccurrence{ref}, current: &ref}
	service := &MediaFileDeletions{repository: repository, services: &Services{Sites: catalog, Resources: &resource.Service{}, cachePolicy: policy}, authorizer: access}
	return service, catalog, repository
}

func deleteSiteFile(service *MediaFileDeletions) error {
	ctx := context.WithValue(context.Background(), deletionCallKey{}, true)
	_, err := service.Delete(ctx, security.System(), media.DeleteFileInput{SiteID: 1, MediaID: 1, ExpectedFileID: 2, ExpectedUpdatedAt: time.Now()})
	return err
}

func TestSiteFileDeletionAndCatalogUpdateShareLockOrder(t *testing.T) {
	access := &deletionSiteAccess{requested: make(chan struct{})}
	service, catalog, _ := newSiteDeletionTest(t, access)
	updatePrepared := make(chan struct{})
	if err := catalog.AddRuntimePreparer(context.Background(), func(ctx context.Context, _ site.RuntimePlan) (site.RuntimePreparation, error) {
		if ctx.Value(updateCallKey{}) != nil {
			// Update owns mutationMu, and its repository write will acquire cache
			// locks next. Let deletion reach site preparation first: with the old
			// order it already owned those cache locks, creating a deterministic cycle.
			close(updatePrepared)
			<-access.requested
		}
		return site.RuntimePreparation{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	updateDone := make(chan error, 1)
	go func() {
		ctx := context.WithValue(context.Background(), updateCallKey{}, true)
		_, err := catalog.Update(ctx, security.System(), site.UpdateInput{ID: 1, ProfileCode: "delete", Name: "Updated", Domain: "delete.test", Locale: "en", Settings: map[string]any{"icon": int64(1)}})
		updateDone <- err
	}()
	select {
	case <-updatePrepared:
	case <-time.After(2 * time.Second):
		t.Fatal("update did not reach its preparation barrier")
	}
	deleteDone := make(chan error, 1)
	go func() { deleteDone <- deleteSiteFile(service) }()
	for _, done := range []<-chan error{updateDone, deleteDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("catalog mutation/cache lock inversion deadlocked update and deletion")
		}
	}
	current, _ := catalog.RuntimeByID(1)
	if current.Site().Name != "Updated" || len(current.Site().FileReferences) != 0 {
		t.Fatal("lost update or stale file publication", current.Site())
	}
}

func TestSiteFileDeletionAbortsPreparationOnRaceAndGuardFailure(t *testing.T) {
	for _, name := range []string{"guard failure", "repository abort", "missing occurrence", "moved owner", "changed path", "changed container", "changed target", "new site owner", "completed"} {
		t.Run(name, func(t *testing.T) {
			service, catalog, repository := newSiteDeletionTest(t, &deletionSiteAccess{})
			before, _ := catalog.RuntimeByID(1)
			var aborted, published atomic.Int32
			if err := catalog.AddRuntimePreparer(context.Background(), func(ctx context.Context, _ site.RuntimePlan) (site.RuntimePreparation, error) {
				if ctx.Value(deletionCallKey{}) == nil {
					return site.RuntimePreparation{}, nil
				}
				return site.RuntimePreparation{Abort: func() { aborted.Add(1) }, Publish: func() { published.Add(1) }}, nil
			}); err != nil {
				t.Fatal(err)
			}
			expected := media.ErrFileDeleteConflict
			ref := *repository.current
			switch name {
			case "guard failure":
				repository.failure = media.ErrFileInUse
				expected = media.ErrFileInUse
			case "repository abort":
				repository.abortPrepared = true
			case "missing occurrence":
				repository.current = nil
			case "moved owner":
				ref.OwnerKind = "resource"
				repository.current = &ref
			case "changed path":
				ref.Path = []string{"moved"}
				repository.current = &ref
			case "changed container":
				ref.Container = "other"
				repository.current = &ref
			case "changed target":
				ref.Target = field.ReferenceMedia
				repository.current = &ref
			case "new site owner":
				repository.refs = nil
			case "completed":
				expected = nil
			}
			if err := deleteSiteFile(service); !errors.Is(err, expected) {
				t.Fatalf("error=%v expected=%v", err, expected)
			}
			current, _ := catalog.RuntimeByID(1)
			if name == "completed" {
				if published.Load() != 1 || aborted.Load() != 0 || current == before {
					t.Fatal("invalid successful preparation lifecycle", published.Load(), aborted.Load())
				}
			} else {
				wantAbort := int32(1)
				if name == "new site owner" {
					wantAbort = 0
				}
				if aborted.Load() != wantAbort || published.Load() != 0 || current != before {
					t.Fatal("failed deletion published or leaked preparation", aborted.Load(), published.Load())
				}
			}
			unlocked := make(chan error, 1)
			go func() { unlocked <- catalog.Reload(context.Background()) }()
			select {
			case err := <-unlocked:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("failed deletion leaked catalog mutation lock")
			}
			// Also prove all cache write locks were released on the error path.
			cacheUnlocked := make(chan error, 1)
			go func() {
				cacheUnlocked <- withRepositoryCacheWrite(service.services.cachePolicy, []cache.Tag{sitesTag}, func() error { return nil })
			}()
			select {
			case <-cacheUnlocked:
			case <-time.After(2 * time.Second):
				t.Fatal("deletion leaked cache locks")
			}
		})
	}
}
