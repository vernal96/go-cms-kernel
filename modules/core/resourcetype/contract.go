package resourcetype

import (
	"errors"
	"fmt"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
)

const (
	ContentEditorHTML     field.EditorCode = "html"
	ContentEditorTextarea field.EditorCode = "textarea"
)

type compiledType struct {
	base           Type
	metadata       Metadata
	settingsSchema *field.Schema
	contentTypes   map[string]struct{}
}

// Compile freezes ResourceType metadata and compiles its TypeSettings
// contract. Defaults are deliberately partial: required settings are enforced
// when an actual resource payload is normalized, not at registration time.
func Compile(resourceType Type, resolver field.TypeResolver) (Type, error) {
	if resourceType == nil {
		return nil, errors.New("resource type is nil")
	}

	metadata := CloneMetadata(resourceType.Metadata())
	settingsSchema, err := field.Compile(metadata.SettingsFields, resolver)
	if err != nil {
		return nil, fmt.Errorf("settings metadata: %w", err)
	}
	for _, definition := range metadata.SettingsFields {
		storage, exists := settingsSchema.Storage(definition.Key)
		if !exists {
			return nil, fmt.Errorf(
				"settings field %q type %q has no JSON configuration storage semantics",
				definition.Key,
				definition.Type,
			)
		}
		if storage.Kind == field.StorageReference {
			return nil, fmt.Errorf(
				"settings field %q type %q requires unsupported reference lifecycle",
				definition.Key,
				definition.Type,
			)
		}
	}

	metadata.SettingsDefaults, err = settingsSchema.ValidatePartial(
		metadata.SettingsDefaults,
	)
	if err != nil {
		return nil, fmt.Errorf("settings defaults: %w", err)
	}
	contentTypes := make(map[string]struct{}, len(metadata.ContentTypes))
	for _, option := range metadata.ContentTypes {
		contentTypes[option.Code] = struct{}{}
	}

	return &compiledType{
		base:           resourceType,
		metadata:       metadata,
		settingsSchema: settingsSchema,
		contentTypes:   contentTypes,
	}, nil
}

func (t *compiledType) Code() Code         { return t.base.Code() }
func (t *compiledType) PathMode() PathMode { return t.base.PathMode() }

func (t *compiledType) Metadata() Metadata {
	return CloneMetadata(t.metadata)
}

func (t *compiledType) Normalize(payload Payload) (Payload, error) {
	payload = clonePayload(payload)
	settings := cloneMap(t.metadata.SettingsDefaults)
	for key, value := range payload.TypeSettings {
		settings[key] = cloneValue(value)
	}

	normalized, err := t.settingsSchema.Validate(settings)
	if err != nil {
		return Payload{}, fmt.Errorf("type settings: %w", err)
	}
	payload.TypeSettings = normalized
	if err := t.validateContentType(payload.ContentType); err != nil {
		return Payload{}, err
	}

	payload, err = t.base.Normalize(payload)
	if err != nil {
		return Payload{}, err
	}
	payload.TypeSettings, err = t.settingsSchema.Validate(payload.TypeSettings)
	if err != nil {
		return Payload{}, fmt.Errorf("type settings: %w", err)
	}
	if err := t.validateContentType(payload.ContentType); err != nil {
		return Payload{}, err
	}
	return clonePayload(payload), nil
}

func (t *compiledType) validateContentType(contentType *string) error {
	if !t.metadata.Capabilities.SupportsContent || contentType == nil || *contentType == "" {
		return nil
	}
	if _, exists := t.contentTypes[*contentType]; !exists {
		return fmt.Errorf("unsupported content_type %q", *contentType)
	}
	return nil
}

func CloneMetadata(source Metadata) Metadata {
	result := source
	result.SettingsFields = field.CloneDefinitions(source.SettingsFields)
	result.SettingsDefaults = cloneMap(source.SettingsDefaults)
	result.ContentTypes = append([]ContentTypeOption(nil), source.ContentTypes...)
	return result
}
