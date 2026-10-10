package forms

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	coremedia "github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/security"
)

type SubmitInput struct {
	FormCode        string
	MultipartValues map[string][]string
	Values          map[string]any
	Uploads         []UploadInput
	UserAgent       string
	ClientAddress   string
}

func (s *Service) PublicForm(ctx context.Context, code string) (FormDetail, error) {
	if err := validateCode(code, "form"); err != nil {
		return FormDetail{}, ErrNotFound
	}
	return s.repository.FormDetailByCode(ctx, s.siteID, code, true)
}

func (s *Service) CaptchaPublicConfig(ctx context.Context, item FormField) (map[string]any, error) {
	options, err := captchaOptions(item.Options)
	if err != nil {
		return nil, err
	}
	code := options.Provider
	if code == "" {
		code = s.defaultCaptcha
	}
	provider, exists := s.captcha[code]
	if !exists {
		return nil, ErrCaptchaUnavailable
	}
	result, err := provider.PublicConfig(ctx)
	if err != nil {
		return nil, err
	}
	if result == nil {
		result = map[string]any{}
	}
	result["provider"] = code
	return result, nil
}

func (s *Service) Submit(ctx context.Context, actor security.Actor, input SubmitInput) (ResultDetail, error) {
	if err := validateCode(input.FormCode, "form"); err != nil {
		return ResultDetail{}, ErrNotFound
	}
	client := strings.TrimSpace(input.ClientAddress)
	if client == "" {
		client = "unknown"
	}
	rateKey := fmt.Sprintf("%d:%s:%s", s.siteID, input.FormCode, client)
	if !s.rateLimiter.Allow(rateKey, time.Now().UTC()) {
		return ResultDetail{}, ErrRateLimited
	}
	if input.MultipartValues != nil {
		input.Values = multipartValues(input.MultipartValues)
	}
	if err := s.validateScalarLimits(input.Values); err != nil {
		return ResultDetail{}, err
	}
	if len(input.Uploads) > s.limits.MaxUploadCount {
		return ResultDetail{}, ErrRequestTooLarge
	}

	prepared, err := s.validateSubmission(ctx, input, client)
	if err != nil {
		return ResultDetail{}, err
	}
	values, err := submissionResultValues(prepared)
	if err != nil {
		return ResultDetail{}, err
	}
	spooled, cleanup, err := s.spoolSubmissionUploads(ctx, prepared)
	if err != nil {
		return ResultDetail{}, err
	}

	defaultStatus := defaultStatus(prepared.detail.Statuses)
	result := Result{
		SiteID: s.siteID, FormID: prepared.detail.Form.ID, FormCode: prepared.detail.Form.Code, FormName: prepared.detail.Form.Name,
		StatusID: defaultStatus.ID, StatusCode: defaultStatus.Code, StatusName: defaultStatus.Name, StatusColor: defaultStatus.Color,
		UserID: actor.AuditUserID(), UserAgent: truncateUTF8(strings.TrimSpace(input.UserAgent), 1024),
	}
	if s.limits.StoreClientAddress {
		result.ClientAddress = truncateUTF8(client, 255)
	}
	actions := matchingActions(prepared.detail.Actions, Trigger{Type: TriggerSubmitted})
	record := SubmissionRecord{Result: result, Values: values, Uploads: spooled, Actions: actions}
	var created ResultDetail
	err = s.lifecycle.withActive(func() error {
		var createErr error
		created, createErr = s.repository.CreateResult(ctx, record)
		return createErr
	})
	if err != nil {
		cleanup()
		return ResultDetail{}, err
	}
	if len(created.Executions) == 0 && len(spooled) > 0 {
		keys := make([]string, len(spooled))
		for i, item := range spooled {
			keys[i] = item.SpoolReference
		}
		if cleanupErr := s.deleteSpoolReferences(context.WithoutCancel(ctx), keys); cleanupErr != nil {
			if s.logger != nil {
				s.logger.ErrorContext(context.WithoutCancel(ctx), "Forms upload cleanup failed", slog.String("event", "forms.spool.cleanup.failed"), slog.Int64("result_id", int64(created.Result.ID)), slog.Any("error", cleanupErr))
			}
		} else if markErr := s.repository.MarkUploadSpoolDeleted(context.WithoutCancel(ctx), s.siteID, created.Result.ID, keys); markErr != nil && s.logger != nil {
			s.logger.ErrorContext(context.WithoutCancel(ctx), "Forms upload cleanup metadata failed", slog.String("event", "forms.spool.cleanup.failed"), slog.Int64("result_id", int64(created.Result.ID)), slog.Any("error", markErr))
		}
	}
	if s.logger != nil {
		s.logger.InfoContext(ctx, "Forms result accepted", slog.String("event", "forms.result.accepted"), slog.Int64("site_id", int64(s.siteID)), slog.Int64("form_id", int64(prepared.detail.Form.ID)), slog.Int64("result_id", int64(created.Result.ID)), slog.Int("action_count", len(created.Executions)), slog.Int("upload_count", len(spooled)))
		s.logQueuedActions(ctx, created.Executions)
	}
	return created, nil
}

type validatedSubmission struct {
	detail  FormDetail
	active  []FormField
	values  map[string]any
	uploads []UploadInput
	schema  *field.Schema
}

func (s *Service) validateSubmission(ctx context.Context, input SubmitInput, client string) (validatedSubmission, error) {
	detail, err := s.repository.FormDetailByCode(ctx, s.siteID, input.FormCode, true)
	if err != nil {
		return validatedSubmission{}, err
	}
	if err := validateMandatoryStructure(detail.Fields, detail.Elements, detail.Layout); err != nil {
		return validatedSubmission{}, err
	}
	if !exactlyOneDefault(detail.Statuses) {
		return validatedSubmission{}, fmt.Errorf("%w: form status configuration is invalid", ErrInvalid)
	}
	if input.MultipartValues != nil {
		input.Values, err = normalizeMultipartLists(detail.Fields, input.Values, s.fieldTypes)
		if err != nil {
			return validatedSubmission{}, err
		}
	}
	active, err := resolveActiveFields(detail.Fields, input.Values, s.fieldTypes)
	if err != nil {
		return validatedSubmission{}, err
	}
	if err := validateSubmissionFieldNames(detail.Fields, input.Values, input.Uploads); err != nil {
		return validatedSubmission{}, err
	}
	definitions, ordinaryValues := submissionFieldInputs(active, input.Values)
	schema, err := field.CompilePersistent(definitions, s.fieldTypes)
	if err != nil {
		return validatedSubmission{}, err
	}
	normalized, err := schema.Validate(ordinaryValues)
	if err != nil {
		return validatedSubmission{}, err
	}
	if err := s.validateFileReferences(ctx, schema, normalized); err != nil {
		return validatedSubmission{}, err
	}
	if err := s.verifyCaptchas(ctx, detail.Form, active, input.Values, client); err != nil {
		return validatedSubmission{}, err
	}
	uploads, err := s.validateUploadInputs(active, input.Uploads)
	if err != nil {
		return validatedSubmission{}, err
	}
	return validatedSubmission{detail: detail, active: active, values: normalized, uploads: uploads, schema: schema}, nil
}

func (s *Service) validateFileReferences(ctx context.Context, schema *field.Schema, values map[string]any) error {
	references, err := schema.FileReferences(values)
	if err != nil {
		return fmt.Errorf("Forms file references are invalid: %w", err)
	}
	fieldErrors := make(FieldValidationErrors)
	for _, reference := range references {
		resolved, err := s.media.Resolve(ctx, security.System(), coremedia.ID(reference.ID))
		if err != nil || !field.FileMatches(reference.Options, resolved.File.Storage, resolved.File.MIMEType) {
			fieldErrors[reference.Key] = append(fieldErrors[reference.Key], "file")
		}
	}
	if len(fieldErrors) > 0 {
		return fieldErrors
	}
	return nil
}

func validateSubmissionFieldNames(fields []FormField, values map[string]any, uploads []UploadInput) error {
	known := make(map[string]FormField, len(fields))
	for _, item := range fields {
		known[item.Code] = item
	}
	fieldErrors := make(FieldValidationErrors)
	for code := range values {
		if _, exists := known[code]; !exists {
			fieldErrors[code] = append(fieldErrors[code], "defined")
		}
	}
	for _, upload := range uploads {
		item, exists := known[upload.FieldCode]
		if !exists || item.Type != FieldTypeUpload {
			fieldErrors[upload.FieldCode] = append(fieldErrors[upload.FieldCode], "defined")
		}
	}
	if len(fieldErrors) > 0 {
		return fieldErrors
	}
	return nil
}

func submissionFieldInputs(active []FormField, values map[string]any) ([]field.Definition, map[string]any) {
	definitions := make([]field.Definition, 0, len(active))
	ordinaryValues := make(map[string]any)
	for _, item := range active {
		if item.Type == FieldTypeCaptcha || item.Type == FieldTypeUpload {
			continue
		}
		definitions = append(definitions, item.Definition())
		if value, exists := values[item.Code]; exists {
			ordinaryValues[item.Code] = value
		}
	}
	return definitions, ordinaryValues
}

func submissionResultValues(prepared validatedSubmission) ([]ResultValue, error) {
	references, err := prepared.schema.References(prepared.values)
	if err != nil {
		return nil, err
	}
	stored, err := prepared.schema.StoredValues(prepared.values)
	if err != nil {
		return nil, err
	}
	values := make([]ResultValue, 0, len(stored))
	for _, storedValue := range stored {
		formField, exists := fieldByCode(prepared.active, storedValue.Key)
		if !exists {
			return nil, errors.New("validated Forms field metadata is unavailable")
		}
		fieldID := formField.ID
		fileReferences := []field.Reference{}
		for _, reference := range references {
			if len(reference.Path) == 0 || reference.Path[0] != storedValue.Key || (reference.Target != field.ReferenceFile && reference.Target != field.ReferenceMedia) {
				continue
			}
			path := reference.Path[1:]
			if storedValue.Multiple {
				if len(path) == 0 || path[0] != fmt.Sprint(storedValue.Position) {
					continue
				}
				path = path[1:]
			}
			reference.Path = append([]string{}, path...)
			fileReferences = append(fileReferences, reference)
		}
		values = append(values, ResultValue{
			FieldID: &fieldID, FieldCode: formField.Code, FieldLabel: formField.Label,
			ResultLabel: formField.EffectiveResultLabel(), FieldType: formField.Type,
			Multiple: storedValue.Multiple, StorageKind: storedValue.Kind, Position: storedValue.Position, Value: storedValue.Value,
			ReferenceTarget: storedValue.ReferenceTarget, References: storedValue.References,
			FileReferences: fileReferences,
		})
	}
	return values, nil
}

func (s *Service) spoolSubmissionUploads(ctx context.Context, prepared validatedSubmission) ([]ResultUpload, func(), error) {
	spooled := make([]ResultUpload, 0, len(prepared.uploads))
	cleanup := func() {
		if s.spool == nil {
			return
		}
		for _, item := range spooled {
			_ = s.spool.Delete(context.WithoutCancel(ctx), item.SpoolReference)
		}
	}
	for _, upload := range prepared.uploads {
		if s.spool == nil {
			cleanup()
			return nil, nil, fmt.Errorf("%w: Forms uploads are disabled", ErrInvalid)
		}
		storedUpload, err := s.spool.Put(ctx, upload, s.limits.MaxUploadFileSize)
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		formField, _ := fieldByCode(prepared.active, upload.FieldCode)
		fieldID := formField.ID
		storedUpload.FieldID = &fieldID
		spooled = append(spooled, storedUpload)
	}
	return spooled, cleanup, nil
}

func (s *Service) logQueuedActions(ctx context.Context, executions []ActionExecution) {
	if s.logger == nil {
		return
	}
	for _, execution := range executions {
		s.logger.InfoContext(ctx, "Forms action queued",
			slog.String("event", "forms.action.queued"),
			slog.Int64("site_id", int64(s.siteID)),
			slog.Int64("result_id", int64(execution.ResultID)),
			slog.Int64("action_execution_id", int64(execution.ID)),
			slog.String("action_code", execution.ActionCode),
			slog.String("trigger", string(execution.Trigger.Type)),
		)
	}
}

func (s *Service) validateScalarLimits(values map[string]any) error {
	if len(values) > s.limits.MaxScalarFields {
		return ErrRequestTooLarge
	}
	for _, value := range values {
		raw, err := json.Marshal(value)
		if err != nil || int64(len(raw)) > s.limits.MaxScalarValueSize {
			return ErrRequestTooLarge
		}
	}
	return nil
}

func (s *Service) verifyCaptchas(ctx context.Context, form Form, fields []FormField, values map[string]any, client string) error {
	for _, item := range fields {
		if item.Type != FieldTypeCaptcha {
			continue
		}
		token, _ := values[item.Code].(string)
		if strings.TrimSpace(token) == "" {
			return FieldValidationErrors{item.Code: {"required"}}
		}
		options, err := captchaOptions(item.Options)
		if err != nil {
			return err
		}
		providerCode := options.Provider
		if providerCode == "" {
			providerCode = s.defaultCaptcha
		}
		provider, exists := s.captcha[providerCode]
		if !exists {
			return ErrCaptchaUnavailable
		}
		if err := provider.Verify(ctx, CaptchaInput{SiteID: s.siteID, FormCode: form.Code, Token: token, ClientAddress: client}); err != nil {
			return FieldValidationErrors{item.Code: {"captcha"}}
		}
	}
	return nil
}

func (s *Service) validateUploadInputs(fields []FormField, inputs []UploadInput) ([]UploadInput, error) {
	active := make(map[string]FormField, len(fields))
	for _, item := range fields {
		active[item.Code] = item
	}
	grouped := make(map[string][]UploadInput)
	var total int64
	for _, input := range inputs {
		item, exists := active[input.FieldCode]
		if !exists {
			continue
		}
		if item.Type != FieldTypeUpload {
			return nil, FieldValidationErrors{input.FieldCode: {"type"}}
		}
		options, err := uploadOptions(item.Options)
		if err != nil {
			return nil, err
		}
		limit := s.limits.MaxUploadFileSize
		if options.MaxFileSize > 0 && options.MaxFileSize < limit {
			limit = options.MaxFileSize
		}
		if input.Size < 0 || input.Size > limit {
			return nil, FieldValidationErrors{input.FieldCode: {"max"}}
		}
		if !mimeAllowed(options.MIMETypes, input.MIMEType) {
			return nil, FieldValidationErrors{input.FieldCode: {"mime"}}
		}
		total += input.Size
		if total > s.limits.MaxTotalUploadBytes {
			return nil, ErrRequestTooLarge
		}
		grouped[input.FieldCode] = append(grouped[input.FieldCode], input)
	}
	result := make([]UploadInput, 0, len(inputs))
	for _, item := range fields {
		if item.Type != FieldTypeUpload {
			continue
		}
		items := grouped[item.Code]
		options, err := uploadOptions(item.Options)
		if err != nil {
			return nil, err
		}
		if item.Required && len(items) == 0 {
			return nil, FieldValidationErrors{item.Code: {"required"}}
		}
		maxFiles := 1
		if options.Multiple {
			maxFiles = options.MaxFiles
			if maxFiles == 0 {
				maxFiles = s.limits.MaxUploadCount
			}
		}
		if len(items) > maxFiles {
			return nil, FieldValidationErrors{item.Code: {"max_files"}}
		}
		for index := range items {
			items[index].Position = index
			result = append(result, items[index])
		}
	}
	return result, nil
}

func fieldByCode(items []FormField, code string) (FormField, bool) {
	for _, item := range items {
		if item.Code == code {
			return item, true
		}
	}
	return FormField{}, false
}
func defaultStatus(items []Status) Status {
	for _, item := range items {
		if item.IsDefault {
			return item
		}
	}
	return Status{}
}
func matchingActions(items []Action, trigger Trigger) []Action {
	result := []Action{}
	for _, item := range items {
		if matchesTrigger(item, trigger) {
			result = append(result, item)
		}
	}
	return result
}

func truncateUTF8(value string, size int) string {
	if len(value) <= size {
		return value
	}
	value = value[:size]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
