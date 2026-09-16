package forms

import (
	"encoding/json"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
)

// DecodeFieldOptions is shared by Forms HTTP and persistence. Only Forms'
// own transient/configuration types need module-specific decoding.
func DecodeFieldOptions(code field.TypeCode, raw json.RawMessage) (any, error) {
	if len(raw) == 0 {
		raw = nil
	}
	switch code {
	case FieldTypeCaptcha:
		return field.DecodeOptions[CaptchaOptions](raw)
	case FieldTypeConsent:
		return field.DecodeOptions[ConsentOptions](raw)
	case FieldTypeUpload:
		return field.DecodeOptions[UploadOptions](raw)
	default:
		return field.DecodeOptionsJSON(code, raw)
	}
}
