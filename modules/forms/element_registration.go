package forms

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
)

type ElementRegistrar interface{ RegisterElementType(ElementType) error }

func (c *elementCatalog) Register(element ElementType) error {
	if element == nil || (reflect.ValueOf(element).Kind() == reflect.Pointer && reflect.ValueOf(element).IsNil()) {
		return errors.New("Forms element type is nil")
	}
	code := element.Code()
	if code == "" || strings.TrimSpace(string(code)) != string(code) {
		return errors.New("Forms element type code is invalid")
	}
	metadata := element.Metadata()
	if metadata.Code != code || strings.TrimSpace(metadata.Label) == "" {
		return fmt.Errorf("Forms element %q metadata is invalid", code)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sealed {
		return errors.New("Forms element registry is sealed")
	}
	if _, exists := c.types[code]; exists {
		return fmt.Errorf("Forms element %q is already registered", code)
	}
	if definition, ok := element.(ElementDefinition); ok {
		definition.Description = definition.Metadata()
		element = definition
	}
	c.types[code] = element
	return nil
}

func (c *elementCatalog) Seal() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sealed {
		return errors.New("Forms element registry is already sealed")
	}
	c.sealed = true
	return nil
}
