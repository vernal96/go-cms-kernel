package postgres

import (
	"encoding/json"
	"fmt"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/mail"
)

type variableJSON struct {
	Key      string          `json:"key"`
	Type     field.TypeCode  `json:"type"`
	Label    string          `json:"label"`
	Required bool            `json:"required"`
	Rules    []string        `json:"rules"`
	Options  json.RawMessage `json:"options,omitempty"`
}

func encodeVariables(definitions []field.Definition) ([]byte, error) {
	items := make([]variableJSON, len(definitions))
	for index, definition := range definitions {
		item := variableJSON{Key: definition.Key, Type: definition.Type, Label: definition.Label, Required: definition.Required != nil && *definition.Required, Rules: append([]string(nil), definition.Rules...)}
		options, err := field.EncodeOptionsJSON(definition.Options)
		if err != nil {
			return nil, err
		}
		item.Options = options
		items[index] = item
	}
	return json.Marshal(items)
}

func decodeVariables(raw []byte) ([]field.Definition, error) {
	var items []variableJSON
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	result := make([]field.Definition, len(items))
	for index, item := range items {
		required := item.Required
		options, err := field.DecodeOptionsJSON(item.Type, item.Options)
		if err != nil {
			return nil, fmt.Errorf("decode variable %q options: %w", item.Key, err)
		}
		result[index] = field.Definition{Key: item.Key, Type: item.Type, Label: item.Label, Required: &required, Rules: append([]string(nil), item.Rules...), Options: options}
	}
	return result, nil
}

func encodeJSON(value any) ([]byte, error) { return json.Marshal(value) }

func decodeJSON(raw []byte, target any) error { return json.Unmarshal(raw, target) }

func encodeMessageAttachments(items []mail.Attachment) ([]byte, error) {
	return mail.EncodeAttachmentsForStorage(items)
}

func decodeMessageAttachments(raw []byte) ([]mail.Attachment, error) {
	return mail.DecodeAttachmentsFromStorage(raw)
}
