package command

// The shape pin: a mechanical guard against an aggregate dependency interface
// growing back in either of the two packages that had one.
//
// It lives in internal/command and reads ../translate as well, because the rule
// is one rule over two packages and a second copy of it would be a second thing
// to keep in step. Only production files are read: a test double stands in for
// the whole host, so the doubles in this package and in translate DO name every
// role at once, deliberately, and gating them would be gating the fixture rather
// than the design.
//
// Two numbers, because either alone is escapable. Embed count catches the
// composite spelled as embedding; transitive method count catches the same
// surface spelled as a flat method list.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	// maxEmbeds is the most other interfaces one interface may embed. The widest
	// legitimate case is Bridge at 3, which composes the three seams of ONE
	// concrete type (RPC, prompt slot, priming) rather than unrelated host roles.
	maxEmbeds = 3
	// maxMethods is the widest transitive method surface any one interface here
	// may declare. Bridge sets it at 12, the real method count of a per-chat ACP
	// bridge. The deleted composites were 27 (command) and 17 (translate).
	maxMethods = 12
)

// ifaceDecl is one interface type declaration and its shape.
type ifaceDecl struct {
	pkgDir   string
	file     string
	name     string
	embeds   []string
	direct   int
	external int
}

// TestNoFatDependencyAggregate fails if any interface in command or translate
// exceeds either shape bound.
func TestNoFatDependencyAggregate(t *testing.T) {
	decls := collectInterfaces(t, ".", "../translate")
	if len(decls) == 0 {
		t.Fatal("parsed zero interfaces; the pin is not reading any sources")
	}

	byName := make(map[string]ifaceDecl, len(decls))
	for _, d := range decls {
		byName[d.pkgDir+"."+d.name] = d
	}

	for _, d := range decls {
		if len(d.embeds) > maxEmbeds {
			t.Errorf("%s: interface %s embeds %d interfaces (%s), max %d — an interface aggregating that many roles is a DI container, and each consumer should name only the roles it uses",
				d.file, d.name, len(d.embeds), strings.Join(d.embeds, ", "), maxEmbeds)
		}
		if n := transitiveMethods(byName, d, map[string]bool{}); n > maxMethods {
			t.Errorf("%s: interface %s declares %d methods transitively, max %d — split it by role at its consumers",
				d.file, d.name, n, maxMethods)
		}
	}
}

// transitiveMethods counts d's own methods plus those of every embedded
// interface declared in the same package. An embedded interface from another
// package counts as 1, which is the floor rather than the truth: the packages
// under this pin only embed same-package interfaces, so a cross-package embed
// appearing here is itself worth a look.
func transitiveMethods(byName map[string]ifaceDecl, d ifaceDecl, seen map[string]bool) int {
	key := d.pkgDir + "." + d.name
	if seen[key] {
		return 0
	}
	seen[key] = true
	total := d.direct + d.external
	for _, e := range d.embeds {
		if sub, ok := byName[d.pkgDir+"."+e]; ok {
			total += transitiveMethods(byName, sub, seen)
		}
	}
	return total
}

// collectInterfaces parses the production files of each directory and returns
// every interface type declaration in them.
func collectInterfaces(t *testing.T, dirs ...string) []ifaceDecl {
	t.Helper()
	var out []ifaceDecl
	for _, dir := range dirs {
		fset := token.NewFileSet()
		// ReadDir + ParseFile rather than parser.ParseDir, which Go 1.25
		// deprecated for ignoring build tags. The suggested replacement,
		// go/packages, type-checks a whole program to answer a question about
		// syntax; this walk never needed the package grouping ParseDir returned,
		// only the files.
		ents, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, ent := range ents {
			name := ent.Name()
			if ent.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(dir, name)
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			ast.Inspect(file, func(n ast.Node) bool {
				ts, ok := n.(*ast.TypeSpec)
				if !ok {
					return true
				}
				it, ok := ts.Type.(*ast.InterfaceType)
				if !ok {
					return true
				}
				out = append(out, describe(dir, name, ts.Name.Name, it))
				return true
			})
		}
	}
	return out
}

// describe splits an interface's fields into embedded interfaces and methods.
func describe(dir, file, name string, it *ast.InterfaceType) ifaceDecl {
	d := ifaceDecl{pkgDir: dir, file: filepath.Join(dir, file), name: name}
	for _, f := range it.Methods.List {
		if len(f.Names) > 0 {
			d.direct++
			continue
		}
		switch e := f.Type.(type) {
		case *ast.Ident:
			d.embeds = append(d.embeds, e.Name)
		case *ast.SelectorExpr:
			d.embeds = append(d.embeds, exprName(e))
			d.external++
		default:
			// A type constraint element (union, ~T) is not a role.
			d.direct++
		}
	}
	return d
}

// exprName renders a qualified embedded name for the failure message.
func exprName(e *ast.SelectorExpr) string {
	if x, ok := e.X.(*ast.Ident); ok {
		return x.Name + "." + e.Sel.Name
	}
	return e.Sel.Name
}
