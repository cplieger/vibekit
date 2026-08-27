package kascap

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// acpMethodRe matches an ACP method name written as a Go string literal.
//
// The four families are the whole wire vocabulary: the base protocol
// (`session/*`, `fs/*`, `terminal/*`, `initialize`) and kiro-cli's extensions
// (`_kiro/*`, `_session/*`).
var acpMethodRe = regexp.MustCompile(
	`"(_?kiro/[a-zA-Z/_]+|_?session/[a-zA-Z/_]+|fs/[a-zA-Z/_]+|terminal/[a-zA-Z/_]+|initialize)"`,
)

// acpMethodFloor is the count below which the derivation below is presumed
// broken rather than the surface presumed shrunk.
//
// Measured at 82 on 2026-08-27. A test that derives its own subject can fail
// open — a refactor that builds method names by concatenation, or moves them
// into a generated file this walk skips, would leave the sweep with nothing to
// check and every assertion vacuously true. The floor is the guard against
// that, set well under the real count so a legitimate removal does not trip it.
const acpMethodFloor = 60

// TestACPMethodsPresent is the upgrade gate that answers the question a
// kiro-cli bump actually raises: does the agent server still speak every verb
// vibekit depends on?
//
// It exists because the capability census could not answer it. That census
// introspects the bundle's IDENTIFIERS, so kiro-cli 2.20.0 broke it by shipping
// a mangled build — 23.3 MB to 11.3 MB, every local and module-level function
// renamed — and the resulting red gate said nothing whatsoever about whether the
// upgrade was safe. Measured across that same bump: all 82 methods below are
// present in both bundles, unchanged, while eight of the census's nine
// extractors had to be re-anchored.
//
// A method name is a WIRE STRING. A minifier may not touch it, because both
// ends compare it byte for byte, which is exactly the property that makes it a
// durable anchor where an identifier is not. So this test should keep working
// across upstream builds that reshape the census, and when it DOES fail it
// names a verb vibekit calls into the void.
//
// Scope, stated because it bounds what a pass means: presence of the name, not
// agreement about its params, its result shape or its semantics. Those are the
// goldens' and the census's job. A vanished method is the failure mode this
// catches, and it is the one that takes a feature out silently.
func TestACPMethodsPresent(t *testing.T) {
	active := activeKASVersion(t)
	src, path := bundleSource(t, active)
	methods := vibekitACPMethods(t)

	if len(methods) < acpMethodFloor {
		t.Fatalf(`derived only %d ACP method name(s) from vibekit's own sources, want >= %d.
The sweep, not the surface, is what changed: a method spelled by concatenation
or moved into a file this walk skips is invisible to it, and every assertion
below would then pass for having nothing to check.`, len(methods), acpMethodFloor)
	}

	var absent []string
	for _, m := range methods {
		if !strings.Contains(src, `"`+m+`"`) && !strings.Contains(src, `'`+m+`'`) {
			absent = append(absent, m)
		}
	}
	if len(absent) > 0 {
		t.Errorf(`kiro-cli %s does not carry %d ACP method name(s) vibekit uses:
  %s
vibekit either calls these and gets an unknown-method error, or serves them and
the handler is now dead. Read each one against the bundle: a RENAME upstream
needs the same rename here, and a REMOVAL needs the feature retired rather than
left calling into the void.
Bundle: %s`, active, len(absent), strings.Join(absent, "\n  "), path)
		return
	}
	t.Logf("kiro-cli %s carries all %d ACP method names vibekit uses", active, len(methods))
}

// vibekitACPMethods returns every ACP method name vibekit's production sources
// name, sorted and deduplicated.
//
// Derived rather than listed, so a method added to vibekit is covered without
// anyone remembering to extend a fixture — the failure mode a hand-kept list
// has is silently omitting the one verb that later breaks. Test files are
// excluded because they deliberately name verbs that do NOT exist, to exercise
// the unknown-method paths (`session/somethingNew`, `terminal/not_a_verb`), and
// those would read as upstream removals here.
func vibekitACPMethods(t *testing.T) []string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	seen := make(map[string]bool)
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "static", "testdata":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range acpMethodRe.FindAllStringSubmatch(string(raw), -1) {
			// A trailing slash is a dispatch PREFIX (`_kiro/workflow/`), not a
			// method, so no bundle carries it as a literal.
			if strings.HasSuffix(m[1], "/") {
				continue
			}
			seen[m[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	out := make([]string, 0, len(seen))
	for m := range seen {
		out = append(out, m)
	}
	slices.Sort(out)
	return out
}
