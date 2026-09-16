package kernel_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestPublicPackagesDoNotImportConsumerInternalsOrOldModule(t *testing.T) {
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range file.Imports {
			importPath, _ := strconv.Unquote(spec.Path.Value)
			if strings.HasPrefix(importPath, "github.com/vernal96/go-cms/internal/") ||
				strings.HasPrefix(importPath, "github.com/vernal96/go-cms/kernel") ||
				strings.HasPrefix(importPath, "github.com/vernal96/go-cms/connectors") {
				t.Errorf("%s imports non-public or obsolete package %s", path, importPath)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
