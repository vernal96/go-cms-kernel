package forms

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	corefile "github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

var (
	FormReadPermission     = permission.MustCode("forms", "form", permission.Read)
	FormCreatePermission   = permission.MustCode("forms", "form", permission.Create)
	FormUpdatePermission   = permission.MustCode("forms", "form", permission.Update)
	FormDeletePermission   = permission.MustCode("forms", "form", permission.Delete)
	ResultReadPermission   = permission.MustCode("forms", "result", permission.Read)
	ResultUpdatePermission = permission.MustCode("forms", "result", permission.Update)
	ResultDeletePermission = permission.MustCode("forms", "result", permission.Delete)
	ActionReadPermission   = permission.MustCode("forms", "action", permission.Read)
	ActionCreatePermission = permission.MustCode("forms", "action", permission.Create)
	ActionUpdatePermission = permission.MustCode("forms", "action", permission.Update)
	ActionDeletePermission = permission.MustCode("forms", "action", permission.Delete)
	StatusReadPermission   = permission.MustCode("forms", "status", permission.Read)
	StatusCreatePermission = permission.MustCode("forms", "status", permission.Create)
	StatusUpdatePermission = permission.MustCode("forms", "status", permission.Update)
	StatusDeletePermission = permission.MustCode("forms", "status", permission.Delete)
)

type Service struct {
	siteID         site.ID
	repository     Repository
	fieldTypes     field.TypeCatalog
	elements       *elementCatalog
	actions        *actionRegistry
	captcha        map[string]CaptchaProvider
	defaultCaptcha string
	authorizer     security.Authorizer
	files          corefile.ManagementService
	spool          *UploadSpool
	lifecycle      *runtimeLifecycle
	limits         PublicLimits
	rateLimiter    *submitRateLimiter
	logger         *slog.Logger
}

func NewService(
	siteID site.ID,
	repository Repository,
	fieldTypes field.TypeCatalog,
	elements *elementCatalog,
	actions *actionRegistry,
	captcha map[string]CaptchaProvider,
	defaultCaptcha string,
	authorizer security.Authorizer,
	files corefile.ManagementService,
	spool *UploadSpool,
	limits PublicLimits,
	logger *slog.Logger,
) (*Service, error) {
	if siteID <= 0 || repository == nil || fieldTypes == nil || elements == nil || actions == nil || authorizer == nil || files == nil {
		return nil, errors.New("Forms service dependencies are invalid")
	}
	if _, exists := captcha[defaultCaptcha]; !exists {
		return nil, fmt.Errorf("default Forms CAPTCHA provider %q is unavailable", defaultCaptcha)
	}
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	return &Service{
		siteID: siteID, repository: repository, fieldTypes: fieldTypes, elements: elements,
		actions: actions, captcha: captcha, defaultCaptcha: defaultCaptcha, authorizer: authorizer,
		files: files, spool: spool, lifecycle: &runtimeLifecycle{}, limits: limits,
		rateLimiter: newSubmitRateLimiter(limits.RateLimit, limits.RateWindow, limits.RateEntries), logger: logger,
	}, nil
}

func (s *Service) ListForms(ctx context.Context, actor security.Actor, query PageQuery) (FormSummaryPage, error) {
	if err := s.authorizer.Check(ctx, actor, FormReadPermission); err != nil {
		return FormSummaryPage{}, err
	}
	query, err := normalizePage(query)
	if err != nil {
		return FormSummaryPage{}, err
	}
	return s.repository.ListForms(ctx, s.siteID, query)
}

func (s *Service) FormDetail(ctx context.Context, actor security.Actor, id FormID) (FormDetail, error) {
	if err := s.authorizer.Check(ctx, actor, FormReadPermission); err != nil {
		return FormDetail{}, err
	}
	if err := s.authorizer.Check(ctx, actor, StatusReadPermission); err != nil {
		return FormDetail{}, err
	}
	if err := s.authorizer.Check(ctx, actor, ActionReadPermission); err != nil {
		return FormDetail{}, err
	}
	return s.repository.FormDetail(ctx, s.siteID, id)
}

func (s *Service) CreateForm(ctx context.Context, actor security.Actor, item Form) (FormDetail, error) {
	if err := s.authorizer.Check(ctx, actor, FormCreatePermission); err != nil {
		return FormDetail{}, err
	}
	item.ID, item.SiteID = 0, s.siteID
	item.CreatedBy, item.UpdatedBy = actor.AuditUserID(), actor.AuditUserID()
	if err := validateForm(item); err != nil {
		return FormDetail{}, err
	}
	consent := FormField{Code: MandatoryConsentCode, Type: FieldTypeConsent, Label: "Согласие на обработку персональных данных", Required: true, ResultLabel: "Согласие", ShowInResults: true, ResultPosition: 0}
	consent.FormID = 1
	if err := validateFormField(consent, s.fieldTypes); err != nil {
		return FormDetail{}, err
	}
	captcha := FormField{Code: MandatoryCaptchaCode, Type: FieldTypeCaptcha, Label: "Подтвердите, что вы не робот", Required: true, Options: CaptchaOptions{Provider: s.defaultCaptcha}, ResultPosition: 1}
	captcha.FormID = 1
	if err := validateFormField(captcha, s.fieldTypes); err != nil {
		return FormDetail{}, err
	}
	submitConfig, _ := json.Marshal(map[string]any{"label": "Отправить"})
	submit := Element{FormID: 1, Code: MandatorySubmitCode, Type: ElementSubmitButton, Config: submitConfig}
	if err := validateElement(submit, s.elements); err != nil {
		return FormDetail{}, err
	}
	status := Status{FormID: 1, Code: DefaultStatusCode, Name: "Новый", Color: "#409eff", Position: 0, IsDefault: true}
	if err := validateStatus(status); err != nil {
		return FormDetail{}, err
	}
	return s.repository.CreateForm(ctx, CreateFormInput{Form: item, Consent: consent, Captcha: captcha, Submit: submit, Status: status})
}

func (s *Service) UpdateForm(ctx context.Context, actor security.Actor, item Form) (Form, error) {
	if err := s.authorizer.Check(ctx, actor, FormUpdatePermission); err != nil {
		return Form{}, err
	}
	if item.ID <= 0 {
		return Form{}, fmt.Errorf("%w: form ID is invalid", ErrInvalid)
	}
	item.SiteID, item.UpdatedBy = s.siteID, actor.AuditUserID()
	if err := validateForm(item); err != nil {
		return Form{}, err
	}
	return s.repository.UpdateForm(ctx, item)
}

func (s *Service) SetFormEnabled(ctx context.Context, actor security.Actor, id FormID, enabled bool) (Form, error) {
	if err := s.authorizer.Check(ctx, actor, FormUpdatePermission); err != nil {
		return Form{}, err
	}
	if id <= 0 {
		return Form{}, fmt.Errorf("%w: form ID is invalid", ErrInvalid)
	}
	if enabled {
		detail, err := s.repository.FormDetail(ctx, s.siteID, id)
		if err != nil {
			return Form{}, err
		}
		if err := validateMandatoryStructure(detail.Fields, detail.Elements, detail.Layout); err != nil {
			return Form{}, err
		}
		if !exactlyOneDefault(detail.Statuses) {
			return Form{}, fmt.Errorf("%w: form must have exactly one default status", ErrInvalid)
		}
	}
	return s.repository.SetFormEnabled(ctx, s.siteID, id, enabled, actor.AuditUserID())
}

func (s *Service) DeleteForm(ctx context.Context, actor security.Actor, id FormID) error {
	if err := s.authorizer.Check(ctx, actor, FormDeletePermission); err != nil {
		return err
	}
	if id <= 0 {
		return fmt.Errorf("%w: form ID is invalid", ErrInvalid)
	}
	keys, err := s.repository.DeleteForm(ctx, s.siteID, id)
	if err != nil {
		return err
	}
	return s.deleteSpoolReferences(context.WithoutCancel(ctx), keys)
}

func (s *Service) CreateField(ctx context.Context, actor security.Actor, formID FormID, item FormField, placement LayoutPlacement) (FormField, LayoutNode, error) {
	if err := s.authorizer.Check(ctx, actor, FormUpdatePermission); err != nil {
		return FormField{}, LayoutNode{}, err
	}
	item.ID, item.FormID = 0, formID
	if err := validateFormField(item, s.fieldTypes); err != nil {
		return FormField{}, LayoutNode{}, err
	}
	detail, err := s.repository.FormDetail(ctx, s.siteID, formID)
	if err != nil {
		return FormField{}, LayoutNode{}, err
	}
	if err := validateFieldConditions(append(detail.Fields, item), s.fieldTypes); err != nil {
		return FormField{}, LayoutNode{}, err
	}
	return s.repository.CreateField(ctx, s.siteID, formID, item, placement)
}

func (s *Service) UpdateField(ctx context.Context, actor security.Actor, formID FormID, item FormField) (FormField, error) {
	if err := s.authorizer.Check(ctx, actor, FormUpdatePermission); err != nil {
		return FormField{}, err
	}
	detail, err := s.repository.FormDetail(ctx, s.siteID, formID)
	if err != nil {
		return FormField{}, err
	}
	current, exists := fieldByID(detail.Fields, item.ID)
	if !exists {
		return FormField{}, ErrNotFound
	}
	item.FormID = formID
	if current.Code == MandatoryConsentCode && (item.Code != MandatoryConsentCode || item.Type != FieldTypeConsent || !item.Required) {
		return FormField{}, fmt.Errorf("%w: mandatory consent cannot be changed", ErrConflict)
	}
	if current.Code == MandatoryCaptchaCode && (item.Code != MandatoryCaptchaCode || item.Type != FieldTypeCaptcha || !item.Required) {
		return FormField{}, fmt.Errorf("%w: mandatory CAPTCHA cannot be changed", ErrConflict)
	}
	if err := validateFormField(item, s.fieldTypes); err != nil {
		return FormField{}, err
	}
	if err := validateFieldConditions(replaceField(detail.Fields, item), s.fieldTypes); err != nil {
		return FormField{}, err
	}
	// Options can change storage cardinality even when the code/type is stable.
	if err := s.validateActionsAgainstFields(ctx, actor, detail, replaceField(detail.Fields, item)); err != nil {
		return FormField{}, err
	}
	return s.repository.UpdateField(ctx, s.siteID, item)
}

func (s *Service) DeleteField(ctx context.Context, actor security.Actor, formID FormID, id FieldID) error {
	if err := s.authorizer.Check(ctx, actor, FormUpdatePermission); err != nil {
		return err
	}
	detail, err := s.repository.FormDetail(ctx, s.siteID, formID)
	if err != nil {
		return err
	}
	current, exists := fieldByID(detail.Fields, id)
	if !exists {
		return ErrNotFound
	}
	if current.Code == MandatoryConsentCode || current.Code == MandatoryCaptchaCode {
		return fmt.Errorf("%w: mandatory field cannot be deleted", ErrConflict)
	}
	remaining := make([]FormField, 0, len(detail.Fields)-1)
	for _, item := range detail.Fields {
		if item.ID != id {
			remaining = append(remaining, item)
		}
	}
	if err := s.validateActionsAgainstFields(ctx, actor, detail, remaining); err != nil {
		return err
	}
	return s.repository.DeleteField(ctx, s.siteID, formID, id)
}

func (s *Service) CreateElement(ctx context.Context, actor security.Actor, formID FormID, item Element, placement LayoutPlacement) (Element, LayoutNode, error) {
	if err := s.authorizer.Check(ctx, actor, FormUpdatePermission); err != nil {
		return Element{}, LayoutNode{}, err
	}
	item.ID, item.FormID = 0, formID
	if err := validateElement(item, s.elements); err != nil {
		return Element{}, LayoutNode{}, err
	}
	if item.Type == ElementImage {
		if err := s.validateImage(ctx, actor, item.Config); err != nil {
			return Element{}, LayoutNode{}, err
		}
	}
	if item.Type == ElementSubmitButton {
		detail, err := s.repository.FormDetail(ctx, s.siteID, formID)
		if err != nil {
			return Element{}, LayoutNode{}, err
		}
		for _, existing := range detail.Elements {
			if existing.Type == ElementSubmitButton {
				return Element{}, LayoutNode{}, fmt.Errorf("%w: submit element already exists", ErrConflict)
			}
		}
	}
	return s.repository.CreateElement(ctx, s.siteID, formID, item, placement)
}

func (s *Service) UpdateElement(ctx context.Context, actor security.Actor, formID FormID, item Element) (Element, error) {
	if err := s.authorizer.Check(ctx, actor, FormUpdatePermission); err != nil {
		return Element{}, err
	}
	detail, err := s.repository.FormDetail(ctx, s.siteID, formID)
	if err != nil {
		return Element{}, err
	}
	current, exists := elementByID(detail.Elements, item.ID)
	if !exists {
		return Element{}, ErrNotFound
	}
	item.FormID = formID
	if current.Type == ElementSubmitButton && (item.Type != ElementSubmitButton || item.Code != MandatorySubmitCode) {
		return Element{}, fmt.Errorf("%w: mandatory submit element cannot change identity", ErrConflict)
	}
	if item.Type == ElementSubmitButton && current.Type != ElementSubmitButton {
		return Element{}, fmt.Errorf("%w: submit element already exists", ErrConflict)
	}
	if err := validateElement(item, s.elements); err != nil {
		return Element{}, err
	}
	if item.Type == ElementImage {
		if err := s.validateImage(ctx, actor, item.Config); err != nil {
			return Element{}, err
		}
	}
	return s.repository.UpdateElement(ctx, s.siteID, item)
}

func (s *Service) DeleteElement(ctx context.Context, actor security.Actor, formID FormID, id ElementID) error {
	if err := s.authorizer.Check(ctx, actor, FormUpdatePermission); err != nil {
		return err
	}
	detail, err := s.repository.FormDetail(ctx, s.siteID, formID)
	if err != nil {
		return err
	}
	item, exists := elementByID(detail.Elements, id)
	if !exists {
		return ErrNotFound
	}
	if item.Type == ElementSubmitButton {
		return fmt.Errorf("%w: submit element cannot be deleted", ErrConflict)
	}
	return s.repository.DeleteElement(ctx, s.siteID, formID, id)
}

func (s *Service) CreateContainer(ctx context.Context, actor security.Actor, formID FormID, item LayoutNode) (LayoutNode, error) {
	if err := s.authorizer.Check(ctx, actor, FormUpdatePermission); err != nil {
		return LayoutNode{}, err
	}
	item.ID, item.FormID, item.Kind = 0, formID, LayoutContainer
	if item.ContainerType != ContainerGroup && item.ContainerType != ContainerSlide {
		return LayoutNode{}, fmt.Errorf("%w: container type is invalid", ErrInvalid)
	}
	if item.Position < 0 || item.FieldID != nil || item.ElementID != nil {
		return LayoutNode{}, fmt.Errorf("%w: container is invalid", ErrInvalid)
	}
	if len(item.Config) > 0 && !json.Valid(item.Config) {
		return LayoutNode{}, fmt.Errorf("%w: container config is invalid", ErrInvalid)
	}
	return s.repository.CreateContainer(ctx, s.siteID, formID, item)
}

func (s *Service) DeleteContainer(ctx context.Context, actor security.Actor, formID FormID, id LayoutNodeID) error {
	if err := s.authorizer.Check(ctx, actor, FormUpdatePermission); err != nil {
		return err
	}
	return s.repository.DeleteContainer(ctx, s.siteID, formID, id)
}

func (s *Service) ReplaceLayout(ctx context.Context, actor security.Actor, formID FormID, desired []LayoutNode) ([]LayoutNode, error) {
	if err := s.authorizer.Check(ctx, actor, FormUpdatePermission); err != nil {
		return nil, err
	}
	detail, err := s.repository.FormDetail(ctx, s.siteID, formID)
	if err != nil {
		return nil, err
	}
	normalized, err := normalizeAndValidateLayout(detail, desired)
	if err != nil {
		return nil, err
	}
	return s.repository.ReplaceLayout(ctx, s.siteID, formID, normalized)
}

func (s *Service) CreateStatus(ctx context.Context, actor security.Actor, formID FormID, item Status) (Status, error) {
	if err := s.authorizer.Check(ctx, actor, StatusCreatePermission); err != nil {
		return Status{}, err
	}
	item.ID, item.FormID = 0, formID
	if err := validateStatus(item); err != nil {
		return Status{}, err
	}
	return s.repository.CreateStatus(ctx, s.siteID, formID, item)
}

func (s *Service) UpdateStatus(ctx context.Context, actor security.Actor, formID FormID, item Status) (Status, error) {
	if err := s.authorizer.Check(ctx, actor, StatusUpdatePermission); err != nil {
		return Status{}, err
	}
	detail, err := s.repository.FormDetail(ctx, s.siteID, formID)
	if err != nil {
		return Status{}, err
	}
	current, exists := statusByID(detail.Statuses, item.ID)
	if !exists {
		return Status{}, ErrNotFound
	}
	item.FormID = formID
	if current.IsDefault && !item.IsDefault {
		return Status{}, fmt.Errorf("%w: choose another default status instead", ErrConflict)
	}
	if err := validateStatus(item); err != nil {
		return Status{}, err
	}
	if current.Code != item.Code {
		for _, action := range detail.Actions {
			if action.Trigger.From == current.Code || action.Trigger.To == current.Code {
				return Status{}, fmt.Errorf("%w: status is referenced by action %q", ErrConflict, action.Code)
			}
		}
	}
	return s.repository.UpdateStatus(ctx, s.siteID, item)
}

func (s *Service) DeleteStatus(ctx context.Context, actor security.Actor, formID FormID, id StatusID) error {
	if err := s.authorizer.Check(ctx, actor, StatusDeletePermission); err != nil {
		return err
	}
	detail, err := s.repository.FormDetail(ctx, s.siteID, formID)
	if err != nil {
		return err
	}
	item, exists := statusByID(detail.Statuses, id)
	if !exists {
		return ErrNotFound
	}
	if item.IsDefault {
		return fmt.Errorf("%w: default status cannot be deleted", ErrConflict)
	}
	for _, action := range detail.Actions {
		if action.Trigger.From == item.Code || action.Trigger.To == item.Code {
			return fmt.Errorf("%w: status is referenced by action %q", ErrConflict, action.Code)
		}
	}
	return s.repository.DeleteStatus(ctx, s.siteID, formID, id)
}

func (s *Service) CreateAction(ctx context.Context, actor security.Actor, formID FormID, item Action) (Action, error) {
	if err := s.authorizer.Check(ctx, actor, ActionCreatePermission); err != nil {
		return Action{}, err
	}
	detail, err := s.repository.FormDetail(ctx, s.siteID, formID)
	if err != nil {
		return Action{}, err
	}
	item.ID, item.FormID = 0, formID
	if err := s.validateActionConfig(ctx, actor, detail, item, detail.Fields); err != nil {
		return Action{}, err
	}
	return s.repository.CreateAction(ctx, s.siteID, formID, item)
}

func (s *Service) UpdateAction(ctx context.Context, actor security.Actor, formID FormID, item Action) (Action, error) {
	if err := s.authorizer.Check(ctx, actor, ActionUpdatePermission); err != nil {
		return Action{}, err
	}
	detail, err := s.repository.FormDetail(ctx, s.siteID, formID)
	if err != nil {
		return Action{}, err
	}
	item.FormID = formID
	if _, exists := actionByID(detail.Actions, item.ID); !exists {
		return Action{}, ErrNotFound
	}
	if err := s.validateActionConfig(ctx, actor, detail, item, detail.Fields); err != nil {
		return Action{}, err
	}
	return s.repository.UpdateAction(ctx, s.siteID, item)
}

func (s *Service) DeleteAction(ctx context.Context, actor security.Actor, formID FormID, id ActionID) error {
	if err := s.authorizer.Check(ctx, actor, ActionDeletePermission); err != nil {
		return err
	}
	return s.repository.DeleteAction(ctx, s.siteID, formID, id)
}

func (s *Service) AvailableElementTypes() []ElementTypeMetadata { return s.elements.Metadata() }
func (s *Service) AvailableActionTypes() []ActionTypeMetadata   { return s.actions.Metadata() }

func (s *Service) AvailableFieldTypes() []field.TypeCode { return s.fieldTypes.FieldTypes() }

func (s *Service) validateImage(ctx context.Context, actor security.Actor, raw json.RawMessage) error {
	var config struct {
		FileID corefile.ID `json:"file_id"`
	}
	if json.Unmarshal(raw, &config) != nil || config.FileID <= 0 {
		return fmt.Errorf("%w: image file is invalid", ErrInvalid)
	}
	if _, err := s.files.URL(ctx, actor, config.FileID); err != nil {
		return fmt.Errorf("%w: image file must be readable and public: %v", ErrInvalid, err)
	}
	return nil
}

func (s *Service) PublicImageURL(ctx context.Context, raw json.RawMessage) (string, error) {
	var config struct {
		FileID corefile.ID `json:"file_id"`
	}
	if json.Unmarshal(raw, &config) != nil || config.FileID <= 0 {
		return "", ErrInvalid
	}
	return s.files.URL(ctx, security.System(), config.FileID)
}

func (s *Service) validateActionConfig(ctx context.Context, actor security.Actor, detail FormDetail, item Action, fields []FormField) error {
	if err := validateAction(item, detail.Statuses); err != nil {
		return err
	}
	actionType, exists := s.actions.Type(item.ActionType)
	if !exists {
		return fmt.Errorf("%w: %s", ErrActionUnavailable, item.ActionType)
	}
	return actionType.ValidateConfig(ctx, ActionValidationContext{Actor: actor, Form: detail.Form, Fields: append([]FormField(nil), fields...), Trigger: item.Trigger}, item.Config)
}

func (s *Service) validateActionsAgainstFields(ctx context.Context, actor security.Actor, detail FormDetail, fields []FormField) error {
	for _, action := range detail.Actions {
		actionType, exists := s.actions.Type(action.ActionType)
		if !exists {
			return fmt.Errorf("%w: action %q cannot be safely revalidated", ErrConflict, action.Code)
		}
		if err := actionType.ValidateConfig(ctx, ActionValidationContext{Actor: actor, Form: detail.Form, Fields: fields, Trigger: action.Trigger}, action.Config); err != nil {
			return fmt.Errorf("%w: field is referenced by action %q: %v", ErrConflict, action.Code, err)
		}
	}
	return nil
}

func (s *Service) deleteSpoolReferences(ctx context.Context, keys []string) error {
	if len(keys) == 0 || s.spool == nil {
		return nil
	}
	var result error
	for _, key := range keys {
		result = errors.Join(result, s.spool.Delete(ctx, key))
	}
	return result
}

func exactlyOneDefault(items []Status) bool {
	count := 0
	for _, item := range items {
		if item.IsDefault {
			count++
		}
	}
	return count == 1
}
func fieldByID(items []FormField, id FieldID) (FormField, bool) {
	for _, item := range items {
		if item.ID == id {
			return item, true
		}
	}
	return FormField{}, false
}
func elementByID(items []Element, id ElementID) (Element, bool) {
	for _, item := range items {
		if item.ID == id {
			return item, true
		}
	}
	return Element{}, false
}
func statusByID(items []Status, id StatusID) (Status, bool) {
	for _, item := range items {
		if item.ID == id {
			return item, true
		}
	}
	return Status{}, false
}
func actionByID(items []Action, id ActionID) (Action, bool) {
	for _, item := range items {
		if item.ID == id {
			return item, true
		}
	}
	return Action{}, false
}
func replaceField(items []FormField, updated FormField) []FormField {
	result := append([]FormField(nil), items...)
	for i := range result {
		if result[i].ID == updated.ID {
			result[i] = updated
		}
	}
	return result
}

func fieldsByResultPosition(items []FormField) []FormField {
	result := append([]FormField(nil), items...)
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].ResultPosition == result[j].ResultPosition {
			return result[i].Code < result[j].Code
		}
		return result[i].ResultPosition < result[j].ResultPosition
	})
	return result
}

func (s *Service) AvailableFieldMetadata() []field.Metadata {
	result := []field.Metadata{}
	for _, code := range s.AvailableFieldTypes() {
		if item, exists := s.fieldTypes.FieldType(code); exists {
			result = append(result, field.DescribeType(item))
		}
	}
	return result
}
