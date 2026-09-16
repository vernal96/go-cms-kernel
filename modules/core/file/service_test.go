package file

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel/filesystem"
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

type deniedCreateAuthorizer struct{}

func (deniedCreateAuthorizer) Check(_ context.Context, _ security.Actor, code permission.Code) error {
	if code == createPermission {
		return security.ErrForbidden
	}
	return nil
}

type memoryDisk struct {
	code       filesystem.Code
	visibility filesystem.Visibility
	mu         sync.Mutex
	objects    map[string][]byte
	puts       int
	deletes    int
	deleteErr  error
}

func newMemoryDisk(
	code filesystem.Code,
	visibility filesystem.Visibility,
) *memoryDisk {
	return &memoryDisk{
		code:       code,
		visibility: visibility,
		objects:    make(map[string][]byte),
	}
}

func (d *memoryDisk) Code() filesystem.Code             { return d.code }
func (d *memoryDisk) Visibility() filesystem.Visibility { return d.visibility }
func (*memoryDisk) Ping(context.Context) error          { return nil }
func (*memoryDisk) Close() error                        { return nil }
func (d *memoryDisk) PutNew(
	_ context.Context,
	key string,
	source io.Reader,
	_ string,
) error {
	content, err := io.ReadAll(source)
	if err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, exists := d.objects[key]; exists {
		return filesystem.ErrConflict
	}
	d.puts++
	d.objects[key] = content
	return nil
}
func (d *memoryDisk) Open(
	_ context.Context,
	key string,
) (io.ReadCloser, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	content, exists := d.objects[key]
	if !exists {
		return nil, filesystem.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(content)), nil
}
func (d *memoryDisk) Delete(_ context.Context, key string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.deletes++
	if d.deleteErr != nil {
		return d.deleteErr
	}
	delete(d.objects, key)
	return nil
}
func (d *memoryDisk) URL(
	_ context.Context,
	reference filesystem.Reference,
) (string, error) {
	if d.visibility != filesystem.VisibilityPublic {
		return "", filesystem.ErrInvalidVisibility
	}
	return "https://public.test/" + reference.ID, nil
}
func (d *memoryDisk) TemporaryURL(
	_ context.Context,
	reference filesystem.Reference,
	_ time.Time,
) (string, error) {
	if d.visibility != filesystem.VisibilityPrivate {
		return "", filesystem.ErrInvalidVisibility
	}
	return "https://private.test/" + reference.ID, nil
}

type memoryDisks map[filesystem.Code]filesystem.Disk

func (d memoryDisks) Disk(code filesystem.Code) (filesystem.Disk, bool) {
	result, exists := d[code]
	return result, exists
}

type memoryRepository struct {
	mu              sync.Mutex
	nextFolderID    FolderID
	nextFileID      ID
	folders         map[FolderID]Folder
	files           map[ID]File
	createFileError error
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{
		nextFolderID: 1,
		nextFileID:   1,
		folders:      make(map[FolderID]Folder),
		files:        make(map[ID]File),
	}
}

func (r *memoryRepository) NameAvailable(
	_ context.Context,
	storage filesystem.Code,
	folderID *FolderID,
	name string,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.nameAvailable(storage, folderID, name, 0, 0)
}

func (r *memoryRepository) nameAvailable(
	storage filesystem.Code,
	folderID *FolderID,
	name string,
	excludeFolder FolderID,
	excludeFile ID,
) error {
	for id, item := range r.folders {
		if id != excludeFolder &&
			item.Storage == storage &&
			equalFolderIDs(item.ParentID, folderID) &&
			item.Name == name {
			return ErrConflict
		}
	}
	for id, item := range r.files {
		if id != excludeFile &&
			item.Storage == storage &&
			equalFolderIDs(item.FolderID, folderID) &&
			item.Name == name {
			return ErrConflict
		}
	}
	return nil
}

func (r *memoryRepository) CreateFolder(
	_ context.Context,
	item Folder,
) (Folder, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.nameAvailable(
		item.Storage, item.ParentID, item.Name, 0, 0,
	); err != nil {
		return Folder{}, err
	}
	item.ID = r.nextFolderID
	r.nextFolderID++
	item.CreatedAt = time.Now()
	item.UpdatedAt = item.CreatedAt
	r.folders[item.ID] = CloneFolder(item)
	return CloneFolder(item), nil
}

func (r *memoryRepository) FolderByID(
	_ context.Context,
	id FolderID,
) (Folder, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, exists := r.folders[id]
	if !exists {
		return Folder{}, ErrFolderNotFound
	}
	return CloneFolder(item), nil
}

func (r *memoryRepository) ListFolders(
	_ context.Context,
	storage filesystem.Code,
	parentID *FolderID,
) ([]Folder, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []Folder
	for _, item := range r.folders {
		if item.Storage == storage && equalFolderIDs(item.ParentID, parentID) {
			result = append(result, CloneFolder(item))
		}
	}
	return result, nil
}

func TestServiceResolvesNestedFolderPathWithinStorage(t *testing.T) {
	repository := newMemoryRepository()
	service, err := NewService(repository, memoryDisks{"private": newMemoryDisk("private", filesystem.VisibilityPrivate)}, testAuthorizer{})
	if err != nil {
		t.Fatal(err)
	}
	mailFolder, err := service.CreateFolder(context.Background(), security.User(1), CreateFolderInput{Storage: "private", Name: "mail"})
	if err != nil {
		t.Fatal(err)
	}
	uploads, err := service.CreateFolder(context.Background(), security.User(1), CreateFolderInput{Storage: "private", ParentID: &mailFolder.ID, Name: "uploads"})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := service.ResolveFolder(context.Background(), security.User(1), "private", "mail/uploads")
	if err != nil || resolved.ID != uploads.ID {
		t.Fatalf("resolved folder = %#v, %v", resolved, err)
	}
	if _, err := service.ResolveFolder(context.Background(), security.User(1), "private", "../mail"); err == nil {
		t.Fatal("unsafe folder path was accepted")
	}
}

func TestServiceEnsuresNestedFolderPathIdempotentlyAndConcurrently(t *testing.T) {
	repository := newMemoryRepository()
	service, err := NewService(repository, memoryDisks{"private": newMemoryDisk("private", filesystem.VisibilityPrivate)}, testAuthorizer{})
	if err != nil {
		t.Fatal(err)
	}
	const workers = 8
	results := make(chan Folder, workers)
	errorsFound := make(chan error, workers)
	var group sync.WaitGroup
	for index := 0; index < workers; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			folder, ensureErr := service.EnsureFolderPath(context.Background(), security.User(1), "private", "mail/uploads")
			if ensureErr != nil {
				errorsFound <- ensureErr
				return
			}
			results <- folder
		}()
	}
	group.Wait()
	close(results)
	close(errorsFound)
	for ensureErr := range errorsFound {
		t.Fatal(ensureErr)
	}
	var expected FolderID
	for folder := range results {
		if expected == 0 {
			expected = folder.ID
		}
		if folder.ID != expected || folder.Storage != "private" || folder.Name != "uploads" {
			t.Fatalf("ensured folder = %#v, expected ID %d", folder, expected)
		}
	}
	if len(repository.folders) != 2 {
		t.Fatalf("concurrent ensure created %d folders", len(repository.folders))
	}
	again, err := service.EnsureFolderPath(context.Background(), security.User(1), "private", "mail/uploads")
	if err != nil || again.ID != expected || len(repository.folders) != 2 {
		t.Fatalf("repeated ensure = %#v, %v, folders=%d", again, err, len(repository.folders))
	}
}

func TestServiceEnsureFolderPathRejectsUnsafePathsAndRequiresCreatePermission(t *testing.T) {
	for _, unsafe := range []string{"../mail", "/mail/uploads", "mail/../../secret", `mail\\uploads`} {
		repository := newMemoryRepository()
		service, _ := NewService(repository, memoryDisks{"private": newMemoryDisk("private", filesystem.VisibilityPrivate)}, testAuthorizer{})
		if _, err := service.EnsureFolderPath(context.Background(), security.User(1), "private", unsafe); err == nil || len(repository.folders) != 0 {
			t.Fatalf("unsafe path %q = %v, folders=%d", unsafe, err, len(repository.folders))
		}
	}
	repository := newMemoryRepository()
	service, _ := NewService(repository, memoryDisks{"private": newMemoryDisk("private", filesystem.VisibilityPrivate)}, deniedCreateAuthorizer{})
	if _, err := service.EnsureFolderPath(context.Background(), security.User(1), "private", "mail/uploads"); !errors.Is(err, security.ErrForbidden) || len(repository.folders) != 0 {
		t.Fatalf("denied ensure = %v, folders=%d", err, len(repository.folders))
	}
}

func (r *memoryRepository) CreateFile(
	_ context.Context,
	item File,
) (File, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.createFileError != nil {
		return File{}, r.createFileError
	}
	if err := r.nameAvailable(
		item.Storage, item.FolderID, item.Name, 0, 0,
	); err != nil {
		return File{}, err
	}
	item.ID = r.nextFileID
	r.nextFileID++
	item.CreatedAt = time.Now()
	item.UpdatedAt = item.CreatedAt
	r.files[item.ID] = Clone(item)
	return Clone(item), nil
}

func (r *memoryRepository) FileByID(
	_ context.Context,
	id ID,
) (File, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, exists := r.files[id]
	if !exists {
		return File{}, ErrNotFound
	}
	return Clone(item), nil
}

func (r *memoryRepository) ListFiles(
	_ context.Context,
	storage filesystem.Code,
	folderID *FolderID,
) ([]File, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []File
	for _, item := range r.files {
		if item.Storage == storage && equalFolderIDs(item.FolderID, folderID) {
			result = append(result, Clone(item))
		}
	}
	return result, nil
}

func (r *memoryRepository) MoveFile(
	_ context.Context,
	_ *security.UserID,
	id ID,
	folderID *FolderID,
) (File, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, exists := r.files[id]
	if !exists {
		return File{}, ErrNotFound
	}
	if err := r.nameAvailable(
		item.Storage, folderID, item.Name, 0, id,
	); err != nil {
		return File{}, err
	}
	item.FolderID = cloneFolderID(folderID)
	item.UpdatedAt = time.Now()
	r.files[id] = Clone(item)
	return Clone(item), nil
}

func (r *memoryRepository) MoveFolder(
	_ context.Context,
	_ *security.UserID,
	id FolderID,
	parentID *FolderID,
) (Folder, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, exists := r.folders[id]
	if !exists {
		return Folder{}, ErrFolderNotFound
	}
	if err := r.nameAvailable(
		item.Storage, parentID, item.Name, id, 0,
	); err != nil {
		return Folder{}, err
	}
	item.ParentID = cloneFolderID(parentID)
	item.UpdatedAt = time.Now()
	r.folders[id] = CloneFolder(item)
	return CloneFolder(item), nil
}

func (r *memoryRepository) DeleteFile(
	ctx context.Context,
	id ID,
	deletePhysical DeletePhysical,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.files[id]; !exists {
		return ErrNotFound
	}
	selected := map[ID]struct{}{id: {}}
	r.expandFileChildren(selected)
	items := r.selectedFiles(selected)
	if err := deletePhysical(ctx, items); err != nil {
		return err
	}
	for selectedID := range selected {
		delete(r.files, selectedID)
	}
	return nil
}

func (r *memoryRepository) DeleteFolder(
	ctx context.Context,
	id FolderID,
	deletePhysical DeletePhysical,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.folders[id]; !exists {
		return ErrFolderNotFound
	}
	folders := map[FolderID]struct{}{id: {}}
	for changed := true; changed; {
		changed = false
		for folderID, item := range r.folders {
			if item.ParentID != nil {
				if _, exists := folders[*item.ParentID]; exists {
					if _, included := folders[folderID]; !included {
						folders[folderID] = struct{}{}
						changed = true
					}
				}
			}
		}
	}
	selected := make(map[ID]struct{})
	for fileID, item := range r.files {
		if item.FolderID != nil {
			if _, exists := folders[*item.FolderID]; exists {
				selected[fileID] = struct{}{}
			}
		}
	}
	r.expandFileChildren(selected)
	if err := deletePhysical(ctx, r.selectedFiles(selected)); err != nil {
		return err
	}
	for fileID := range selected {
		delete(r.files, fileID)
	}
	for folderID := range folders {
		delete(r.folders, folderID)
	}
	return nil
}

func (r *memoryRepository) expandFileChildren(selected map[ID]struct{}) {
	for changed := true; changed; {
		changed = false
		for id, item := range r.files {
			if item.ParentID != nil {
				if _, exists := selected[*item.ParentID]; exists {
					if _, included := selected[id]; !included {
						selected[id] = struct{}{}
						changed = true
					}
				}
			}
		}
	}
}

func (r *memoryRepository) selectedFiles(selected map[ID]struct{}) []File {
	result := make([]File, 0, len(selected))
	for id := range selected {
		result = append(result, Clone(r.files[id]))
	}
	return result
}

func TestServiceUploadMeasuresContentAndCompensatesDatabaseFailure(t *testing.T) {
	repository := newMemoryRepository()
	public := newMemoryDisk("public", filesystem.VisibilityPublic)
	service, err := NewService(repository, memoryDisks{"public": public}, testAuthorizer{})
	if err != nil {
		t.Fatal(err)
	}

	folder, err := service.CreateFolder(context.Background(), security.System(), CreateFolderInput{
		Storage: "public",
		Name:    "images",
	})
	if err != nil {
		t.Fatal(err)
	}
	content := "hello file"
	created, err := service.Upload(context.Background(), security.System(), UploadInput{
		FolderID: &folder.ID,
		Storage:  "public",
		Name:     "hello.txt",
		Content:  strings.NewReader(content),
	})
	if err != nil {
		t.Fatal(err)
	}
	checksum := sha256.Sum256([]byte(content))
	if created.Size != int64(len(content)) ||
		created.MIMEType != "text/plain; charset=utf-8" ||
		created.ChecksumSHA256 != hex.EncodeToString(checksum[:]) {
		t.Fatalf("created file = %#v", created)
	}
	if rawURL, err := service.URL(
		context.Background(),
		security.System(),
		created.ID,
	); err != nil || rawURL == "" {
		t.Fatalf("public URL = %q, %v", rawURL, err)
	}

	repository.createFileError = errors.New("database unavailable")
	_, err = service.Upload(context.Background(), security.System(), UploadInput{
		Storage: "public",
		Name:    "orphan.txt",
		Content: strings.NewReader("orphan"),
	})
	if err == nil || !strings.Contains(err.Error(), "database unavailable") {
		t.Fatalf("upload error = %v", err)
	}
	public.mu.Lock()
	objectCount := len(public.objects)
	public.mu.Unlock()
	if objectCount != 1 {
		t.Fatalf("physical object count after compensation = %d", objectCount)
	}
}

func TestServicePropagatesAuditActor(t *testing.T) {
	t.Parallel()

	repository := newMemoryRepository()
	public := newMemoryDisk("public", filesystem.VisibilityPublic)
	service, err := NewService(
		repository,
		memoryDisks{"public": public},
		testAuthorizer{},
	)
	if err != nil {
		t.Fatal(err)
	}
	actor := security.User(77)
	folder, err := service.CreateFolder(
		context.Background(),
		actor,
		CreateFolderInput{Storage: "public", Name: "audit"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if folder.CreatedBy == nil ||
		*folder.CreatedBy != 77 ||
		folder.UpdatedBy == nil ||
		*folder.UpdatedBy != 77 {
		t.Fatalf("folder audit = %#v", folder)
	}
	created, err := service.Upload(
		context.Background(),
		actor,
		UploadInput{
			FolderID: &folder.ID,
			Storage:  "public",
			Name:     "audit.txt",
			Content:  strings.NewReader("audit"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if created.CreatedBy == nil ||
		*created.CreatedBy != 77 ||
		created.UpdatedBy == nil ||
		*created.UpdatedBy != 77 {
		t.Fatalf("file audit = %#v", created)
	}
}

func TestServiceUsesSharedNamespaceAndVirtualMoves(t *testing.T) {
	repository := newMemoryRepository()
	public := newMemoryDisk("public", filesystem.VisibilityPublic)
	service, _ := NewService(repository, memoryDisks{"public": public}, testAuthorizer{})

	if _, err := service.CreateFolder(
		context.Background(),
		security.System(),
		CreateFolderInput{Storage: "public", Name: "same"},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Upload(context.Background(), security.System(), UploadInput{
		Storage: "public",
		Name:    "same",
		Content: strings.NewReader("content"),
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("shared namespace error = %v", err)
	}

	target, _ := service.CreateFolder(
		context.Background(),
		security.System(),
		CreateFolderInput{Storage: "public", Name: "target"},
	)
	created, _ := service.Upload(context.Background(), security.System(), UploadInput{
		Storage: "public",
		Name:    "movable",
		Content: strings.NewReader("content"),
	})
	puts, deletes := public.puts, public.deletes
	moved, err := service.MoveFile(context.Background(), security.System(), MoveFileInput{
		ID:       created.ID,
		FolderID: &target.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if moved.Path != created.Path ||
		public.puts != puts ||
		public.deletes != deletes {
		t.Fatalf("move touched physical storage: %#v", moved)
	}
}

func TestServiceFolderDeleteIncludesCrossStorageDerivativesAndRetries(t *testing.T) {
	repository := newMemoryRepository()
	public := newMemoryDisk("public", filesystem.VisibilityPublic)
	private := newMemoryDisk("private", filesystem.VisibilityPrivate)
	service, _ := NewService(repository, memoryDisks{
		"public":  public,
		"private": private,
	}, testAuthorizer{})
	folder, _ := service.CreateFolder(context.Background(), security.System(), CreateFolderInput{
		Storage: "public",
		Name:    "source",
	})
	original, _ := service.Upload(context.Background(), security.System(), UploadInput{
		FolderID: &folder.ID,
		Storage:  "public",
		Name:     "original",
		Content:  strings.NewReader("original"),
	})
	derived, _ := service.Upload(context.Background(), security.System(), UploadInput{
		Storage:  "private",
		Name:     "derived",
		ParentID: &original.ID,
		Content:  strings.NewReader("derived"),
	})

	private.deleteErr = errors.New("private unavailable")
	if err := service.DeleteFolder(
		context.Background(),
		security.System(),
		folder.ID,
	); err == nil {
		t.Fatal("expected partial physical delete error")
	}
	if _, err := service.GetFile(
		context.Background(),
		security.System(),
		original.ID,
	); err != nil {
		t.Fatalf("metadata was removed after failed delete: %v", err)
	}

	private.deleteErr = nil
	if err := service.DeleteFolder(context.Background(), security.System(), folder.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetFile(
		context.Background(),
		security.System(),
		derived.ID,
	); !errors.Is(err, ErrNotFound) {
		t.Fatalf("derived file still exists: %v", err)
	}
}

func equalFolderIDs(first, second *FolderID) bool {
	if first == nil || second == nil {
		return first == nil && second == nil
	}
	return *first == *second
}

var _ filesystem.Disk = (*memoryDisk)(nil)
var _ Repository = (*memoryRepository)(nil)
