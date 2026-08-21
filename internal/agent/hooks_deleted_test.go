package agent

// The shape of what D69 removed, asserted so it cannot come back by accident.
//
// A compile error already guards most of it — every deleted identifier is gone,
// so a caller would not build. What a compile cannot catch is the two things
// below: a ROUTE re-registered with a fresh handler, and a METHOD NAME
// reintroduced as a constant, which is the first step of re-adding the surface
// and the one that makes it reachable again.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestHooksRoutes_HaveNoTriggerVerb pins the route table.
//
// POST /api/hooks/{id}/trigger was Run-now's whole entry point, and its absence is
// what makes the shell path unreachable from the browser. Asserted through the real
// mux rather than by reading the source, so a route registered anywhere else in the
// package fails this too.
func TestHooksRoutes_HaveNoTriggerVerb(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	(&Settings{}).registerHooksRoutes(mux)

	for _, tc := range []struct {
		name    string
		method  string
		path    string
		matched bool
	}{
		{"the list read survives", http.MethodGet, "/api/hooks", true},
		{"the enabled toggle survives", http.MethodPost, "/api/hooks/abc/enabled", true},
		{"Run now is gone", http.MethodPost, "/api/hooks/abc/trigger", false},
		{"and gone for every id", http.MethodPost, "/api/hooks/YS5qc29uI2hvb2stMA/trigger", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequest(tc.method, tc.path, nil)
			_, pattern := mux.Handler(r)
			if got := pattern != ""; got != tc.matched {
				t.Errorf("%s %s matched pattern %q; want matched=%v",
					tc.method, tc.path, pattern, tc.matched)
			}
		})
	}
}

// TestHookMethodConstants_OmitTheRunNowPair pins the wire vocabulary.
//
// Naming a KAS method in this package is what makes it reachable, so the two
// Run-now names went with the surface instead of being kept "for reference".
// Reading the DECLARATIONS rather than grepping the text is what lets the
// don't-re-add-this comments keep saying the names out loud: a comment mentioning
// executeHook is the record of why it went, while a `const` declaring it is the
// first line of bringing it back.
func TestHookMethodConstants_OmitTheRunNowPair(t *testing.T) {
	t.Parallel()
	const gone = "_kiro/hooks/triggerHook, _kiro/hooks/executeHook"

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the runtime package directory: %v", err)
	}
	fset := token.NewFileSet()
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, pErr := parser.ParseFile(fset, name, nil, 0)
		if pErr != nil {
			t.Fatalf("parse %s: %v", name, pErr)
		}
		scanned++
		for _, spec := range constStrings(file) {
			if spec.value == "_kiro/hooks/triggerHook" || spec.value == "_kiro/hooks/executeHook" {
				t.Errorf("%s declares %s = %q; the Run-now pair (%s) is deleted, "+
					"and declaring either name is how the shell path comes back",
					name, spec.name, spec.value, gone)
			}
		}
	}
	// A scan that read nothing would pass for the wrong reason.
	if scanned == 0 {
		t.Fatal("scanned no production sources; the guard is vacuous")
	}
}

// constString is one string-valued constant declaration.
type constString struct {
	name  string
	value string
}

// constStrings returns every string-literal constant a file declares.
func constStrings(file *ast.File) []constString {
	var out []constString
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				out = append(out, constString{name: name.Name, value: strings.Trim(lit.Value, `"`)})
			}
		}
	}
	return out
}
