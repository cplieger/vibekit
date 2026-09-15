package textsearch

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestPackageIsLibraryClean asserts every file in this directory, tests
// included, imports only the standard library: nothing from vibekit and no
// third-party module, so the directory lifts into its own module unchanged.
func TestPackageIsLibraryClean(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no Go files found; the check is reading nothing")
	}
	fset := token.NewFileSet()
	for _, file := range files {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		f, err := parser.ParseFile(fset, file, src, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatalf("%s: unquote import %s: %v", file, imp.Path.Value, err)
			}
			if first, _, _ := strings.Cut(path, "/"); strings.Contains(first, ".") {
				t.Errorf("%s imports %q; this package may import only the standard library", file, path)
			}
		}
	}
}
