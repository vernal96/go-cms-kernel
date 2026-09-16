package forms

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/mail"
	"github.com/vernal96/go-cms-kernel/security"
)

const MailActionTypeCode = "mail"

type mailIntegration interface {
	IntegrationTemplate(context.Context, security.Actor, string) (mail.IntegrationTemplateMetadata, error)
	QueueByCode(context.Context, mail.QueueInput) (mail.Message, error)
}

type MailActionConfig struct {
	TemplateCode string            `json:"template_code"`
	Values       map[string]string `json:"values"`
	Attachments  []string          `json:"attachments"`
}

type mailActionType struct {
	mail       mailIntegration
	fieldTypes field.TypeResolver
}

func (mailActionType) Code() string { return MailActionTypeCode }
func (mailActionType) Metadata() ActionTypeMetadata {
	return ActionTypeMetadata{
		Code: MailActionTypeCode, Label: "Письмо", EditorCode: "forms.mail",
		Fields: []field.ConfigField{{Key: "template_code", Label: "Шаблон Mail", Type: "string", Required: true}},
	}
}

func (t mailActionType) ValidateConfig(ctx context.Context, validation ActionValidationContext, raw json.RawMessage) error {
	config, err := decodeMailActionConfig(raw)
	if err != nil {
		return err
	}
	if validation.Trigger.Type == TriggerStatusChanged && len(config.Attachments) > 0 {
		return fmt.Errorf("%w: status_changed Mail action cannot use transient uploads", ErrInvalid)
	}
	template, err := t.mail.IntegrationTemplate(ctx, validation.Actor, config.TemplateCode)
	if err != nil {
		return err
	}
	if !template.Enabled {
		return fmt.Errorf("%w: Mail template is disabled", ErrInvalid)
	}
	fields := make(map[string]FormField, len(validation.Fields))
	for _, item := range validation.Fields {
		fields[item.Code] = item
	}
	variables := make(map[string]field.Definition, len(template.Variables))
	for _, item := range template.Variables {
		variables[item.Key] = item
	}
	for variable, fieldCode := range config.Values {
		definition, exists := variables[variable]
		if !exists {
			return fmt.Errorf("%w: Mail variable %q is unavailable", ErrInvalid, variable)
		}
		item, exists := fields[fieldCode]
		if !exists || item.Type == FieldTypeCaptcha || item.Type == FieldTypeUpload {
			return fmt.Errorf("%w: Form field %q is not a scalar Mail value", ErrInvalid, fieldCode)
		}
		if definition.Type == field.TypeFile {
			return fmt.Errorf("%w: Mail file variables cannot map transient Form values", ErrInvalid)
		}
		if err := validateMailValueCompatibility(t.fieldTypes, definition, item); err != nil {
			return fmt.Errorf("%w: Mail variable %q and Form field %q are incompatible", ErrInvalid, variable, fieldCode)
		}
	}
	for _, definition := range template.Variables {
		if definition.Required != nil && *definition.Required {
			if _, exists := config.Values[definition.Key]; !exists {
				return fmt.Errorf("%w: required Mail variable %q is not mapped", ErrInvalid, definition.Key)
			}
		}
	}
	seen := make(map[string]struct{}, len(config.Attachments))
	for _, code := range config.Attachments {
		if _, duplicate := seen[code]; duplicate {
			return fmt.Errorf("%w: attachment field %q is duplicated", ErrInvalid, code)
		}
		item, exists := fields[code]
		if !exists || item.Type != FieldTypeUpload {
			return fmt.Errorf("%w: attachment field %q is not a Forms upload", ErrInvalid, code)
		}
		seen[code] = struct{}{}
	}
	return nil
}

func validateMailValueCompatibility(resolver field.TypeResolver, target field.Definition, source FormField) error {
	if resolver == nil {
		return errors.New("field type resolver is unavailable")
	}
	targetType, exists := resolver.FieldType(target.Type)
	if !exists || targetType == nil {
		return errors.New("Mail variable type is unavailable")
	}
	targetValue, err := targetType.Compile(field.CompileContext{Types: resolver}, target.Options)
	if err != nil || targetValue == nil {
		return errors.Join(errors.New("Mail variable type is invalid"), err)
	}
	sourceType, exists := resolver.FieldType(source.Type)
	if !exists || sourceType == nil {
		return errors.New("Form field type is unavailable")
	}
	sourceValue, err := sourceType.Compile(field.CompileContext{Types: resolver}, source.Options)
	if err != nil || sourceValue == nil {
		return errors.Join(errors.New("Form field type is invalid"), err)
	}
	targetStorage, targetPersistent := targetValue.(field.StorageValueType)
	sourceStorage, sourcePersistent := sourceValue.(field.StorageValueType)
	if !targetPersistent || !sourcePersistent || targetStorage.StorageKind() != sourceStorage.StorageKind() || targetStorage.Multiple() != sourceStorage.Multiple() {
		return errors.New("field storage semantics differ")
	}
	return nil
}

func (t mailActionType) Execute(ctx context.Context, execution ActionExecutionContext, raw json.RawMessage) (ActionExecutionResult, error) {
	config, err := decodeMailActionConfig(raw)
	if err != nil {
		return ActionExecutionResult{}, terminalActionError("invalid_config", err)
	}
	valuesByCode := make(map[string][]ResultValue)
	for _, item := range execution.Values {
		valuesByCode[item.FieldCode] = append(valuesByCode[item.FieldCode], item)
	}
	values := make(map[string]any, len(config.Values))
	for variable, code := range config.Values {
		items := valuesByCode[code]
		if len(items) == 0 {
			return ActionExecutionResult{}, terminalActionError("missing_value", fmt.Errorf("mapped result field %q is missing", code))
		}
		values[variable] = ResultFieldValue(items)
	}
	attachments := []mail.TransientAttachment{}
	closers := []io.Closer{}
	defer func() {
		for _, closer := range closers {
			_ = closer.Close()
		}
	}()
	for _, code := range config.Attachments {
		metadata := execution.Uploads.Metadata(code)
		if len(metadata) == 0 {
			return ActionExecutionResult{}, terminalActionError("missing_upload", fmt.Errorf("mapped upload field %q is missing", code))
		}
		for _, item := range metadata {
			body, openErr := execution.Uploads.Open(ctx, code, item.Position)
			if openErr != nil {
				return ActionExecutionResult{}, terminalActionError("missing_upload", openErr)
			}
			closers = append(closers, body)
			attachments = append(attachments, mail.TransientAttachment{Filename: item.Filename, MIMEType: item.MIMEType, Size: item.Size, Body: body})
		}
	}
	message, err := t.mail.QueueByCode(ctx, mail.QueueInput{
		TemplateCode: config.TemplateCode, Values: values, Attachments: attachments,
		Origin: mail.Origin{Kind: mail.OriginAutomatic, Source: "forms", Event: string(execution.Execution.Trigger.Type), Reference: fmt.Sprint(execution.Result.ID)},
	})
	if err != nil {
		switch {
		case errors.Is(err, mail.ErrNotFound), errors.Is(err, mail.ErrTemplateDisabled), errors.Is(err, mail.ErrInvalid), errors.Is(err, mail.ErrNoRecipients), errors.Is(err, mail.ErrSenderNotAllowed):
			return ActionExecutionResult{}, terminalActionError("mail_config", err)
		default:
			return ActionExecutionResult{}, retryableActionError("mail_unavailable", err)
		}
	}
	return ActionExecutionResult{ExternalReference: fmt.Sprint(message.ID)}, nil
}

func decodeMailActionConfig(raw json.RawMessage) (MailActionConfig, error) {
	var config MailActionConfig
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return MailActionConfig{}, fmt.Errorf("%w: Mail action config is invalid", ErrInvalid)
	}
	config.TemplateCode = strings.TrimSpace(config.TemplateCode)
	if config.TemplateCode == "" || config.TemplateCode != strings.ToLower(config.TemplateCode) {
		return MailActionConfig{}, fmt.Errorf("%w: Mail template code is invalid", ErrInvalid)
	}
	if config.Values == nil {
		config.Values = map[string]string{}
	}
	for variable, code := range config.Values {
		if strings.TrimSpace(variable) == "" || variable != strings.TrimSpace(variable) || validateCode(code, "field") != nil {
			return MailActionConfig{}, fmt.Errorf("%w: Mail action value mapping is invalid", ErrInvalid)
		}
	}
	for _, code := range config.Attachments {
		if validateCode(code, "field") != nil {
			return MailActionConfig{}, fmt.Errorf("%w: Mail attachment mapping is invalid", ErrInvalid)
		}
	}
	return config, nil
}

func terminalActionError(code string, err error) *ActionError {
	return &ActionError{Code: code, Err: err}
}
func retryableActionError(code string, err error) *ActionError {
	return &ActionError{Code: code, Retryable: true, Err: err}
}
