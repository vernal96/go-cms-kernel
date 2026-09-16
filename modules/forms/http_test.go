package forms

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/vernal96/go-cms-kernel/connectors/localstorage"
	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/security"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

func multipartSubmission(t *testing.T, mimeType string) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	for key, value := range map[string]string{MandatoryConsentCode: "true", MandatoryCaptchaCode: "valid", "email": "person@example.test"} {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="documents"; filename="document.txt"`)
	header.Set("Content-Type", mimeType)
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, "payload"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return body, writer.FormDataContentType()
}

func publicHTTPService(t *testing.T, repository *repositoryStub) (*Service, *UploadSpool) {
	t.Helper()
	disk, err := localstorage.New(context.Background(), localstorage.Config{Code: "forms-http", Visibility: filesystem.VisibilityPrivate, Root: t.TempDir(), BaseURL: "https://files.example.test", SigningKey: strings.Repeat("k", 32)})
	if err != nil {
		t.Fatal(err)
	}
	spool, err := NewUploadSpool(5, disk)
	if err != nil {
		t.Fatal(err)
	}
	elements, err := newElementCatalog()
	if err != nil {
		t.Fatal(err)
	}
	captcha := &captchaStub{}
	service, err := NewService(5, repository, formsFieldResolver(), elements, newActionRegistry(), map[string]CaptchaProvider{"test": captcha}, "test", allowAuthorizer{}, &filesStub{}, spool, PublicLimits{
		MaxRequestSize: 1 << 20, MaxScalarFields: 20, MaxScalarValueSize: 1 << 10,
		MaxUploadFileSize: 1 << 20, MaxUploadCount: 4, MaxTotalUploadBytes: 1 << 20,
		SubmissionTimeout: time.Second, RateLimit: 20, RateWindow: time.Minute, RateEntries: 100,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return service, spool
}

func publicHTTPFormDetail() FormDetail {
	consentID, captchaID, emailID, uploadID, submitID := FieldID(1), FieldID(2), FieldID(3), FieldID(4), ElementID(1)
	return FormDetail{
		Form: Form{ID: 9, SiteID: 5, Code: "feedback", Name: "Feedback", Enabled: true},
		Fields: []FormField{
			{ID: consentID, FormID: 9, Code: MandatoryConsentCode, Type: FieldTypeConsent, Label: "Согласие", Required: true, Options: ConsentOptions{}},
			{ID: captchaID, FormID: 9, Code: MandatoryCaptchaCode, Type: FieldTypeCaptcha, Label: "CAPTCHA", Required: true, Options: CaptchaOptions{Provider: "test"}},
			{ID: emailID, FormID: 9, Code: "email", Type: field.TypeEmail, Label: "Email", Required: true},
			{ID: uploadID, FormID: 9, Code: "documents", Type: FieldTypeUpload, Label: "Документы", Required: true, Options: UploadOptions{MIMETypes: []string{"text/plain"}}},
		},
		Elements: []Element{{ID: submitID, FormID: 9, Code: MandatorySubmitCode, Type: ElementSubmitButton, Config: []byte(`{"label":"Отправить"}`)}},
		Layout:   []LayoutNode{{ID: 1, Kind: LayoutField, FieldID: &consentID}, {ID: 2, Kind: LayoutField, FieldID: &captchaID, Position: 1}, {ID: 3, Kind: LayoutField, FieldID: &emailID, Position: 2}, {ID: 4, Kind: LayoutField, FieldID: &uploadID, Position: 3}, {ID: 5, Kind: LayoutElement, ElementID: &submitID, Position: 4}},
		Statuses: []Status{{ID: 1, FormID: 9, Code: DefaultStatusCode, Name: "Новый", IsDefault: true}},
	}
}

func servePublicSubmit(t *testing.T, service *Service, body io.Reader, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	router := chi.NewRouter()
	router.Post("/forms/{code}/submit", (&publicFormsHTTP{service: service}).submit)
	request := httptest.NewRequest(http.MethodPost, "/forms/feedback/submit", body)
	request.Header.Set("Content-Type", contentType)
	request = request.WithContext(httptransport.WithActor(request.Context(), security.Guest()))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestPublicMultipartSubmissionStreamsUploadsAndReturnsMinimalResponse(t *testing.T) {
	repository := &repositoryStub{detail: publicHTTPFormDetail()}
	service, _ := publicHTTPService(t, repository)
	body, contentType := multipartSubmission(t, "text/plain")
	response := servePublicSubmit(t, service, body, contentType)
	if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != `{"success":true}` {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	if repository.createCalls != 1 || len(repository.record.Uploads) != 1 || repository.record.Uploads[0].SpoolReference == "" {
		t.Fatalf("submission record = %#v", repository.record)
	}
	if strings.Contains(response.Body.String(), "77") || strings.Contains(response.Body.String(), "forms-spool") {
		t.Fatalf("public response leaked internals: %s", response.Body.String())
	}
}

func TestPublicMultipartRejectsMIMEAndRollsBackSpoolOnDatabaseFailure(t *testing.T) {
	t.Run("MIME", func(t *testing.T) {
		repository := &repositoryStub{detail: publicHTTPFormDetail()}
		service, _ := publicHTTPService(t, repository)
		body, contentType := multipartSubmission(t, "application/octet-stream")
		response := servePublicSubmit(t, service, body, contentType)
		if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), `"documents":["mime"]`) || repository.createCalls != 0 {
			t.Fatalf("response = %d %s, calls=%d", response.Code, response.Body.String(), repository.createCalls)
		}
	})

	t.Run("database rollback", func(t *testing.T) {
		repository := &repositoryStub{detail: publicHTTPFormDetail(), createErr: errors.New("database secret path")}
		service, spool := publicHTTPService(t, repository)
		body, contentType := multipartSubmission(t, "text/plain")
		response := servePublicSubmit(t, service, body, contentType)
		if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "secret") {
			t.Fatalf("response = %d %s", response.Code, response.Body.String())
		}
		scan, err := spool.scannerProvider.OpenPrefixScan(context.Background(), spool.prefix)
		if err != nil {
			t.Fatal(err)
		}
		page, err := scan.Next(context.Background(), 10)
		_ = scan.Close()
		if err != nil || len(page.Keys) != 0 {
			t.Fatalf("spool after rollback = %#v, %v", page.Keys, err)
		}
	})
}

func TestPublicAndManagementErrorsUseSafeDistinctMappings(t *testing.T) {
	publicCases := []struct {
		err    error
		status int
		code   string
	}{
		{ErrNotFound, http.StatusNotFound, "not_found"},
		{ErrRequestTooLarge, http.StatusRequestEntityTooLarge, "request_too_large"},
		{ErrRateLimited, http.StatusTooManyRequests, "rate_limited"},
		{ErrRuntimeDraining, http.StatusServiceUnavailable, "unavailable"},
		{errors.New("SQL /private/path"), http.StatusInternalServerError, "internal_error"},
	}
	for _, testCase := range publicCases {
		response := httptest.NewRecorder()
		writePublicError(response, testCase.err)
		if response.Code != testCase.status || !strings.Contains(response.Body.String(), testCase.code) || strings.Contains(response.Body.String(), "/private/path") {
			t.Fatalf("public error %v = %d %s", testCase.err, response.Code, response.Body.String())
		}
	}
	response := httptest.NewRecorder()
	writeManagementError(response, security.ErrForbidden)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "forbidden") {
		t.Fatalf("management forbidden = %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	writePublicError(response, FieldValidationErrors{"email": {"email"}})
	var validation map[string]any
	if json.Unmarshal(response.Body.Bytes(), &validation) != nil || response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("validation response = %d %s", response.Code, response.Body.String())
	}
}
