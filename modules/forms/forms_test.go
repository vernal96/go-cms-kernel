package forms

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel/connectors/localstorage"
	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/eventbus"
	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/job"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	corefile "github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/mail"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

type testFieldResolver map[field.TypeCode]field.Type

func (r testFieldResolver) FieldTypes() []field.TypeCode {
	result := make([]field.TypeCode, 0, len(r))
	for code := range r {
		result = append(result, code)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}
func (r testFieldResolver) FieldType(code field.TypeCode) (field.Type, bool) {
	item, exists := r[code]
	return item, exists
}
func formsFieldResolver() testFieldResolver {
	result := testFieldResolver{}
	for _, item := range append(field.StandardTypes(), fieldTypes()...) {
		result[item.Code()] = item
	}
	return result
}

func TestConditionalFieldsUseNormalizedControllersAndIgnoreInactiveValues(t *testing.T) {
	resolver := formsFieldResolver()
	fields := []FormField{
		{ID: 1, FormID: 1, Code: "kind", Type: field.TypeInteger, Label: "Тип"},
		{ID: 2, FormID: 1, Code: "email", Type: field.TypeEmail, Label: "Email", Required: true, VisibleWhen: &field.VisibleWhen{Field: "kind", Value: float64(2)}},
	}
	active, err := resolveActiveFields(fields, map[string]any{"kind": float64(1), "email": "not-an-email"}, resolver)
	if err != nil || len(active) != 1 || active[0].Code != "kind" {
		t.Fatalf("inactive conditional fields = %#v, %v", active, err)
	}
	active, err = resolveActiveFields(fields, map[string]any{"kind": float64(2)}, resolver)
	if err != nil || len(active) != 2 {
		t.Fatalf("normalized active fields = %#v, %v", active, err)
	}
	cyclic := []FormField{
		{ID: 1, FormID: 1, Code: "one", Type: field.TypeString, Label: "One", VisibleWhen: &field.VisibleWhen{Field: "two", Value: "yes"}},
		{ID: 2, FormID: 1, Code: "two", Type: field.TypeString, Label: "Two", VisibleWhen: &field.VisibleWhen{Field: "one", Value: "yes"}},
	}
	if err := validateFieldConditions(cyclic, resolver); !errors.Is(err, ErrInvalid) {
		t.Fatalf("visibility cycle error = %v", err)
	}
}

func TestLayoutRejectsCyclesAndForeignReferencesAndSortsSiblings(t *testing.T) {
	fieldID, elementID := FieldID(1), ElementID(2)
	detail := FormDetail{
		Form:     Form{ID: 1},
		Fields:   []FormField{{ID: fieldID, FormID: 1, Code: MandatoryConsentCode, Type: FieldTypeConsent, Required: true}, {ID: 3, FormID: 1, Code: MandatoryCaptchaCode, Type: FieldTypeCaptcha, Required: true}},
		Elements: []Element{{ID: elementID, FormID: 1, Code: MandatorySubmitCode, Type: ElementSubmitButton}},
		Layout:   []LayoutNode{{ID: 10}, {ID: 11}, {ID: 12}, {ID: 13}, {ID: 14}},
	}
	captchaID := FieldID(3)
	containerA, containerB := LayoutNodeID(13), LayoutNodeID(14)
	valid := []LayoutNode{
		{ID: 12, Kind: LayoutElement, ElementID: &elementID, Position: 2},
		{ID: 10, Kind: LayoutField, FieldID: &fieldID, Position: 0},
		{ID: containerA, Kind: LayoutContainer, ContainerType: ContainerGroup, Position: 3},
		{ID: 11, Kind: LayoutField, FieldID: &captchaID, Position: 1},
		{ID: containerB, ParentID: &containerA, Kind: LayoutContainer, ContainerType: ContainerSlide, Position: 0},
	}
	normalized, err := normalizeAndValidateLayout(detail, valid)
	if err != nil || normalized[0].ID != 10 || normalized[1].ID != 11 || normalized[2].ID != 12 {
		t.Fatalf("normalized layout = %#v, %v", normalized, err)
	}
	cycle := append([]LayoutNode(nil), valid...)
	cycle[2].ParentID = &containerB
	if _, err := normalizeAndValidateLayout(detail, cycle); !errors.Is(err, ErrInvalid) {
		t.Fatalf("layout cycle error = %v", err)
	}
	foreignField := FieldID(999)
	foreign := append([]LayoutNode(nil), valid...)
	foreign[0].ElementID, foreign[0].FieldID, foreign[0].Kind = nil, &foreignField, LayoutField
	if _, err := normalizeAndValidateLayout(detail, foreign); !errors.Is(err, ErrInvalid) {
		t.Fatalf("foreign field error = %v", err)
	}
}

type fakeActionType struct{ code string }

func (f fakeActionType) Code() string { return f.code }
func (f fakeActionType) Metadata() ActionTypeMetadata {
	return ActionTypeMetadata{Code: f.code, Label: "Fake"}
}
func (fakeActionType) ValidateConfig(context.Context, ActionValidationContext, json.RawMessage) error {
	return nil
}
func (fakeActionType) Execute(context.Context, ActionExecutionContext, json.RawMessage) (ActionExecutionResult, error) {
	return ActionExecutionResult{}, nil
}

func TestActionRegistriesAreIsolatedAndSealAfterContributions(t *testing.T) {
	first, second := newActionRegistry(), newActionRegistry()
	if err := first.Register(fakeActionType{code: "custom"}); err != nil {
		t.Fatal(err)
	}
	if _, exists := second.Type("custom"); exists {
		t.Fatal("custom action leaked across site registries")
	}
	if err := first.Register(fakeActionType{code: "custom"}); err == nil {
		t.Fatal("duplicate action code was accepted")
	}
	if err := first.Seal(); err != nil {
		t.Fatal(err)
	}
	if err := first.Register(fakeActionType{code: "late"}); err == nil {
		t.Fatal("registration after finalization was accepted")
	}
}

type formsTestDatabaseResolver struct{}

func (formsTestDatabaseResolver) MainModuleDatabase(kernel.ModuleCode) (kernel.ModuleDatabase, bool) {
	return nil, false
}
func (formsTestDatabaseResolver) ModuleDatabase(kernel.ConnectionCode, kernel.ModuleCode) (kernel.ModuleDatabase, bool) {
	return nil, false
}

type formsTestEventBus struct{}

func (formsTestEventBus) Publish(context.Context, eventbus.Message) error { return nil }
func (formsTestEventBus) Consume(context.Context, eventbus.Subscription, eventbus.Handler) error {
	return nil
}

type actionRegistryModule struct{ runtime *Runtime }

func (actionRegistryModule) Registry() kernel.ModuleRegistry {
	return kernel.ModuleRegistry{FieldTypes: field.StandardTypes()}
}

func (actionRegistryModule) Code() kernel.ModuleCode { return ModuleCode }
func (m actionRegistryModule) Build(context.Context, kernel.ModuleContext) (kernel.ModuleRuntime, error) {
	return m.runtime, nil
}

type customActionContributor struct{ action ActionType }

func (customActionContributor) Code() kernel.ModuleCode { return "forms_custom_action_test" }
func (customActionContributor) Dependencies() []kernel.ModuleCode {
	return []kernel.ModuleCode{ModuleCode}
}
func (m customActionContributor) Build(_ context.Context, moduleContext kernel.ModuleContext) (kernel.ModuleRuntime, error) {
	dependency, err := kernel.ModuleDependencyFrom[interface {
		kernel.ModuleRuntime
		ActionRegistrar
		ElementRegistrar
	}](moduleContext, ModuleCode)
	if err != nil {
		return nil, err
	}
	if err := dependency.RegisterActionType(m.action); err != nil {
		return nil, err
	}
	if err := dependency.RegisterElementType(ElementDefinition{Description: ElementTypeMetadata{Code: "contributor.notice", Label: "Notice"}}); err != nil {
		return nil, err
	}
	return customActionContributorRuntime{}, nil
}

type customActionContributorRuntime struct{}

func (customActionContributorRuntime) ModuleCode() kernel.ModuleCode {
	return "forms_custom_action_test"
}

func TestContributorModuleRegistersActionBeforeFormsFinalization(t *testing.T) {
	actions := newActionRegistry()
	elements, err := newElementCatalog()
	if err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{actions: actions, service: &Service{elements: elements}}
	factory, err := kernel.NewProfileRuntimeFactory(formsTestDatabaseResolver{}, kernel.RuntimeServices{
		EventBus: formsTestEventBus{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	blueprint, err := factory.Compile(context.Background(), kernel.Profile{Code: "forms-actions", Modules: []kernel.ProfileModule{
		{Module: actionRegistryModule{runtime: runtime}},
		{Module: customActionContributor{action: fakeActionType{code: "custom"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := blueprint.Build(context.Background(), kernel.NewRuntimeScope("9", "", "", nil)); err != nil {
		t.Fatal(err)
	}
	if _, exists := elements.Type("contributor.notice"); !exists {
		t.Fatal("contributor element was not registered")
	}
	if _, exists := actions.Type("custom"); !exists {
		t.Fatal("contributor action was not registered")
	}
	if err := runtime.RegisterActionType(fakeActionType{code: "late"}); err == nil {
		t.Fatal("Forms action registry remained mutable after runtime publication")
	}
}

type allowAuthorizer struct{}

func (allowAuthorizer) Check(context.Context, security.Actor, permission.Code) error { return nil }

type filesStub struct{ corefile.ManagementService }
type repositoryStub struct {
	Repository
	detail      FormDetail
	record      SubmissionRecord
	createErr   error
	createCalls int
	formInput   CreateFormInput
}

func (r *repositoryStub) CreateForm(_ context.Context, input CreateFormInput) (FormDetail, error) {
	r.formInput = input
	return FormDetail{Form: input.Form, Fields: []FormField{input.Consent, input.Captcha}, Elements: []Element{input.Submit}, Statuses: []Status{input.Status}}, nil
}

func (r *repositoryStub) FormDetailByCode(context.Context, site.ID, string, bool) (FormDetail, error) {
	return r.detail, nil
}
func (r *repositoryStub) CreateResult(_ context.Context, record SubmissionRecord) (ResultDetail, error) {
	r.createCalls++
	r.record = record
	if r.createErr != nil {
		return ResultDetail{}, r.createErr
	}
	record.Result.ID = 77
	for index := range record.Values {
		record.Values[index].ID = ResultValueID(index + 1)
		record.Values[index].ResultID = 77
	}
	return ResultDetail{Result: record.Result, Values: record.Values, Uploads: record.Uploads}, nil
}
func (*repositoryStub) MarkUploadSpoolDeleted(context.Context, site.ID, ResultID, []string) error {
	return nil
}

func TestCreateFormBuildsMandatoryDefaultsBeforeAtomicRepositoryCall(t *testing.T) {
	repository := &repositoryStub{}
	elements, err := newElementCatalog()
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(5, repository, formsFieldResolver(), elements, newActionRegistry(), map[string]CaptchaProvider{"test": &captchaStub{}}, "test", allowAuthorizer{}, &filesStub{}, nil, PublicLimits{
		MaxRequestSize: 1 << 20, MaxScalarFields: 20, MaxScalarValueSize: 1 << 10,
		MaxUploadFileSize: 1 << 20, MaxUploadCount: 4, MaxTotalUploadBytes: 1 << 20,
		SubmissionTimeout: time.Second, RateLimit: 10, RateWindow: time.Minute, RateEntries: 100,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateForm(context.Background(), security.User(44), Form{Code: "feedback", Name: "Feedback", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	input := repository.formInput
	if created.Form.SiteID != 5 || input.Consent.Code != MandatoryConsentCode || !input.Consent.Required || input.Captcha.Code != MandatoryCaptchaCode || !input.Captcha.Required || input.Submit.Type != ElementSubmitButton || input.Status.Code != DefaultStatusCode || !input.Status.IsDefault {
		t.Fatalf("default form input = %#v", input)
	}
}

type transitionRepository struct {
	Repository
	active bool
}

func (r *transitionRepository) HasActiveExecutions(context.Context, site.ID) (bool, error) {
	return r.active, nil
}

func TestRuntimeTransitionBlocksActiveWorkAndAbortRestoresRuntime(t *testing.T) {
	repository := &transitionRepository{active: true}
	lifecycle := &runtimeLifecycle{}
	runtime := &Runtime{service: &Service{siteID: 5, repository: repository, lifecycle: lifecycle}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	transition := kernel.RuntimeTransition{Reason: kernel.RuntimeTransitionProfileChange, ScopeID: "5"}
	if _, err := runtime.PrepareRuntimeTransition(context.Background(), transition); !errors.Is(err, kernel.ErrRuntimeTransitionBlocked) {
		t.Fatalf("active transition error = %v", err)
	}
	if err := lifecycle.withActive(func() error { return nil }); err != nil {
		t.Fatalf("blocked transition did not abort drain: %v", err)
	}
	repository.active = false
	prepared, err := runtime.PrepareRuntimeTransition(context.Background(), transition)
	if err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.withActive(func() error { return nil }); !errors.Is(err, ErrRuntimeDraining) {
		t.Fatalf("prepared transition did not drain runtime: %v", err)
	}
	prepared.Abort()
	if err := lifecycle.withActive(func() error { return nil }); err != nil {
		t.Fatalf("transition abort did not restore runtime: %v", err)
	}
}

type workerRepository struct {
	Repository
	work      ExecutionWork
	status    ExecutionStatus
	attempts  int
	finishErr string
	external  string
}

func (r *workerRepository) ClaimExecution(_ context.Context, _ site.ID, id ActionExecutionID, maxAttempts int) (ExecutionWork, bool, error) {
	if id != r.work.Execution.ID {
		return ExecutionWork{}, false, ErrNotFound
	}
	if r.status != ExecutionPending && r.status != ExecutionRetryable {
		return ExecutionWork{}, false, nil
	}
	if r.attempts >= maxAttempts {
		r.status = ExecutionFailed
		return ExecutionWork{}, false, nil
	}
	r.attempts++
	r.status = ExecutionRunning
	r.work.Execution.Status = ExecutionRunning
	r.work.Execution.AttemptCount = r.attempts
	return r.work, true, nil
}
func (r *workerRepository) FinishExecution(_ context.Context, id ActionExecutionID, status ExecutionStatus, safeError, external string) error {
	if id != r.work.Execution.ID || r.status != ExecutionRunning {
		return ErrConflict
	}
	r.status, r.finishErr, r.external = status, safeError, external
	return nil
}
func (r *workerRepository) ResultHasActiveSubmittedExecutions(context.Context, site.ID, ResultID) (bool, error) {
	return r.status == ExecutionPending || r.status == ExecutionRunning || r.status == ExecutionRetryable, nil
}

type countingAction struct {
	code      string
	calls     int
	retryable bool
}

func (a *countingAction) Code() string { return a.code }
func (a *countingAction) Metadata() ActionTypeMetadata {
	return ActionTypeMetadata{Code: a.code, Label: "Counting"}
}
func (*countingAction) ValidateConfig(context.Context, ActionValidationContext, json.RawMessage) error {
	return nil
}
func (a *countingAction) Execute(context.Context, ActionExecutionContext, json.RawMessage) (ActionExecutionResult, error) {
	a.calls++
	if a.retryable {
		return ActionExecutionResult{}, retryableActionError("temporary", errors.New("temporary failure"))
	}
	return ActionExecutionResult{ExternalReference: "external-7"}, nil
}

func actionJobEnvelope(t *testing.T, siteID site.ID, executionID ActionExecutionID) job.Envelope {
	t.Helper()
	envelope, err := job.NewScoped(ExecuteActionJobName, 1, fmt.Sprint(siteID), struct {
		ExecutionID ActionExecutionID `json:"action_execution_id"`
	}{executionID})
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}

func TestWorkerDoesNotRepeatSucceededExecutionAndBoundsRetries(t *testing.T) {
	const siteID site.ID = 5
	baseWork := ExecutionWork{Execution: ActionExecution{ID: 11, SiteID: siteID, ResultID: 19, ActionCode: "custom", ActionType: "custom", Trigger: Trigger{Type: TriggerSubmitted}, Config: []byte(`{}`)}, Result: Result{ID: 19, SiteID: siteID}}

	t.Run("success and duplicate delivery", func(t *testing.T) {
		repository := &workerRepository{work: baseWork, status: ExecutionPending}
		action := &countingAction{code: "custom"}
		registry := newActionRegistry()
		if err := registry.Register(action); err != nil {
			t.Fatal(err)
		}
		worker, err := newWorker(siteID, repository, registry, nil, &runtimeLifecycle{}, 3, nil)
		if err != nil {
			t.Fatal(err)
		}
		envelope := actionJobEnvelope(t, siteID, 11)
		if err := worker.Handle(context.Background(), envelope); err != nil {
			t.Fatal(err)
		}
		if err := worker.Handle(context.Background(), envelope); err != nil {
			t.Fatal(err)
		}
		if action.calls != 1 || repository.status != ExecutionSucceeded || repository.external != "external-7" {
			t.Fatalf("calls=%d status=%s external=%q", action.calls, repository.status, repository.external)
		}
	})

	t.Run("retry limit", func(t *testing.T) {
		repository := &workerRepository{work: baseWork, status: ExecutionPending}
		action := &countingAction{code: "custom", retryable: true}
		registry := newActionRegistry()
		_ = registry.Register(action)
		worker, err := newWorker(siteID, repository, registry, nil, &runtimeLifecycle{}, 2, nil)
		if err != nil {
			t.Fatal(err)
		}
		envelope := actionJobEnvelope(t, siteID, 11)
		if err := worker.Handle(context.Background(), envelope); err == nil || repository.status != ExecutionRetryable {
			t.Fatalf("first retry = %v, status=%s", err, repository.status)
		}
		if err := worker.Handle(context.Background(), envelope); err != nil || repository.status != ExecutionFailed {
			t.Fatalf("terminal retry = %v, status=%s", err, repository.status)
		}
		if action.calls != 2 || repository.finishErr == "" {
			t.Fatalf("calls=%d safe error=%q", action.calls, repository.finishErr)
		}
	})
}

func TestWorkerTerminalizesUnavailableActionAndAcknowledgesObsoleteJob(t *testing.T) {
	const siteID site.ID = 5
	repository := &workerRepository{work: ExecutionWork{Execution: ActionExecution{ID: 12, SiteID: siteID, ResultID: 20, ActionCode: "gone", ActionType: "gone", Trigger: Trigger{Type: TriggerSubmitted}, Config: []byte(`{}`)}, Result: Result{ID: 20, SiteID: siteID}}, status: ExecutionPending}
	worker, err := newWorker(siteID, repository, newActionRegistry(), nil, &runtimeLifecycle{}, 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Handle(context.Background(), actionJobEnvelope(t, siteID, 12)); err != nil || repository.status != ExecutionFailed || !strings.Contains(repository.finishErr, "unavailable") {
		t.Fatalf("unavailable action = %v, status=%s, safe=%q", err, repository.status, repository.finishErr)
	}
	if err := worker.Handle(context.Background(), actionJobEnvelope(t, siteID, 999)); err != nil {
		t.Fatalf("obsolete action job = %v", err)
	}
}

type captchaStub struct{ calls int }

func (*captchaStub) Code() string { return "test" }
func (*captchaStub) PublicConfig(context.Context) (map[string]any, error) {
	return map[string]any{"site_key": "public"}, nil
}
func (c *captchaStub) Verify(_ context.Context, input CaptchaInput) error {
	c.calls++
	if input.Token != "valid" {
		return errors.New("invalid CAPTCHA")
	}
	return nil
}

func TestSubmissionPersistsTypedSnapshotsWithoutCaptchaOrHiddenFields(t *testing.T) {
	resolver := formsFieldResolver()
	elements, err := newElementCatalog()
	if err != nil {
		t.Fatal(err)
	}
	actions := newActionRegistry()
	captcha := &captchaStub{}
	consentID, captchaID, subscribeID, emailID, submitID := FieldID(1), FieldID(2), FieldID(3), FieldID(4), ElementID(1)
	repository := &repositoryStub{detail: FormDetail{
		Form: Form{ID: 9, SiteID: 5, Code: "feedback", Name: "Feedback", Enabled: true},
		Fields: []FormField{
			{ID: consentID, FormID: 9, Code: MandatoryConsentCode, Type: FieldTypeConsent, Label: "Согласие", Required: true, Options: ConsentOptions{}},
			{ID: captchaID, FormID: 9, Code: MandatoryCaptchaCode, Type: FieldTypeCaptcha, Label: "CAPTCHA", Required: true, Options: CaptchaOptions{Provider: "test"}},
			{ID: subscribeID, FormID: 9, Code: "subscribe", Type: field.TypeCheckbox, Label: "Подписка"},
			{ID: emailID, FormID: 9, Code: "email", Type: field.TypeEmail, Label: "Email", Required: true, VisibleWhen: &field.VisibleWhen{Field: "subscribe", Value: true}},
		},
		Elements: []Element{{ID: submitID, FormID: 9, Code: MandatorySubmitCode, Type: ElementSubmitButton, Config: []byte(`{"label":"Отправить"}`)}},
		Layout:   []LayoutNode{{ID: 1, FieldID: &consentID}, {ID: 2, FieldID: &captchaID}, {ID: 3, FieldID: &subscribeID}, {ID: 4, FieldID: &emailID}, {ID: 5, ElementID: &submitID}},
		Statuses: []Status{{ID: 1, FormID: 9, Code: DefaultStatusCode, Name: "Новый", IsDefault: true}},
	}}
	service, err := NewService(5, repository, resolver, elements, actions, map[string]CaptchaProvider{"test": captcha}, "test", allowAuthorizer{}, &filesStub{}, nil, PublicLimits{
		MaxRequestSize: 1 << 20, MaxScalarFields: 20, MaxScalarValueSize: 1 << 10,
		MaxUploadFileSize: 1 << 20, MaxUploadCount: 4, MaxTotalUploadBytes: 1 << 20,
		SubmissionTimeout: time.Second, RateLimit: 10, RateWindow: time.Minute, RateEntries: 100,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Submit(context.Background(), security.User(44), SubmitInput{FormCode: "feedback", Values: map[string]any{
		MandatoryConsentCode: true, MandatoryCaptchaCode: "valid", "subscribe": false, "email": "not-an-email",
	}, UserAgent: "test-agent", ClientAddress: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Result.ID != 77 || created.Result.UserID == nil || *created.Result.UserID != 44 || created.Result.UserAgent != "test-agent" {
		t.Fatalf("result metadata = %#v", created.Result)
	}
	if captcha.calls != 1 || len(repository.record.Values) != 2 {
		t.Fatalf("CAPTCHA calls=%d values=%#v", captcha.calls, repository.record.Values)
	}
	for _, item := range repository.record.Values {
		if item.FieldCode == MandatoryCaptchaCode || item.FieldCode == "email" {
			t.Fatalf("transient or hidden value persisted: %#v", item)
		}
	}
}

type mailIntegrationStub struct {
	input mail.QueueInput
	body  string
}

func (*mailIntegrationStub) IntegrationTemplate(context.Context, security.Actor, string) (mail.IntegrationTemplateMetadata, error) {
	required := true
	return mail.IntegrationTemplateMetadata{Code: "feedback", Name: "Feedback", Enabled: true, Variables: []field.Definition{{Key: "email", Type: field.TypeEmail, Label: "Email", Required: &required}}}, nil
}
func (m *mailIntegrationStub) QueueByCode(_ context.Context, input mail.QueueInput) (mail.Message, error) {
	m.input = input
	if len(input.Attachments) > 0 {
		raw, err := io.ReadAll(input.Attachments[0].Body)
		if err != nil {
			return mail.Message{}, err
		}
		m.body = string(raw)
	}
	return mail.Message{ID: 91}, nil
}

type uploadAccessorStub struct{}

func (uploadAccessorStub) Metadata(code string) []ResultUpload {
	if code == "documents" {
		return []ResultUpload{{FieldCode: code, Filename: "document.txt", MIMEType: "text/plain", Size: 7}}
	}
	return nil
}
func (uploadAccessorStub) Open(context.Context, string, int) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("payload")), nil
}

func TestMailActionValidatesMappingsAndCopiesTransientStreams(t *testing.T) {
	integration := &mailIntegrationStub{}
	action := mailActionType{mail: integration, fieldTypes: formsFieldResolver()}
	config := json.RawMessage(`{"template_code":"feedback","values":{"email":"email"},"attachments":["documents"]}`)
	fields := []FormField{{Code: "email", Type: field.TypeEmail}, {Code: "documents", Type: FieldTypeUpload}}
	if err := action.ValidateConfig(context.Background(), ActionValidationContext{Actor: security.User(1), Fields: fields, Trigger: Trigger{Type: TriggerSubmitted}}, config); err != nil {
		t.Fatal(err)
	}
	incompatible := json.RawMessage(`{"template_code":"feedback","values":{"email":"age"}}`)
	if err := action.ValidateConfig(context.Background(), ActionValidationContext{Actor: security.User(1), Fields: append(fields, FormField{Code: "age", Type: field.TypeInteger, Options: field.IntegerOptions{}}), Trigger: Trigger{Type: TriggerSubmitted}}, incompatible); !errors.Is(err, ErrInvalid) {
		t.Fatalf("incompatible Mail mapping error = %v", err)
	}
	if err := action.ValidateConfig(context.Background(), ActionValidationContext{Actor: security.User(1), Fields: fields, Trigger: Trigger{Type: TriggerStatusChanged}}, config); !errors.Is(err, ErrInvalid) {
		t.Fatalf("delayed upload mapping error = %v", err)
	}
	result, err := action.Execute(context.Background(), ActionExecutionContext{
		Execution: ActionExecution{Trigger: Trigger{Type: TriggerSubmitted}}, Result: Result{ID: 77},
		Values: []ResultValue{{FieldCode: "email", Position: 0, Value: "person@example.test"}}, Uploads: uploadAccessorStub{},
	}, config)
	if err != nil || result.ExternalReference != "91" {
		t.Fatalf("Mail action result = %#v, %v", result, err)
	}
	if integration.input.TemplateCode != "feedback" || integration.input.Values["email"] != "person@example.test" || integration.body != "payload" {
		t.Fatalf("Mail queue input = %#v, body=%q", integration.input, integration.body)
	}
}

func TestUploadSpoolUsesPrivateSitePrefixAndVerifiesSize(t *testing.T) {
	disk, err := localstorage.New(context.Background(), localstorage.Config{Code: "forms-test", Visibility: filesystem.VisibilityPrivate, Root: t.TempDir(), BaseURL: "https://files.example.test", SigningKey: strings.Repeat("s", 32)})
	if err != nil {
		t.Fatal(err)
	}
	spool, err := NewUploadSpool(8, disk)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := spool.Put(context.Background(), UploadInput{FieldCode: "document", Filename: "../document.txt", MIMEType: "text/plain", Size: 7, Body: strings.NewReader("payload")}, 16)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Filename != "document.txt" || !strings.HasPrefix(stored.SpoolReference, "forms-spool/8/") || len(stored.Checksum) != 64 {
		t.Fatalf("stored upload = %#v", stored)
	}
	body, err := spool.Open(context.Background(), stored.SpoolReference)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(body)
	_ = body.Close()
	if string(raw) != "payload" {
		t.Fatalf("spooled body = %q", raw)
	}
	if _, err := spool.Put(context.Background(), UploadInput{FieldCode: "bad", Filename: "bad.txt", MIMEType: "text/plain", Size: 8, Body: strings.NewReader("short")}, 16); !errors.Is(err, ErrInvalid) {
		t.Fatalf("size mismatch error = %v", err)
	}
}
