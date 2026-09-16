package forms

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/security"
)

type ElementTypeMetadata struct {
	EditorCode string              `json:"editor_code,omitempty"`
	Code       ElementTypeCode     `json:"code"`
	Label      string              `json:"label"`
	Fields     []field.ConfigField `json:"fields"`
}

type ElementType interface {
	Code() ElementTypeCode
	Metadata() ElementTypeMetadata
	ValidateConfig(json.RawMessage) error
}

type elementCatalog struct {
	mu     sync.RWMutex
	sealed bool
	types  map[ElementTypeCode]ElementType
}

func newElementCatalog() (*elementCatalog, error) {
	items := []ElementType{
		ElementDefinition{Description: ElementTypeMetadata{Code: ElementText, Label: "Текст", Fields: []field.ConfigField{{Key: "content", Label: "Текст", Type: field.TypeTextarea, Required: true}}}},
		ElementDefinition{Description: ElementTypeMetadata{Code: ElementHeading, Label: "Заголовок", Fields: []field.ConfigField{{Key: "text", Label: "Заголовок", Type: field.TypeString, Required: true}, {Key: "level", Label: "Уровень", Type: field.TypeInteger, Required: true, Default: 2, Rules: []string{"min=1", "max=6"}}}}},
		ElementDefinition{Description: ElementTypeMetadata{Code: ElementImage, Label: "Изображение", Fields: []field.ConfigField{{Key: "file_id", Label: "Публичное изображение", Type: field.TypeFile, Required: true, Options: map[string]any{"storages": []string{"public"}}}, {Key: "alt", Label: "Alt", Type: field.TypeString}}}},
		ElementDefinition{Description: ElementTypeMetadata{Code: ElementSubmitButton, Label: "Кнопка отправки", Fields: []field.ConfigField{{Key: "label", Label: "Текст кнопки", Type: field.TypeString, Required: true, Default: "Отправить"}}}},
	}

	result := &elementCatalog{types: make(map[ElementTypeCode]ElementType, len(items))}
	for _, item := range items {
		if err := result.Register(item); err != nil {
			return nil, err
		}
	}

	return result, nil
}

func (c *elementCatalog) Type(code ElementTypeCode) (ElementType, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	item, exists := c.types[code]
	return item, exists
}

func (c *elementCatalog) Metadata() []ElementTypeMetadata {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make([]ElementTypeMetadata, 0, len(c.types))
	for _, item := range c.types {
		metadata := item.Metadata()
		metadata.Fields = field.CloneConfigFields(metadata.Fields)
		result = append(result, metadata)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Code < result[j].Code })
	return result
}

type ActionTypeMetadata struct {
	Code       string              `json:"code"`
	Label      string              `json:"label"`
	Available  bool                `json:"available"`
	EditorCode string              `json:"editor_code,omitempty"`
	Fields     []field.ConfigField `json:"fields,omitempty"`
}

type ActionValidationContext struct {
	Actor   security.Actor
	Form    Form
	Fields  []FormField
	Trigger Trigger
}

type ActionExecutionContext struct {
	Execution ActionExecution
	Result    Result
	Values    []ResultValue
	Uploads   UploadAccessor
}

type ActionExecutionResult struct {
	ExternalReference string
}

type ActionType interface {
	Code() string
	Metadata() ActionTypeMetadata
	ValidateConfig(context.Context, ActionValidationContext, json.RawMessage) error
	Execute(context.Context, ActionExecutionContext, json.RawMessage) (ActionExecutionResult, error)
}

type ActionRegistrar interface {
	RegisterActionType(ActionType) error
}

type actionRegistry struct {
	mu     sync.RWMutex
	types  map[string]ActionType
	sealed bool
}

func newActionRegistry() *actionRegistry {
	return &actionRegistry{types: make(map[string]ActionType)}
}

func (r *actionRegistry) Register(actionType ActionType) error {
	if actionType == nil {
		return errors.New("Forms action type is nil")
	}
	code := strings.TrimSpace(actionType.Code())
	if code == "" || code != actionType.Code() {
		return errors.New("Forms action type code is invalid")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sealed {
		return errors.New("Forms action registry is sealed")
	}
	if _, exists := r.types[code]; exists {
		return fmt.Errorf("Forms action type %q is already registered", code)
	}
	metadata := actionType.Metadata()
	if metadata.Code != code || strings.TrimSpace(metadata.Label) == "" {
		return fmt.Errorf("Forms action type %q metadata is invalid", code)
	}
	r.types[code] = actionType
	return nil
}

func (r *actionRegistry) Seal() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sealed {
		return errors.New("Forms action registry is already sealed")
	}
	r.sealed = true
	return nil
}

func (r *actionRegistry) Type(code string) (ActionType, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	item, exists := r.types[code]
	return item, exists
}

func (r *actionRegistry) Metadata() []ActionTypeMetadata {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]ActionTypeMetadata, 0, len(r.types))
	for _, item := range r.types {
		metadata := item.Metadata()
		metadata.Available = true
		metadata.Fields = field.CloneConfigFields(metadata.Fields)
		result = append(result, metadata)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Code < result[j].Code })
	return result
}

type UploadAccessor interface {
	Metadata(fieldCode string) []ResultUpload
	Open(context.Context, string, int) (io.ReadCloser, error)
}

type ActionError struct {
	Code      string
	Retryable bool
	Err       error
}

func (e *ActionError) Error() string {
	if e == nil || e.Err == nil {
		return "Forms action failed"
	}
	return e.Err.Error()
}

func (e *ActionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}
