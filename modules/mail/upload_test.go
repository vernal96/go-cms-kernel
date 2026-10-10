package mail

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

type uploadRecordingFiles struct {
	file.ManagementService
	folderCalls int
	disk        filesystem.Code
	path        string
	input       file.UploadInput
	content     []byte
}

func (f *uploadRecordingFiles) EnsureFolderPath(_ context.Context, _ security.Actor, disk filesystem.Code, path string) (file.Folder, error) {
	f.folderCalls++
	f.disk, f.path = disk, path
	return file.Folder{ID: 42, Storage: disk}, nil
}

func (f *uploadRecordingFiles) UploadAvailable(_ context.Context, _ security.Actor, input file.UploadInput) (file.File, error) {
	f.input = input
	var err error
	f.content, err = io.ReadAll(input.Content)
	if err != nil {
		return file.File{}, err
	}
	now := time.Date(2026, time.October, 10, 0, 0, 0, 0, time.UTC)
	return file.File{ID: 77, FolderID: input.FolderID, Storage: input.Storage, Name: input.Name, MIMEType: "image/png", Size: int64(len(f.content)), CreatedAt: now, UpdatedAt: now}, nil
}

type permissionRecorder struct {
	denied permission.Code
	seen   []permission.Code
}

func (a *permissionRecorder) Check(_ context.Context, _ security.Actor, code permission.Code) error {
	a.seen = append(a.seen, code)
	if code == a.denied {
		return security.ErrForbidden
	}
	return nil
}

func (a *permissionRecorder) Allowed(ctx context.Context, actor security.Actor, codes []permission.Code) ([]permission.Code, error) {
	allowed := make([]permission.Code, 0, len(codes))
	for _, code := range codes {
		if err := a.Check(ctx, actor, code); err != nil {
			if errors.Is(err, security.ErrForbidden) {
				continue
			}
			return nil, err
		}
		allowed = append(allowed, code)
	}
	return allowed, nil
}

func mailUploadService(t *testing.T, authorizer security.Authorizer) (*Service, *uploadRecordingFiles, *memoryRepository) {
	t.Helper()
	renderer, _ := testRenderer(t, SenderPolicy{})
	repository := &memoryRepository{template: mailTemplate()}
	repository.template.ID = 3
	service, err := NewService(5, repository, renderer, authorizer, testUsers{}, nil, Limits{MaxRecipients: 100, MaxMessageSize: 1 << 20, MaxAttachmentSize: 1 << 20}, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	service.uploadFiles = &uploadRecordingFiles{}
	return service, service.uploadFiles.(*uploadRecordingFiles), repository
}

func TestUploadVariableFileUsesTemplateDiskAndSniffsMIMEBeforeStorage(t *testing.T) {
	t.Parallel()
	service, uploads, repository := mailUploadService(t, allowAuthorizer{})
	repository.template.Variables = append(repository.template.Variables, field.Definition{
		Key: "logo", Type: field.TypeFile, Label: "Logo",
		Options: field.FileOptions{Disk: "mail-private", VirtualPath: "templates/welcome", SettingsCode: "attachment", MIMETypes: []string{"image/png"}},
	})
	png := []byte("\x89PNG\r\n\x1a\ntrusted png bytes")
	created, err := service.UploadVariableFile(context.Background(), security.User(9), 3, []string{"logo"}, "logo.png", bytes.NewReader(png))
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != 77 || uploads.disk != "mail-private" || uploads.path != "templates/welcome" || uploads.input.Storage != "mail-private" || uploads.input.FolderID == nil || *uploads.input.FolderID != 42 {
		t.Fatalf("upload target = file %#v, disk=%q path=%q input=%#v", created, uploads.disk, uploads.path, uploads.input)
	}
	if !bytes.Equal(uploads.content, png) {
		t.Fatalf("stored bytes differ: got %q", uploads.content)
	}

	repository.template.Variables[3].Options = field.FileOptions{Disk: "documents", VirtualPath: "mail", SettingsCode: "attachment", MIMETypes: []string{"application/pdf"}}
	uploads.folderCalls = 0
	_, err = service.UploadVariableFile(context.Background(), security.User(9), 3, []string{"invoice"}, "invoice.pdf", strings.NewReader("plain text masquerading as pdf"))
	var validation field.ValidationErrors
	if !errors.As(err, &validation) && !errors.Is(err, ErrInvalid) {
		t.Fatalf("disallowed MIME error = %v", err)
	}
	if uploads.folderCalls != 0 {
		t.Fatalf("rejected MIME touched storage %d times", uploads.folderCalls)
	}
}

func TestUploadVariableFileRequiresTemplateMessageAndCoreFilePermissions(t *testing.T) {
	t.Parallel()
	for _, denied := range []permission.Code{TemplateReadPermission, MessageCreatePermission, coreFileCreatePermission} {
		authorizer := &permissionRecorder{denied: denied}
		service, uploads, _ := mailUploadService(t, authorizer)
		_, err := service.UploadVariableFile(context.Background(), security.User(9), 3, []string{"invoice"}, "invoice.pdf", strings.NewReader("content"))
		if !errors.Is(err, security.ErrForbidden) {
			t.Fatalf("permission %q error = %v", denied, err)
		}
		if uploads.folderCalls != 0 {
			t.Fatalf("permission %q allowed storage mutation", denied)
		}
	}
}

func TestMailVariableUploadHTTPRejectsStorageOverridesAndReturnsCoreFileShape(t *testing.T) {
	t.Parallel()
	service, uploads, repository := mailUploadService(t, allowAuthorizer{})
	repository.template.Variables = append(repository.template.Variables, field.Definition{
		Key: "logo", Type: field.TypeFile, Label: "Logo",
		Options: field.FileOptions{Disk: "mail-private", VirtualPath: "templates/welcome", SettingsCode: "attachment", MIMETypes: []string{"image/png"}},
	})
	handler, err := NewHTTPHandler(service)
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Mount("/api/sites/{siteID}/mail", handler)

	request, contentType := multipartUploadRequest(t, map[string]string{"field_path": `["logo"]`}, "\x89PNG\r\n\x1a\nbytes")
	response := httptest.NewRecorder()
	request = request.WithContext(httptransport.WithActor(request.Context(), security.User(9)))
	router.ServeHTTP(response, requestWithPath(request, "/api/sites/5/mail/send/templates/3/variables/files", contentType))
	if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), `"kind":"file"`) || !strings.Contains(response.Body.String(), `"id":77`) || !strings.Contains(response.Body.String(), `"storage":"mail-private"`) {
		t.Fatalf("upload response = %d, %s", response.Code, response.Body.String())
	}

	request, contentType = multipartUploadRequest(t, map[string]string{"field_path": `["logo"]`, "disk": "public"}, "\x89PNG\r\n\x1a\nbytes")
	response = httptest.NewRecorder()
	request = request.WithContext(httptransport.WithActor(request.Context(), security.User(9)))
	router.ServeHTTP(response, requestWithPath(request, "/api/sites/5/mail/send/templates/3/variables/files", contentType))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("storage override status = %d, %s", response.Code, response.Body.String())
	}
	if uploads.folderCalls != 1 {
		t.Fatalf("storage override reached storage: folder calls=%d", uploads.folderCalls)
	}
}

func multipartUploadRequest(t *testing.T, target map[string]string, content string) (*http.Request, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	targetJSON := `{"field_path":` + target["field_path"]
	if disk, exists := target["disk"]; exists {
		targetJSON += `,"disk":"` + disk + `"`
	}
	targetJSON += `}`
	if err := writer.WriteField("target", targetJSON); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("file", "logo.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return httptest.NewRequest(http.MethodPost, "/", &body), writer.FormDataContentType()
}

func requestWithPath(request *http.Request, path, contentType string) *http.Request {
	request.URL.Path = path
	request.Header.Set("Content-Type", contentType)
	return request
}
