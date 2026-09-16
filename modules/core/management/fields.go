package management

type FieldValidationError struct {
	Key   string `json:"key"`
	Rule  string `json:"rule"`
	Param string `json:"param"`
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
