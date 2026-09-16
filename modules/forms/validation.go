package forms

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
)

const (
	MandatoryConsentCode = "privacy_consent"
	MandatoryCaptchaCode = "captcha"
	MandatorySubmitCode  = "submit"
	DefaultStatusCode    = "new"
)

var semanticCodePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:_[a-z0-9]+)*$`)

func validateCode(code, kind string) error {
	if !semanticCodePattern.MatchString(code) {
		return fmt.Errorf("%w: %s code %q is invalid", ErrInvalid, kind, code)
	}
	return nil
}

func validateForm(item Form) error {
	if item.SiteID <= 0 {
		return fmt.Errorf("%w: form site is invalid", ErrInvalid)
	}
	if err := validateCode(item.Code, "form"); err != nil {
		return err
	}
	if strings.TrimSpace(item.Name) == "" || item.Name != strings.TrimSpace(item.Name) {
		return fmt.Errorf("%w: form name is invalid", ErrInvalid)
	}
	if len(item.Name) > 255 || len(item.Description) > 10000 {
		return fmt.Errorf("%w: form text is too long", ErrInvalid)
	}
	return nil
}

func validateFormField(item FormField, resolver field.TypeResolver) error {
	if item.ShowOnSite && (item.Type == FieldTypeCaptcha || item.Type == FieldTypeUpload) {
		return fmt.Errorf("%w: transient fields cannot be published", ErrInvalid)
	}
	if item.FormID <= 0 {
		return fmt.Errorf("%w: field form is invalid", ErrInvalid)
	}
	if err := validateCode(item.Code, "field"); err != nil {
		return err
	}
	if item.ResultPosition < 0 {
		return fmt.Errorf("%w: result position is invalid", ErrInvalid)
	}
	if strings.TrimSpace(item.ResultLabel) != item.ResultLabel || len(item.ResultLabel) > 255 {
		return fmt.Errorf("%w: result label is invalid", ErrInvalid)
	}
	definition := item.Definition()
	var err error
	if item.Type == FieldTypeCaptcha || item.Type == FieldTypeUpload {
		_, err = field.Compile([]field.Definition{definition}, resolver)
	} else {
		_, err = field.CompilePersistent([]field.Definition{definition}, resolver)
	}
	if err != nil {
		return fmt.Errorf("%w: field %q: %v", ErrInvalid, item.Code, err)
	}
	return nil
}

func validateFieldConditions(items []FormField, resolver field.TypeResolver) error {
	byCode := make(map[string]FormField, len(items))
	for _, item := range items {
		byCode[item.Code] = item
	}
	for _, item := range items {
		if item.VisibleWhen == nil {
			continue
		}
		controller, exists := byCode[item.VisibleWhen.Field]
		if !exists || controller.Code == item.Code || controller.Type == FieldTypeCaptcha || controller.Type == FieldTypeUpload {
			return fmt.Errorf("%w: field %q has an invalid visibility controller", ErrInvalid, item.Code)
		}
		if _, err := normalizeConditionValue(controller, item.VisibleWhen.Value, resolver); err != nil {
			return fmt.Errorf("%w: field %q visibility value is invalid", ErrInvalid, item.Code)
		}
	}
	state := make(map[string]uint8, len(items))
	var visit func(string) error
	visit = func(code string) error {
		if state[code] == 1 {
			return fmt.Errorf("%w: field visibility contains a cycle", ErrInvalid)
		}
		if state[code] == 2 {
			return nil
		}
		state[code] = 1
		item := byCode[code]
		if item.VisibleWhen != nil {
			if err := visit(item.VisibleWhen.Field); err != nil {
				return err
			}
		}
		state[code] = 2
		return nil
	}
	for code := range byCode {
		if err := visit(code); err != nil {
			return err
		}
	}
	return nil
}

func resolveActiveFields(items []FormField, values map[string]any, resolver field.TypeResolver) ([]FormField, error) {
	if err := validateFieldConditions(items, resolver); err != nil {
		return nil, err
	}
	byCode := make(map[string]FormField, len(items))
	for _, item := range items {
		byCode[item.Code] = item
	}
	active := make(map[string]bool, len(items))
	resolved := make(map[string]bool, len(items))
	var evaluate func(string) (bool, error)
	evaluate = func(code string) (bool, error) {
		if resolved[code] {
			return active[code], nil
		}
		item := byCode[code]
		if item.VisibleWhen == nil {
			resolved[code], active[code] = true, true
			return true, nil
		}
		controller := byCode[item.VisibleWhen.Field]
		controllerActive, err := evaluate(controller.Code)
		if err != nil || !controllerActive {
			resolved[code], active[code] = true, false
			return false, err
		}
		actual, exists := values[controller.Code]
		if !exists {
			resolved[code], active[code] = true, false
			return false, nil
		}
		normalizedActual, err := normalizeConditionValue(controller, actual, resolver)
		if err != nil {
			return false, FieldValidationErrors{controller.Code: {"type"}}
		}
		normalizedExpected, err := normalizeConditionValue(controller, item.VisibleWhen.Value, resolver)
		if err != nil {
			return false, err
		}
		resolved[code], active[code] = true, reflect.DeepEqual(normalizedActual, normalizedExpected)
		return active[code], nil
	}
	result := make([]FormField, 0, len(items))
	for _, item := range items {
		visible, err := evaluate(item.Code)
		if err != nil {
			return nil, err
		}
		if visible {
			result = append(result, item)
		}
	}
	return result, nil
}

func normalizeConditionValue(item FormField, value any, resolver field.TypeResolver) (any, error) {
	fieldType, exists := resolver.FieldType(item.Type)
	if !exists || fieldType == nil {
		return nil, ErrInvalid
	}
	valueType, err := fieldType.Compile(field.CompileContext{Types: resolver}, item.Options)
	if err != nil || valueType == nil {
		return nil, errors.Join(ErrInvalid, err)
	}
	normalized, err := valueType.Normalize(value)
	if err != nil {
		return nil, err
	}
	if err := valueType.Validate(normalized); err != nil {
		return nil, err
	}
	return normalized, nil
}

func validateElement(item Element, catalog *elementCatalog) error {
	if item.FormID <= 0 {
		return fmt.Errorf("%w: element form is invalid", ErrInvalid)
	}
	if err := validateCode(item.Code, "element"); err != nil {
		return err
	}
	elementType, exists := catalog.Type(item.Type)
	if !exists {
		return fmt.Errorf("%w: element type %q is unavailable", ErrInvalid, item.Type)
	}
	return elementType.ValidateConfig(item.Config)
}

func normalizeAndValidateLayout(detail FormDetail, desired []LayoutNode) ([]LayoutNode, error) {
	if len(desired) != len(detail.Layout) {
		return nil, fmt.Errorf("%w: complete layout must contain every existing node", ErrInvalid)
	}
	fields := make(map[FieldID]FormField, len(detail.Fields))
	for _, item := range detail.Fields {
		fields[item.ID] = item
	}
	elements := make(map[ElementID]Element, len(detail.Elements))
	for _, item := range detail.Elements {
		elements[item.ID] = item
	}
	existing := make(map[LayoutNodeID]struct{}, len(detail.Layout))
	for _, item := range detail.Layout {
		existing[item.ID] = struct{}{}
	}
	nodes := make(map[LayoutNodeID]LayoutNode, len(desired))
	placedFields := make(map[FieldID]struct{}, len(fields))
	placedElements := make(map[ElementID]struct{}, len(elements))
	for index, item := range desired {
		if item.ID <= 0 {
			return nil, fmt.Errorf("%w: layout node at index %d has invalid ID", ErrInvalid, index)
		}
		if _, exists := existing[item.ID]; !exists {
			return nil, fmt.Errorf("%w: layout node %d does not belong to form", ErrInvalid, item.ID)
		}
		if _, duplicate := nodes[item.ID]; duplicate {
			return nil, fmt.Errorf("%w: layout node %d is duplicated", ErrInvalid, item.ID)
		}
		item.FormID = detail.Form.ID
		if item.Position < 0 {
			return nil, fmt.Errorf("%w: layout position is negative", ErrInvalid)
		}
		switch item.Kind {
		case LayoutField:
			if item.FieldID == nil || item.ElementID != nil || item.ContainerType != "" {
				return nil, fmt.Errorf("%w: field layout reference is invalid", ErrInvalid)
			}
			if _, exists := fields[*item.FieldID]; !exists {
				return nil, fmt.Errorf("%w: layout references a foreign field", ErrInvalid)
			}
			if _, duplicate := placedFields[*item.FieldID]; duplicate {
				return nil, fmt.Errorf("%w: field is placed more than once", ErrInvalid)
			}
			placedFields[*item.FieldID] = struct{}{}
		case LayoutElement:
			if item.ElementID == nil || item.FieldID != nil || item.ContainerType != "" {
				return nil, fmt.Errorf("%w: element layout reference is invalid", ErrInvalid)
			}
			if _, exists := elements[*item.ElementID]; !exists {
				return nil, fmt.Errorf("%w: layout references a foreign element", ErrInvalid)
			}
			if _, duplicate := placedElements[*item.ElementID]; duplicate {
				return nil, fmt.Errorf("%w: element is placed more than once", ErrInvalid)
			}
			placedElements[*item.ElementID] = struct{}{}
		case LayoutContainer:
			if item.FieldID != nil || item.ElementID != nil || (item.ContainerType != ContainerGroup && item.ContainerType != ContainerSlide) {
				return nil, fmt.Errorf("%w: container layout reference is invalid", ErrInvalid)
			}
		default:
			return nil, fmt.Errorf("%w: layout kind %q is invalid", ErrInvalid, item.Kind)
		}
		if len(item.Config) > 0 && !json.Valid(item.Config) {
			return nil, fmt.Errorf("%w: layout config is invalid", ErrInvalid)
		}
		nodes[item.ID] = item
	}
	for id := range fields {
		if _, exists := placedFields[id]; !exists {
			return nil, fmt.Errorf("%w: field %d is not represented in layout", ErrInvalid, id)
		}
	}
	for id := range elements {
		if _, exists := placedElements[id]; !exists {
			return nil, fmt.Errorf("%w: element %d is not represented in layout", ErrInvalid, id)
		}
	}

	children := make(map[LayoutNodeID][]LayoutNodeID)
	root := LayoutNodeID(0)
	positions := make(map[LayoutNodeID]map[int]struct{})
	for _, item := range nodes {
		parentID := root
		if item.ParentID != nil {
			if *item.ParentID == item.ID {
				return nil, fmt.Errorf("%w: layout node cannot parent itself", ErrInvalid)
			}
			parent, exists := nodes[*item.ParentID]
			if !exists || parent.Kind != LayoutContainer {
				return nil, fmt.Errorf("%w: layout parent is missing or not a container", ErrInvalid)
			}
			parentID = *item.ParentID
		}
		if positions[parentID] == nil {
			positions[parentID] = make(map[int]struct{})
		}
		if _, duplicate := positions[parentID][item.Position]; duplicate {
			return nil, fmt.Errorf("%w: layout sibling position is duplicated", ErrInvalid)
		}
		positions[parentID][item.Position] = struct{}{}
		children[parentID] = append(children[parentID], item.ID)
	}
	state := make(map[LayoutNodeID]uint8, len(nodes))
	var visit func(LayoutNodeID) error
	visit = func(id LayoutNodeID) error {
		if state[id] == 1 {
			return fmt.Errorf("%w: layout contains a cycle", ErrInvalid)
		}
		if state[id] == 2 {
			return nil
		}
		state[id] = 1
		for _, child := range children[id] {
			if err := visit(child); err != nil {
				return err
			}
		}
		state[id] = 2
		return nil
	}
	for id := range nodes {
		if err := visit(id); err != nil {
			return nil, err
		}
	}

	result := append([]LayoutNode(nil), desired...)
	sort.SliceStable(result, func(i, j int) bool {
		leftParent, rightParent := LayoutNodeID(0), LayoutNodeID(0)
		if result[i].ParentID != nil {
			leftParent = *result[i].ParentID
		}
		if result[j].ParentID != nil {
			rightParent = *result[j].ParentID
		}
		if leftParent == rightParent {
			if result[i].Position == result[j].Position {
				return result[i].ID < result[j].ID
			}
			return result[i].Position < result[j].Position
		}
		return leftParent < rightParent
	})
	return result, validateMandatoryStructure(detail.Fields, detail.Elements, result)
}

func validateMandatoryStructure(fields []FormField, elements []Element, layout []LayoutNode) error {
	var consentID, captchaID FieldID
	for _, item := range fields {
		switch item.Code {
		case MandatoryConsentCode:
			if item.Type != FieldTypeConsent || !item.Required {
				return fmt.Errorf("%w: mandatory consent is invalid", ErrInvalid)
			}
			consentID = item.ID
		case MandatoryCaptchaCode:
			if item.Type != FieldTypeCaptcha || !item.Required {
				return fmt.Errorf("%w: mandatory CAPTCHA is invalid", ErrInvalid)
			}
			captchaID = item.ID
		}
	}
	if consentID <= 0 || captchaID <= 0 {
		return fmt.Errorf("%w: mandatory fields are missing", ErrInvalid)
	}
	submitIDs := make(map[ElementID]struct{})
	for _, item := range elements {
		if item.Type == ElementSubmitButton {
			submitIDs[item.ID] = struct{}{}
		}
	}
	if len(submitIDs) != 1 {
		return fmt.Errorf("%w: form must contain exactly one submit element", ErrInvalid)
	}
	var consentPlaced, captchaPlaced, submitPlaced bool
	for _, item := range layout {
		if item.FieldID != nil && *item.FieldID == consentID {
			consentPlaced = true
		}
		if item.FieldID != nil && *item.FieldID == captchaID {
			captchaPlaced = true
		}
		if item.ElementID != nil {
			if _, exists := submitIDs[*item.ElementID]; exists {
				submitPlaced = true
			}
		}
	}
	if !consentPlaced || !captchaPlaced || !submitPlaced {
		return fmt.Errorf("%w: mandatory layout nodes are missing", ErrInvalid)
	}
	return nil
}

func validateStatus(item Status) error {
	if item.FormID <= 0 {
		return fmt.Errorf("%w: status form is invalid", ErrInvalid)
	}
	if err := validateCode(item.Code, "status"); err != nil {
		return err
	}
	if strings.TrimSpace(item.Name) == "" || item.Name != strings.TrimSpace(item.Name) || item.Position < 0 {
		return fmt.Errorf("%w: status metadata is invalid", ErrInvalid)
	}
	return nil
}

func validateTrigger(trigger Trigger, statuses []Status) error {
	switch trigger.Type {
	case TriggerSubmitted:
		if trigger.From != "" || trigger.To != "" {
			return fmt.Errorf("%w: submitted trigger cannot constrain statuses", ErrInvalid)
		}
	case TriggerStatusChanged:
		available := make(map[string]struct{}, len(statuses))
		for _, item := range statuses {
			available[item.Code] = struct{}{}
		}
		for _, code := range []string{trigger.From, trigger.To} {
			if code == "" {
				continue
			}
			if _, exists := available[code]; !exists {
				return fmt.Errorf("%w: trigger status %q is unavailable", ErrInvalid, code)
			}
		}
	default:
		return fmt.Errorf("%w: trigger type %q is invalid", ErrInvalid, trigger.Type)
	}
	return nil
}

func validateAction(item Action, statuses []Status) error {
	if item.FormID <= 0 {
		return fmt.Errorf("%w: action form is invalid", ErrInvalid)
	}
	if err := validateCode(item.Code, "action"); err != nil {
		return err
	}
	if strings.TrimSpace(item.Name) == "" || item.Name != strings.TrimSpace(item.Name) || item.Position < 0 {
		return fmt.Errorf("%w: action metadata is invalid", ErrInvalid)
	}
	if strings.TrimSpace(item.ActionType) == "" || item.ActionType != strings.TrimSpace(item.ActionType) {
		return fmt.Errorf("%w: action type is invalid", ErrInvalid)
	}
	if len(item.Config) == 0 || !json.Valid(item.Config) {
		return fmt.Errorf("%w: action config is invalid", ErrInvalid)
	}
	return validateTrigger(item.Trigger, statuses)
}

func matchesTrigger(action Action, trigger Trigger) bool {
	if !action.Enabled || action.Trigger.Type != trigger.Type {
		return false
	}
	if trigger.Type == TriggerSubmitted {
		return true
	}
	return (action.Trigger.From == "" || action.Trigger.From == trigger.From) &&
		(action.Trigger.To == "" || action.Trigger.To == trigger.To)
}

func normalizePage(query PageQuery) (PageQuery, error) {
	if query.Page == 0 {
		query.Page = 1
	}
	if query.PerPage == 0 {
		query.PerPage = 20
	}
	query.Search = strings.TrimSpace(query.Search)
	if query.Page < 1 || query.PerPage < 1 || query.PerPage > 100 || len(query.Search) > 255 {
		return PageQuery{}, fmt.Errorf("%w: pagination is invalid", ErrInvalid)
	}
	return query, nil
}

func fieldValidationErrors(err error) FieldValidationErrors {
	var items field.ValidationErrors
	if !errors.As(err, &items) {
		return nil
	}
	result := make(FieldValidationErrors)
	for _, item := range items {
		result[item.Key] = append(result[item.Key], item.Rule)
	}
	return result
}
