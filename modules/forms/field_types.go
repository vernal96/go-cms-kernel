package forms

import (
	"errors"
	"fmt"
	"strings"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
)

const (
	FieldTypeCaptcha field.TypeCode = "forms.captcha"
	FieldTypeConsent field.TypeCode = "forms.consent"
	FieldTypeUpload  field.TypeCode = "forms.upload"
)

func fieldTypes() []field.Type {
	return []field.Type{
		field.DescribedType{Type: captchaFieldType{}, Presentation: field.Metadata{Label: "CAPTCHA", Options: []field.ConfigField{{Key: "provider", Label: "CAPTCHA-провайдер", Type: field.TypeString}}}},
		field.DescribedType{Type: consentFieldType{}, Presentation: field.Metadata{Label: "Согласие", Editor: "checkbox", Options: []field.ConfigField{{Key: "text", Label: "Текст согласия", Type: field.TypeTextarea}, {Key: "url", Label: "Ссылка на документ", Type: field.TypeString}}}},
		field.DescribedType{Type: uploadFieldType{}, Presentation: field.Metadata{Label: "Загрузка файлов", Options: []field.ConfigField{{Key: "mime_types", Label: "Разрешённые MIME-типы", Type: field.TypeJSON, Editor: "core.string-list"}, {Key: "max_file_size", Label: "Максимум байт на файл", Type: field.TypeInteger}, {Key: "multiple", Label: "Несколько файлов", Type: field.TypeCheckbox}, {Key: "max_files", Label: "Максимум файлов", Type: field.TypeInteger}}}},
	}
}

type captchaFieldType struct{}

func (captchaFieldType) Code() field.TypeCode { return FieldTypeCaptcha }
func (captchaFieldType) Compile(ctx field.CompileContext, options any) (field.ValueType, error) {
	if _, err := captchaOptions(options); err != nil {
		return nil, err
	}
	return captchaValue{}, nil
}

type captchaValue struct{}

func (captchaValue) Normalize(value any) (any, error) {
	token, ok := value.(string)
	if !ok {
		return nil, errors.New("CAPTCHA token must be a string")
	}
	return strings.TrimSpace(token), nil
}
func (captchaValue) Empty(value any) bool { valueString, _ := value.(string); return valueString == "" }
func (captchaValue) Validate(any) error   { return nil }
func (captchaValue) Rules() []string      { return nil }
func (captchaValue) Example() any         { return "token" }

type consentFieldType struct{}

func (consentFieldType) Code() field.TypeCode { return FieldTypeConsent }
func (consentFieldType) Compile(ctx field.CompileContext, options any) (field.ValueType, error) {
	if _, err := consentOptions(options); err != nil {
		return nil, err
	}
	return consentValue{}, nil
}

type consentValue struct{}

func (consentValue) StorageKind() field.StorageKind { return field.StorageBoolean }
func (consentValue) Multiple() bool                 { return false }
func (consentValue) Normalize(value any) (any, error) {
	result, ok := value.(bool)
	if !ok {
		return nil, errors.New("consent value must be boolean")
	}
	return result, nil
}
func (consentValue) Empty(value any) bool { result, _ := value.(bool); return !result }
func (consentValue) Validate(any) error   { return nil }
func (consentValue) Rules() []string      { return nil }
func (consentValue) Example() any         { return true }

type uploadFieldType struct{}

func (uploadFieldType) Code() field.TypeCode { return FieldTypeUpload }
func (uploadFieldType) Compile(ctx field.CompileContext, options any) (field.ValueType, error) {
	if _, err := uploadOptions(options); err != nil {
		return nil, err
	}
	return uploadValue{}, nil
}

type uploadValue struct{}

func (uploadValue) Normalize(any) (any, error) {
	return nil, errors.New("upload values are parsed from multipart streams")
}
func (uploadValue) Empty(any) bool     { return true }
func (uploadValue) Validate(any) error { return nil }
func (uploadValue) Rules() []string    { return nil }
func (uploadValue) Example() any       { return "upload" }

func captchaOptions(value any) (CaptchaOptions, error) {
	switch options := value.(type) {
	case nil:
		return CaptchaOptions{}, nil
	case CaptchaOptions:
		return options, validateCaptchaOptions(options)
	case *CaptchaOptions:
		if options != nil {
			return *options, validateCaptchaOptions(*options)
		}
	}
	return CaptchaOptions{}, fmt.Errorf("CAPTCHA options have type %T", value)
}

func validateCaptchaOptions(options CaptchaOptions) error {
	if options.Provider != strings.TrimSpace(options.Provider) {
		return errors.New("CAPTCHA provider code is invalid")
	}
	return nil
}

func consentOptions(value any) (ConsentOptions, error) {
	switch options := value.(type) {
	case nil:
		return ConsentOptions{}, nil
	case ConsentOptions:
		return options, nil
	case *ConsentOptions:
		if options != nil {
			return *options, nil
		}
	}
	return ConsentOptions{}, fmt.Errorf("consent options have type %T", value)
}

func uploadOptions(value any) (UploadOptions, error) {
	var options UploadOptions
	switch typed := value.(type) {
	case nil:
	case UploadOptions:
		options = typed
	case *UploadOptions:
		if typed != nil {
			options = *typed
		}
	default:
		return UploadOptions{}, fmt.Errorf("upload options have type %T", value)
	}
	if options.MaxFileSize < 0 || options.MaxFiles < 0 {
		return UploadOptions{}, errors.New("upload limits are invalid")
	}
	if !options.Multiple && options.MaxFiles > 1 {
		return UploadOptions{}, errors.New("single upload field cannot allow multiple files")
	}
	seen := make(map[string]struct{}, len(options.MIMETypes))
	for _, pattern := range options.MIMETypes {
		if !validMIMEPattern(pattern) {
			return UploadOptions{}, fmt.Errorf("upload MIME pattern %q is invalid", pattern)
		}
		if _, exists := seen[pattern]; exists {
			return UploadOptions{}, fmt.Errorf("upload MIME pattern %q is duplicated", pattern)
		}
		seen[pattern] = struct{}{}
	}
	return options, nil
}

func validMIMEPattern(value string) bool {
	parts := strings.Split(strings.TrimSpace(value), "/")
	return len(parts) == 2 && parts[0] != "" && parts[0] != "*" && parts[1] != "" &&
		(parts[1] == "*" || !strings.Contains(parts[1], "*")) && !strings.ContainsAny(value, " \t\r\n")
}

func mimeAllowed(patterns []string, mimeType string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, pattern := range patterns {
		if pattern == mimeType || (strings.HasSuffix(pattern, "/*") && strings.HasPrefix(mimeType, strings.TrimSuffix(pattern, "*"))) {
			return true
		}
	}
	return false
}
