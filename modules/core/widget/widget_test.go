package widget

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
)

type testResolver map[field.TypeCode]field.Type

func (r testResolver) FieldType(
	code field.TypeCode,
) (field.Type, bool) {
	fieldType, exists := r[code]
	return fieldType, exists
}

func standardResolver() testResolver {
	result := make(testResolver)
	for _, fieldType := range field.StandardTypes() {
		result[fieldType.Code()] = fieldType
	}
	return result
}

type testWidget struct {
	definition Definition
	new        func(map[string]any) (Instance, error)
}

func (w *testWidget) Definition() Definition {
	return CloneDefinition(w.definition)
}

func (w *testWidget) New(values map[string]any) (Instance, error) {
	return w.new(values)
}

type testInstance struct {
	data map[string]any
}

func (i testInstance) Render(
	context.Context,
	RenderInput,
) (map[string]any, error) {
	return i.data, nil
}

func boolPointer(value bool) *bool {
	return &value
}

func testModule(code string) ModuleDescriptor {
	return ModuleDescriptor{Code: code, Label: "Test " + code}
}

func TestCatalogQualifiesCompilesAndClonesWidgets(t *testing.T) {
	var received map[string]any
	summary := NewRef("summary")
	catalog, err := Compile(
		[]Source{{
			Module: testModule("content"),
			Widgets: []Widget{&testWidget{
				definition: Definition{
					Reference:   summary,
					Label:       "Summary",
					Description: "Article summary",
					Fields: []field.Definition{{
						Key:      "count",
						Type:     field.TypeInteger,
						Label:    "Count",
						Required: boolPointer(true),
					}},
				},
				new: func(values map[string]any) (Instance, error) {
					received = values
					return testInstance{data: values}, nil
				},
			}},
		}},
		nil,
		standardResolver(),
	)
	if err != nil {
		t.Fatal(err)
	}

	runtime, exists := catalog.Widget("content_summary")
	if !exists {
		t.Fatal("qualified widget is unavailable")
	}
	resolved, code, exists := catalog.Resolve(summary)
	if !exists || resolved != runtime || code != "content_summary" {
		t.Fatalf("resolved widget = %#v, %q, %t", resolved, code, exists)
	}
	instance, err := runtime.New(map[string]any{
		"count": json.Number("3"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if received["count"] != int64(3) {
		t.Fatalf("normalized params = %#v", received)
	}
	data, err := instance.Render(context.Background(), RenderInput{})
	if err != nil || !reflect.DeepEqual(data, received) {
		t.Fatalf("render data = %#v, %v", data, err)
	}

	definitions := catalog.Definitions()
	if len(definitions) != 1 ||
		definitions[0].Code != "content_summary" ||
		definitions[0].Module.Code != "content" ||
		definitions[0].Module.Label != "Test content" {
		t.Fatalf("definitions = %#v", definitions)
	}
	definitions[0].Fields[0].Label = "Changed"
	if runtime.Definition().Fields[0].Label != "Count" {
		t.Fatal("catalog definition shares caller memory")
	}
}

func TestRuntimeClassifiesParamsAndInstanceFailures(t *testing.T) {
	catalog, err := Compile(
		[]Source{{
			Module: testModule("content"),
			Widgets: []Widget{&testWidget{
				definition: Definition{
					Reference:   NewRef("summary"),
					Label:       "Summary",
					Description: "Article summary",
					Fields: []field.Definition{{
						Key:      "title",
						Type:     field.TypeString,
						Label:    "Title",
						Required: boolPointer(true),
					}},
				},
				new: func(map[string]any) (Instance, error) {
					return nil, errors.New("constructor failed")
				},
			}},
		}},
		nil,
		standardResolver(),
	)
	if err != nil {
		t.Fatal(err)
	}
	runtime, _ := catalog.Widget("content_summary")

	if _, err := runtime.New(map[string]any{}); !errors.Is(
		err,
		ErrInvalidParams,
	) {
		t.Fatalf("invalid params error = %v", err)
	}
	if _, err := runtime.New(map[string]any{
		"title": "Article",
	}); !errors.Is(err, ErrInstanceFailed) {
		t.Fatalf("instance error = %v", err)
	}
}

func TestCatalogRejectsInvalidAndDuplicateWidgets(t *testing.T) {
	valid := func(code Code) Widget {
		return &testWidget{
			definition: Definition{
				Reference:   NewRef(code),
				Label:       "Widget",
				Description: "Description",
			},
			new: func(map[string]any) (Instance, error) {
				return testInstance{}, nil
			},
		}
	}

	testCases := []struct {
		name    string
		sources []Source
		match   string
	}{
		{
			name: "typed nil",
			sources: []Source{{
				Module:  testModule("content"),
				Widgets: []Widget{(*testWidget)(nil)},
			}},
			match: "is nil",
		},
		{
			name: "duplicate local",
			sources: []Source{{
				Module: testModule("content"),
				Widgets: []Widget{
					valid("summary"),
					valid("summary"),
				},
			}},
			match: "duplicate widget code",
		},
		{
			name: "duplicate global",
			sources: []Source{
				{Module: testModule("content_news"), Widgets: []Widget{valid("top")}},
				{Module: testModule("content"), Widgets: []Widget{valid("news_top")}},
			},
			match: "duplicate global widget code",
		},
		{
			name: "empty description",
			sources: []Source{{
				Module: testModule("content"),
				Widgets: []Widget{&testWidget{
					definition: Definition{
						Reference: NewRef("summary"),
						Label:     "Summary",
					},
				}},
			}},
			match: "invalid description",
		},
		{
			name: "unknown field type",
			sources: []Source{{
				Module: testModule("content"),
				Widgets: []Widget{&testWidget{
					definition: Definition{
						Reference:   NewRef("summary"),
						Label:       "Summary",
						Description: "Description",
						Fields: []field.Definition{{
							Key:   "value",
							Type:  "unknown",
							Label: "Value",
						}},
					},
				}},
			}},
			match: "unknown type",
		},
		{
			name: "unknown editor tab field",
			sources: []Source{{
				Module: testModule("content"),
				Widgets: []Widget{&testWidget{definition: Definition{
					Reference: NewRef("summary"), Label: "Summary", Description: "Description",
					EditorTabs: []field.EditorTab{{Code: "main", Label: "Main", Fields: []string{"missing"}}},
				}}},
			}},
			match: "unknown field",
		},
		{
			name: "unassigned editor field",
			sources: []Source{{
				Module: testModule("content"),
				Widgets: []Widget{&testWidget{definition: Definition{
					Reference: NewRef("summary"), Label: "Summary", Description: "Description",
					Fields:     []field.Definition{{Key: "title", Type: field.TypeString, Label: "Title"}},
					EditorTabs: []field.EditorTab{{Code: "main", Label: "Main"}},
				}}},
			}},
			match: "not assigned",
		},
	}

	for _, test := range testCases {
		t.Run(test.name, func(t *testing.T) {
			_, err := Compile(test.sources, nil, standardResolver())
			if err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestZeroFieldWidgetAndImplicitDefaultView(t *testing.T) {
	empty := NewRef("empty")
	compact := NewView(empty, "compact", "Compact")
	catalog, err := Compile([]Source{{
		Module: testModule("content"),
		Widgets: []Widget{&testWidget{
			definition: Definition{Reference: empty, Label: "Empty", Description: "No params"},
			new: func(values map[string]any) (Instance, error) {
				return testInstance{data: values}, nil
			},
		}},
	}}, []View{compact}, standardResolver())
	if err != nil {
		t.Fatal(err)
	}
	runtime, _ := catalog.Widget("content_empty")
	if _, err := runtime.New(map[string]any{}); err != nil {
		t.Fatalf("empty params: %v", err)
	}
	definition := runtime.Definition()
	if len(definition.Views) != 1 || definition.Views[0] != compact {
		t.Fatalf("custom views = %#v", definition.Views)
	}
	if err := runtime.ValidatePresentation(DefaultPresentation()); err != nil {
		t.Fatalf("implicit default: %v", err)
	}
	custom := DefaultPresentation()
	custom.View = "compact"
	if err := runtime.ValidatePresentation(custom); err != nil {
		t.Fatalf("custom view: %v", err)
	}
	custom.View = "missing"
	if err := runtime.ValidatePresentation(custom); !errors.Is(err, ErrInvalidPresentation) {
		t.Fatalf("invalid view error = %v", err)
	}
}

func TestPresentationAndAreasValidate(t *testing.T) {
	if !ValidArea(AreaBody) || !ValidArea(AreaSidebar) || ValidArea("footer") {
		t.Fatal("widget area validation is incorrect")
	}
	for _, presentation := range []Presentation{
		{Columns: 0, Enabled: true},
		{Columns: 13, Enabled: true},
		{Columns: 12, MarginTop: -1, Enabled: true},
		{Columns: 12, MarginBottom: 4, Enabled: true},
	} {
		if !errors.Is(presentation.Validate(), ErrInvalidPresentation) {
			t.Fatalf("presentation accepted: %#v", presentation)
		}
	}
	if err := DefaultPresentation().Validate(); err != nil {
		t.Fatal(err)
	}
}
