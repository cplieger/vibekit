package wirespec

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/wiregen/v2"
)

// The bug class these tests exist for: an SSE event vibekit broadcasts whose
// payload type is not in the registry, so the generator emits no decoder for
// it, so the client hand-declares the shape in bus.ts and validates nothing at
// runtime. It is not hypothetical — the agent-terminal trio shipped that way
// and the SSE table still carries the comment recording it. Nothing in the
// build catches it, because both halves compile perfectly: the Go side
// broadcasts a struct and the TypeScript side declares an interface, and
// neither one mentions the other.
//
// So the registry is walked from both ends. A payload registered but unbound
// is a type emitted for nobody; a payload declared but unregistered is an
// event on the wire with no contract behind it.

// apiPkgPath is read off a registration rather than written as a literal, so
// renaming the api package cannot leave these tests silently scanning nothing.
var apiPkgPath = wiregen.TypeRef[api.Message]().PkgPath

// emptySignalPayloads are the payload types deliberately absent from the
// registry. Each is an empty struct: the event is a pure invalidation signal
// ("something changed, refetch"), so there is no field for a generated decoder
// to validate and the emitted TypeScript would be an empty interface behind a
// decoder that accepts anything. The event type itself is the whole message.
//
// A payload only belongs here while it stays empty. Give one of them a field
// and it needs a decoder like any other payload, which is why
// TestPayloadExemptions_AreStillEmptyStructs fails if that happens.
var emptySignalPayloads = []string{
	"CompactionStartedPayload",
	"ForgesChangedPayload",
	"HooksChangedPayload",
	"MCPConfigChangedPayload",
	"SettingsUpdatedPayload",
}

// unboundDataPayloads are payload types that DO carry data and are NOT
// registered. This is the defect above, live, in four places: each of these is
// broadcast by production code, and each has its shape hand-written in
// static-src/bus.ts with no generated decoder and no runtime validation —
// chat_status as {status?, description?}, mcp_prewarm as {package, state},
// mode_changed as {mode_id}, working_label as {label}.
//
// The list is here rather than in a comment somewhere because a known gap that
// no test names is indistinguishable from a gap nobody has noticed. Closing it
// means registering each type, adding its SSE binding, regenerating
// static-src/wire/, and moving the client to the generated decoder — a
// cross-language change that lands as one commit, which is why this one only
// pins the boundary instead of crossing it.
//
// Shrinking this list is the fix. Growing it requires a reason.
var unboundDataPayloads = []string{
	"ChatStatusPayload",
	"MCPPrewarmPayload",
	"ModeChangedPayload",
	"WorkingLabelPayload",
}

// declaredAPIPayloads parses internal/api and returns every exported type
// whose name ends in Payload, mapped to the field count of its struct
// definition.
//
// It reads the SOURCE rather than reflecting over a hand-written list of types,
// because the whole question is which types exist that the list does not
// mention. Reflection can only enumerate what something already references.
//
// Every .go file is parsed directly rather than going through a package loader,
// and that is deliberate rather than a shortcut: a loader resolves build tags
// and would drop a file that does not match the current platform, while a
// payload declared behind a build tag is on the wire wherever it does build and
// needs a registration just the same. Excluding it here would be the loader
// hiding exactly the case this test is for.
func declaredAPIPayloads(t *testing.T) map[string]int {
	t.Helper()
	dir := filepath.Join("..", "api")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read internal/api: %v", err)
	}
	fset := token.NewFileSet()
	out := map[string]int{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || !ts.Name.IsExported() || !strings.HasSuffix(ts.Name.Name, "Payload") {
					continue
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					continue
				}
				out[ts.Name.Name] = st.Fields.NumFields()
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("parsed internal/api and found no exported *Payload types; the scan is broken, not the registry")
	}
	return out
}

// registeredAPIPayloads returns the names of the api.*Payload types in the
// registry.
func registeredAPIPayloads(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, wt := range Registry().Types {
		if wt.PkgPath == apiPkgPath && strings.HasSuffix(wt.Name, "Payload") {
			out = append(out, wt.Name)
		}
	}
	return out
}

// TestRegistry_EveryDeclaredPayloadIsRegisteredOrExempt is the table walk: a
// new api.*Payload type cannot reach the SSE wire without either a
// registration or an explicit entry in one of the two exemption lists above.
func TestRegistry_EveryDeclaredPayloadIsRegisteredOrExempt(t *testing.T) {
	declared := declaredAPIPayloads(t)
	registered := registeredAPIPayloads(t)

	for name := range declared {
		switch {
		case slices.Contains(registered, name):
		case slices.Contains(emptySignalPayloads, name):
		case slices.Contains(unboundDataPayloads, name):
		default:
			t.Errorf("api.%s is declared but has no wire registration.\n"+
				"Add wiregen.TypeRef[api.%s]() to wireTypes plus its {EventType, TypeName} entry in sseEvents,\n"+
				"or — if it is an empty invalidation signal with no field to decode — add it to emptySignalPayloads with that reason.",
				name, name)
		}
	}
}

// TestRegistry_EveryRegisteredPayloadHasAnSSEBinding walks the other
// direction. A payload in wireTypes with no entry in sseEvents is a
// TypeScript type generated for an event no decoder is registered against —
// the client would fall back to using it as a bare interface, which is the
// state the registration was supposed to leave behind.
func TestRegistry_EveryRegisteredPayloadHasAnSSEBinding(t *testing.T) {
	r := Registry()
	bound := make([]string, 0, len(r.SSEEvents))
	for _, e := range r.SSEEvents {
		bound = append(bound, e.TypeName)
	}

	for _, name := range registeredAPIPayloads(t) {
		if !slices.Contains(bound, name) {
			t.Errorf("api.%s is registered in wireTypes but bound to no SSE event.\n"+
				"Add its {EventType: \"…\", TypeName: %q} entry to sseEvents, or drop the registration.",
				name, name)
		}
	}
}

// TestRegistry_EverySSEBindingNamesARegisteredType guards the typo. TypeName
// is a STRING, so a misspelling is not a compile error here; it becomes a
// generator-time lookup that finds nothing.
func TestRegistry_EverySSEBindingNamesARegisteredType(t *testing.T) {
	r := Registry()
	names := make([]string, 0, len(r.Types))
	for _, wt := range r.Types {
		names = append(names, wt.Name)
	}

	for _, e := range r.SSEEvents {
		if !slices.Contains(names, e.TypeName) {
			t.Errorf("SSE event %q binds TypeName %q, which is not a registered type (typo, or the registration was removed)",
				e.EventType, e.TypeName)
		}
	}
}

// TestRegistry_NoDuplicateSSEEventTypes pins one-decoder-per-event. Two
// entries for one event type is a silent last-wins in the generated registry.
func TestRegistry_NoDuplicateSSEEventTypes(t *testing.T) {
	seen := map[string]string{}
	for _, e := range Registry().SSEEvents {
		if prev, dup := seen[e.EventType]; dup {
			t.Errorf("SSE event %q is bound twice: %q then %q (the generated registry keeps one)",
				e.EventType, prev, e.TypeName)
		}
		seen[e.EventType] = e.TypeName
	}
}

// TestPayloadExemptions_AreStillEmptyStructs holds the empty-signal exemption
// to its stated reason. The moment one of those payloads gains a field, the
// argument for exempting it ("nothing to decode") stops being true.
func TestPayloadExemptions_AreStillEmptyStructs(t *testing.T) {
	declared := declaredAPIPayloads(t)
	for _, name := range emptySignalPayloads {
		fields, ok := declared[name]
		if !ok {
			t.Errorf("emptySignalPayloads names api.%s, which no longer exists in internal/api; drop the entry", name)
			continue
		}
		if fields != 0 {
			t.Errorf("api.%s is exempt as an empty invalidation signal but now has %d field(s).\n"+
				"It carries data, so it needs a registration and an SSE binding; remove it from emptySignalPayloads.",
				name, fields)
		}
	}
}

// TestPayloadExemptions_AreNotStale stops either exemption list from rotting
// into a place names go to be forgotten. An entry that has since been
// registered, or whose type is gone, must leave the list — otherwise the list
// stops describing the gap and starts hiding a fixed one.
func TestPayloadExemptions_AreNotStale(t *testing.T) {
	declared := declaredAPIPayloads(t)
	registered := registeredAPIPayloads(t)

	for _, list := range []struct {
		name    string
		entries []string
	}{
		{"emptySignalPayloads", emptySignalPayloads},
		{"unboundDataPayloads", unboundDataPayloads},
	} {
		for _, name := range list.entries {
			if _, ok := declared[name]; !ok {
				t.Errorf("%s names api.%s, which is not declared in internal/api any more; drop the entry",
					list.name, name)
			}
			if slices.Contains(registered, name) {
				t.Errorf("%s names api.%s, but it IS registered now; drop the entry (the exemption is spent)",
					list.name, name)
			}
		}
	}
}
