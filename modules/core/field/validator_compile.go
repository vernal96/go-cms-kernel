package field

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
)

func compileValidators(def Definition, valueType ValueType, resolver ValidatorResolver) ([]compiledValidator, error) {
	if len(def.Validators) == 0 {
		return nil, nil
	}
	if resolver == nil {
		return nil, errors.New("validator resolver is nil")
	}
	result := make([]compiledValidator, 0, len(def.Validators))
	seen := make(map[ValidatorCode]bool, len(def.Validators))
	ctx := ValidatorContext{FieldType: def.Type, ValueType: valueType}
	if storage, ok := valueType.(StorageValueType); ok {
		ctx.Storage = storage.StorageKind()
		ctx.Multiple = storage.Multiple()
	}
	for index, definition := range def.Validators {
		if err := ValidateValidatorCode(definition.Type); err != nil {
			return nil, fmt.Errorf("at index %d: %w", index, err)
		}
		if definition.Type == "required" {
			return nil, errors.New("required belongs to the field schema")
		}
		if seen[definition.Type] {
			return nil, fmt.Errorf("duplicate validator %q", definition.Type)
		}
		seen[definition.Type] = true
		typ, exists := resolver.ValidatorType(definition.Type)
		if !exists || nilInterface(typ) {
			return nil, fmt.Errorf("unknown validator %q", definition.Type)
		}
		compiled, err := typ.Compile(ctx, definition.Options)
		if err != nil {
			return nil, fmt.Errorf("validator %q: %w", definition.Type, err)
		}
		if nilInterface(compiled) {
			return nil, fmt.Errorf("validator %q compiled to nil", definition.Type)
		}
		scope := compiled.Scope()
		switch scope {
		case ValidatorScopeValue:
		case ValidatorScopeItems:
			if !ctx.Multiple {
				return nil, fmt.Errorf("validator %q item scope requires a multiple field", definition.Type)
			}
		default:
			return nil, fmt.Errorf("validator %q has invalid scope %q", definition.Type, scope)
		}
		result = append(result, compiledValidator{code: definition.Type, value: compiled, items: scope == ValidatorScopeItems})
	}
	for _, pair := range [][2]ValidatorCode{{"min", "max"}, {"min_length", "max_length"}, {"min_items", "max_items"}, {"min_digits", "max_digits"}} {
		var min, max *float64
		for _, item := range result {
			builtin, ok := item.value.(builtinValidator)
			if !ok {
				continue
			}
			value, ok := builtin.params["value"].(float64)
			if !ok {
				continue
			}
			if item.code == pair[0] {
				min = &value
			}
			if item.code == pair[1] {
				max = &value
			}
		}
		if min != nil && max != nil && *min > *max {
			return nil, fmt.Errorf("validator %q exceeds %q", pair[0], pair[1])
		}
	}
	return result, nil
}

func validateCompiledValidators(key string, value any, validators []compiledValidator) ValidationErrors {
	result := ValidationErrors{}
	for _, item := range validators {
		if item.items {
			entries := reflect.ValueOf(value)
			for i := 0; i < entries.Len(); i++ {
				result = append(result, validateOneValidator(fmt.Sprintf("%s[%d]", key, i), entries.Index(i).Interface(), item)...)
			}
		} else {
			result = append(result, validateOneValidator(key, value, item)...)
		}
	}
	return result
}
func hasMinItems(validators []compiledValidator) bool {
	for _, item := range validators {
		if item.code == "min_items" || item.code == "items_between" || item.code == "items_count" {
			return true
		}
	}
	return false
}
func validateOneValidator(key string, value any, item compiledValidator) ValidationErrors {
	err := item.value.Validate(value)
	if err == nil {
		return nil
	}
	var failures ValidationErrors
	if errors.As(err, &failures) {
		out := make(ValidationErrors, len(failures))
		for i, failure := range failures {
			failure.Key = key
			if failure.Code == "" {
				failure.Code = item.code
			}
			out[i] = failure
		}
		return out
	}
	return ValidationErrors{{Key: key, Code: item.code}}
}
func ruleParams(rule RuleError) map[string]any {
	if rule.Param == "" {
		return nil
	}
	key := "value"
	switch rule.Rule {
	case "min_items", "max_items":
		key = "value"
	case "pattern":
		key = "pattern"
	}
	if number, err := strconv.ParseFloat(rule.Param, 64); err == nil {
		return map[string]any{key: number}
	}
	return map[string]any{key: rule.Param}
}
