package permission

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

type Action string
type Code string

const (
	Read   Action = "read"
	Create Action = "create"
	Update Action = "update"
	Delete Action = "delete"
)

var (
	ErrUnknown = errors.New("unknown permission")

	codePartPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	actions         = []Action{Read, Create, Update, Delete}
)

type Entity struct {
	Code    string
	Actions []Action
}

type Definition struct {
	Module string
	Entity string
	Action Action
	Code   Code
}

type Catalog struct {
	definitions map[Code]Definition
	codes       []Code
}

func NewCatalog(definitions []Definition) (*Catalog, error) {
	catalog := &Catalog{
		definitions: make(map[Code]Definition, len(definitions)),
	}

	for index, definition := range definitions {
		normalized, err := normalizeDefinition(definition)
		if err != nil {
			return nil, fmt.Errorf(
				"permission definition at index %d: %w",
				index,
				err,
			)
		}
		if _, exists := catalog.definitions[normalized.Code]; exists {
			return nil, fmt.Errorf(
				"permission %q is registered more than once",
				normalized.Code,
			)
		}
		catalog.definitions[normalized.Code] = normalized
		catalog.codes = append(catalog.codes, normalized.Code)
	}

	sort.Slice(catalog.codes, func(left, right int) bool {
		return catalog.codes[left] < catalog.codes[right]
	})

	return catalog, nil
}

func Definitions(
	module string,
	entities []Entity,
) ([]Definition, error) {
	if !codePartPattern.MatchString(module) {
		return nil, fmt.Errorf("invalid permission module %q", module)
	}

	result := make([]Definition, 0, len(entities)*len(actions))
	used := make(map[string]struct{}, len(entities))
	for index, entity := range entities {
		if !codePartPattern.MatchString(entity.Code) {
			return nil, fmt.Errorf(
				"permission entity at index %d has invalid code %q",
				index,
				entity.Code,
			)
		}
		if _, exists := used[entity.Code]; exists {
			return nil, fmt.Errorf(
				"permission entity %q is registered more than once",
				entity.Code,
			)
		}
		used[entity.Code] = struct{}{}

		entityActions := entity.Actions
		if len(entityActions) == 0 {
			entityActions = actions
		}
		actionUsed := make(map[Action]struct{}, len(entityActions))
		for _, action := range entityActions {
			if !validAction(action) {
				return nil, fmt.Errorf("permission entity %q has invalid action %q", entity.Code, action)
			}
			if _, exists := actionUsed[action]; exists {
				return nil, fmt.Errorf("permission entity %q action %q is duplicated", entity.Code, action)
			}
			actionUsed[action] = struct{}{}
			code, err := NewCode(module, entity.Code, action)
			if err != nil {
				return nil, err
			}
			result = append(result, Definition{
				Module: module,
				Entity: entity.Code,
				Action: action,
				Code:   code,
			})
		}
	}

	return result, nil
}

func NewCode(
	module string,
	entity string,
	action Action,
) (Code, error) {
	if !codePartPattern.MatchString(module) {
		return "", fmt.Errorf("invalid permission module %q", module)
	}
	if !codePartPattern.MatchString(entity) {
		return "", fmt.Errorf("invalid permission entity %q", entity)
	}
	if !validAction(action) {
		return "", fmt.Errorf("invalid permission action %q", action)
	}

	return Code(module + "." + entity + "." + string(action)), nil
}

func MustCode(
	module string,
	entity string,
	action Action,
) Code {
	code, err := NewCode(module, entity, action)
	if err != nil {
		panic(err)
	}
	return code
}

func Parse(code Code) (Definition, error) {
	parts := strings.Split(string(code), ".")
	if len(parts) != 3 {
		return Definition{}, fmt.Errorf("invalid permission code %q", code)
	}

	action := Action(parts[2])
	normalized, err := NewCode(parts[0], parts[1], action)
	if err != nil {
		return Definition{}, err
	}
	if normalized != code {
		return Definition{}, fmt.Errorf("invalid permission code %q", code)
	}

	return Definition{
		Module: parts[0],
		Entity: parts[1],
		Action: action,
		Code:   code,
	}, nil
}

func (c *Catalog) Has(code Code) bool {
	if c == nil {
		return false
	}
	_, exists := c.definitions[code]
	return exists
}

func (c *Catalog) Definition(
	code Code,
) (Definition, bool) {
	if c == nil {
		return Definition{}, false
	}
	definition, exists := c.definitions[code]
	return definition, exists
}

func (c *Catalog) Codes() []Code {
	if c == nil {
		return nil
	}
	return append([]Code(nil), c.codes...)
}

func (c *Catalog) Require(code Code) error {
	if !c.Has(code) {
		return fmt.Errorf("%w: %s", ErrUnknown, code)
	}
	return nil
}

func normalizeDefinition(
	definition Definition,
) (Definition, error) {
	code, err := NewCode(
		definition.Module,
		definition.Entity,
		definition.Action,
	)
	if err != nil {
		return Definition{}, err
	}
	if definition.Code != "" && definition.Code != code {
		return Definition{}, fmt.Errorf(
			"permission code %q does not match %q",
			definition.Code,
			code,
		)
	}
	definition.Code = code
	return definition, nil
}

func validAction(action Action) bool {
	switch action {
	case Read, Create, Update, Delete:
		return true
	default:
		return false
	}
}
