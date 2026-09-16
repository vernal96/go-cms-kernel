package mail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/eventbus"
	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/job"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	coreuser "github.com/vernal96/go-cms-kernel/modules/core/user"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

type testFields map[field.TypeCode]field.Type

func (r testFields) FieldType(code field.TypeCode) (field.Type, bool) {
	value, ok := r[code]
	return value, ok
}

func standardFields() testFields {
	result := testFields{}
	for _, item := range field.StandardTypes() {
		result[item.Code()] = item
	}
	return result
}

type testFiles struct {
	items     map[file.ID]file.File
	body      map[file.ID]string
	urls      map[file.ID]string
	denyUsers bool
}

func (f *testFiles) GetFile(_ context.Context, actor security.Actor, id file.ID) (file.File, error) {
	if f.denyUsers && actor.IsUser() {
		return file.File{}, security.ErrForbidden
	}
	item, ok := f.items[id]
	if !ok {
		return file.File{}, file.ErrNotFound
	}
	return item, nil
}
func (f *testFiles) URL(_ context.Context, _ security.Actor, id file.ID) (string, error) {
	value, ok := f.urls[id]
	if !ok {
		return "", errors.New("not public")
	}
	return value, nil
}
func (f *testFiles) Open(_ context.Context, _ security.Actor, id file.ID) (file.OpenedFile, error) {
	item, err := f.GetFile(context.Background(), security.System(), id)
	if err != nil {
		return file.OpenedFile{}, err
	}
	return file.OpenedFile{File: item, Body: io.NopCloser(strings.NewReader(f.body[id]))}, nil
}

func testRenderer(t *testing.T, policy SenderPolicy) (*Renderer, *testFiles) {
	t.Helper()
	files := &testFiles{items: map[file.ID]file.File{7: {ID: 7, Name: "invoice.pdf", MIMEType: "application/pdf", Size: 3, ChecksumSHA256: "abc"}}, body: map[file.ID]string{7: "pdf"}, urls: map[file.ID]string{7: "https://example.com/files/7"}}
	renderer, err := NewRenderer(standardFields(), files, site.Site{ID: 5, ProfileCode: "test", Domain: "example.com", Locale: "ru-RU"}, nil, RendererConfig{SenderPolicy: policy})
	if err != nil {
		t.Fatal(err)
	}
	return renderer, files
}

func mailTemplate() Template {
	return Template{
		SiteID: 5, Code: "welcome", Name: "Welcome", Enabled: true,
		From:    AddressTemplate{Name: "Site", Email: "noreply@example.com"},
		To:      []AddressTemplate{{Name: "{{data.name}}", Email: "{{data.email}}"}},
		Subject: "Hello {{data.name}}", ContentType: ContentHTML,
		HTMLBody: "<p>{{data.name}} {{data.count}}</p>",
		Variables: []field.Definition{
			{Key: "name", Type: field.TypeString, Label: "Name"},
			{Key: "email", Type: field.TypeEmail, Label: "Email"},
			{Key: "count", Type: field.TypeInteger, Label: "Count"},
			{Key: "invoice", Type: field.TypeFile, Label: "Invoice", Options: field.FileOptions{}},
		},
	}
}

func TestRendererUsesTypedOptionalValuesAndEscapesHTML(t *testing.T) {
	t.Parallel()
	renderer, _ := testRenderer(t, SenderPolicy{AllowedDomains: []string{"example.com"}})
	result, err := renderer.Render(context.Background(), mailTemplate(), map[string]any{"name": `<script>alert(1)</script>`, "email": "person@example.net", "count": float64(2)}, security.User(9))
	if err != nil {
		t.Fatal(err)
	}
	if result.Subject != "Hello <script>alert(1)</script>" || !strings.Contains(result.HTMLBody, "&lt;script&gt;") || !strings.Contains(result.HTMLBody, "2") {
		t.Fatalf("rendered = %#v", result)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("warnings = %#v", result.Warnings)
	}

	missing, err := renderer.Render(context.Background(), mailTemplate(), map[string]any{"email": "person@example.net"}, security.User(9))
	if err != nil {
		t.Fatal(err)
	}
	if len(missing.Warnings) != 4 {
		t.Fatalf("missing warnings = %#v", missing.Warnings)
	}
}

func TestRendererRejectsHeadersRecipientsAndSenderSpoofing(t *testing.T) {
	t.Parallel()
	renderer, _ := testRenderer(t, SenderPolicy{AllowedAddresses: []string{"noreply@example.com"}})
	template := mailTemplate()
	for _, test := range []struct {
		name   string
		mutate func(*Template)
		values map[string]any
		target error
	}{
		{"header injection", func(item *Template) { item.Subject = "ok\r\nBcc: bad@example.com" }, map[string]any{"email": "person@example.net"}, ErrInvalid},
		{"invalid recipient", func(*Template) {}, map[string]any{"email": "not-an-email"}, ErrInvalid},
		{"no recipient", func(item *Template) { item.To = []AddressTemplate{{Email: "{{data.name}}"}} }, nil, ErrNoRecipients},
		{"sender policy", func(item *Template) { item.From.Email = "spoof@evil.test" }, map[string]any{"email": "person@example.net"}, ErrSenderNotAllowed},
	} {
		item := template
		test.mutate(&item)
		_, err := renderer.Render(context.Background(), item, test.values, security.User(9))
		if !errors.Is(err, test.target) {
			t.Fatalf("%s error = %v", test.name, err)
		}
	}
}

func TestRendererValidatesUsedAddressRowsBeforeRendering(t *testing.T) {
	t.Parallel()
	renderer, _ := testRenderer(t, SenderPolicy{})
	for _, test := range []struct {
		name   string
		mutate func(*Template)
		target error
	}{
		{name: "from email", mutate: func(item *Template) { item.From = AddressTemplate{Name: "Sender"} }, target: ErrInvalid},
		{name: "to name without email", mutate: func(item *Template) { item.To = []AddressTemplate{{Name: "Recipient"}} }, target: ErrInvalid},
		{name: "cc name without email", mutate: func(item *Template) { item.CC = []AddressTemplate{{Name: "Copy"}} }, target: ErrInvalid},
		{name: "bcc name without email", mutate: func(item *Template) { item.BCC = []AddressTemplate{{Name: "Hidden"}} }, target: ErrInvalid},
		{name: "reply-to email", mutate: func(item *Template) { item.ReplyTo = &AddressTemplate{Name: "Replies"} }, target: ErrInvalid},
		{name: "only blank recipient rows", mutate: func(item *Template) { item.To = []AddressTemplate{{}, {Name: "  ", Email: "  "}} }, target: ErrNoRecipients},
	} {
		t.Run(test.name, func(t *testing.T) {
			template := mailTemplate()
			test.mutate(&template)
			if err := renderer.ValidateTemplate(template); !errors.Is(err, test.target) {
				t.Fatalf("validation error = %v", err)
			}
		})
	}

	template := mailTemplate()
	template.To = append(template.To, AddressTemplate{})
	template.CC = []AddressTemplate{{}}
	template.BCC = []AddressTemplate{{Name: "  ", Email: "  "}}
	if err := renderer.ValidateTemplate(template); err != nil {
		t.Fatalf("blank optional address rows were not ignored: %v", err)
	}
}

func TestRendererDefaultsSenderPolicyToCurrentSiteDomain(t *testing.T) {
	t.Parallel()
	renderer, _ := testRenderer(t, SenderPolicy{})
	template := mailTemplate()
	template.From.Email = "sender@other.test"
	_, err := renderer.Render(context.Background(), template, map[string]any{"email": "person@example.net"}, security.User(9))
	if !errors.Is(err, ErrSenderNotAllowed) {
		t.Fatalf("sender outside current site domain error = %v", err)
	}
}

func TestRendererResolvesStaticAndVariableAttachments(t *testing.T) {
	t.Parallel()
	renderer, _ := testRenderer(t, SenderPolicy{})
	template := mailTemplate()
	staticID := file.ID(7)
	template.ContentType, template.TextBody, template.HTMLBody = ContentText, "Invoice {{data.invoice}}", ""
	template.Attachments = []AttachmentTemplate{
		{Source: AttachmentStatic, FileID: &staticID, FilenameTemplate: "static-{{data.name}}.pdf"},
		{Source: AttachmentVariable, Variable: "data.invoice"},
	}
	result, err := renderer.Render(context.Background(), template, map[string]any{"name": "Alice", "email": "a@example.net", "invoice": float64(7)}, security.User(9))
	if err != nil {
		t.Fatal(err)
	}
	if result.TextBody != "Invoice https://example.com/files/7" || len(result.Attachments) != 2 || result.Attachments[0].Filename != "static-Alice.pdf" || result.Attachments[1].FileID == nil || *result.Attachments[1].FileID != 7 {
		t.Fatalf("rendered attachments = %#v", result)
	}
	missing, err := renderer.Render(context.Background(), template, map[string]any{"email": "a@example.net"}, security.User(9))
	if err != nil {
		t.Fatal(err)
	}
	if len(missing.Attachments) != 1 {
		t.Fatalf("missing variable attachment = %#v", missing)
	}
}

func TestRendererPreservesRequiredFieldsAndUsesPrivateSiteVariables(t *testing.T) {
	t.Parallel()
	required := true
	params := []field.Definition{{Key: "company", Type: field.TypeString, Label: "Company"}}
	files := &testFiles{items: map[file.ID]file.File{}, urls: map[file.ID]string{}}
	renderer, err := NewRenderer(standardFields(), files, site.Site{ID: 5, ProfileCode: "dev", Domain: "example.com", Locale: "ru-RU", IsPublic: true, Settings: map[string]any{"company": "ACME"}}, params, RendererConfig{})
	if err != nil {
		t.Fatal(err)
	}
	template := mailTemplate()
	template.To = []AddressTemplate{{Email: "person@example.net"}}
	found := false
	for _, variable := range renderer.SiteVariables() {
		if variable.Variable == "site.field.company" {
			found = true
		}
	}
	if !found {
		t.Fatal("private mail parameter missing from metadata")
	}
	template.Subject = "{{site.id}} {{site.profile_code}} {{site.domain}} {{site.locale}} {{site.is_public}} {{site.field.company}} {{data.name}}"
	template.HTMLBody = "<p>Body</p>"
	template.Variables = []field.Definition{{Key: "name", Type: field.TypeString, Label: "Name", Required: &required}}
	if _, err := renderer.Render(context.Background(), template, nil, security.User(9)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing required value = %v", err)
	}
	result, err := renderer.Render(context.Background(), template, map[string]any{"name": "Alice"}, security.User(9))
	if err != nil {
		t.Fatal(err)
	}
	if result.Subject != "5 dev example.com ru-RU true ACME Alice" {
		t.Fatalf("site variables = %q", result.Subject)
	}
	if _, err := renderer.Render(context.Background(), template, map[string]any{
		"name": "Alice",
		"site": map[string]any{"domain": "attacker.example"},
	}, security.User(9)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("client-supplied site variables = %v", err)
	}
	template.Subject = "{{site.secret}}"
	if err := renderer.ValidateTemplate(template); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown site variable = %v", err)
	}
}

func TestRendererRejectsMissingRequiredStringEmailAndFile(t *testing.T) {
	t.Parallel()
	required := true
	renderer, _ := testRenderer(t, SenderPolicy{})
	for _, code := range []field.TypeCode{field.TypeString, field.TypeEmail, field.TypeFile} {
		template := mailTemplate()
		template.To = []AddressTemplate{{Email: "person@example.net"}}
		template.HTMLBody = "<p>Body</p>"
		definition := field.Definition{Key: "required_value", Type: code, Label: "Required", Required: &required}
		if code == field.TypeFile {
			definition.Options = field.FileOptions{}
		}
		template.Variables = []field.Definition{definition}
		if _, err := renderer.Render(context.Background(), template, nil, security.User(9)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("missing required %s = %v", code, err)
		}
	}
}

func TestAttachmentAuthorizationDistinguishesEditingManualAndTrustedSiteSources(t *testing.T) {
	t.Parallel()
	files := &testFiles{items: map[file.ID]file.File{7: {ID: 7, Name: "contract.pdf", MIMEType: "application/pdf", Size: 3}}, body: map[file.ID]string{7: "pdf"}, urls: map[file.ID]string{}, denyUsers: true}
	params := []field.Definition{{Key: "contract", Type: field.TypeFile, Label: "Contract", Options: field.FileOptions{}}}
	renderer, err := NewRenderer(standardFields(), files, site.Site{ID: 5, ProfileCode: "dev", Domain: "example.com", Locale: "ru-RU", Settings: map[string]any{"contract": int64(7)}}, params, RendererConfig{})
	if err != nil {
		t.Fatal(err)
	}
	staticID := file.ID(7)
	staticTemplate := mailTemplate()
	staticTemplate.Attachments = []AttachmentTemplate{{Source: AttachmentStatic, FileID: &staticID}}
	if err := renderer.ValidateTemplateFiles(context.Background(), security.User(9), staticTemplate); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unauthorized static edit = %v", err)
	}
	manual := mailTemplate()
	manual.Attachments = []AttachmentTemplate{{Source: AttachmentVariable, Variable: "data.invoice"}}
	if _, err := renderer.Render(context.Background(), manual, map[string]any{"email": "a@example.net", "invoice": float64(7)}, security.User(9)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unauthorized manual file = %v", err)
	}
	siteTemplate := mailTemplate()
	siteTemplate.Attachments = []AttachmentTemplate{{Source: AttachmentSite, Variable: "site.field.contract"}}
	result, err := renderer.Render(context.Background(), siteTemplate, map[string]any{"email": "a@example.net"}, security.User(9))
	if err != nil || len(result.Attachments) != 1 || result.Attachments[0].FileID == nil || *result.Attachments[0].FileID != 7 {
		t.Fatalf("trusted site attachment = %#v, %v", result.Attachments, err)
	}
}

type allowAuthorizer struct{}

func (allowAuthorizer) Check(context.Context, security.Actor, permission.Code) error { return nil }

type testUsers struct{}

func (testUsers) Current(_ context.Context, actor security.Actor) (coreuser.User, error) {
	id, _ := actor.UserID()
	return coreuser.User{ID: id, Login: "editor", Name: "Editor"}, nil
}

type memoryRepository struct {
	template  Template
	message   Message
	queued    eventbus.Message
	attempts  []DeliveryAttempt
	claimable bool
	finishErr error
	createErr error
	activeErr error
	active    *bool
	createAt  chan struct{}
	createGo  chan struct{}
	createOne sync.Once
}

func (r *memoryRepository) ListTemplates(context.Context, site.ID, PageQuery) (TemplatePage, error) {
	return TemplatePage{Items: []Template{r.template}, Total: 1}, nil
}
func (r *memoryRepository) ListEnabledTemplates(context.Context, site.ID, PageQuery) (TemplatePage, error) {
	if !r.template.Enabled {
		return TemplatePage{Items: []Template{}}, nil
	}
	return TemplatePage{Items: []Template{r.template}, Total: 1}, nil
}
func (r *memoryRepository) TemplateByID(_ context.Context, siteID site.ID, id TemplateID) (Template, error) {
	if r.template.SiteID != siteID || r.template.ID != id {
		return Template{}, ErrNotFound
	}
	return r.template, nil
}
func (r *memoryRepository) TemplateByCode(_ context.Context, siteID site.ID, code string) (Template, error) {
	if r.template.SiteID != siteID || r.template.Code != code {
		return Template{}, ErrNotFound
	}
	return r.template, nil
}
func (r *memoryRepository) CreateTemplate(_ context.Context, item Template) (Template, error) {
	item.ID = 1
	r.template = item
	return item, nil
}
func (r *memoryRepository) UpdateTemplate(_ context.Context, item Template) (Template, error) {
	r.template = item
	return item, nil
}
func (r *memoryRepository) SetTemplateEnabled(_ context.Context, siteID site.ID, id TemplateID, enabled bool, updatedBy *security.UserID) (Template, error) {
	if r.template.SiteID != 0 && (r.template.SiteID != siteID || r.template.ID != id) {
		return Template{}, ErrNotFound
	}
	r.template.Enabled = enabled
	r.template.UpdatedBy = updatedBy
	return r.template, nil
}
func (r *memoryRepository) DeleteTemplate(context.Context, site.ID, TemplateID) error { return nil }
func (r *memoryRepository) CreateMessageAndJob(_ context.Context, item Message, queued eventbus.Message) (Message, error) {
	if r.createAt != nil {
		r.createOne.Do(func() { close(r.createAt) })
		<-r.createGo
	}
	if r.createErr != nil {
		return Message{}, r.createErr
	}
	item.ID = 11
	var envelope job.Envelope
	if err := json.Unmarshal(queued.Body, &envelope); err != nil {
		return Message{}, err
	}
	envelope.Payload, _ = json.Marshal(struct {
		MessageID MessageID `json:"message_id"`
	}{item.ID})
	queued.Body, _ = json.Marshal(envelope)
	r.message, r.queued, r.claimable = item, queued, true
	return item, nil
}
func (r *memoryRepository) ListMessages(context.Context, site.ID, MessageQuery) (MessageSummaryPage, error) {
	return MessageSummaryPage{Items: []MessageSummary{{ID: r.message.ID}}, Total: 1}, nil
}
func (r *memoryRepository) MessageDetail(context.Context, site.ID, MessageID) (MessageDetail, error) {
	return MessageDetail{Message: r.message, Attempts: r.attempts}, nil
}
func (r *memoryRepository) DeleteMessage(context.Context, site.ID, MessageID) error { return nil }
func (r *memoryRepository) ClaimMessage(_ context.Context, siteID site.ID, id MessageID, _ int) (Message, DeliveryAttempt, bool, error) {
	if !r.claimable || r.message.ID != id || r.message.SiteID != siteID || r.message.Status == StatusAccepted {
		return Message{}, DeliveryAttempt{}, false, nil
	}
	r.claimable = false
	r.message.Status = StatusSending
	attempt := DeliveryAttempt{MessageID: id, AttemptNumber: len(r.attempts) + 1, Status: AttemptSending}
	r.attempts = append(r.attempts, attempt)
	return r.message, attempt, true, nil
}
func (r *memoryRepository) FinishAttempt(_ context.Context, _ MessageID, number int, result DeliveryResult, failure *DeliveryError, terminal bool) error {
	if r.finishErr != nil {
		return r.finishErr
	}
	item := &r.attempts[number-1]
	item.Driver, item.ResponseCode = result.Driver, result.ResponseCode
	if failure != nil {
		item.Status, item.SafeError, r.message.Status = AttemptFailed, failure.Error(), StatusFailed
		if failure.Retryable && !terminal {
			r.message.Status, r.claimable = StatusRetryable, true
		}
	} else {
		item.Status, r.message.Status = AttemptAccepted, StatusAccepted
	}
	return nil
}
func (r *memoryRepository) HasActiveMessages(context.Context, site.ID) (bool, error) {
	if r.activeErr != nil {
		return false, r.activeErr
	}
	if r.active != nil {
		return *r.active, nil
	}
	return r.message.Status == StatusQueued || r.message.Status == StatusSending || r.message.Status == StatusRetryable, nil
}
func (r *memoryRepository) Cleanup(context.Context, site.ID, time.Duration, int) (int64, error) {
	return 0, nil
}
func (r *memoryRepository) ActiveSpoolKeys(context.Context, site.ID, []string) (map[string]struct{}, error) {
	return map[string]struct{}{}, nil
}

func TestPreviewAndQueueShareRendererAndPersistImmutableSnapshot(t *testing.T) {
	t.Parallel()
	renderer, _ := testRenderer(t, SenderPolicy{})
	repository := &memoryRepository{template: mailTemplate()}
	repository.template.ID = 3
	service, err := NewService(5, repository, renderer, allowAuthorizer{}, testUsers{}, nil, Limits{MaxRecipients: 100, MaxMessageSize: 1 << 20, MaxAttachmentSize: 1 << 20}, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]any{"name": "Alice", "email": "a@example.net", "count": float64(2)}
	preview, err := service.Preview(context.Background(), security.User(9), 3, values)
	if err != nil {
		t.Fatal(err)
	}
	repository.template.Subject = "Updated {{data.name}}"
	message, err := service.QueueManual(context.Background(), security.User(9), ManualSendInput{TemplateID: 3, Values: values})
	if err != nil {
		t.Fatal(err)
	}
	if message.Subject != "Updated Alice" || message.Subject == preview.Subject || message.HTMLBody != preview.HTMLBody || message.Status != StatusQueued || message.RequestedBy == nil || *message.RequestedBy != 9 || !strings.HasPrefix(message.RFCMessageID, "<") {
		t.Fatalf("queued = %#v preview=%#v", message, preview)
	}
	if message.RequestedByName != "Editor" {
		t.Fatalf("requester name = %q", message.RequestedByName)
	}
	repository.template.Subject = "Changed"
	if repository.message.Subject != message.Subject {
		t.Fatal("queued snapshot changed with template")
	}
	var envelope job.Envelope
	if err := json.Unmarshal(repository.queued.Body, &envelope); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		MessageID MessageID `json:"message_id"`
	}
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil || payload.MessageID != message.ID || envelope.ScopeID != "5" {
		t.Fatalf("job payload = %#v, %v", payload, err)
	}
	repository.template.Enabled = false
	if _, err := service.Preview(context.Background(), security.User(9), 3, values); !errors.Is(err, ErrTemplateDisabled) {
		t.Fatalf("disabled preview error = %v", err)
	}
	if _, err := service.QueueByCode(context.Background(), QueueInput{TemplateCode: "welcome", Values: values}); !errors.Is(err, ErrTemplateDisabled) {
		t.Fatalf("disabled automatic queue error = %v", err)
	}
}

func TestRendererRejectsTemplateOwnedByAnotherSite(t *testing.T) {
	t.Parallel()
	renderer, _ := testRenderer(t, SenderPolicy{})
	template := mailTemplate()
	template.SiteID = 6
	if err := renderer.ValidateTemplate(template); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cross-site template validation error = %v", err)
	}
}

func TestTemplateEnableValidatesCurrentRuntimeWithoutReauthorizingStaticFile(t *testing.T) {
	t.Parallel()
	renderer, files := testRenderer(t, SenderPolicy{})
	template := mailTemplate()
	template.ID = 3
	template.Enabled = false
	template.Attachments = []AttachmentTemplate{{Source: AttachmentStatic, FileID: fileIDPointer(7)}}
	repository := &memoryRepository{template: template}
	service, err := NewService(5, repository, renderer, allowAuthorizer{}, testUsers{}, nil, Limits{MaxRecipients: 100, MaxMessageSize: 1 << 20, MaxAttachmentSize: 1 << 20}, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	files.denyUsers = true
	item, err := service.SetTemplateEnabled(context.Background(), security.User(9), 3, true)
	if err != nil || !item.Enabled {
		t.Fatalf("enable approved static attachment = %#v, %v", item, err)
	}
	repository.template.Enabled = false
	repository.template.Subject = "{{site.field.removed}}"
	if _, err := service.SetTemplateEnabled(context.Background(), security.User(9), 3, true); !errors.Is(err, ErrInvalid) || repository.template.Enabled {
		t.Fatalf("removed site field enable = %v, enabled=%t", err, repository.template.Enabled)
	}
	if _, err := service.SetTemplateEnabled(context.Background(), security.User(9), 3, false); err != nil {
		t.Fatalf("disable invalid template = %v", err)
	}
	repository.template.SiteID = 6
	if _, err := service.SetTemplateEnabled(context.Background(), security.User(9), 3, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-site enable error = %v", err)
	}
}

func fileIDPointer(value file.ID) *file.ID { return &value }

func TestMailRuntimeTransitionBlocksActiveStatusesAndRestoresQueueAfterAbort(t *testing.T) {
	t.Parallel()
	for _, status := range []MessageStatus{StatusQueued, StatusSending, StatusRetryable} {
		status := status
		t.Run(string(status), func(t *testing.T) {
			for _, reason := range []kernel.RuntimeTransitionReason{kernel.RuntimeTransitionProfileChange, kernel.RuntimeTransitionSiteDelete} {
				reason := reason
				t.Run(string(reason), func(t *testing.T) {
					renderer, _ := testRenderer(t, SenderPolicy{})
					repository := &memoryRepository{template: mailTemplate(), message: Message{SiteID: 5, Status: status}}
					service, _ := NewService(5, repository, renderer, allowAuthorizer{}, testUsers{}, nil, Limits{MaxRecipients: 100, MaxMessageSize: 1 << 20, MaxAttachmentSize: 1 << 20}, "example.com")
					runtime := &Runtime{service: service, spoolCleanupBatch: 1}
					_, err := runtime.PrepareRuntimeTransition(context.Background(), kernel.RuntimeTransition{Reason: reason, ScopeID: "5", FromProfile: "mail", ToProfile: "other"})
					if !errors.Is(err, kernel.ErrRuntimeTransitionBlocked) || !errors.Is(err, ErrActiveMessages) {
						t.Fatalf("active %s transition error = %v", status, err)
					}
					if _, err := service.QueueByCode(context.Background(), QueueInput{TemplateCode: "welcome", Values: map[string]any{"email": "a@example.net"}}); err != nil {
						t.Fatalf("queue after blocked transition = %v", err)
					}
				})
			}
		})
	}
}

func TestMailRuntimeDrainRejectsNewQueueAndWorkerClaimUntilAbort(t *testing.T) {
	t.Parallel()
	renderer, files := testRenderer(t, SenderPolicy{})
	active := false
	repository := &memoryRepository{template: mailTemplate(), active: &active}
	repository.template.ID = 3
	transport := NullTransport{}
	service, _ := NewService(5, repository, renderer, allowAuthorizer{}, testUsers{}, nil, Limits{MaxRecipients: 100, MaxMessageSize: 1 << 20, MaxAttachmentSize: 1 << 20}, "example.com")
	runtime := &Runtime{service: service, spoolCleanupBatch: 1}
	prepared, err := runtime.PrepareRuntimeTransition(context.Background(), kernel.RuntimeTransition{Reason: kernel.RuntimeTransitionSiteDelete, ScopeID: "5", FromProfile: "mail"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.QueueByCode(context.Background(), QueueInput{TemplateCode: "welcome", Values: map[string]any{"email": "a@example.net"}}); !errors.Is(err, ErrRuntimeDraining) {
		t.Fatalf("queue while draining error = %v", err)
	}
	if _, err := service.QueueManual(context.Background(), security.User(9), ManualSendInput{TemplateID: repository.template.ID, Values: map[string]any{"email": "a@example.net"}}); !errors.Is(err, ErrRuntimeDraining) {
		t.Fatalf("manual queue while draining error = %v", err)
	}
	repository.message = Message{ID: 12, SiteID: 5, Status: StatusQueued}
	repository.claimable = true
	worker, _ := newWorker(5, repository, files, nil, transport, service.lifecycle, 5, nil)
	envelope, _ := job.NewScoped(SendJobName, 1, "5", struct {
		MessageID MessageID `json:"message_id"`
	}{12})
	if err := worker.Handle(context.Background(), envelope); !errors.Is(err, ErrRuntimeDraining) || !repository.claimable {
		t.Fatalf("worker drain error = %v, claimable=%t", err, repository.claimable)
	}
	prepared.Abort()
	if _, err := service.QueueByCode(context.Background(), QueueInput{TemplateCode: "welcome", Values: map[string]any{"email": "a@example.net"}}); err != nil {
		t.Fatalf("queue after abort = %v", err)
	}
}

func TestMailRuntimeTransitionAllowsTerminalOnlyMessages(t *testing.T) {
	t.Parallel()
	for _, status := range []MessageStatus{StatusAccepted, StatusFailed} {
		status := status
		t.Run(string(status), func(t *testing.T) {
			renderer, _ := testRenderer(t, SenderPolicy{})
			repository := &memoryRepository{template: mailTemplate(), message: Message{SiteID: 5, Status: status}}
			service, _ := NewService(5, repository, renderer, allowAuthorizer{}, testUsers{}, nil, Limits{MaxRecipients: 100, MaxMessageSize: 1 << 20, MaxAttachmentSize: 1 << 20}, "example.com")
			runtime := &Runtime{service: service, spoolCleanupBatch: 1}
			prepared, err := runtime.PrepareRuntimeTransition(context.Background(), kernel.RuntimeTransition{Reason: kernel.RuntimeTransitionProfileChange, ScopeID: "5", FromProfile: "mail", ToProfile: "other"})
			if err != nil {
				t.Fatalf("terminal status %s blocked transition: %v", status, err)
			}
			prepared.Abort()
		})
	}
}

func TestMailTransitionWaitsForConcurrentQueueThenObservesItActive(t *testing.T) {
	renderer, _ := testRenderer(t, SenderPolicy{})
	repository := &memoryRepository{template: mailTemplate(), createAt: make(chan struct{}), createGo: make(chan struct{})}
	service, _ := NewService(5, repository, renderer, allowAuthorizer{}, testUsers{}, nil, Limits{MaxRecipients: 100, MaxMessageSize: 1 << 20, MaxAttachmentSize: 1 << 20}, "example.com")
	runtime := &Runtime{service: service, spoolCleanupBatch: 1}
	queueResult := make(chan error, 1)
	go func() {
		_, err := service.QueueByCode(context.Background(), QueueInput{TemplateCode: "welcome", Values: map[string]any{"email": "a@example.net"}})
		queueResult <- err
	}()
	<-repository.createAt
	transitionResult := make(chan error, 1)
	go func() {
		_, err := runtime.PrepareRuntimeTransition(context.Background(), kernel.RuntimeTransition{Reason: kernel.RuntimeTransitionSiteDelete, ScopeID: "5", FromProfile: "mail"})
		transitionResult <- err
	}()
	select {
	case err := <-transitionResult:
		t.Fatalf("transition passed an in-flight queue: %v", err)
	default:
	}
	close(repository.createGo)
	if err := <-queueResult; err != nil {
		t.Fatal(err)
	}
	if err := <-transitionResult; !errors.Is(err, kernel.ErrRuntimeTransitionBlocked) {
		t.Fatalf("transition did not observe queued message: %v", err)
	}
}

type uploadCatalog struct {
	items []filesystem.DiskInfo
	err   error
}

func (c uploadCatalog) Disks(context.Context, security.Actor) ([]filesystem.DiskInfo, error) {
	return c.items, c.err
}

func TestMailUploadStorageMustExist(t *testing.T) {
	t.Parallel()
	catalog := uploadCatalog{items: []filesystem.DiskInfo{{Code: "private", Visibility: filesystem.VisibilityPrivate}}}
	if err := validateUploadStorage(context.Background(), catalog, "private"); err != nil {
		t.Fatal(err)
	}
	if err := validateUploadStorage(context.Background(), catalog, "prviate"); err == nil {
		t.Fatal("unknown Mail upload storage was accepted")
	}
}

func TestMailHTTPRequiresAuthentication(t *testing.T) {
	t.Parallel()
	renderer, _ := testRenderer(t, SenderPolicy{})
	repository := &memoryRepository{template: mailTemplate()}
	repository.template.ID = 3
	service, err := NewService(5, repository, renderer, allowAuthorizer{}, testUsers{}, nil, Limits{MaxRecipients: 100, MaxMessageSize: 1 << 20, MaxAttachmentSize: 1 << 20}, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHTTPHandler(service)
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Mount("/api/sites/{siteID}/mail", handler)

	unauthenticated := httptest.NewRecorder()
	guestRequest := httptest.NewRequest(http.MethodGet, "/api/sites/8/mail/messages", nil)
	guestRequest = guestRequest.WithContext(httptransport.WithActor(guestRequest.Context(), security.Guest()))
	router.ServeHTTP(unauthenticated, guestRequest)
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", unauthenticated.Code)
	}

	service.uploadStorage, service.uploadPath = "private", "mail/uploads"
	request := httptest.NewRequest(http.MethodPatch, "/api/sites/5/mail/templates/3/enabled", strings.NewReader(`{"enabled":false}`))
	request = request.WithContext(httptransport.WithActor(request.Context(), security.User(9)))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || repository.template.Enabled || !strings.Contains(response.Body.String(), `"enabled":false`) {
		t.Fatalf("semantic disable = %d, %s, template=%#v", response.Code, response.Body.String(), repository.template)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/sites/5/mail/templates", strings.NewReader(`{"site_id":99,"code":"manual_api","name":"Manual API","enabled":true,"transport":"default","from":{"name":"","email":"noreply@example.com"},"to":[{"name":"","email":"person@example.net"}],"cc":[],"bcc":[],"reply_to":null,"subject":"Subject","content_type":"text","text_body":"Body","html_body":"","attachments":[],"variables":[]}`))
	request = request.WithContext(httptransport.WithActor(request.Context(), security.User(9)))
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity || repository.template.SiteID != 5 {
		t.Fatalf("manual API site ownership = %d, %s, template=%#v", response.Code, response.Body.String(), repository.template)
	}

	validTemplate := `{"code":"manual_api","name":"Manual API","enabled":true,"from":{"name":"Sender","email":"noreply@example.com"},"to":[{"name":"Recipient","email":"person@example.net"}],"cc":[],"bcc":[],"reply_to":null,"subject":"Subject","content_type":"text","text_body":"Body","html_body":"","attachments":[],"variables":[]}`
	request = httptest.NewRequest(http.MethodPost, "/api/sites/5/mail/templates", strings.NewReader(validTemplate))
	request = request.WithContext(httptransport.WithActor(request.Context(), security.User(9)))
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || strings.Contains(response.Body.String(), `"transport"`) {
		t.Fatalf("template create = %d, %s", response.Code, response.Body.String())
	}

	for _, call := range []struct {
		method string
		path   string
		body   string
		status int
	}{
		{http.MethodPost, "/api/sites/5/mail/preview", `{"template_id":1,"values":{}}`, http.StatusOK},
		{http.MethodPost, "/api/sites/5/mail/send", `{"template_id":1,"values":{}}`, http.StatusAccepted},
		{http.MethodGet, "/api/sites/5/mail/messages", "", http.StatusOK},
		{http.MethodGet, "/api/sites/5/mail/messages/11", "", http.StatusOK},
	} {
		request = httptest.NewRequest(call.method, call.path, strings.NewReader(call.body))
		request = request.WithContext(httptransport.WithActor(request.Context(), security.User(9)))
		response = httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != call.status || strings.Contains(response.Body.String(), `"transport"`) {
			t.Fatalf("%s %s = %d, %s", call.method, call.path, response.Code, response.Body.String())
		}
	}

	request = httptest.NewRequest(http.MethodGet, "/api/sites/5/mail/variables", nil)
	request = request.WithContext(httptransport.WithActor(request.Context(), security.User(9)))
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"upload_storage":"private"`) || !strings.Contains(response.Body.String(), `"upload_path":"mail/uploads"`) {
		t.Fatalf("editor config = %d, %s", response.Code, response.Body.String())
	}

}

func TestTemplateResponseUsesStableLowercaseChoiceDTO(t *testing.T) {
	t.Parallel()
	template := mailTemplate()
	template.Variables = []field.Definition{{Key: "kind", Type: field.TypeSelect, Label: "Kind", Options: field.SelectOptions{Choices: []field.Choice{{Value: "a", Label: "A"}}}}}
	response, err := toTemplateResponse(template)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"choices":[{"value":"a","label":"A"}]`) || strings.Contains(string(raw), `"Value"`) || strings.Contains(string(raw), `"transport"`) {
		t.Fatalf("response JSON = %s", raw)
	}
}

type countingTransport struct {
	count          int
	failure        error
	attachmentBody string
}

func (*countingTransport) Driver() string { return "test" }
func (t *countingTransport) Send(_ context.Context, delivery Delivery) (DeliveryResult, error) {
	t.count++
	if len(delivery.Attachments) > 0 {
		data, _ := io.ReadAll(delivery.Attachments[0].Body)
		expected := t.attachmentBody
		if expected == "" {
			expected = "pdf"
		}
		if string(data) != expected {
			return DeliveryResult{}, errors.New("wrong attachment")
		}
	}
	return DeliveryResult{Driver: "test", ResponseCode: "250"}, t.failure
}

func queuedDeliveryMessage(id MessageID) Message {
	return Message{
		ID: id, SiteID: 5, RFCMessageID: fmt.Sprintf("<message-%d@example.com>", id),
		From: Address{Email: "noreply@example.com"}, To: []Address{{Email: "person@example.net"}},
		ContentType: ContentText, TextBody: "Body", Status: StatusQueued, RequestedAt: time.Now().UTC(),
	}
}

func TestWorkerRecordsAttemptAndSkipsDuplicateTerminalDelivery(t *testing.T) {
	t.Parallel()
	_, files := testRenderer(t, SenderPolicy{})
	fileID := file.ID(7)
	message := queuedDeliveryMessage(11)
	message.Attachments = []Attachment{{Source: AttachmentStatic, FileID: &fileID, Filename: "invoice.pdf", MIMEType: "application/pdf", Size: 3}}
	repository := &memoryRepository{message: message, claimable: true}
	transport := &countingTransport{}
	worker, _ := NewWorker(5, repository, files, nil, transport, 5, nil)
	payload, _ := json.Marshal(struct {
		MessageID MessageID `json:"message_id"`
	}{11})
	item := job.Envelope{ID: "01H00000000000000000000000", Name: SendJobName, ScopeID: "5", SchemaVersion: 1, Payload: payload}
	if err := worker.Handle(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	if err := worker.Handle(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	if transport.count != 1 || repository.message.Status != StatusAccepted || len(repository.attempts) != 1 || repository.attempts[0].ResponseCode != "250" {
		t.Fatalf("worker state = count %d message %#v attempts %#v", transport.count, repository.message, repository.attempts)
	}
}

func TestWorkerRecordsFailureThenRetryAndRejectsWrongSiteScope(t *testing.T) {
	t.Parallel()
	_, files := testRenderer(t, SenderPolicy{})
	repository := &memoryRepository{message: queuedDeliveryMessage(12), claimable: true}
	transport := &countingTransport{failure: &DeliveryError{Retryable: true, Code: "temporary", Err: errors.New("temporary failure")}}
	worker, _ := NewWorker(5, repository, files, nil, transport, 5, nil)
	payload, _ := json.Marshal(struct {
		MessageID MessageID `json:"message_id"`
	}{12})
	wrongScope := job.Envelope{ID: "01H00000000000000000000001", Name: SendJobName, ScopeID: "6", SchemaVersion: 1, Payload: payload}
	if err := worker.Handle(context.Background(), wrongScope); err == nil || !repository.claimable {
		t.Fatalf("wrong-scope delivery error = %v, claimable=%t", err, repository.claimable)
	}
	item := wrongScope
	item.ScopeID = "5"
	if err := worker.Handle(context.Background(), item); err == nil || repository.message.Status != StatusRetryable || len(repository.attempts) != 1 || repository.attempts[0].Status != AttemptFailed {
		t.Fatalf("failed delivery = %v, message=%#v attempts=%#v", err, repository.message, repository.attempts)
	}
	transport.failure = nil
	if err := worker.Handle(context.Background(), item); err != nil || repository.message.Status != StatusAccepted || len(repository.attempts) != 2 || repository.attempts[1].Status != AttemptAccepted {
		t.Fatalf("retry delivery = %v, message=%#v attempts=%#v", err, repository.message, repository.attempts)
	}
}

func TestWorkerMakesRetryableFailureTerminalAtMaximumAttempts(t *testing.T) {
	t.Parallel()
	_, files := testRenderer(t, SenderPolicy{})
	repository := &memoryRepository{message: queuedDeliveryMessage(13), claimable: true}
	transport := &countingTransport{failure: &DeliveryError{Retryable: true, Code: "421", Err: errors.New("temporary")}}
	worker, _ := NewWorker(5, repository, files, nil, transport, 1, nil)
	payload, _ := json.Marshal(struct {
		MessageID MessageID `json:"message_id"`
	}{13})
	item := job.Envelope{ID: "01H00000000000000000000009", Name: SendJobName, ScopeID: "5", SchemaVersion: 1, Payload: payload}
	if err := worker.Handle(context.Background(), item); err != nil || repository.message.Status != StatusFailed {
		t.Fatalf("maximum attempt = %v, %s", err, repository.message.Status)
	}
	if err := worker.Handle(context.Background(), item); err != nil || transport.count != 1 {
		t.Fatalf("terminal duplicate = %v, count=%d", err, transport.count)
	}
}

func TestWorkerTerminalizesMalformedImmutableMessageWithoutSending(t *testing.T) {
	t.Parallel()
	_, files := testRenderer(t, SenderPolicy{})
	message := queuedDeliveryMessage(14)
	message.Subject = "safe\r\nBcc: attacker@example.com"
	repository := &memoryRepository{message: message, claimable: true}
	transport := &countingTransport{}
	worker, _ := NewWorker(5, repository, files, nil, transport, 5, nil)
	payload, _ := json.Marshal(struct {
		MessageID MessageID `json:"message_id"`
	}{14})
	item := job.Envelope{ID: "01H00000000000000000000010", Name: SendJobName, ScopeID: "5", SchemaVersion: 1, Payload: payload}
	if err := worker.Handle(context.Background(), item); err != nil || repository.message.Status != StatusFailed || transport.count != 0 {
		t.Fatalf("malformed immutable message = %v, status=%s, sends=%d", err, repository.message.Status, transport.count)
	}
}

type memorySpoolDisk struct{ objects map[string][]byte }

func (d *memorySpoolDisk) Code() filesystem.Code             { return "spool-test" }
func (d *memorySpoolDisk) Visibility() filesystem.Visibility { return filesystem.VisibilityPrivate }
func (d *memorySpoolDisk) Ping(context.Context) error        { return nil }
func (d *memorySpoolDisk) PutNew(_ context.Context, key string, body io.Reader, _ string) error {
	if d.objects == nil {
		d.objects = map[string][]byte{}
	}
	if _, exists := d.objects[key]; exists {
		return filesystem.ErrConflict
	}
	value, err := io.ReadAll(body)
	if err == nil {
		d.objects[key] = value
	}
	return err
}
func (d *memorySpoolDisk) Open(_ context.Context, key string) (io.ReadCloser, error) {
	value, exists := d.objects[key]
	if !exists {
		return nil, filesystem.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(value)), nil
}
func (d *memorySpoolDisk) Delete(_ context.Context, key string) error {
	if _, exists := d.objects[key]; !exists {
		return filesystem.ErrNotFound
	}
	delete(d.objects, key)
	return nil
}
func (d *memorySpoolDisk) URL(context.Context, filesystem.Reference) (string, error) {
	return "", filesystem.ErrUnauthorized
}
func (d *memorySpoolDisk) TemporaryURL(context.Context, filesystem.Reference, time.Time) (string, error) {
	return "", filesystem.ErrUnauthorized
}
func (d *memorySpoolDisk) Close() error { return nil }
func (d *memorySpoolDisk) WalkPrefix(_ context.Context, prefix string, visit func(string) error) error {
	for key := range d.objects {
		if strings.HasPrefix(key, prefix) {
			if err := visit(key); err != nil {
				return err
			}
		}
	}
	return nil
}
func (d *memorySpoolDisk) OpenPrefixScan(_ context.Context, prefix string) (filesystem.PrefixScan, error) {
	keys := make([]string, 0, len(d.objects))
	for key := range d.objects {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return &memoryPrefixScan{keys: keys}, nil
}

type memoryPrefixScan struct {
	keys   []string
	offset int
	closed bool
}

func (s *memoryPrefixScan) Next(ctx context.Context, limit int) (filesystem.PrefixScanPage, error) {
	if err := ctx.Err(); err != nil {
		s.closed = true
		return filesystem.PrefixScanPage{}, err
	}
	end := s.offset + limit
	if end > len(s.keys) {
		end = len(s.keys)
	}
	page := filesystem.PrefixScanPage{Keys: append([]string(nil), s.keys[s.offset:end]...), Done: end == len(s.keys)}
	s.offset = end
	if page.Done {
		s.closed = true
	}
	return page, nil
}

func (s *memoryPrefixScan) Close() error {
	s.closed = true
	return nil
}

func TestTransientSpoolSurvivesRetryAndIsDeletedOnTerminalSuccess(t *testing.T) {
	t.Parallel()
	disk := &memorySpoolDisk{objects: map[string][]byte{}}
	spool, err := NewAttachmentSpool(5, disk)
	if err != nil {
		t.Fatal(err)
	}
	renderer, files := testRenderer(t, SenderPolicy{})
	repository := &memoryRepository{template: mailTemplate()}
	repository.template.ID = 3
	transport := &countingTransport{failure: &DeliveryError{Retryable: true, Code: "421", Err: errors.New("temporary")}, attachmentBody: "resume"}
	service, err := NewService(5, repository, renderer, allowAuthorizer{}, testUsers{}, spool, Limits{MaxRecipients: 100, MaxMessageSize: 1 << 20, MaxAttachmentSize: 1 << 20}, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	queued, err := service.QueueByCode(context.Background(), QueueInput{TemplateCode: "welcome", Values: map[string]any{"email": "a@example.net"}, Attachments: []TransientAttachment{{Filename: "resume.pdf", MIMEType: "application/pdf", Size: 6, Body: strings.NewReader("resume")}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(disk.objects) != 1 || len(queued.Attachments) != 1 || queued.Attachments[0].Source != AttachmentTransient {
		t.Fatalf("queued spool = %#v, objects=%d", queued.Attachments, len(disk.objects))
	}
	publicJSON, _ := json.Marshal(queued)
	if strings.Contains(string(publicJSON), "mail-spool") {
		t.Fatalf("spool key leaked: %s", publicJSON)
	}
	worker, err := NewWorker(5, repository, files, spool, transport, 5, nil)
	if err != nil {
		t.Fatal(err)
	}
	var envelope job.Envelope
	if err := json.Unmarshal(repository.queued.Body, &envelope); err != nil {
		t.Fatal(err)
	}
	if err := worker.Handle(context.Background(), envelope); err == nil || len(disk.objects) != 1 || repository.message.Status != StatusRetryable {
		t.Fatalf("retry state = %v, objects=%d, status=%s", err, len(disk.objects), repository.message.Status)
	}
	transport.failure = nil
	if err := worker.Handle(context.Background(), envelope); err != nil || len(disk.objects) != 0 || repository.message.Status != StatusAccepted {
		t.Fatalf("terminal state = %v, objects=%d, status=%s", err, len(disk.objects), repository.message.Status)
	}
}

func TestQueueFailureBestEffortDeletesNewTransientSpoolObjects(t *testing.T) {
	t.Parallel()
	disk := &memorySpoolDisk{objects: map[string][]byte{}}
	spool, err := NewAttachmentSpool(5, disk)
	if err != nil {
		t.Fatal(err)
	}
	renderer, _ := testRenderer(t, SenderPolicy{})
	repository := &memoryRepository{template: mailTemplate(), createErr: errors.New("database unavailable")}
	service, err := NewService(5, repository, renderer, allowAuthorizer{}, testUsers{}, spool, Limits{MaxRecipients: 100, MaxMessageSize: 1 << 20, MaxAttachmentSize: 1 << 20}, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.QueueByCode(context.Background(), QueueInput{
		TemplateCode: "welcome",
		Values:       map[string]any{"email": "a@example.net"},
		Attachments: []TransientAttachment{{
			Filename: "resume.pdf", MIMEType: "application/pdf", Size: 6, Body: strings.NewReader("resume"),
		}},
	})
	if err == nil || len(disk.objects) != 0 {
		t.Fatalf("queue failure = %v, remaining spool objects=%d", err, len(disk.objects))
	}
}

func TestTerminalDeliveryFailureDeletesTransientSpoolObjects(t *testing.T) {
	t.Parallel()
	disk := &memorySpoolDisk{objects: map[string][]byte{}}
	spool, _ := NewAttachmentSpool(5, disk)
	renderer, files := testRenderer(t, SenderPolicy{})
	repository := &memoryRepository{template: mailTemplate()}
	transport := &countingTransport{failure: &DeliveryError{Code: "550", Err: errors.New("rejected")}}
	service, _ := NewService(5, repository, renderer, allowAuthorizer{}, testUsers{}, spool, Limits{MaxRecipients: 100, MaxMessageSize: 1 << 20, MaxAttachmentSize: 1 << 20}, "example.com")
	_, err := service.QueueByCode(context.Background(), QueueInput{
		TemplateCode: "welcome",
		Values:       map[string]any{"email": "a@example.net"},
		Attachments: []TransientAttachment{{
			Filename: "resume.pdf", MIMEType: "application/pdf", Size: 6, Body: strings.NewReader("resume"),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var envelope job.Envelope
	if err := json.Unmarshal(repository.queued.Body, &envelope); err != nil {
		t.Fatal(err)
	}
	worker, _ := NewWorker(5, repository, files, spool, transport, 5, nil)
	if err := worker.Handle(context.Background(), envelope); err != nil || repository.message.Status != StatusFailed || len(disk.objects) != 0 {
		t.Fatalf("terminal failure = %v, status=%s, spool objects=%d", err, repository.message.Status, len(disk.objects))
	}
}

func TestSpoolCleanupDeletesOnlyOldInactiveObjects(t *testing.T) {
	t.Parallel()
	prefix := spoolRootPrefix + "5/"
	activeKey := prefix + "1-00000000-0000-4000-8000-000000000001"
	orphanKey := prefix + "2-00000000-0000-4000-8000-000000000002"
	newKey := prefix + strconv.FormatInt(time.Now().UTC().Unix(), 10) + "-00000000-0000-4000-8000-000000000003"
	disk := &memorySpoolDisk{objects: map[string][]byte{
		activeKey: []byte("active"),
		orphanKey: []byte("orphan"),
		newKey:    []byte("new"),
	}}
	spool, _ := NewAttachmentSpool(5, disk)
	deleted, err := spool.Cleanup(context.Background(), time.Now().Add(-time.Hour), 10, func(_ context.Context, _ []string) (map[string]struct{}, error) {
		return map[string]struct{}{activeKey: {}}, nil
	})
	if err != nil || deleted != 1 {
		t.Fatalf("cleanup = %d, %v", deleted, err)
	}
	if _, exists := disk.objects[activeKey]; !exists {
		t.Fatal("active spool attachment deleted")
	}
	if _, exists := disk.objects[orphanKey]; exists {
		t.Fatal("orphan spool attachment retained")
	}
}

func TestSpoolCleanupPagesPastProtectedObjects(t *testing.T) {
	t.Parallel()
	prefix := spoolRootPrefix + "5/"
	activeKeys := []string{
		prefix + "1-00000000-0000-4000-8000-000000000001",
		prefix + "2-00000000-0000-4000-8000-000000000002",
		prefix + "3-00000000-0000-4000-8000-000000000003",
		prefix + "4-00000000-0000-4000-8000-000000000004",
		prefix + "5-00000000-0000-4000-8000-000000000005",
	}
	orphanKey := prefix + "6-00000000-0000-4000-8000-000000000006"
	objects := map[string][]byte{orphanKey: []byte("orphan")}
	protected := make(map[string]struct{}, len(activeKeys))
	for _, key := range activeKeys {
		objects[key] = []byte("active")
		protected[key] = struct{}{}
	}
	disk := &memorySpoolDisk{objects: objects}
	spool, err := NewAttachmentSpool(5, disk)
	if err != nil {
		t.Fatal(err)
	}
	active := func(_ context.Context, keys []string) (map[string]struct{}, error) {
		result := map[string]struct{}{}
		for _, key := range keys {
			if _, exists := protected[key]; exists {
				result[key] = struct{}{}
			}
		}
		return result, nil
	}
	deleted := 0
	for step := 0; step < 8 && deleted == 0; step++ {
		count, cleanupErr := spool.Cleanup(context.Background(), time.Now(), 2, active)
		if cleanupErr != nil {
			t.Fatal(cleanupErr)
		}
		deleted += count
	}
	if deleted != 1 {
		t.Fatalf("orphan cleanup count = %d", deleted)
	}
	if _, exists := disk.objects[orphanKey]; exists {
		t.Fatal("later orphan remained starved behind protected object")
	}
}

func TestSpoolsCannotAccessOrCleanAnotherSitePrefix(t *testing.T) {
	t.Parallel()
	key5 := spoolRootPrefix + "5/1-00000000-0000-4000-8000-000000000001"
	key6 := spoolRootPrefix + "6/1-00000000-0000-4000-8000-000000000002"
	disk := &memorySpoolDisk{objects: map[string][]byte{key5: []byte("five"), key6: []byte("six")}}
	spool5, _ := NewAttachmentSpool(5, disk)
	foreign := newStoredAttachment(AttachmentTransient, nil, key6, "foreign.txt", "text/plain", 3, "")
	if _, err := spool5.Open(context.Background(), foreign); err == nil {
		t.Fatal("cross-site spool open succeeded")
	}
	if err := spool5.Delete(context.Background(), foreign); err == nil {
		t.Fatal("cross-site spool delete succeeded")
	}
	if deleted, err := spool5.Cleanup(context.Background(), time.Now(), 10, func(context.Context, []string) (map[string]struct{}, error) {
		return map[string]struct{}{}, nil
	}); err != nil || deleted != 1 {
		t.Fatalf("site cleanup = %d, %v", deleted, err)
	}
	if _, exists := disk.objects[key6]; !exists {
		t.Fatal("site cleanup deleted another site's object")
	}
}

func TestMailRuntimeTransitionPurgesOnlyOwningSiteSpool(t *testing.T) {
	t.Parallel()
	key5 := spoolRootPrefix + "5/1-00000000-0000-4000-8000-000000000001"
	key6 := spoolRootPrefix + "6/1-00000000-0000-4000-8000-000000000002"
	disk := &memorySpoolDisk{objects: map[string][]byte{key5: []byte("five"), key6: []byte("six")}}
	spool, _ := NewAttachmentSpool(5, disk)
	renderer, _ := testRenderer(t, SenderPolicy{})
	active := false
	repository := &memoryRepository{template: mailTemplate(), active: &active}
	service, _ := NewService(5, repository, renderer, allowAuthorizer{}, testUsers{}, spool, Limits{MaxRecipients: 100, MaxMessageSize: 1 << 20, MaxAttachmentSize: 1 << 20}, "example.com")
	runtime := &Runtime{service: service, spool: spool, spoolCleanupBatch: 1}
	prepared, err := runtime.PrepareRuntimeTransition(context.Background(), kernel.RuntimeTransition{Reason: kernel.RuntimeTransitionSiteDelete, ScopeID: "5", FromProfile: "mail"})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := disk.objects[key5]; exists {
		t.Fatal("owning site spool object survived transition purge")
	}
	if _, exists := disk.objects[key6]; !exists {
		t.Fatal("transition purge deleted another site's spool object")
	}
	prepared.Abort()
}

func TestActiveMessageBlocksTransitionBeforeSpoolPurge(t *testing.T) {
	t.Parallel()
	key := spoolRootPrefix + "5/1-00000000-0000-4000-8000-000000000001"
	disk := &memorySpoolDisk{objects: map[string][]byte{key: []byte("active")}}
	spool, _ := NewAttachmentSpool(5, disk)
	renderer, _ := testRenderer(t, SenderPolicy{})
	active := true
	repository := &memoryRepository{template: mailTemplate(), active: &active}
	service, _ := NewService(5, repository, renderer, allowAuthorizer{}, testUsers{}, spool, Limits{MaxRecipients: 100, MaxMessageSize: 1 << 20, MaxAttachmentSize: 1 << 20}, "example.com")
	runtime := &Runtime{service: service, spool: spool, spoolCleanupBatch: 1}
	if _, err := runtime.PrepareRuntimeTransition(context.Background(), kernel.RuntimeTransition{Reason: kernel.RuntimeTransitionSiteDelete, ScopeID: "5", FromProfile: "mail"}); !errors.Is(err, kernel.ErrRuntimeTransitionBlocked) {
		t.Fatalf("active transition error = %v", err)
	}
	if _, exists := disk.objects[key]; !exists {
		t.Fatal("active message spool object was purged")
	}
}

func TestSpoolRejectsInjectedOrMalformedInternalKeys(t *testing.T) {
	t.Parallel()
	for _, key := range []string{
		"../secret",
		spoolRootPrefix + "secret",
		spoolRootPrefix + "5/1-../../secret",
		spoolRootPrefix + "5/1-not-a-message-id",
	} {
		if validSpoolKey(key) {
			t.Fatalf("unsafe spool key %q accepted", key)
		}
	}
}

func TestDeliveryErrorClassificationUsesProtocolSemantics(t *testing.T) {
	t.Parallel()
	if failure := classifyDeliveryError(&textproto.Error{Code: 421, Msg: "later"}); !failure.Retryable || failure.Code != "421" {
		t.Fatalf("SMTP 421 = %#v", failure)
	}
	if failure := classifyDeliveryError(&textproto.Error{Code: 550, Msg: "rejected"}); failure.Retryable || failure.Code != "550" {
		t.Fatalf("SMTP 550 = %#v", failure)
	}
}

func TestConfigRejectsInvalidDeliveryAndSpoolLimits(t *testing.T) {
	t.Parallel()
	base := Config{SendMaxAttempts: 5, MaxRecipients: 100, MaxMessageSize: 25 << 20, MaxAttachmentSize: 20 << 20}
	for name, mutate := range map[string]func(*Config){
		"attempts":        func(config *Config) { config.SendMaxAttempts = 0 },
		"recipients":      func(config *Config) { config.MaxRecipients = 0 },
		"message size":    func(config *Config) { config.MaxMessageSize = -1 },
		"attachment size": func(config *Config) { config.MaxAttachmentSize = 0 },
		"upload path":     func(config *Config) { config.UploadPath = "../mail" },
		"absolute path":   func(config *Config) { config.UploadPath = "/mail/uploads" },
		"spool TTL": func(config *Config) {
			config.SpoolEnabled = true
			config.SpoolTTL = 0
			config.SpoolCleanupInterval = time.Hour
			config.SpoolCleanupBatch = 10
		},
	} {
		config := base
		mutate(&config)
		if _, err := normalizeConfig(config); err == nil {
			t.Fatalf("%s configuration accepted", name)
		}
	}
}

func TestServiceRejectsUnsafeMessageIDDomain(t *testing.T) {
	t.Parallel()
	renderer, _ := testRenderer(t, SenderPolicy{})
	repository := &memoryRepository{template: mailTemplate()}
	_, err := NewService(5, repository, renderer, allowAuthorizer{}, testUsers{}, nil, Limits{MaxRecipients: 100, MaxMessageSize: 1 << 20, MaxAttachmentSize: 1 << 20}, "example.com\r\nBcc: attacker@example.com")
	if err == nil {
		t.Fatal("unsafe Message-ID domain accepted")
	}
}

func TestNullLogAndSMTPConfiguration(t *testing.T) {
	t.Parallel()
	var nilCountingTransport *countingTransport
	if err := validateTransport(nilCountingTransport); err == nil {
		t.Fatal("typed nil transport was accepted")
	}
	result, err := (NullTransport{}).Send(context.Background(), Delivery{})
	if err != nil || result.Driver != "null" {
		t.Fatalf("null = %#v %v", result, err)
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	_, err = (LogTransport{Logger: logger}).Send(context.Background(), Delivery{Message: Message{ID: 1}})
	if err != nil || strings.Contains(logs.String(), "body") {
		t.Fatalf("log transport = %q %v", logs.String(), err)
	}
	if _, err := NewSMTPTransport(SMTPConfig{Host: "smtp.example.com", Port: 587, TLSEnabled: true, Timeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSMTPTransport(SMTPConfig{Host: "", Port: 587, Timeout: time.Second}); err == nil {
		t.Fatal("invalid SMTP config accepted")
	}
}
