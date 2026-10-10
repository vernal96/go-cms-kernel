package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	kernel "github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

type fieldUploadFiles struct {
	file.ManagementService
	puts int
	disk filesystem.Code
	path string
	data []byte
}

func (s *fieldUploadFiles) EnsureFolderPath(_ context.Context, _ security.Actor, disk filesystem.Code, path string) (file.Folder, error) {
	s.disk, s.path = disk, path
	return file.Folder{ID: 17, Storage: disk}, nil
}

func (s *fieldUploadFiles) UploadAvailable(_ context.Context, _ security.Actor, input file.UploadInput) (file.File, error) {
	s.puts++
	var err error
	s.data, err = io.ReadAll(input.Content)
	return file.File{ID: 31, Storage: input.Storage, FolderID: input.FolderID, Name: input.Name, Size: int64(len(s.data)), MIMEType: http.DetectContentType(s.data)}, err
}

type uploadDefinitionsModule struct{ siteSettingsHTTPModule }

func (m uploadDefinitionsModule) Build(context.Context, kernel.ModuleContext) (kernel.ModuleRuntime, error) {
	return m, nil
}

func (uploadDefinitionsModule) Widgets() []widget.Widget {
	return []widget.Widget{widget.Functional{Description: widget.Definition{Reference: widget.NewRef("asset"), Label: "Asset", Description: "Uploaded asset", Fields: uploadDefinitions()}}}
}

func uploadDefinitions() []field.Definition {
	return []field.Definition{{Key: "asset", Label: "Asset", Type: field.TypeFile, Options: field.FileOptions{Disk: "trusted", VirtualPath: "trusted/exact/path", SettingsCode: "image", MIMETypes: []string{"image/png"}}}}
}

func fieldUploadServices(t *testing.T) (*Files, *Sites, *Resources, *fieldUploadFiles) {
	t.Helper()
	ctx := context.Background()
	factory, err := kernel.NewProfileRuntimeFactory(extensionTestDatabaseResolver{}, kernel.RuntimeServices{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), EventBus: extensionTestBus{}})
	if err != nil {
		t.Fatal(err)
	}
	blueprint, err := factory.Compile(ctx, kernel.Profile{
		Code: "settings", Params: uploadDefinitions(), Modules: []kernel.Module{uploadDefinitionsModule{}},
		Templates: []template.Definition{{Code: "page", Label: "Page", Fields: []field.Definition{{Key: "rows", Label: "Rows", Type: field.TypeRepeater, Options: field.RepeaterOptions{Fields: uploadDefinitions()}}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := site.NewRuntimeFromBlueprint(ctx, site.Site{ID: 7, Domain: "upload.test", Name: "Upload", ProfileCode: "settings", Locale: "en"}, blueprint)
	if err != nil {
		t.Fatal(err)
	}
	auth := authorization{sites: extensionTestSites{runtime: runtime}, authorizer: managementAuthorizer{}, policy: AllowAllSitesPolicy{}}
	sites := &Sites{authorization: auth, profileSource: siteSettingsHTTPProfiles{blueprint: blueprint}}
	code := template.Code("page")
	resources := &Resources{authorization: auth, resourceRepo: &extensionTestResources{item: resource.Resource{ID: 9, SiteID: 7, Template: &code}}}
	storage := &fieldUploadFiles{}
	return &Files{files: storage, authorizer: managementAuthorizer{}}, sites, resources, storage
}

func TestFieldUploadResolvesSiteResourceAndWidgetOwners(t *testing.T) {
	for _, target := range []FileFieldTarget{
		{Owner: "site", ProfileCode: "settings", FieldPath: []string{"asset"}},
		{Owner: "site", SiteID: 7, FieldPath: []string{"asset"}},
		{Owner: "site", SiteID: 7, ProfileCode: "settings", FieldPath: []string{"asset"}},
		{Owner: "resource", SiteID: 7, TemplateCode: "page", FieldPath: []string{"rows", "0", "asset"}},
		{Owner: "resource", SiteID: 7, ResourceID: 9, FieldPath: []string{"rows", "2", "asset"}},
		{Owner: "widget", SiteID: 7, WidgetCode: "core_asset", FieldPath: []string{"asset"}},
		{Owner: "widget", SiteID: 7, ResourceID: 9, WidgetCode: "core_asset", FieldPath: []string{"asset"}},
	} {
		files, sites, resources, storage := fieldUploadServices(t)
		result, err := files.UploadFieldFile(context.Background(), security.User(1), sites, resources, target, "asset.png", bytes.NewBufferString("\x89PNG\r\n\x1a\n"))
		if err != nil {
			t.Fatalf("target %#v: %v", target, err)
		}
		if storage.puts != 1 || storage.disk != "trusted" || storage.path != "trusted/exact/path" || result.ID != 31 || result.FolderID == nil || *result.FolderID != 17 {
			t.Fatalf("target %#v: storage=%#v, result=%#v", target, storage, result)
		}
	}
}

func TestFieldUploadEnforcesPermissionsAndSiteOwnershipBeforeStorage(t *testing.T) {
	for _, code := range []permission.Code{FileCreatePermission, SiteCreatePermission, SiteUpdatePermission, ResourceCreatePermission, ResourceUpdatePermission} {
		files, sites, resources, storage := fieldUploadServices(t)
		denied := managementAuthorizer{denied: map[permission.Code]error{code: security.ErrForbidden}}
		files.authorizer, sites.authorizer, resources.authorizer = denied, denied, denied
		target := FileFieldTarget{Owner: "resource", SiteID: 7, ResourceID: 9, FieldPath: []string{"rows", "0", "asset"}}
		switch code {
		case SiteCreatePermission:
			target = FileFieldTarget{Owner: "site", ProfileCode: "settings", FieldPath: []string{"asset"}}
		case SiteUpdatePermission:
			target = FileFieldTarget{Owner: "site", SiteID: 7, FieldPath: []string{"asset"}}
		case ResourceCreatePermission:
			target.ResourceID, target.TemplateCode = 0, "page"
		}
		_, err := files.UploadFieldFile(context.Background(), security.User(1), sites, resources, target, "asset.png", bytes.NewBufferString("\x89PNG\r\n\x1a\n"))
		if !errors.Is(err, security.ErrForbidden) || storage.puts != 0 || storage.path != "" {
			t.Fatalf("permission %s: err=%v, storage=%#v", code, err, storage)
		}
	}
	files, sites, resources, storage := fieldUploadServices(t)
	resources.resourceRepo = &extensionTestResources{item: resource.Resource{ID: 9, SiteID: 8}}
	_, err := files.UploadFieldFile(context.Background(), security.User(1), sites, resources, FileFieldTarget{Owner: "resource", SiteID: 7, ResourceID: 9, TemplateCode: "page", FieldPath: []string{"rows", "0", "asset"}}, "asset.png", bytes.NewBufferString("\x89PNG\r\n\x1a\n"))
	if !errors.Is(err, resource.ErrNotFound) || storage.puts != 0 {
		t.Fatalf("cross-site upload: %v, puts=%d", err, storage.puts)
	}
}

func fieldMultipart(t *testing.T, target string, content string, extra string) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	if err := writer.WriteField("target", target); err != nil {
		t.Fatal(err)
	}
	if extra != "" {
		if err := writer.WriteField(extra, "untrusted"); err != nil {
			t.Fatal(err)
		}
	}
	part, err := writer.CreateFormFile("file", "asset.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return body, writer.FormDataContentType()
}

func TestFieldUploadHTTPRejectsOverridesSpoofedMIMEAndMissingAuthentication(t *testing.T) {
	for _, tc := range []struct {
		target  string
		content string
		extra   string
		actor   security.Actor
		status  int
	}{
		{`{"owner":"site","profile_code":"settings","field_path":["asset"]}`, "\x89PNG\r\n\x1a\n", "", security.User(1), 201},
		{`{"owner":"site","profile_code":"settings","field_path":["asset"]}`, "plain text", "", security.User(1), 422},
		{`{"owner":"site","profile_code":"settings","field_path":["unknown"]}`, "\x89PNG\r\n\x1a\n", "", security.User(1), 422},
		{`{"owner":"site","profile_code":"settings","field_path":["asset"],"disk":"untrusted"}`, "\x89PNG\r\n\x1a\n", "", security.User(1), 400},
		{`{"owner":"site","profile_code":"settings","field_path":["asset"]}`, "\x89PNG\r\n\x1a\n", "folder_id", security.User(1), 400},
		{`{"owner":"site","profile_code":"settings","field_path":["asset"]}`, "\x89PNG\r\n\x1a\n", "", security.Guest(), 401},
	} {
		files, sites, resources, storage := fieldUploadServices(t)
		router := chi.NewRouter()
		registerFileRoutes(router, &filesHTTP{files: files, sites: sites, resources: resources, maxUploadSize: 1 << 20, uploadTimeout: time.Minute})
		body, contentType := fieldMultipart(t, tc.target, tc.content, tc.extra)
		request := httptest.NewRequest(http.MethodPost, "/files/field-uploads", body)
		request.Header.Set("Content-Type", contentType)
		request = request.WithContext(httptransport.WithActor(request.Context(), tc.actor))
		response := httptest.NewRecorder()
		httptransport.RequireAuthenticated(router).ServeHTTP(response, request)
		if response.Code != tc.status {
			t.Fatalf("target=%s, extra=%s, status=%d: %s", tc.target, tc.extra, response.Code, response.Body.String())
		}
		if tc.status != 201 && (storage.puts != 0 || storage.path != "") {
			t.Fatalf("rejected request mutated storage: %#v", storage)
		}
		if tc.status == 201 {
			var result FilesystemItemDTO
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.ID != 31 || result.Storage != "trusted" {
				t.Fatalf("upload response=%s: %v", response.Body.String(), err)
			}
		}
	}
}
