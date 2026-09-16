// Package templating provides strict, allowlisted scalar interpolation.
//
// It deliberately is not a programming language: templates contain literals and
// declared variables only. Feature packages own their variable catalogs and the
// semantic conversion of domain values into supported scalar values.
package templating

import (
	"errors"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var variablePattern = regexp.MustCompile(
	`^[a-z][a-z0-9_]*(?:\.[a-z][a-z0-9_]*)+$`,
)

type Context uint8

const (
	PlainText Context = iota
	HTML
	Header
)

type Limits struct {
	MaxSourceLength int
	MaxResultLength int
}

type part struct {
	literal  string
	variable string
}

type Compiled struct {
	parts  []part
	limits Limits
}

type CompileError struct {
	Variable string
	Message  string
}

func (e CompileError) Error() string {
	if e.Variable == "" {
		return e.Message
	}
	return fmt.Sprintf("variable %q: %s", e.Variable, e.Message)
}

type RenderError struct {
	Variable string
	Message  string
}

func (e RenderError) Error() string {
	if e.Variable == "" {
		return e.Message
	}
	return fmt.Sprintf("variable %q: %s", e.Variable, e.Message)
}

type Warning struct {
	Variable string `json:"variable"`
	Message  string `json:"message"`
}

type Result struct {
	Value    string    `json:"value"`
	Warnings []Warning `json:"warnings"`
}

// Resolver returns a scalar value for a declared variable. A false exists value
// means the variable currently has no value and renders as an empty string with
// a warning.
type Resolver func(variable string) (value any, exists bool, err error)

func Compile(source string, allowedVariables map[string]struct{}, limits Limits) (Compiled, error) {
	if limits.MaxSourceLength <= 0 || limits.MaxResultLength <= 0 {
		return Compiled{}, CompileError{Message: "template limits must be positive"}
	}
	if utf8.RuneCountInString(source) > limits.MaxSourceLength {
		return Compiled{}, CompileError{Message: fmt.Sprintf(
			"template exceeds %d characters",
			limits.MaxSourceLength,
		)}
	}

	parts := make([]part, 0, 4)
	rest := source
	for rest != "" {
		opening := strings.Index(rest, "{{")
		closing := strings.Index(rest, "}}")
		if closing >= 0 && (opening < 0 || closing < opening) {
			return Compiled{}, CompileError{Message: "unexpected closing delimiter"}
		}
		if opening < 0 {
			parts = append(parts, part{literal: rest})
			break
		}
		if opening > 0 {
			parts = append(parts, part{literal: rest[:opening]})
		}
		rest = rest[opening+2:]
		closing = strings.Index(rest, "}}")
		if closing < 0 {
			return Compiled{}, CompileError{Message: "unclosed variable"}
		}
		variable := strings.TrimSpace(rest[:closing])
		if !variablePattern.MatchString(variable) {
			return Compiled{}, CompileError{Variable: variable, Message: "invalid variable syntax"}
		}
		if _, exists := allowedVariables[variable]; !exists {
			return Compiled{}, CompileError{Variable: variable, Message: "unknown variable"}
		}
		parts = append(parts, part{variable: variable})
		rest = rest[closing+2:]
	}
	return Compiled{parts: parts, limits: limits}, nil
}

func Render(compiled Compiled, resolver Resolver, context Context) (Result, error) {
	if resolver == nil {
		return Result{}, errors.New("template resolver is nil")
	}
	if context != PlainText && context != HTML && context != Header {
		return Result{}, errors.New("template rendering context is invalid")
	}

	var result strings.Builder
	warnings := make([]Warning, 0)
	length := 0
	write := func(value string) error {
		length += utf8.RuneCountInString(value)
		if length > compiled.limits.MaxResultLength {
			return RenderError{Message: fmt.Sprintf(
				"result exceeds %d characters",
				compiled.limits.MaxResultLength,
			)}
		}
		result.WriteString(value)
		return nil
	}

	for _, current := range compiled.parts {
		if current.variable == "" {
			if err := write(current.literal); err != nil {
				return Result{}, err
			}
			continue
		}
		value, exists, err := resolver(current.variable)
		if err != nil {
			return Result{}, RenderError{Variable: current.variable, Message: err.Error()}
		}
		if !exists || value == nil {
			warnings = append(warnings, Warning{
				Variable: current.variable,
				Message:  "variable has no current value",
			})
			continue
		}
		scalar, err := scalarString(value)
		if err != nil {
			return Result{}, RenderError{Variable: current.variable, Message: err.Error()}
		}
		if context == HTML {
			scalar = html.EscapeString(scalar)
		}
		if err := write(scalar); err != nil {
			return Result{}, err
		}
	}

	value := result.String()
	if context == Header && !safeHeader(value) {
		return Result{}, RenderError{Message: "rendered header contains unsafe control characters"}
	}
	return Result{Value: value, Warnings: warnings}, nil
}

func scalarString(value any) (string, error) {
	switch current := value.(type) {
	case string:
		return current, nil
	case bool:
		return strconv.FormatBool(current), nil
	case int:
		return strconv.FormatInt(int64(current), 10), nil
	case int8:
		return strconv.FormatInt(int64(current), 10), nil
	case int16:
		return strconv.FormatInt(int64(current), 10), nil
	case int32:
		return strconv.FormatInt(int64(current), 10), nil
	case int64:
		return strconv.FormatInt(current, 10), nil
	case uint:
		return strconv.FormatUint(uint64(current), 10), nil
	case uint8:
		return strconv.FormatUint(uint64(current), 10), nil
	case uint16:
		return strconv.FormatUint(uint64(current), 10), nil
	case uint32:
		return strconv.FormatUint(uint64(current), 10), nil
	case uint64:
		return strconv.FormatUint(current, 10), nil
	case float32:
		return strconv.FormatFloat(float64(current), 'f', -1, 32), nil
	case float64:
		return strconv.FormatFloat(current, 'f', -1, 64), nil
	default:
		return "", fmt.Errorf("unsupported scalar type %T", value)
	}
}

func safeHeader(value string) bool {
	for _, current := range value {
		if current == '\t' {
			continue
		}
		if unicode.IsControl(current) {
			return false
		}
	}
	return true
}
