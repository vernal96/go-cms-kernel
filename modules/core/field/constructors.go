package field

// String declares a string field. Its optional settings configure list values.
func String(key, label string, options ...StringOptions) Definition {
	return definition(key, label, TypeString, firstOption(options))
}

// Integer declares an integer field. Its optional settings configure list values and step.
func Integer(key, label string, options ...IntegerOptions) Definition {
	return definition(key, label, TypeInteger, firstOption(options))
}

// Float declares a floating-point field. Its optional settings configure list values and step.
func Float(key, label string, options ...FloatOptions) Definition {
	return definition(key, label, TypeFloat, firstOption(options))
}

// Checkbox declares a boolean field.
func Checkbox(key, label string) Definition {
	return definition(key, label, TypeCheckbox, nil)
}

// Radio declares a single-choice field.
func Radio(key, label string, options RadioOptions) Definition {
	return definition(key, label, TypeRadio, options)
}

// Select declares a choice-list field.
func Select(key, label string, options SelectOptions) Definition {
	return definition(key, label, TypeSelect, options)
}

// Textarea declares a multiline string field.
func Textarea(key, label string, options ...StringOptions) Definition {
	return definition(key, label, TypeTextarea, firstOption(options))
}

// Email declares an email field.
func Email(key, label string, options ...StringOptions) Definition {
	return definition(key, label, TypeEmail, firstOption(options))
}

// Phone declares a phone field. Its optional settings configure list values.
func Phone(key, label string, options ...PhoneOptions) Definition {
	return definition(key, label, TypePhone, firstOption(options))
}

// Media declares a media reference field.
func Media(key, label string, options MediaOptions) Definition {
	return definition(key, label, TypeMedia, options)
}

// JSON declares a structured JSON field.
func JSON(key, label string) Definition {
	return definition(key, label, TypeJSON, nil)
}

// Repeater declares a field containing rows described by nested field definitions.
func Repeater(key, label string, options RepeaterOptions) Definition {
	return definition(key, label, TypeRepeater, options)
}

// WithRequired returns a copy with the required flag set.
func (d Definition) WithRequired(required bool) Definition {
	d = cloneDefinition(d)
	d.Required = required
	return d
}

// WithValidators returns a copy with the provided validators.
func (d Definition) WithValidators(validators ...ValidatorDefinition) Definition {
	d = cloneDefinition(d)
	d.Validators = CloneValidatorDefinitions(validators)
	return d
}

// WithEditor returns a copy with the specified editor metadata.
func (d Definition) WithEditor(editor EditorCode) Definition {
	d = cloneDefinition(d)
	d.Editor = editor
	return d
}

// WithVisibleWhen returns a copy with a detached visibility condition.
func (d Definition) WithVisibleWhen(condition *VisibleWhen) Definition {
	d = cloneDefinition(d)
	if condition == nil {
		d.VisibleWhen = nil
		return d
	}
	cloned := *condition
	cloned.Value = cloneEditorValue(condition.Value)
	d.VisibleWhen = &cloned
	return d
}

// WithPublic returns a copy with public projection enabled or disabled.
func (d Definition) WithPublic(public bool) Definition {
	d = cloneDefinition(d)
	d.Public = public
	return d
}

func cloneDefinition(definition Definition) Definition {
	return CloneDefinitions([]Definition{definition})[0]
}

func definition(key, label string, code TypeCode, options any) Definition {
	return Definition{
		Key:     key,
		Label:   label,
		Type:    code,
		Options: cloneOptions(options),
	}
}

func firstOption[T any](options []T) any {
	if len(options) > 1 {
		panic("field constructor accepts at most one options value")
	}
	if len(options) == 0 {
		return nil
	}
	return options[0]
}
