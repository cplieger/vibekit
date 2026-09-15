package buffer

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestStructure_OnlyWriteAndReadTakeTheMutex pins the mechanism behind the
// live_turn version: every mutation mints through write, every read goes through
// read, and nothing else touches buf.mu. Two assertions over the package's
// non-test files: (a) `buf.mu.Lock()` appears only in write and read; (b) every
// EXPORTED method on *Buffer contains exactly one call to one of them. An
// unexported helper is exempt from (b) and may not name buf.mu.
//
// Structural rather than behavioural on purpose: whatever shape a mutation takes
// (an index write, a map write, a builder call), a method that bypasses write
// cannot return a version the emitter can stamp, and this is decidable by an AST
// walk where a predicate on assignment syntax is not.
func TestStructure_OnlyWriteAndReadTakeTheMutex(t *testing.T) {
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("Setup: glob: %v", err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("Setup: parse %s: %v", name, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Body == nil || receiverType(fn) != "Buffer" {
				continue
			}
			locks := countCalls(fn.Body, "mu", "Lock")
			guards := countCalls(fn.Body, "buf", "write") + countCalls(fn.Body, "buf", "read")
			switch fn.Name.Name {
			case "write", "read":
				if locks != 1 {
					t.Errorf("%s: %s takes buf.mu %d times, want exactly 1", name, fn.Name.Name, locks)
				}
			default:
				if locks != 0 {
					t.Errorf("%s: %s takes buf.mu directly; only write and read may", name, fn.Name.Name)
				}
				if fn.Name.IsExported() && guards != 1 {
					t.Errorf("%s: exported %s calls write/read %d times, want exactly 1", name, fn.Name.Name, guards)
				}
			}
		}
	}
}

// TestStructure_NewIsTheOnlyConstructor pins that no package outside internal/buffer
// builds a Buffer by composite literal: such a buffer carries id 0, so every one
// of them would present the same live_turn version.
func TestStructure_NewIsTheOnlyConstructor(t *testing.T) {
	root := filepath.Join("..", "..")
	var hits []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", ".git", "static", ".worktrees":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.Contains(path, string(filepath.Separator)+"buffer"+string(filepath.Separator)) {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// A map or slice TYPE (`map[K]*buffer.Buffer{}`) is not a construction.
		if bufferLiteral.Match(src) {
			hits = append(hits, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Setup: walk: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("Buffer built by composite literal outside internal/buffer (use buffer.New()):\n  %s", strings.Join(hits, "\n  "))
	}
}

// bufferLiteral matches a Buffer composite literal, addressed or not.
var bufferLiteral = regexp.MustCompile(`(^|[^*\]])buffer\.Buffer\{`)

func receiverType(fn *ast.FuncDecl) string {
	if len(fn.Recv.List) != 1 {
		return ""
	}
	expr := fn.Recv.List[0].Type
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if id, ok := expr.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// countCalls counts calls of the form <x>.<sel>(...) where the selector chain ends
// in recv.sel, so "mu"/"Lock" matches buf.mu.Lock() and "buf"/"write" matches
// buf.write(...).
func countCalls(body *ast.BlockStmt, recv, sel string) int {
	n := 0
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		s, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || s.Sel.Name != sel {
			return true
		}
		switch x := s.X.(type) {
		case *ast.Ident:
			if x.Name == recv {
				n++
			}
		case *ast.SelectorExpr:
			if x.Sel.Name == recv {
				n++
			}
		}
		return true
	})
	return n
}
