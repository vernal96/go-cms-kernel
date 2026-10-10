package forms

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	corefile "github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

type uploadFormRepository struct {
	Repository
	detail FormDetail
	siteID site.ID
	formID FormID
}

func (r *uploadFormRepository) FormDetail(_ context.Context, siteID site.ID, formID FormID) (FormDetail, error) {
	r.siteID, r.formID = siteID, formID
	if siteID != 5 || formID != 9 {
		return FormDetail{}, ErrNotFound
	}
	return r.detail, nil
}

type uploadBuilderFiles struct {
	corefile.ManagementService
	disk filesystem.Code
	path string
	puts int
}

func (s *uploadBuilderFiles) EnsureFolderPath(_ context.Context, _ security.Actor, disk filesystem.Code, path string) (corefile.Folder, error) {
	s.disk, s.path = disk, path
	return corefile.Folder{ID: 19, Storage: disk}, nil
}

func (s *uploadBuilderFiles) UploadAvailable(_ context.Context, _ security.Actor, input corefile.UploadInput) (corefile.File, error) {
	s.puts++
	content, err := io.ReadAll(input.Content)
	return corefile.File{ID: 31, FolderID: input.FolderID, Storage: input.Storage, Name: input.Name, Size: int64(len(content)), MIMEType: http.DetectContentType(content)}, err
}

type uploadDeniedAuthorizer struct {
	allowAuthorizer
	code permission.Code
}

func (a uploadDeniedAuthorizer) Check(_ context.Context, _ security.Actor, code permission.Code) error {
	if code == a.code {
		return security.ErrForbidden
	}
	return nil
}

func builderUploadService(t *testing.T) (*Service, *uploadFormRepository, *uploadBuilderFiles) {
	t.Helper()
	service, _ := publicHTTPService(t, &repositoryStub{})
	repository := &uploadFormRepository{detail: FormDetail{Form: Form{ID: 9, SiteID: 5}, Fields: []FormField{{ID: 11, FormID: 9, Code: "asset", Type: field.TypeFile, Options: testImageFileOptions()}}}}
	files := &uploadBuilderFiles{}
	service.repository, service.files = repository, files
	return service, repository, files
}

func TestBuilderUploadUsesRegisteredElementOrSavedFieldOptions(t *testing.T) {
	for _, target := range []FileFieldTarget{
		{Owner: "element", ElementType: ElementImage, FieldPath: []string{"file_id"}},
		{Owner: "field", FieldPath: []string{"asset"}},
	} {
		service, repository, files := builderUploadService(t)
		item, err := service.UploadFieldFile(context.Background(), security.User(1), 9, target, "image.png", bytes.NewBufferString("\x89PNG\r\n\x1a\n"))
		if err != nil {
			t.Fatal(err)
		}
		if repository.siteID != 5 || repository.formID != 9 || files.puts != 1 || files.disk != "public" || files.path != "forms/images" || item.FolderID == nil || *item.FolderID != 19 {
			t.Fatalf("trusted builder destination: repository=%#v files=%#v item=%#v", repository, files, item)
		}
	}
}

func TestBuilderUploadRejectsMissingOwnerAndPermissionsBeforeStorage(t *testing.T) {
	target := FileFieldTarget{Owner: "element", ElementType: ElementImage, FieldPath: []string{"file_id"}}
	for _, code := range []permission.Code{FormUpdatePermission, permission.MustCode("core", "file", permission.Create)} {
		service, _, files := builderUploadService(t)
		service.authorizer = uploadDeniedAuthorizer{code: code}
		_, err := service.UploadFieldFile(context.Background(), security.User(1), 9, target, "image.png", bytes.NewBufferString("\x89PNG\r\n\x1a\n"))
		if !errors.Is(err, security.ErrForbidden) || files.puts != 0 || files.path != "" {
			t.Fatalf("permission %s: err=%v files=%#v", code, err, files)
		}
	}
	service, _, files := builderUploadService(t)
	_, err := service.UploadFieldFile(context.Background(), security.User(1), 99, target, "image.png", bytes.NewBufferString("\x89PNG\r\n\x1a\n"))
	if !errors.Is(err, ErrNotFound) || files.puts != 0 || files.path != "" {
		t.Fatalf("missing owner: err=%v files=%#v", err, files)
	}
}

func TestBuilderUploadHTTPEnforcesMultipartAndMIME(t *testing.T) {
	for _, tc := range []struct {
		target  string
		content string
		status  int
	}{
		{`{"owner":"element","element_type":"image","field_path":["file_id"]}`, "\x89PNG\r\n\x1a\n", 201},
		{`{"owner":"element","element_type":"image","field_path":["file_id"]}`, "spoofed png", 422},
		{`{"owner":"element","element_type":"image","field_path":["file_id"],"disk":"untrusted"}`, "\x89PNG\r\n\x1a\n", 400},
		{`{"owner":"element","element_type":"image","field_path":["alt"]}`, "\x89PNG\r\n\x1a\n", 422},
	} {
		service, _, files := builderUploadService(t)
		handler, err := NewManagementHTTPHandler(service)
		if err != nil {
			t.Fatal(err)
		}
		body := &bytes.Buffer{}
		writer := multipart.NewWriter(body)
		if err := writer.WriteField("target", tc.target); err != nil {
			t.Fatal(err)
		}
		part, err := writer.CreateFormFile("file", "image.png")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(part, tc.content); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/forms/9/file-fields/uploads", body)
		request.Header.Set("Content-Type", writer.FormDataContentType())
		request = request.WithContext(httptransport.WithActor(request.Context(), security.User(1)))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != tc.status {
			t.Fatalf("target=%s status=%d body=%s", tc.target, response.Code, response.Body.String())
		}
		if tc.status != 201 && (files.puts != 0 || files.path != "") {
			t.Fatalf("rejected upload changed storage: %#v", files)
		}
	}
}
