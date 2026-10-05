package kernel_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestModuleDeclarationsAreTypeChecked(t *testing.T) {
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	manifest, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	manifest = []byte(strings.Replace(string(manifest), "module github.com/vernal96/go-cms-kernel", "module modulecompiletest", 1) + "\nrequire github.com/vernal96/go-cms-kernel v0.0.0\nreplace github.com/vernal96/go-cms-kernel => " + root + "\n")
	if err := os.WriteFile(filepath.Join(directory, "go.mod"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	sums, err := os.ReadFile("go.sum")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "go.sum"), sums, 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, expression, failure string }{
		{"valid", `[]kernel.Module{core.New(core.Config{}), admin.New(), search.New(), mail.New(mail.Config{}), forms.New(forms.Config{}), seo.New(seo.Config{MaxTemplateLength: 2000}), counter.New(25)}`, ""},
		{"wrong config", `core.New(seo.Config{})`, "cannot use"},
		{"wrong limit type", `counter.New("25")`, "cannot use"},
		{"extra argument", `admin.New(core.Config{})`, "too many arguments"},
		{"missing argument", `core.New()`, "not enough arguments"},
		{"foreign field", `seo.New(seo.Config{Caches: nil})`, "unknown field Caches"},
		{"old declaration", `kernel.ProfileModule{}`, "undefined"},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := `package main
import (
 "github.com/vernal96/go-cms-kernel"
 "github.com/vernal96/go-cms-kernel/modules/core"
 "github.com/vernal96/go-cms-kernel/modules/admin"
 "github.com/vernal96/go-cms-kernel/modules/search"
 "github.com/vernal96/go-cms-kernel/modules/mail"
 "github.com/vernal96/go-cms-kernel/modules/forms"
 "github.com/vernal96/go-cms-kernel/modules/seo"
 "github.com/vernal96/go-cms-kernel/examples/counter"
)
var _ kernel.Module = counter.New(1)
var _ = core.New
var _ = admin.New
var _ = search.New
var _ = mail.New
var _ = forms.New
var _ = seo.New
var _ = ` + test.expression + "\nfunc main() {}\n"
			if err := os.WriteFile(filepath.Join(directory, "main.go"), []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			command := exec.Command("go", "build", "-mod=mod", "-o", filepath.Join(directory, "result"), ".")
			command.Dir = directory
			command.Env = append(os.Environ(), "GOWORK=off")
			output, err := command.CombinedOutput()
			if test.failure == "" {
				if err != nil {
					t.Fatalf("valid declaration failed: %v\n%s", err, output)
				}
				return
			}
			if err == nil || !strings.Contains(string(output), test.failure) {
				t.Fatalf("expected compile failure %q, got %v\n%s", test.failure, err, output)
			}
		})
	}
}
