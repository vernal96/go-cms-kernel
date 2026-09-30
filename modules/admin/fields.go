package admin

type FieldValidationError struct {
	Key    string         `json:"key"`
	Code   string         `json:"code"`
	Params map[string]any `json:"params,omitempty"`
}

type ValidationError struct {
	Message string
	Fields  []FieldValidationError
}

func (e ValidationError) Error() string {
	return e.Message
}

func (e ValidationError) Unwrap() error {
	return ErrValidation
}
