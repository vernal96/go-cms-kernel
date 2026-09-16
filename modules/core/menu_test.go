package core

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel/cache"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
)

func TestMenuQueryValidation(t *testing.T) {
	for _, raw := range []string{"", "depth=1", "parent_id=12&depth=2"} {
		if _, err := parseMenuInput(raw); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
	}
	for _, raw := range []string{"depth=0", "parent_id=-1", "depth=1&depth=2", "other=3", "parent_id=", "depth=bad", "depth=%zz"} {
		if _, err := parseMenuInput(raw); !errors.Is(err, resource.ErrInvalid) {
			t.Fatalf("%s: %v", raw, err)
		}
	}
}

func TestMenuCacheDeadlineInvalidationAndFailure(t *testing.T) {
	ctx := context.Background()
	store := newMemoryCacheStore()
	policy := newTestRepositoryCachePolicy(store)
	runtime := &Runtime{menuStore: store, menuTTL: time.Minute, services: &Services{cachePolicy: policy}}
	calls := 0
	loader := func(context.Context, time.Time) (resource.MenuResult, *time.Time, error) {
		calls++
		return resource.MenuResult{Items: []resource.MenuItem{}}, nil, nil
	}
	read := func(siteID site.ID, depth int) {
		t.Helper()
		if _, err := runtime.cachedMenu(ctx, siteID, resource.MenuInput{Depth: depth}, loader); err != nil {
			t.Fatal(err)
		}
	}
	read(1, 0)
	read(1, 0)
	read(1, 1)
	read(2, 0)
	if calls != 3 {
		t.Fatalf("loads=%d", calls)
	}
	policy.invalidate(ctx, siteResourcesTag(1))
	read(1, 0)
	read(1, 1)
	read(2, 0)
	if calls != 5 {
		t.Fatalf("invalidation loads=%d", calls)
	}
	key := "menu:v1:site:1:parent:root:depth:0"
	raw, _ := json.Marshal(menuEnvelope{ExpiresAt: time.Now().Add(-time.Second)})
	store.values[key] = raw
	read(1, 0)
	if calls != 6 {
		t.Fatal("expired menu returned")
	}
	store.values[key] = []byte("broken")
	read(1, 0)
	store.getErr = errors.New("unavailable")
	store.setErr = store.getErr
	read(1, 0)
	if calls != 8 {
		t.Fatalf("recovery loads=%d", calls)
	}
	store.getErr = nil
	store.setErr = nil
	parent := resource.ID(12)
	input := resource.MenuInput{ParentID: &parent}
	for i := 0; i < 2; i++ {
		_, err := runtime.cachedMenu(ctx, 1, input, func(context.Context, time.Time) (resource.MenuResult, *time.Time, error) {
			calls++
			return resource.MenuResult{}, nil, resource.ErrNotFound
		})
		if !errors.Is(err, resource.ErrNotFound) {
			t.Fatal(err)
		}
	}
	if calls != 10 {
		t.Fatal("negative result cached")
	}
	boundary := time.Now().Add(time.Second)
	_, err := runtime.cachedMenu(ctx, 3, input, func(context.Context, time.Time) (resource.MenuResult, *time.Time, error) {
		return resource.MenuResult{Items: []resource.MenuItem{}}, &boundary, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	option := store.options["menu:v1:site:3:parent:12:depth:0"]
	if option.TTL <= 0 || option.TTL > time.Second {
		t.Fatalf("publication TTL=%v", option.TTL)
	}
}

func TestMenuConcurrentFillCannotSurviveMutation(t *testing.T) {
	ctx := context.Background()
	store := newMemoryCacheStore()
	policy := newTestRepositoryCachePolicy(store)
	runtime := &Runtime{menuStore: store, menuTTL: time.Minute, services: &Services{cachePolicy: policy}}
	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	title := "old"
	go func() {
		_, err := runtime.cachedMenu(ctx, 1, resource.MenuInput{}, func(context.Context, time.Time) (resource.MenuResult, *time.Time, error) {
			value := title
			close(started)
			<-release
			return resource.MenuResult{Items: []resource.MenuItem{{Title: value, Children: []resource.MenuItem{}}}}, nil, nil
		})
		done <- err
	}()
	<-started
	written := make(chan error, 1)
	go func() {
		written <- withRepositoryCacheWrite(policy, []cache.Tag{siteResourcesTag(1)}, func() error { title = "new"; policy.invalidate(ctx, siteResourcesTag(1)); return nil })
	}()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	result, err := runtime.cachedMenu(ctx, 1, resource.MenuInput{}, func(context.Context, time.Time) (resource.MenuResult, *time.Time, error) {
		return resource.MenuResult{Items: []resource.MenuItem{{Title: title}}}, nil, nil
	})
	if err != nil || result.Items[0].Title != "new" {
		t.Fatalf("menu=%v err=%v", result, err)
	}
}

type menuDeniedAuthorizer struct{}

type menuRevisionRepository struct {
	resource.Repository
	resource.RevisionRepository
}

func (menuRevisionRepository) RestoreRevision(_ context.Context, _ *security.UserID, _, candidate resource.Resource, _ int64) (resource.Resource, error) {
	return candidate, nil
}

func TestRevisionRestoreInvalidatesMenuResults(t *testing.T) {
	ctx := context.Background()
	store := newMemoryCacheStore()
	policy := newTestRepositoryCachePolicy(store)
	runtime := &Runtime{menuStore: store, menuTTL: time.Minute, services: &Services{cachePolicy: policy}}
	calls := 0
	loader := func(context.Context, time.Time) (resource.MenuResult, *time.Time, error) {
		calls++
		return resource.MenuResult{Items: []resource.MenuItem{}}, nil, nil
	}
	for _, id := range []site.ID{1, 2} {
		if _, err := runtime.cachedMenu(ctx, id, resource.MenuInput{}, loader); err != nil {
			t.Fatal(err)
		}
	}
	repository := invalidatingResourceRepository{base: menuRevisionRepository{}, policy: policy}
	item := resource.Resource{ID: 42, SiteID: 1}
	if _, err := repository.RestoreRevision(ctx, nil, item, item, 1); err != nil {
		t.Fatal(err)
	}
	for _, id := range []site.ID{1, 2} {
		if _, err := runtime.cachedMenu(ctx, id, resource.MenuInput{}, loader); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 3 {
		t.Fatalf("loads=%d; restored site must reload, other site must stay cached", calls)
	}
}

func (menuDeniedAuthorizer) Check(context.Context, security.Actor, permission.Code) error {
	return security.ErrForbidden
}
func TestMenuChecksPermissionBeforeCache(t *testing.T) {
	runtime := &Runtime{authorization: menuDeniedAuthorizer{}, menuStore: newMemoryCacheStore()}
	ctx := WithSiteRuntime(context.Background(), &site.Runtime{})
	ctx = httptransport.WithActor(ctx, security.System())
	response := httptest.NewRecorder()
	runtime.serveMenu(response, httptest.NewRequest("GET", "/menu", nil).WithContext(ctx))
	if response.Code != 403 {
		t.Fatalf("status=%d", response.Code)
	}
}
