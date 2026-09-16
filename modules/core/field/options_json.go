package field

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// DecodeOptionsJSON reconstructs the built-in Go option types at JSON
// boundaries. Contributed types receive an object for their own compiler.
func DecodeOptionsJSON(code TypeCode, raw json.RawMessage) (any, error) {
	empty := len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
	if empty {
		raw = nil
	}
	switch code {
	case TypeString, TypeTextarea, TypeEmail:
		return DecodeOptions[StringOptions](raw)
	case TypeMedia:
		return DecodeOptions[MediaOptions](raw)
	case TypeCheckbox, TypeJSON:
		if !empty {
			return nil, fmt.Errorf("field type %q does not support options", code)
		}
		return nil, nil
	case TypeRepeater:
		return DecodeOptions[RepeaterOptions](raw)
	case TypeInteger:
		return DecodeOptions[IntegerOptions](raw)
	case TypeFloat:
		return DecodeOptions[FloatOptions](raw)
	case TypeRadio:
		return DecodeOptions[RadioOptions](raw)
	case TypeSelect:
		return DecodeOptions[SelectOptions](raw)
	case TypePhone:
		return DecodeOptions[PhoneOptions](raw)
	case TypeFile:
		return DecodeOptions[FileOptions](raw)
	default:
		if empty {
			return nil, nil
		}
		return DecodeOptions[map[string]any](raw)
	}
}

func EncodeOptionsJSON(value any) (json.RawMessage, error) {
	raw, err := json.Marshal(value)
	if bytes.Equal(raw, []byte("null")) {
		raw = nil
	}
	return raw, err
}
