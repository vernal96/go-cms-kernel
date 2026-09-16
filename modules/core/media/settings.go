package media

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/security"
)

type SettingsDefinition struct {
	Code   string
	Fields []field.Definition
}

func SettingsFields(definitions []SettingsDefinition) (map[string][]field.Definition, error) {
	result := make(map[string][]field.Definition, len(definitions))
	for _, def := range definitions {
		if def.Code == "" || strings.TrimSpace(def.Code) != def.Code {
			return nil, errors.New("invalid media settings code")
		}
		if _, exists := result[def.Code]; exists {
			return nil, fmt.Errorf("duplicate media settings %q", def.Code)
		}
		result[def.Code] = field.CloneDefinitions(def.Fields)
	}
	return result, nil
}

type settingsSchema struct {
	schema *field.Schema
	fields []field.Descriptor
}

type SettingsCatalog struct{ schemas map[string]settingsSchema }

func CompileSettings(definitions []SettingsDefinition, resolver field.TypeResolver) (*SettingsCatalog, error) {
	definitionsByCode, err := SettingsFields(definitions)
	if err != nil {
		return nil, err
	}
	catalog := &SettingsCatalog{schemas: make(map[string]settingsSchema)}
	for _, def := range definitions {
		schema, err := field.CompilePersistent(definitionsByCode[def.Code], resolver)
		if err != nil {
			return nil, fmt.Errorf("media settings %q: %w", def.Code, err)
		}
		// Nested Media ownership and arbitrary reference collectors need dedicated
		// lifecycle support. Plain File references use the existing File policy.
		if err := schema.ValidateReferenceTargets(field.ReferenceFile); err != nil {
			return nil, fmt.Errorf("media settings %q: %w", def.Code, err)
		}
		descriptors, err := field.DescribeDefinitions(schema.Definitions(), resolver)
		if err != nil {
			return nil, err
		}
		catalog.schemas[def.Code] = settingsSchema{schema, descriptors}
	}
	return catalog, nil
}

var ErrSettings = errors.New("invalid media settings")
var ErrSettingsConflict = errors.New("media changed; reload settings")

type SettingsState struct {
	Code              string             `json:"code"`
	Values            map[string]any     `json:"values"`
	Fields            []field.Descriptor `json:"fields"`
	ExpectedUpdatedAt time.Time          `json:"expected_updated_at"`
}

type SettingsRepository interface {
	UpdateSettings(context.Context, *security.UserID, ID, map[string]any, time.Time) (Media, error)
}

type SettingsService struct {
	catalog    *SettingsCatalog
	repository Repository
	writer     SettingsRepository
	files      file.Service
	authorizer security.Authorizer
}

func NewSettingsService(catalog *SettingsCatalog, repository Repository, files file.Service, authorizer security.Authorizer) (*SettingsService, error) {
	writer, ok := repository.(SettingsRepository)
	if catalog == nil || !ok || files == nil || authorizer == nil {
		return nil, errors.New("media settings dependencies are unavailable")
	}
	return &SettingsService{catalog, repository, writer, files, authorizer}, nil
}
func (s *SettingsService) Get(ctx context.Context, actor security.Actor, id ID, code string) (SettingsState, error) {
	if err := s.authorizer.Check(ctx, actor, readPermission); err != nil {
		return SettingsState{}, err
	}
	if _, exists := s.catalog.schemas[code]; !exists {
		return SettingsState{}, fmt.Errorf("%w: unknown code %q", ErrSettings, code)
	}
	item, err := s.repository.ByID(ctx, id)
	if err != nil {
		return SettingsState{}, err
	}
	return s.state(item, code)
}
func (s *SettingsService) state(item Media, code string) (SettingsState, error) {
	values := map[string]any{}
	if raw, exists := item.Params["settings"]; exists {
		var ok bool
		values, ok = raw.(map[string]any)
		if !ok {
			return SettingsState{}, fmt.Errorf("%w: stored settings must be an object", ErrSettings)
		}
	}
	schema := s.catalog.schemas[code]
	descriptors := append([]field.Descriptor{}, schema.fields...)
	for i := range descriptors {
		descriptors[i].Options = append([]byte(nil), descriptors[i].Options...)
		descriptors[i].Rules = append([]string{}, descriptors[i].Rules...)
		if descriptors[i].VisibleWhen != nil {
			v := *descriptors[i].VisibleWhen
			v.Value = cloneValue(v.Value)
			descriptors[i].VisibleWhen = &v
		}
		if descriptors[i].Public != nil {
			v := *descriptors[i].Public
			descriptors[i].Public = &v
		}
	}
	return SettingsState{Code: code, Values: cloneMap(values), Fields: descriptors, ExpectedUpdatedAt: item.UpdatedAt}, nil
}
func (s *SettingsService) Save(ctx context.Context, actor security.Actor, id ID, code string, values map[string]any, expected time.Time) (SettingsState, error) {
	if err := s.authorizer.Check(ctx, actor, updatePermission); err != nil {
		return SettingsState{}, err
	}
	if expected.IsZero() {
		return SettingsState{}, ErrSettingsConflict
	}
	schema, exists := s.catalog.schemas[code]
	if !exists {
		return SettingsState{}, fmt.Errorf("%w: unknown code %q", ErrSettings, code)
	}
	normalized, err := schema.schema.Validate(values)
	if err != nil {
		return SettingsState{}, err
	}
	refs, err := schema.schema.References(normalized)
	if err != nil {
		return SettingsState{}, err
	}
	for _, ref := range refs {
		if ref.Target != field.ReferenceFile {
			return SettingsState{}, fmt.Errorf("%w: unsupported reference", ErrSettings)
		}
		item, err := s.files.GetFile(ctx, actor, file.ID(ref.ID))
		if err != nil {
			return SettingsState{}, err
		}
		if !field.FileMatches(ref.Options, item.Storage, item.MIMEType) {
			return SettingsState{}, field.ValidationErrors{{Key: ref.Key, Rule: "file"}}
		}
	}
	item, err := s.writer.UpdateSettings(ctx, actor.AuditUserID(), id, normalized, expected)
	if err != nil {
		return SettingsState{}, err
	}
	return s.state(item, code)
}
