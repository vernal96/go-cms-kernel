package media

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

type testAuthorizer struct{}

func (testAuthorizer) Check(
	context.Context,
	security.Actor,
	permission.Code,
) error {
	return nil
}

type memoryFiles struct {
	file.Service
	items map[file.ID]file.File
}

func (f memoryFiles) GetFile(
	_ context.Context,
	_ security.Actor,
	id file.ID,
) (file.File, error) {
	item, exists := f.items[id]
	if !exists {
		return file.File{}, file.ErrNotFound
	}
	return file.Clone(item), nil
}

type memoryRepository struct {
	nextID      ID
	items       map[ID]Media
	usages      map[ID][]Usage
	createError error
	updateError error
	deleteError error
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{
		nextID: 1,
		items:  make(map[ID]Media),
		usages: make(map[ID][]Usage),
	}
}

func (r *memoryRepository) Create(
	_ context.Context,
	_ *security.UserID,
	item Media,
) (Media, error) {
	if r.createError != nil {
		return Media{}, r.createError
	}
	item = Clone(item)
	item.ID = r.nextID
	r.nextID++
	item.CreatedAt = time.Now().UTC()
	item.UpdatedAt = item.CreatedAt
	r.items[item.ID] = item
	return Clone(item), nil
}

func (r *memoryRepository) ByID(
	_ context.Context,
	id ID,
) (Media, error) {
	item, exists := r.items[id]
	if !exists {
		return Media{}, ErrNotFound
	}
	return Clone(item), nil
}

func (r *memoryRepository) Update(
	ctx context.Context,
	_ *security.UserID,
	item Media,
	validate ValidateUsages,
) (Media, error) {
	if r.updateError != nil {
		return Media{}, r.updateError
	}
	current, exists := r.items[item.ID]
	if !exists {
		return Media{}, ErrNotFound
	}
	if err := validate(
		ctx,
		append([]Usage(nil), r.usages[item.ID]...),
	); err != nil {
		return Media{}, err
	}
	item = Clone(item)
	item.CreatedAt = current.CreatedAt
	item.UpdatedAt = time.Now().UTC()
	r.items[item.ID] = item
	return Clone(item), nil
}

func (r *memoryRepository) Delete(
	_ context.Context,
	id ID,
) error {
	if r.deleteError != nil {
		return r.deleteError
	}
	if _, exists := r.items[id]; !exists {
		return ErrNotFound
	}
	delete(r.items, id)
	return nil
}

func newServiceForTest(
	t *testing.T,
	policies FilePolicies,
) (Service, *memoryRepository) {
	t.Helper()

	repository := newMemoryRepository()
	service, err := NewService(
		repository,
		memoryFiles{items: map[file.ID]file.File{
			1: {
				ID:       1,
				Name:     "photo.png",
				MIMEType: "image/png",
				Size:     42,
			},
			2: {
				ID:       2,
				Name:     "document.pdf",
				MIMEType: "application/pdf",
				Size:     84,
			},
		}},
		policies,
		testAuthorizer{},
	)
	if err != nil {
		t.Fatal(err)
	}
	return service, repository
}

func TestServiceCreateResolveAndClone(t *testing.T) {
	service, _ := newServiceForTest(t, nil)
	title := "  Hero image  "
	params := map[string]any{
		"meta_alt": "Hero",
		"nested": map[string]any{
			"width": 1200,
		},
	}

	created, err := service.Create(context.Background(), security.System(), CreateInput{
		FileID: 1,
		Title:  &title,
		Params: params,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Title == nil || *created.Title != "Hero image" {
		t.Fatalf("title = %#v", created.Title)
	}

	params["meta_alt"] = "mutated"
	params["nested"].(map[string]any)["width"] = 1
	stored, err := service.Get(context.Background(), security.System(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Params["meta_alt"] != "Hero" ||
		fmt.Sprint(
			stored.Params["nested"].(map[string]any)["width"],
		) != "1200" {
		t.Fatalf("stored params = %#v", stored.Params)
	}

	resolved, err := service.Resolve(context.Background(), security.System(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.File.ID != 1 ||
		resolved.File.MIMEType != "image/png" {
		t.Fatalf("resolved = %#v", resolved)
	}

	*stored.Title = "changed"
	stored.Params["meta_alt"] = "changed"
	again, err := service.Get(context.Background(), security.System(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if *again.Title != "Hero image" ||
		again.Params["meta_alt"] != "Hero" {
		t.Fatalf("media clone leaked mutation: %#v", again)
	}
}

func TestServiceCreateRejectsInvalidFileAndParams(t *testing.T) {
	service, _ := newServiceForTest(t, nil)

	if _, err := service.Create(context.Background(), security.System(), CreateInput{
		FileID: 99,
	}); !errors.Is(err, ErrInvalidReference) {
		t.Fatalf("missing file error = %v", err)
	}

	if _, err := service.Create(context.Background(), security.System(), CreateInput{
		FileID: 1,
		Params: map[string]any{
			"invalid": func() {},
		},
	}); err == nil {
		t.Fatal("non-JSON params were accepted")
	}

	blank := " \t "
	created, err := service.Create(context.Background(), security.System(), CreateInput{
		FileID: 1,
		Title:  &blank,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Title != nil || len(created.Params) != 0 {
		t.Fatalf("normalized media = %#v", created)
	}
}

func TestServiceUpdateRevalidatesCurrentUsages(t *testing.T) {
	const imageUsage UsageKind = "test.image"
	service, repository := newServiceForTest(t, FilePolicies{
		imageUsage: func(
			_ context.Context,
			linkedFile file.File,
			_ Usage,
		) error {
			if !strings.HasPrefix(linkedFile.MIMEType, "image/") {
				return ErrInvalidReference
			}
			return nil
		},
	})

	created, err := service.Create(context.Background(), security.System(), CreateInput{
		FileID: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	repository.usages[created.ID] = []Usage{{
		Kind:    imageUsage,
		OwnerID: 17,
	}}

	if _, err := service.Update(context.Background(), security.System(), UpdateInput{
		ID:     created.ID,
		FileID: 2,
		Params: map[string]any{},
	}); !errors.Is(err, ErrInvalidReference) {
		t.Fatalf("non-image replacement error = %v", err)
	}
	stored, err := service.Get(context.Background(), security.System(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.FileID != 1 {
		t.Fatalf("failed update changed file to %d", stored.FileID)
	}

	title := "Updated"
	updated, err := service.Update(context.Background(), security.System(), UpdateInput{
		ID:     created.ID,
		FileID: 1,
		Title:  &title,
		Params: map[string]any{"meta_alt": "Updated"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title == nil || *updated.Title != title {
		t.Fatalf("updated media = %#v", updated)
	}
}

func TestServiceUpdateRejectsUnknownUsageAndDeleteIsMetadataOnly(
	t *testing.T,
) {
	service, repository := newServiceForTest(t, nil)
	created, err := service.Create(context.Background(), security.System(), CreateInput{
		FileID: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	repository.usages[created.ID] = []Usage{{
		Kind:    "future.owner",
		OwnerID: 9,
	}}

	if _, err := service.Update(context.Background(), security.System(), UpdateInput{
		ID:     created.ID,
		FileID: 1,
	}); !errors.Is(err, ErrUnknownUsage) {
		t.Fatalf("unknown usage error = %v", err)
	}

	if err := service.Delete(context.Background(), security.System(), created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Get(
		context.Background(),
		security.System(),
		created.ID,
	); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted media error = %v", err)
	}
}
