package media

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/file"
	image "github.com/vernal96/go-cms-kernel/modules/core/image"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

type imageRepo struct {
	Repository
	mu   sync.Mutex
	item Media
	fail bool
}

func (r *imageRepo) ByID(context.Context, ID) (Media, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return Clone(r.item), nil
}
func (r *imageRepo) UpdateImage(ctx context.Context, _ *security.UserID, m Media, expected time.Time, validate ValidateUsages) (Media, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail {
		return Media{}, errors.New("update failed")
	}
	if !expected.Equal(r.item.UpdatedAt) {
		return Media{}, ErrImageConflict
	}
	if err := validate(ctx, nil); err != nil {
		return Media{}, err
	}
	m.UpdatedAt = r.item.UpdatedAt.Add(time.Microsecond)
	r.item = Clone(m)
	return Clone(m), nil
}

type imageFiles struct {
	file.ManagementService
	mu          sync.Mutex
	repo        *imageRepo
	items       map[file.ID]file.File
	next        file.ID
	opened      []file.ID
	deleted     []file.ID
	cleanupFail bool
	uploadFail  bool
	references  map[file.ID]bool
}

func (f *imageFiles) GetFile(_ context.Context, _ security.Actor, id file.ID) (file.File, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.items[id]
	if !ok {
		return v, file.ErrNotFound
	}
	return file.Clone(v), nil
}
func (f *imageFiles) Open(ctx context.Context, a security.Actor, id file.ID) (file.OpenedFile, error) {
	v, err := f.GetFile(ctx, a, id)
	if err != nil {
		return file.OpenedFile{}, err
	}
	f.mu.Lock()
	f.opened = append(f.opened, id)
	f.mu.Unlock()
	return file.OpenedFile{File: v, Body: io.NopCloser(bytes.NewBufferString("original bytes"))}, nil
}
func (f *imageFiles) UploadAvailable(_ context.Context, _ security.Actor, in file.UploadInput) (file.File, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.uploadFail {
		return file.File{}, errors.New("upload failed")
	}
	f.next++
	v := file.File{ID: f.next, Storage: in.Storage, FolderID: in.FolderID, ParentID: in.ParentID, MIMEType: "image/png"}
	f.items[v.ID] = file.Clone(v)
	return v, nil
}
func (f *imageFiles) DeleteFile(ctx context.Context, _ security.Actor, id file.ID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.references[id] {
		return file.ErrInUse
	}
	if f.cleanupFail {
		return errors.New("storage down")
	}
	m, _ := f.repo.ByID(ctx, 1)
	if m.FileID == id {
		return file.ErrInUse
	}
	delete(f.items, id)
	f.deleted = append(f.deleted, id)
	return nil
}

type imageProcessor struct {
	barrier chan struct{}
	arrived chan struct{}
}

func (p imageProcessor) Transform(ctx context.Context, r io.Reader, _ image.TransformOptions) (image.Result, error) {
	raw, _ := io.ReadAll(r)
	if string(raw) != "original bytes" {
		return image.Result{}, errors.New("wrong source")
	}
	if p.arrived != nil {
		p.arrived <- struct{}{}
		<-p.barrier
	}
	return image.Result{Bytes: []byte("edited bytes"), MIMEType: "image/png", Width: 10, Height: 10}, ctx.Err()
}
func imageFixture(t *testing.T, p image.Processor) (*ImageService, *imageRepo, *imageFiles) {
	t.Helper()
	r := &imageRepo{item: Media{ID: 1, FileID: 1, UpdatedAt: time.Now().UTC(), Params: map[string]any{"other": "retained"}}}
	f := &imageFiles{repo: r, items: map[file.ID]file.File{1: {ID: 1, Storage: "private", MIMEType: "image/png"}}, next: 1}
	s, err := NewImageService(r, f, nil, testAuthorizer{}, p, image.DefaultLimits(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return s, r, f
}
func TestImageEditRootSiblingCleanupRestore(t *testing.T) {
	s, r, f := imageFixture(t, imageProcessor{})
	ctx := context.Background()
	a := security.User(1)
	first, err := s.Edit(ctx, a, 1, r.item.UpdatedAt, image.TransformOptions{Width: 10, Height: 10})
	if err != nil {
		t.Fatal(err)
	}
	if first.Current.ID == 1 || *first.Current.ParentID != 1 || first.Media.FileID != first.Current.ID || !first.CanRestore {
		t.Fatalf("first: %+v", first)
	}
	second, err := s.Edit(ctx, a, 1, first.Media.UpdatedAt, image.TransformOptions{Width: 20, Height: 20})
	if err != nil {
		t.Fatal(err)
	}
	if *second.Current.ParentID != 1 || second.Current.ID == first.Current.ID {
		t.Fatalf("not a sibling: %+v", second)
	}
	if len(f.opened) != 2 || f.opened[0] != 1 || f.opened[1] != 1 {
		t.Fatalf("sources %v", f.opened)
	}
	if _, ok := f.items[first.Current.ID]; ok {
		t.Fatal("old derivative retained")
	}
	if f.items[1].ID != 1 {
		t.Fatal("original deleted")
	}
	restored, err := s.Restore(ctx, a, 1, second.Media.UpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Media.FileID != 1 || restored.CanRestore || restored.Transform != nil || len(f.items) != 1 {
		t.Fatalf("restore: %+v files:%v", restored, f.items)
	}
	if _, ok := restored.Media.Params["image"]; ok {
		t.Fatal("metadata retained")
	}
	if restored.Media.Params["other"] != "retained" {
		t.Fatal("other metadata lost")
	}
	again, err := s.Restore(ctx, a, 1, restored.Media.UpdatedAt)
	if err != nil || !again.Media.UpdatedAt.Equal(restored.Media.UpdatedAt) {
		t.Fatal("restore not idempotent", err)
	}
}
func TestImageFailedSwitchCompensates(t *testing.T) {
	s, r, f := imageFixture(t, imageProcessor{})
	r.fail = true
	_, err := s.Edit(context.Background(), security.User(1), 1, r.item.UpdatedAt, image.TransformOptions{Width: 10, Height: 10})
	if err == nil || len(f.items) != 1 || r.item.FileID != 1 || len(f.deleted) != 1 {
		t.Fatalf("failed compensation: err %v files %v", err, f.items)
	}
}
func TestImageCleanupFailureKeepsNewReference(t *testing.T) {
	s, r, f := imageFixture(t, imageProcessor{})
	first, err := s.Edit(context.Background(), security.User(1), 1, r.item.UpdatedAt, image.TransformOptions{})
	if err != nil {
		t.Fatal(err)
	}
	f.cleanupFail = true
	next, err := s.Edit(context.Background(), security.User(1), 1, first.Media.UpdatedAt, image.TransformOptions{})
	if err != nil || r.item.FileID != next.Current.ID || next.Current.ID == first.Current.ID {
		t.Fatal("successful switch rolled back", err)
	}
}
func TestConcurrentImageEditCompensatesLoser(t *testing.T) {
	p := imageProcessor{barrier: make(chan struct{}), arrived: make(chan struct{}, 2)}
	s, r, f := imageFixture(t, p)
	expected := r.item.UpdatedAt
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, err := s.Edit(context.Background(), security.User(1), 1, expected, image.TransformOptions{})
			results <- err
		}()
	}
	<-p.arrived
	<-p.arrived
	close(p.barrier)
	a, b := <-results, <-results
	if (a == nil) == (b == nil) {
		t.Fatalf("results %v %v", a, b)
	}
	if a != nil && !errors.Is(a, ErrImageConflict) || b != nil && !errors.Is(b, ErrImageConflict) {
		t.Fatal(a, b)
	}
	if len(f.items) != 2 || f.items[r.item.FileID].ID == 0 || r.item.FileID == 1 {
		t.Fatalf("incorrect final files %v media %v", f.items, r.item)
	}
}

func TestReferencedOldDerivativeIsRetained(t *testing.T) {
	s, r, f := imageFixture(t, imageProcessor{})
	first, err := s.Edit(context.Background(), security.User(1), 1, r.item.UpdatedAt, image.TransformOptions{})
	if err != nil {
		t.Fatal(err)
	}
	f.references = map[file.ID]bool{first.Current.ID: true}
	next, err := s.Edit(context.Background(), security.User(1), 1, first.Media.UpdatedAt, image.TransformOptions{})
	if err != nil || next.Current.ID == first.Current.ID || len(f.items) != 3 {
		t.Fatal("referenced old file lost", err)
	}
}
func TestFailedUploadDoesNotChangeMedia(t *testing.T) {
	s, r, f := imageFixture(t, imageProcessor{})
	f.uploadFail = true
	_, err := s.Edit(context.Background(), security.User(1), 1, r.item.UpdatedAt, image.TransformOptions{})
	if err == nil || r.item.FileID != 1 || len(f.items) != 1 {
		t.Fatal("failed upload mutated media", err)
	}
}

type imageDenyAuthorizer struct{ code permission.Code }

func (a imageDenyAuthorizer) Check(_ context.Context, _ security.Actor, code permission.Code) error {
	if code == a.code {
		return security.ErrForbidden
	}
	return nil
}
func TestImagePermissionsBeforeProcessing(t *testing.T) {
	for _, code := range []permission.Code{"core.media.read", "core.media.update", "core.file.create"} {
		t.Run(string(code), func(t *testing.T) {
			s, r, f := imageFixture(t, imageProcessor{})
			s.media.authorizer = imageDenyAuthorizer{code}
			_, err := s.Edit(context.Background(), security.User(1), 1, r.item.UpdatedAt, image.TransformOptions{})
			if !errors.Is(err, security.ErrForbidden) || len(f.opened) != 0 || len(f.items) != 1 {
				t.Fatal("unauthorized image operation", err)
			}
		})
	}
}
