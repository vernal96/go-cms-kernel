package postgres

import (
	"encoding/json"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/forms"
)

func encodeFieldOptions(item forms.FormField) ([]byte, error) {
	return field.EncodeOptionsJSON(item.Options)
}
func decodeFieldOptions(code field.TypeCode, raw []byte) (any, error) {
	return forms.DecodeFieldOptions(code, raw)
}

func encodeJSON(value any) ([]byte, error)    { return json.Marshal(value) }
func decodeJSON(raw []byte, target any) error { return json.Unmarshal(raw, target) }
