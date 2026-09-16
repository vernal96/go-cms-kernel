package site

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/eventbus"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

type testResolver struct{}

type testEventBus struct{}

func (testEventBus) Publish(context.Context, eventbus.Message) error {
	return nil
}

func (testEventBus) Consume(
	context.Context,
	eventbus.Subscription,
	eventbus.Handler,
) error {
	return nil
}

func (testResolver) MainModuleDatabase(
	kernel.ModuleCode,
) (kernel.ModuleDatabase, bool) {
	return nil, false
}
func (testResolver) ModuleDatabase(
	kernel.ConnectionCode,
	kernel.ModuleCode,
) (kernel.ModuleDatabase, bool) {
	return nil, false
}

type testProfiles map[kernel.ProfileCode]*kernel.ProfileBlueprint

func (p testProfiles) ProfileBlueprint(
	code kernel.ProfileCode,
) (*kernel.ProfileBlueprint, bool) {
	blueprint, exists := p[code]
	return blueprint, exists
}

type testAccess struct {
	allow bool
}

func (a testAccess) Check(
	context.Context,
	security.Actor,
	permission.Code,
) error {
	if !a.allow {
		return security.ErrForbidden
	}
	return nil
}
func (testAccess) IsGuestSubject(
	_ context.Context,
	actor security.Actor,
) (bool, error) {
	return actor.IsGuest(), nil
}

type memoryRepository struct {
	mu        sync.Mutex
	items     []Site
	updateErr error
	deleteErr error
}

func (r *memoryRepository) List(
	context.Context,
) ([]Site, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Site(nil), r.items...), nil
}

func (r *memoryRepository) Update(
	_ context.Context,
	actorID *security.UserID,
	item Site,
) (Site, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.updateErr != nil {
		return Site{}, r.updateErr
	}
	for index := range r.items {
		if r.items[index].ID != item.ID {
			continue
		}
		item.CreatedAt = r.items[index].CreatedAt
		item.UpdatedAt = time.Now().UTC()
		item.CreatedBy = r.items[index].CreatedBy
		item.UpdatedBy = cloneUserID(actorID)
		r.items[index] = item
		return item, nil
	}
	return Site{}, ErrNotFound
}

func (r *memoryRepository) FindByID(_ context.Context, id ID) (Site, error) {
	for _, item := range r.items {
		if item.ID == id {
			return item, nil
		}
	}
	return Site{}, ErrNotFound
}

func (r *memoryRepository) FindByDomain(_ context.Context, domain string) (Site, error) {
	for _, item := range r.items {
		if item.Domain == domain {
			return item, nil
		}
	}
	return Site{}, ErrNotFound
}

func (r *memoryRepository) ListPage(_ context.Context, query ListQuery) (Page, error) {
	return Page{Items: append([]Site(nil), r.items...), Total: len(r.items)}, nil
}

func (r *memoryRepository) Create(_ context.Context, actorID *security.UserID, item Site) (Site, error) {
	item.ID = ID(len(r.items) + 1)
	item.CreatedAt = time.Now().UTC()
	item.UpdatedAt = item.CreatedAt
	item.CreatedBy = cloneUserID(actorID)
	item.UpdatedBy = cloneUserID(actorID)
	r.items = append(r.items, item)
	return item, nil
}

func (r *memoryRepository) Delete(_ context.Context, id ID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.deleteErr != nil {
		return r.deleteErr
	}
	for index, item := range r.items {
		if item.ID == id {
			r.items = append(r.items[:index], r.items[index+1:]...)
			return nil
		}
	}
	return ErrNotFound
}

type transitionRecorder struct {
	mu        sync.Mutex
	started   []kernel.RuntimeTransition
	committed int
	aborted   int
	fail      error
}

type transitionModule struct {
	code     kernel.ModuleCode
	recorder *transitionRecorder
}

func (m transitionModule) Code() kernel.ModuleCode { return m.code }
func (m transitionModule) Build(context.Context, kernel.ModuleContext) (kernel.ModuleRuntime, error) {
	return &transitionRuntime{code: m.code, recorder: m.recorder}, nil
}

type transitionRuntime struct {
	code     kernel.ModuleCode
	recorder *transitionRecorder
}

func (r *transitionRuntime) ModuleCode() kernel.ModuleCode { return r.code }
func (r *transitionRuntime) PrepareRuntimeTransition(_ context.Context, transition kernel.RuntimeTransition) (kernel.PreparedRuntimeTransition, error) {
	r.recorder.mu.Lock()
	r.recorder.started = append(r.recorder.started, transition)
	fail := r.recorder.fail
	r.recorder.mu.Unlock()
	if fail != nil {
		return nil, fail
	}
	return &recordedPreparedTransition{recorder: r.recorder}, nil
}

type recordedPreparedTransition struct {
	recorder *transitionRecorder
	once     sync.Once
}

func (p *recordedPreparedTransition) Commit() {
	p.once.Do(func() {
		p.recorder.mu.Lock()
		p.recorder.committed++
		p.recorder.mu.Unlock()
	})
}
func (p *recordedPreparedTransition) Abort() {
	p.once.Do(func() {
		p.recorder.mu.Lock()
		p.recorder.aborted++
		p.recorder.mu.Unlock()
	})
}

func compileTransitionProfile(t *testing.T, code kernel.ProfileCode, modules ...kernel.Module) *kernel.ProfileBlueprint {
	t.Helper()
	factory, err := kernel.NewProfileRuntimeFactory(testResolver{}, kernel.RuntimeServices{
		EventBus: testEventBus{}, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	items := make([]kernel.ProfileModule, len(modules))
	for index, module := range modules {
		items[index] = kernel.ProfileModule{Module: module}
	}
	blueprint, err := factory.Compile(context.Background(), kernel.Profile{Code: code, Modules: items})
	if err != nil {
		t.Fatal(err)
	}
	return blueprint
}

func newCatalogForTest(
	t *testing.T,
	item Site,
	access Access,
) *Catalog {
	t.Helper()
	factory, err := kernel.NewProfileRuntimeFactory(
		testResolver{},
		kernel.RuntimeServices{
			EventBus: testEventBus{},
			Logger:   slog.New(slog.NewJSONHandler(io.Discard, nil)),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := factory.Compile(
		context.Background(),
		kernel.Profile{Code: item.ProfileCode},
	)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewCatalog(
		&memoryRepository{items: []Site{item}},
		testProfiles{item.ProfileCode: profile},
		access,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	return catalog
}

func TestResolveRequiresPermissionAndPublicGuestSite(t *testing.T) {
	t.Parallel()

	item := Site{
		ID:          1,
		ProfileCode: "test",
		Domain:      "example.com",
		Locale:      "en-US",
		IsPublic:    true,
	}
	allowed := newCatalogForTest(t, item, testAccess{allow: true})
	if _, err := allowed.ResolveByDomain(
		context.Background(),
		security.Guest(),
		item.Domain,
	); err != nil {
		t.Fatal(err)
	}

	privateItem := item
	privateItem.IsPublic = false
	private := newCatalogForTest(
		t,
		privateItem,
		testAccess{allow: true},
	)
	if _, err := private.ResolveByDomain(
		context.Background(),
		security.Guest(),
		item.Domain,
	); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("private guest error = %v", err)
	}

	denied := newCatalogForTest(t, item, testAccess{})
	if _, err := denied.ResolveByDomain(
		context.Background(),
		security.Guest(),
		item.Domain,
	); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("permission error = %v", err)
	}
}

func TestUpdateAtomicallyReplacesDomainSnapshotAndAudit(t *testing.T) {
	t.Parallel()

	catalog := newCatalogForTest(t, Site{
		ID:          1,
		ProfileCode: "test",
		Domain:      "old.example.com",
		Locale:      "en-US",
		IsPublic:    false,
	}, testAccess{allow: true})
	updated, err := catalog.Update(
		context.Background(),
		security.User(42),
		UpdateInput{
			ID:          1,
			ProfileCode: "test",
			Domain:      "NEW.EXAMPLE.COM.",
			Locale:      "ru-RU",
			Settings:    map[string]any{},
			IsPublic:    true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Site().Domain != "new.example.com" ||
		updated.Site().Locale != "ru-RU" ||
		!updated.Site().IsPublic ||
		updated.Site().UpdatedBy == nil ||
		*updated.Site().UpdatedBy != 42 {
		t.Fatalf("updated site = %#v", updated.Site())
	}
	if _, exists := catalog.RuntimeByDomain("old.example.com"); exists {
		t.Fatal("old domain remains in snapshot")
	}
	if current, exists := catalog.RuntimeByDomain(
		"new.example.com",
	); !exists || current != updated {
		t.Fatalf("new domain runtime = %#v, %t", current, exists)
	}
}

func TestCreateAndDeleteAtomicallyUpdateSnapshot(t *testing.T) {
	t.Parallel()
	catalog := newCatalogForTest(t, Site{
		ID: 1, ProfileCode: "test", Domain: "existing.test", Locale: "en-US",
	}, testAccess{allow: true})
	created, err := catalog.Create(context.Background(), security.User(42), CreateInput{
		ProfileCode: "test",
		Domain:      "NEW.TEST.",
		Locale:      "ru-RU",
		Settings:    map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Site().ID != 2 || created.Site().Domain != "new.test" ||
		created.Site().CreatedBy == nil || *created.Site().CreatedBy != 42 {
		t.Fatalf("created site = %#v", created.Site())
	}
	if current, exists := catalog.RuntimeByID(2); !exists || current != created {
		t.Fatalf("created runtime = %#v, %t", current, exists)
	}
	if err := catalog.Delete(context.Background(), security.User(42), 2); err != nil {
		t.Fatal(err)
	}
	if _, exists := catalog.RuntimeByID(2); exists {
		t.Fatal("deleted site remains in snapshot")
	}
}

func TestReloadPreparesRuntimesBeforeAtomicSnapshotPublication(t *testing.T) {
	item := Site{
		ID:          1,
		ProfileCode: "test",
		Domain:      "first.example.com",
		Locale:      "en-US",
		IsPublic:    true,
	}
	repository := &memoryRepository{items: []Site{item}}
	factory, err := kernel.NewProfileRuntimeFactory(
		testResolver{},
		kernel.RuntimeServices{
			EventBus: testEventBus{},
			Logger:   slog.New(slog.NewJSONHandler(io.Discard, nil)),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	blueprint, err := factory.Compile(
		context.Background(),
		kernel.Profile{Code: item.ProfileCode},
	)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewCatalog(
		repository,
		testProfiles{item.ProfileCode: blueprint},
		testAccess{allow: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	published := []ID(nil)
	if err := catalog.AddRuntimePreparer(
		context.Background(),
		func(
			_ context.Context,
			plan RuntimePlan,
		) (RuntimePreparation, error) {
			ids := make([]ID, 0, len(plan.Next()))
			for _, runtime := range plan.Next() {
				ids = append(ids, runtime.Site().ID)
			}
			return RuntimePreparation{Publish: func() { published = ids }}, nil
		},
	); err != nil {
		t.Fatal(err)
	}
	prepared := make(map[ID]int)
	if err := catalog.AddRuntimePreparer(
		context.Background(),
		func(
			_ context.Context,
			plan RuntimePlan,
		) (RuntimePreparation, error) {
			for _, runtime := range plan.Next() {
				id := runtime.Site().ID
				prepared[id]++
				if id == 3 {
					return RuntimePreparation{}, errors.New("compile failed")
				}
			}
			return RuntimePreparation{}, nil
		},
	); err != nil {
		t.Fatal(err)
	}
	if prepared[1] != 1 {
		t.Fatalf("initial runtime prepare calls = %#v", prepared)
	}
	if !reflect.DeepEqual(published, []ID{1}) {
		t.Fatalf("initial published runtimes = %#v", published)
	}

	repository.items = []Site{{
		ID:          2,
		ProfileCode: "test",
		Domain:      "second.example.com",
		Locale:      "en-US",
		IsPublic:    true,
	}}
	if err := catalog.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if prepared[2] != 1 {
		t.Fatalf("replacement runtime prepare calls = %#v", prepared)
	}
	if !reflect.DeepEqual(published, []ID{2}) {
		t.Fatalf("replacement published runtimes = %#v", published)
	}

	repository.items = []Site{{
		ID:          3,
		ProfileCode: "test",
		Domain:      "broken.example.com",
		Locale:      "en-US",
		IsPublic:    true,
	}}
	if err := catalog.Reload(context.Background()); err == nil {
		t.Fatal("runtime preparation failure was ignored")
	}
	if _, exists := catalog.RuntimeByID(2); !exists {
		t.Fatal("failed runtime preparation replaced the previous snapshot")
	}
	if !reflect.DeepEqual(published, []ID{2}) {
		t.Fatalf("failed later preparation partially published: %#v", published)
	}
}

func TestProfileTransitionAbortsOnRepositoryFailureAndSkipsSameProfileUpdate(t *testing.T) {
	recorder := &transitionRecorder{}
	module := transitionModule{code: "transition", recorder: recorder}
	repository := &memoryRepository{items: []Site{{ID: 1, ProfileCode: "first", Domain: "one.test", Locale: "en-US"}}}
	catalog, err := NewCatalog(repository, testProfiles{
		"first":  compileTransitionProfile(t, "first", module),
		"second": compileTransitionProfile(t, "second", module),
	}, testAccess{allow: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Update(context.Background(), security.User(1), UpdateInput{ID: 1, ProfileCode: "first", Domain: "renamed.test", Locale: "en-US"}); err != nil {
		t.Fatal(err)
	}
	if len(recorder.started) != 0 {
		t.Fatalf("same-profile update started transitions: %#v", recorder.started)
	}
	repository.updateErr = errors.New("database failed")
	if _, err := catalog.Update(context.Background(), security.User(1), UpdateInput{ID: 1, ProfileCode: "second", Domain: "renamed.test", Locale: "en-US"}); err == nil {
		t.Fatal("repository failure was ignored")
	}
	if len(recorder.started) != 1 || recorder.started[0].Reason != kernel.RuntimeTransitionProfileChange || recorder.aborted != 1 || recorder.committed != 0 {
		t.Fatalf("failed transition lifecycle = started:%#v committed:%d aborted:%d", recorder.started, recorder.committed, recorder.aborted)
	}
	current, _ := catalog.RuntimeByID(1)
	if current.Site().ProfileCode != "first" {
		t.Fatalf("failed profile transition published %q", current.Site().ProfileCode)
	}
	repository.updateErr = nil
	if _, err := catalog.Update(context.Background(), security.User(1), UpdateInput{ID: 1, ProfileCode: "second", Domain: "renamed.test", Locale: "en-US"}); err != nil {
		t.Fatal(err)
	}
	if recorder.committed != 1 || recorder.aborted != 1 {
		t.Fatalf("successful transition lifecycle = committed:%d aborted:%d", recorder.committed, recorder.aborted)
	}
}

func TestLaterTransitionParticipantFailureAbortsEarlierParticipant(t *testing.T) {
	first := &transitionRecorder{}
	second := &transitionRecorder{fail: errors.New("later participant failed")}
	repository := &memoryRepository{items: []Site{{ID: 1, ProfileCode: "first", Domain: "one.test", Locale: "en-US"}}}
	catalog, err := NewCatalog(repository, testProfiles{
		"first":  compileTransitionProfile(t, "first", transitionModule{code: "first_participant", recorder: first}, transitionModule{code: "second_participant", recorder: second}),
		"second": compileTransitionProfile(t, "second"),
	}, testAccess{allow: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Update(context.Background(), security.User(1), UpdateInput{ID: 1, ProfileCode: "second", Domain: "one.test", Locale: "en-US"}); err == nil {
		t.Fatal("later participant failure was ignored")
	}
	if first.aborted != 1 || first.committed != 0 || len(second.started) != 1 {
		t.Fatalf("participant rollback = first:%#v second:%#v", first, second)
	}
}

func TestBlockedRuntimeTransitionMapsToSiteConflict(t *testing.T) {
	recorder := &transitionRecorder{fail: fmt.Errorf("active work: %w", kernel.ErrRuntimeTransitionBlocked)}
	module := transitionModule{code: "transition", recorder: recorder}
	repository := &memoryRepository{items: []Site{{ID: 1, ProfileCode: "first", Domain: "one.test", Locale: "en-US"}}}
	catalog, err := NewCatalog(repository, testProfiles{
		"first":  compileTransitionProfile(t, "first", module),
		"second": compileTransitionProfile(t, "second"),
	}, testAccess{allow: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err = catalog.Update(context.Background(), security.User(1), UpdateInput{ID: 1, ProfileCode: "second", Domain: "one.test", Locale: "en-US"})
	if !errors.Is(err, ErrConflict) || !errors.Is(err, kernel.ErrRuntimeTransitionBlocked) {
		t.Fatalf("blocked transition error = %v", err)
	}
}

func TestSiteDeleteTransitionAbortsOnRepositoryFailureAndCommitsOnSuccess(t *testing.T) {
	recorder := &transitionRecorder{}
	module := transitionModule{code: "transition", recorder: recorder}
	repository := &memoryRepository{items: []Site{{ID: 1, ProfileCode: "first", Domain: "one.test", Locale: "en-US"}}, deleteErr: errors.New("delete failed")}
	catalog, err := NewCatalog(repository, testProfiles{"first": compileTransitionProfile(t, "first", module)}, testAccess{allow: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Delete(context.Background(), security.User(1), 1); err == nil {
		t.Fatal("delete repository failure was ignored")
	}
	if recorder.aborted != 1 || recorder.committed != 0 || recorder.started[0].Reason != kernel.RuntimeTransitionSiteDelete {
		t.Fatalf("failed delete lifecycle = %#v", recorder)
	}
	if _, exists := catalog.RuntimeByID(1); !exists {
		t.Fatal("failed delete removed runtime")
	}
	repository.deleteErr = nil
	if err := catalog.Delete(context.Background(), security.User(1), 1); err != nil {
		t.Fatal(err)
	}
	if recorder.committed != 1 || recorder.aborted != 1 {
		t.Fatalf("successful delete lifecycle = %#v", recorder)
	}
}

var _ Repository = (*memoryRepository)(nil)
var _ ManagementRepository = (*memoryRepository)(nil)
var _ Access = testAccess{}
var _ kernel.RuntimeTransitionParticipant = (*transitionRuntime)(nil)
