package template

import (
	"reflect"
	"testing"

	"github.com/vernal96/go-cms-kernel/modules/core/widget"
)

func areaRuntime(t *testing.T, layout Layout) *Runtime {
	t.Helper()
	catalog, err := Compile([]Definition{{Code: "page", Label: "Page", Layout: layout}}, resolver())
	if err != nil {
		t.Fatal(err)
	}
	catalog, err = catalog.CompileWidgets(compileWidgets(t, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	runtime, _ := catalog.Template("page")
	return runtime
}

func TestDynamicAreasDefaultAndRecovery(t *testing.T) {
	empty := areaRuntime(t, nil)
	descriptors := empty.Areas()
	if len(descriptors) != 1 || descriptors[0].Code != widget.AreaDefault || descriptors[0].Label != "Страница сайта" || descriptors[0].AdminSpan != 24 ||
		!reflect.DeepEqual(descriptors[0].Items, []AreaItem{{Kind: "resource_widgets"}}) {
		t.Fatalf("default: %#v", descriptors)
	}
	placements, err := Compose(empty, nil)
	if err != nil || len(placements) != 1 || placements[widget.AreaDefault] == nil {
		t.Fatalf("empty default: %#v %v", placements, err)
	}
	bindings := []widget.Binding{
		{ID: 4, Code: "four", Area: "z_removed", Position: 0},
		{ID: 2, Code: "two", Area: "a_removed", Position: 1},
		{ID: 3, Code: "three", Area: widget.AreaDefault, Position: 0},
		{ID: 1, Code: "one", Area: "a_removed", Position: 0},
	}
	original := widget.CloneBindings(bindings)
	declared := areaRuntime(t, Layout{{Code: "main", Label: "Main", AdminSpan: 16}, {Code: "aside", Label: "Aside", AdminSpan: 8}, {Code: "footer", Label: "Footer"}})
	placements, err = Compose(declared, bindings)
	if err != nil {
		t.Fatal(err)
	}
	if len(placements) != 4 || placementCodes(placements[widget.AreaDefault]) != "three,one,two,four" {
		t.Fatalf("recovery: %#v", placements)
	}
	if !reflect.DeepEqual(bindings, original) {
		t.Fatal("composition mutated bindings")
	}
	restored := areaRuntime(t, Layout{{Code: "a_removed", Label: "Restored"}})
	placements, err = Compose(restored, bindings)
	if err != nil || placementCodes(placements["a_removed"]) != "one,two" || placementCodes(placements[widget.AreaDefault]) != "three,four" {
		t.Fatalf("restored: %#v %v", placements, err)
	}
	for i := range bindings {
		bindings[i].Area = "main"
		bindings[i].Position = i
	}
	placements, err = Compose(declared, bindings)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := placements[widget.AreaDefault]; exists {
		t.Fatal("empty default must disappear with declared areas")
	}
	if placements["aside"] == nil || placements["footer"] == nil {
		t.Fatal("empty declared zones must remain arrays")
	}
}

func TestDynamicAreaValidationAndIsolation(t *testing.T) {
	for _, layout := range []Layout{
		{{Code: "default", Label: "Reserved"}},
		{{Code: "bad code", Label: "Invalid"}},
		{{Code: "valid", Label: ""}},
		{{Code: "valid", Label: "Valid", AdminSpan: 25}},
		{{Code: "valid", Label: "Valid", AdminSpan: -1}},
		{{Code: "same", Label: "One"}, {Code: "same", Label: "Two"}},
	} {
		if _, err := Compile([]Definition{{Code: "page", Label: "Page", Layout: layout}}, resolver()); err == nil {
			t.Fatalf("accepted invalid layout: %#v", layout)
		}
	}
	runtime := areaRuntime(t, Layout{{Code: "empty", Label: "Read only", Items: []Item{}}, {Code: "editable", Label: "Editable"}})
	if runtime.AllowsResourceArea("empty") || !runtime.AllowsResourceArea("editable") || runtime.AllowsResourceArea("unknown") {
		t.Fatal("wrong editable areas")
	}
	copy := runtime.Definition()
	copy.Layout[0].Label = "Mutated"
	copy.Layout[1].Items = []Item{}
	if runtime.Areas()[0].Label != "Read only" || !runtime.AllowsResourceArea("editable") {
		t.Fatal("declaration shares mutable state")
	}
	if runtime.ResolveArea("empty") != widget.AreaDefault {
		t.Fatal("stored widgets without a slot must remain recoverable")
	}
}
