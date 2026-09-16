package field

import "fmt"

type mediaType struct{ settings map[string][]Definition }

// MediaType binds named settings to an immutable field compiler.
func MediaType(settings map[string][]Definition) Type {
	copy := make(map[string][]Definition, len(settings))
	for code, fields := range settings {
		copy[code] = CloneDefinitions(fields)
	}
	return DescribedType{Type: mediaType{settings: copy}, Presentation: Metadata{Label: "Медиа (изображение)", Editor: "media"}}
}

func (mediaType) Code() TypeCode { return TypeMedia }
func (t mediaType) Compile(ctx CompileContext, options any) (ValueType, error) {
	config, err := DecodeOptions[MediaOptions](options)
	if err != nil {
		return nil, err
	}
	var descriptors []Descriptor
	if config.SettingsCode != "" {
		definitions, ok := t.settings[config.SettingsCode]
		if !ok {
			return nil, fmt.Errorf("unknown media settings %q", config.SettingsCode)
		}
		nested, err := ctx.EnterComposite(TypeMedia)
		if err != nil {
			return nil, err
		}
		schema, err := nested.Compile(definitions)
		if err != nil {
			return nil, err
		}
		if err = schema.ValidateReferenceTargets(ReferenceFile); err != nil {
			return nil, err
		}
		descriptors, err = DescribeDefinitions(definitions, ctx.Types)
		if err != nil {
			return nil, err
		}
	}
	return withList(mediaValue{options: config, fields: descriptors}, config.Multiple, config.MinItems, config.MaxItems, false)
}

type mediaValue struct {
	options MediaOptions
	fields  []Descriptor
}

func (v mediaValue) DescribeOptions() (any, error) {
	return struct {
		MediaOptions
		SettingsFields []Descriptor `json:"settings_fields,omitempty"`
	}{v.options, v.fields}, nil
}

func (mediaValue) StorageKind() StorageKind { return StorageReference }
func (mediaValue) Multiple() bool           { return false }
func (mediaValue) ReferenceTarget() string  { return ReferenceMedia }
func (mediaValue) Normalize(value any) (any, error) {
	id, ok := normalizeInteger(value)
	if !ok || id <= 0 {
		return nil, fmt.Errorf("expected positive media id, got %T", value)
	}
	return id, nil
}
func (mediaValue) Empty(any) bool     { return false }
func (mediaValue) Validate(any) error { return nil }
func (mediaValue) Rules() []string    { return nil }
func (mediaValue) Example() any       { return int64(1) }
