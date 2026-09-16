package management

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

type filesystemManagementService struct {
	file.ManagementService
	listing    file.BrowserListing
	catalog    []filesystem.DiskInfo
	catalogErr error
	resolved   file.Folder
	ensured    file.Folder
}

func (s filesystemManagementService) ResolveFolder(context.Context, security.Actor, filesystem.Code, string) (file.Folder, error) {
	return s.resolved, nil
}

func (s filesystemManagementService) EnsureFolderPath(context.Context, security.Actor, filesystem.Code, string) (file.Folder, error) {
	return s.ensured, nil
}

func (s filesystemManagementService) Disks(context.Context, security.Actor) ([]filesystem.DiskInfo, error) {
	if s.catalogErr != nil || s.catalog != nil {
		return s.catalog, s.catalogErr
	}
	return []filesystem.DiskInfo{{Code: "public", Label: "Публичные файлы", Visibility: filesystem.VisibilityPublic}}, nil
}

func (s filesystemManagementService) Browse(context.Context, security.Actor, filesystem.Code, *file.FolderID) (file.BrowserListing, error) {
	return s.listing, nil
}

func TestFilesystemManagementReturnsDiskListingAndCapabilities(t *testing.T) {
	now := time.Now().UTC()
	management := &Files{
		files: filesystemManagementService{listing: file.BrowserListing{
			Storage: "public", Visibility: filesystem.VisibilityPublic,
			Folders: []file.FolderEntry{{Folder: file.Folder{ID: 3, Storage: "public", Name: "images", CreatedAt: now, UpdatedAt: now}, ItemCount: 4}},
			Files:   []file.File{{ID: 5, Storage: "public", Name: "logo.png", MIMEType: "image/png", Size: 128, CreatedAt: now, UpdatedAt: now}},
		}},
		authorizer: managementAuthorizer{denied: map[permission.Code]error{FileDeletePermission: security.ErrForbidden}},
	}
	disks, err := management.FilesystemDisks(context.Background(), security.User(1))
	if err != nil {
		t.Fatal(err)
	}
	if len(disks.Items) != 1 || disks.Items[0].Code != "public" || disks.Items[0].Label != "Публичные файлы" || !disks.Permissions.Read || disks.Permissions.Delete {
		t.Fatalf("filesystem disks = %#v", disks)
	}
	listing, err := management.BrowseFilesystem(context.Background(), security.User(1), "public", nil)
	if err != nil {
		t.Fatal(err)
	}
	if listing.Disk != disks.Items[0] {
		t.Fatalf("listing disk = %#v; catalog disk = %#v", listing.Disk, disks.Items[0])
	}
	if len(listing.Items) != 2 || listing.Items[0].Kind != file.ItemFolder || *listing.Items[0].ItemCount != 4 || *listing.Items[1].MIMEType != "image/png" {
		t.Fatalf("filesystem listing = %#v", listing)
	}
}

func TestBrowseUsesArbitraryCatalogMetadataAndPropagatesCatalogErrors(t *testing.T) {
	info := filesystem.DiskInfo{Code: "archive", Label: "Архив документов", Visibility: filesystem.VisibilityPrivate}
	unavailable := errors.New("catalog unavailable")
	for _, tc := range []struct {
		name       string
		catalog    []filesystem.DiskInfo
		catalogErr error
		wantErr    error
	}{
		{name: "arbitrary disk", catalog: []filesystem.DiskInfo{{Code: "media", Label: "Медиа", Visibility: filesystem.VisibilityPublic}, info}},
		{name: "missing disk", catalog: []filesystem.DiskInfo{}, wantErr: file.ErrStorageNotFound},
		{name: "catalog error", catalogErr: unavailable, wantErr: unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			management := &Files{
				files:      filesystemManagementService{listing: file.BrowserListing{Storage: info.Code, Visibility: info.Visibility}, catalog: tc.catalog, catalogErr: tc.catalogErr},
				authorizer: managementAuthorizer{denied: map[permission.Code]error{}},
			}
			listing, err := management.BrowseFilesystem(context.Background(), security.User(1), info.Code, nil)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if err == nil && listing.Disk != filesystemDiskDTO(info) {
				t.Fatalf("disk = %#v", listing.Disk)
			}
		})
	}
}

func TestFileValidationErrorPreservesAuthorizationAndConflicts(t *testing.T) {
	for _, expected := range []error{security.ErrForbidden, file.ErrConflict, file.ErrStorageMismatch, file.ErrInUse} {
		if actual := fileValidationError(expected); !errors.Is(actual, expected) {
			t.Fatalf("error %v became %v", expected, actual)
		}
	}
	if actual := fileValidationError(file.ErrInvalidInput); !errors.Is(actual, ErrValidation) {
		t.Fatalf("invalid input = %v", actual)
	}
	internal := errors.New("storage unavailable")
	if actual := fileValidationError(internal); !errors.Is(actual, internal) || errors.Is(actual, ErrValidation) {
		t.Fatalf("internal error = %v", actual)
	}
}

func TestFilesRoutesUseUniversalNamespace(t *testing.T) {
	t.Parallel()
	handler := &filesHTTP{
		files: &Files{
			files:      filesystemManagementService{},
			authorizer: managementAuthorizer{denied: map[permission.Code]error{}},
		},
		maxUploadSize: 1 << 20,
		uploadTimeout: time.Minute,
	}
	router := chi.NewRouter()
	registerFileRoutes(router, handler)
	request := httptest.NewRequest(http.MethodGet, "/files/disks", nil)
	request = request.WithContext(httptransport.WithActor(request.Context(), security.User(1)))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("universal route = %d, %s", response.Code, response.Body.String())
	}
	legacy := httptest.NewRecorder()
	router.ServeHTTP(legacy, httptest.NewRequest(http.MethodGet, "/filesystem/disks", nil))
	if legacy.Code != http.StatusNotFound {
		t.Fatalf("legacy route status = %d", legacy.Code)
	}
}

func TestFilesRouteResolvesConfiguredFolderPath(t *testing.T) {
	now := time.Now().UTC()
	handler := &filesHTTP{files: &Files{
		files:      filesystemManagementService{resolved: file.Folder{ID: 7, Storage: "private", Name: "mail", CreatedAt: now, UpdatedAt: now}},
		authorizer: managementAuthorizer{denied: map[permission.Code]error{}},
	}}
	router := chi.NewRouter()
	registerFileRoutes(router, handler)
	request := httptest.NewRequest(http.MethodGet, "/files/folders/resolve?disk=private&path=mail", nil)
	request = request.WithContext(httptransport.WithActor(request.Context(), security.User(1)))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"id":7`) {
		t.Fatalf("resolve route = %d, %s", response.Code, response.Body.String())
	}
}

func TestFilesRouteEnsuresConfiguredFolderPath(t *testing.T) {
	now := time.Now().UTC()
	handler := &filesHTTP{files: &Files{
		files:      filesystemManagementService{ensured: file.Folder{ID: 9, Storage: "private", Name: "uploads", CreatedAt: now, UpdatedAt: now}},
		authorizer: managementAuthorizer{denied: map[permission.Code]error{}},
	}}
	router := chi.NewRouter()
	registerFileRoutes(router, handler)
	request := httptest.NewRequest(http.MethodPost, "/files/folders/ensure", strings.NewReader(`{"disk":"private","path":"mail/uploads"}`))
	request = request.WithContext(httptransport.WithActor(request.Context(), security.User(1)))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"id":9`) {
		t.Fatalf("ensure route = %d, %s", response.Code, response.Body.String())
	}
}
