package forms

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
)

type PublicFormSchema struct {
	Code        string             `json:"code"`
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Fields      []publicField      `json:"fields"`
	Elements    []publicElement    `json:"elements"`
	Layout      []publicLayoutNode `json:"layout"`
}

func (s *Service) publicSchema(ctx context.Context, detail FormDetail) (PublicFormSchema, error) {
	var err error
	fields := make([]publicField, len(detail.Fields))
	for index, item := range detail.Fields {
		options, optionErr := field.EncodeOptionsJSON(item.Options)
		if optionErr != nil {
			return PublicFormSchema{}, optionErr
		}
		fields[index] = publicField{Code: item.Code, Type: item.Type, Label: item.Label, Required: item.Required, Rules: append([]string(nil), item.Rules...), Options: options, Editor: item.Editor, VisibleWhen: cloneVisibleWhen(item.VisibleWhen)}
		if item.Type == FieldTypeCaptcha {
			fields[index].Captcha, err = s.CaptchaPublicConfig(ctx, item)
			if err != nil {
				return PublicFormSchema{}, err
			}
		}
	}
	elements := make([]publicElement, len(detail.Elements))
	elementCodes := make(map[ElementID]string, len(detail.Elements))
	for index, item := range detail.Elements {
		elementCodes[item.ID] = item.Code
		var config any
		if item.Type == ElementImage {
			var raw map[string]any
			if json.Unmarshal(item.Config, &raw) != nil {
				return PublicFormSchema{}, ErrInvalid
			}
			url, urlErr := s.PublicImageURL(ctx, item.Config)
			if urlErr != nil {
				return PublicFormSchema{}, ErrNotFound
			}
			delete(raw, "file_id")
			raw["url"] = url
			config = raw
		} else if json.Unmarshal(item.Config, &config) != nil {
			return PublicFormSchema{}, ErrInvalid
		}
		elements[index] = publicElement{Code: item.Code, Type: item.Type, Config: config}
	}
	fieldCodes := make(map[FieldID]string, len(detail.Fields))
	for _, item := range detail.Fields {
		fieldCodes[item.ID] = item.Code
	}
	keys := make(map[LayoutNodeID]string, len(detail.Layout))
	for index, item := range detail.Layout {
		keys[item.ID] = fmt.Sprintf("n%d", index+1)
	}
	layout := make([]publicLayoutNode, len(detail.Layout))
	for index, item := range detail.Layout {
		node := publicLayoutNode{Key: keys[item.ID], Kind: item.Kind, ContainerType: item.ContainerType, Position: item.Position, Config: item.Config}
		if item.ParentID != nil {
			node.Parent = keys[*item.ParentID]
		}
		if item.FieldID != nil {
			node.FieldCode = fieldCodes[*item.FieldID]
		}
		if item.ElementID != nil {
			node.ElementCode = elementCodes[*item.ElementID]
		}
		layout[index] = node
	}
	return PublicFormSchema{Code: detail.Form.Code, Name: detail.Form.Name, Description: detail.Form.Description, Fields: fields, Elements: elements, Layout: layout}, nil
}

type publicField struct {
	Code        string             `json:"code"`
	Type        field.TypeCode     `json:"type"`
	Label       string             `json:"label"`
	Required    bool               `json:"required"`
	Rules       []string           `json:"rules"`
	Options     json.RawMessage    `json:"options,omitempty"`
	Editor      field.EditorCode   `json:"editor,omitempty"`
	VisibleWhen *field.VisibleWhen `json:"visible_when,omitempty"`
	Captcha     map[string]any     `json:"captcha,omitempty"`
}
type publicElement struct {
	Code   string          `json:"code"`
	Type   ElementTypeCode `json:"type"`
	Config any             `json:"config"`
}
type publicLayoutNode struct {
	Key           string          `json:"key"`
	Parent        string          `json:"parent,omitempty"`
	Kind          LayoutKind      `json:"kind"`
	FieldCode     string          `json:"field_code,omitempty"`
	ElementCode   string          `json:"element_code,omitempty"`
	ContainerType ContainerType   `json:"container_type,omitempty"`
	Position      int             `json:"position"`
	Config        json.RawMessage `json:"config,omitempty"`
}
