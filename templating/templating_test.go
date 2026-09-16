package templating

import (
	"errors"
	"strings"
	"testing"
)

func compileForTest(t *testing.T, source string, resultLimit int) Compiled {
	t.Helper()
	compiled, err := Compile(source, map[string]struct{}{
		"data.name": {}, "data.value": {},
	}, Limits{MaxSourceLength: 100, MaxResultLength: resultLimit})
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

func TestLiteralAndInterpolationAreDeterministic(t *testing.T) {
	t.Parallel()
	compiled := compileForTest(t, "Hello, {{ data.name }}: {{data.value}}", 100)
	resolver := func(variable string) (any, bool, error) {
		return map[string]any{"data.name": "World", "data.value": 42}[variable], true, nil
	}
	first, err := Render(compiled, resolver, PlainText)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Render(compiled, resolver, PlainText)
	if err != nil {
		t.Fatal(err)
	}
	if first.Value != "Hello, World: 42" || second.Value != first.Value || len(first.Warnings) != 0 {
		t.Fatalf("results = %#v / %#v", first, second)
	}
}

func TestCompileRejectsMalformedUnknownAndLongSources(t *testing.T) {
	t.Parallel()
	allowed := map[string]struct{}{"data.name": {}}
	limits := Limits{MaxSourceLength: 20, MaxResultLength: 100}
	for _, source := range []string{
		"{{data.unknown}}", "{{data.name", "data.name}}", "{{name}}", "{{ data.name | html }}",
	} {
		if _, err := Compile(source, allowed, limits); err == nil {
			t.Fatalf("source %q was accepted", source)
		}
	}
	_, err := Compile(strings.Repeat("я", 21), allowed, limits)
	var compileError CompileError
	if !errors.As(err, &compileError) || !strings.Contains(err.Error(), "exceeds 20") {
		t.Fatalf("length error = %v", err)
	}
}

func TestMissingValueRendersEmptyWithWarning(t *testing.T) {
	t.Parallel()
	result, err := Render(
		compileForTest(t, "before {{data.name}} after", 100),
		func(string) (any, bool, error) { return nil, false, nil },
		PlainText,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Value != "before  after" || len(result.Warnings) != 1 || result.Warnings[0].Variable != "data.name" {
		t.Fatalf("result = %#v", result)
	}
}

func TestControlledScalarConversion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		value any
		want  string
	}{
		{"text", "text"}, {true, "true"}, {int64(-2), "-2"}, {uint32(3), "3"}, {12.5, "12.5"},
	}
	for _, test := range tests {
		result, err := Render(
			compileForTest(t, "{{data.value}}", 100),
			func(string) (any, bool, error) { return test.value, true, nil },
			PlainText,
		)
		if err != nil || result.Value != test.want {
			t.Fatalf("value %#v rendered as %#v, %v", test.value, result, err)
		}
	}
	_, err := Render(
		compileForTest(t, "{{data.value}}", 100),
		func(string) (any, bool, error) { return struct{}{}, true, nil },
		PlainText,
	)
	if err == nil || !strings.Contains(err.Error(), "unsupported scalar") {
		t.Fatalf("unsupported scalar error = %v", err)
	}
}

func TestHTMLEscapesVariablesButKeepsTemplateMarkup(t *testing.T) {
	t.Parallel()
	result, err := Render(
		compileForTest(t, "<b>{{data.value}}</b>", 100),
		func(string) (any, bool, error) { return `<script>alert("x")</script>`, true, nil },
		HTML,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Value != "<b>&lt;script&gt;alert(&#34;x&#34;)&lt;/script&gt;</b>" {
		t.Fatalf("HTML = %q", result.Value)
	}
}

func TestHeaderRejectsControlCharactersFromLiteralOrVariable(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		source string
		value  string
	}{
		{"Subject\r\nBcc: attacker@example.com", "ok"},
		{"{{data.value}}", "ok\nBcc: attacker@example.com"},
		{"{{data.value}}", "bad\x00value"},
	} {
		_, err := Render(
			compileForTest(t, test.source, 100),
			func(string) (any, bool, error) { return test.value, true, nil },
			Header,
		)
		if err == nil {
			t.Fatalf("unsafe header %#v was accepted", test)
		}
	}
}

func TestRenderedLengthLimitIncludesEscaping(t *testing.T) {
	t.Parallel()
	_, err := Render(
		compileForTest(t, "{{data.value}}", 5),
		func(string) (any, bool, error) { return "<&", true, nil },
		HTML,
	)
	if err == nil || !strings.Contains(err.Error(), "exceeds 5") {
		t.Fatalf("result length error = %v", err)
	}
}
