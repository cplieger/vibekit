package agent

// The durable-write class, gated by a CENSUS rather than by a list somebody has
// to remember. A write carrying a CONVERSATIONAL RECORD — a prompt, an assistant
// reply, a turn-boundary event, a plan row, a transcript — must run detached from
// the shutdown that made it necessary; every other write is abandonable.
//
// A whitelist cannot see the omission it exists for: SealTurnSegment landed while
// the class was live, in a function nobody had listed. So every store write in
// both packages is walked and must appear in exactly one list below.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// storeWritePackages are the two packages the class spans. Their SOURCE is read
// rather than imported: agent imports translate, so a test here has no other way
// to reach the far half.
var storeWritePackages = []string{"internal/agent", "internal/translate"}

// storeWriteNames is what the census counts as a write: the four chat.Store
// methods, plus the five finalize-path helpers that reach them on the caller's
// own context and so decide nothing themselves.
var storeWriteNames = []string{
	"Mutate", "AppendMessage", "UpsertTurnPlan", "UpdateMessage",
	"persistTurn", "persistDisplacedTurn", "persistTurnReply", "appendEventMessage", "persistOutcomeMarker",
}

// storeWriteFloor is a floor under the census, so a walk that stops finding
// anything fails instead of reporting a clean class.
const storeWriteFloor = 25

// durableSite is one function whose store writes carry a ruling, named by the
// function that performs them and the store calls inside it.
type durableSite struct {
	file  string
	fn    string
	calls []string
	// because is what is lost when the write is refused, or what decides the
	// context when the function is not the decider, so a failure says what the
	// reader loses rather than only which rule tripped.
	because string
}

// durableWriteSites carry a conversational record. finalizeTurn is deliberately
// absent: its whole closer dispatch runs below one seam, which is a different
// shape and has its own test below.
var durableWriteSites = []durableSite{{
	file:    "internal/agent/turn_finalize.go",
	fn:      "amendLostReason",
	calls:   []string{"UpdateMessage"},
	because: "the losing closer's own account of the stop, which nothing re-derives",
}, {
	file:    "internal/agent/bridge_coord.go",
	fn:      "SealTurnSegment",
	calls:   []string{"persistTurn"},
	because: "a compaction-displaced turn's assistant content",
}, {
	file:    "internal/agent/bridge_coord.go",
	fn:      "PersistModelSwitch",
	calls:   []string{"AppendMessage", "Mutate"},
	because: "the model_switched event, which is on no KAS wire",
}, {
	file:    "internal/agent/load_projection.go",
	fn:      "swapProjectedTranscript",
	calls:   []string{"Mutate"},
	because: "the whole reconciled transcript",
}, {
	file:    "internal/translate/compact.go",
	fn:      "handleCompactionCompleted",
	calls:   []string{"AppendMessage", "Mutate"},
	because: "the compacted event, its summary text and the watermark that pairs with it",
}, {
	file:    "internal/translate/compact.go",
	fn:      "handleCompactionFailed",
	calls:   []string{"AppendMessage"},
	because: "the compaction_failed event, which is on no KAS wire",
}, {
	file:    "internal/translate/safety.go",
	fn:      "persistSafetyBlock",
	calls:   []string{"AppendMessage"},
	because: "an infrastructure-safety refusal, which is on no KAS wire",
}, {
	file:    "internal/translate/streaming_content.go",
	fn:      "HandlePlan",
	calls:   []string{"UpsertTurnPlan"},
	because: "the turn's plan row, which the replay wire carries none of and nothing regenerates",
}}

// abandonableWriteSites are the writes ruled NOT durable, and each row says which
// reason it is: the next frame or the next load re-derives the value, or the write
// is scoped to a caller whose abandonment is the point. Naming them here is what
// makes the omission a decision on the record rather than an oversight.
var abandonableWriteSites = []durableSite{{
	file:    "internal/agent/turn_metering.go",
	fn:      "mutateUsage",
	calls:   []string{"Mutate"},
	because: "spend, re-derived from the next usage_summary",
}, {
	file:    "internal/agent/bridge_coord.go",
	fn:      "tryLoadSession",
	calls:   []string{"Mutate"},
	because: "session-chain bookkeeping — no conversational record, and the next spawn records it again",
}, {
	file:    "internal/agent/bridge_coord.go",
	fn:      "persistNewSessionMetadata",
	calls:   []string{"Mutate"},
	because: "the new session's id and facts — no conversational record, and a chat that lost them takes session/new next time",
}, {
	file:    "internal/agent/model_switch.go",
	fn:      "persistModelPick",
	calls:   []string{"Mutate"},
	because: "a model pick on the REQUEST's context, the same class as the command/* writes: a switch nobody is waiting for must not land",
}, {
	file:    "internal/translate/v3_updates.go",
	fn:      "persistUsage",
	calls:   []string{"Mutate"},
	because: "context usage, re-derived from the next frame",
}, {
	file:    "internal/translate/v3_updates.go",
	fn:      "HandleConfigOptionUpdate",
	calls:   []string{"Mutate"},
	because: "the model catalog, re-derived from the next frame",
}, {
	file:    "internal/translate/focus.go",
	fn:      "applyFocusTitle",
	calls:   []string{"Mutate"},
	because: "the chat title, re-derived",
}, {
	file:    "internal/translate/streaming_content.go",
	fn:      "HandleModeUpdate",
	calls:   []string{"Mutate"},
	because: "the session mode, re-derived",
}, {
	file:    "internal/translate/init_errors.go",
	fn:      "HandleAgentNotFound",
	calls:   []string{"Mutate"},
	because: "the fallback mode, re-derived",
}}

// inheritedWriteSites take the caller's context and must not wrap it themselves,
// because the decision is not theirs to make. Two shapes: a persist helper shared
// between the finalize path and the segment seal, where a wrap in one body would
// silently change the OTHER caller's shutdown behaviour; and a closer that already
// runs below finalizeTurn's seam.
var inheritedWriteSites = []durableSite{{
	file:    "internal/agent/bridge_coord.go",
	fn:      "persistTurn",
	calls:   []string{"AppendMessage"},
	because: "finalizeTurn's seam for a closer, SealTurnSegment's own wrap for the seal",
}, {
	file:    "internal/agent/bridge_coord.go",
	fn:      "persistDisplacedTurn",
	calls:   []string{"Mutate"},
	because: "finalizeTurn's seam, the only caller",
}, {
	file:    "internal/agent/turn_finalize.go",
	fn:      "appendEventMessage",
	calls:   []string{"AppendMessage"},
	because: "finalizeTurn's seam, and every caller sits below it",
}, {
	file:    "internal/agent/turn_finalize.go",
	fn:      "persistTurnReply",
	calls:   []string{"persistDisplacedTurn", "persistTurn"},
	because: "finalizeTurn's seam, and both callers sit below it",
}, {
	file:    "internal/agent/turn_finalize.go",
	fn:      "persistTurnContent",
	calls:   []string{"persistTurnReply"},
	because: "finalizeTurn's seam, through closeWithOutcome",
}, {
	file:    "internal/agent/turn_finalize.go",
	fn:      "recordTurnCarrier",
	calls:   []string{"appendEventMessage", "persistOutcomeMarker"},
	because: "finalizeTurn's seam, through closeWithOutcome",
}, {
	file:    "internal/agent/turn_finalize.go",
	fn:      "closeAsInterrupted",
	calls:   []string{"appendEventMessage", "persistTurnReply"},
	because: "finalizeTurn's seam, asserted above it by the seam test",
}, {
	file:    "internal/agent/turn_finalize.go",
	fn:      "closeAsDiscarded",
	calls:   []string{"appendEventMessage"},
	because: "finalizeTurn's seam, asserted above it by the seam test",
}, {
	file:    "internal/agent/turn_finalize.go",
	fn:      "persistOutcomeMarker",
	calls:   []string{"appendEventMessage"},
	because: "finalizeTurn's seam, reached only from closeWithOutcome",
}}

// TestFinalizeTurn_EveryDurableWriteRunsDetached gates the seam itself: one
// detach, below the position wait, above every closer.
//
// The two halves are one test because each is the other's failure mode. A seam
// below a persist helper leaves that helper refused at shutdown; a seam above
// awaitPosition turns a bounded shutdown wait into an unbounded one, and that
// half fails by HANGING in a behavioural test, so having it as a line comparison
// here is what makes it diagnosable.
func TestFinalizeTurn_EveryDurableWriteRunsDetached(t *testing.T) {
	const rel = "internal/agent/turn_finalize.go"
	fset, file := parseModuleFile(t, rel)
	fn := funcDeclNamed(t, file, rel, "finalizeTurn")

	var seams []token.Pos
	var closerSwitch, positionWait token.Pos
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			if isDurableReassign(node) {
				seams = append(seams, node.Pos())
			}
		case *ast.SwitchStmt:
			if node.Tag != nil && exprText(fset, node.Tag) == "tc.Closer" {
				closerSwitch = node.Pos()
			}
		case *ast.CallExpr:
			if sel, ok := node.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "awaitPosition" {
				positionWait = node.Pos()
			}
		}
		return true
	})

	if len(seams) != 1 {
		t.Fatalf("finalizeTurn holds %d `ctx = durable.Context(ctx)` seams, want exactly 1: "+
			"two of them means part of the dispatch is still attached", len(seams))
	}
	seam := seams[0]
	if !positionWait.IsValid() {
		t.Fatal("finalizeTurn no longer calls awaitPosition, so the seam has nothing to sit below")
	}
	if positionWait > seam {
		t.Errorf("awaitPosition at %s runs BELOW the seam at %s; ctx.Done() is its only timed escape, "+
			"so a wedged agent process now hangs the shutdown instead of losing one turn",
			fset.Position(positionWait), fset.Position(seam))
	}
	if !closerSwitch.IsValid() {
		t.Fatal("finalizeTurn no longer dispatches on tc.Closer; the seam covers an unknown set")
	}
	if closerSwitch < seam {
		t.Errorf("the closer switch at %s runs ABOVE the seam at %s, so every closer's writes are attached",
			fset.Position(closerSwitch), fset.Position(seam))
	}

	// Every call into a persist helper is below the seam. The helpers themselves
	// take finalizeTurn's ctx, so one above it is one refused at shutdown.
	persistHelpers := map[string]bool{
		"persistTurn":          true,
		"persistDisplacedTurn": true,
		"appendEventMessage":   true,
		"persistOutcomeMarker": true,
	}
	var seen, above []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !persistHelpers[sel.Sel.Name] {
			return true
		}
		at := sel.Sel.Name + " at " + fset.Position(call.Pos()).String()
		seen = append(seen, at)
		if call.Pos() < seam {
			above = append(above, at)
		}
		return true
	})
	if len(seen) < len(persistHelpers) {
		t.Errorf("found %d persist-helper calls in %s (%v), want at least one per helper %v; "+
			"the walk stopped looking rather than found nothing", len(seen), rel, seen, persistHelpers)
	}
	if len(above) > 0 {
		t.Errorf("these persist calls run ABOVE the seam, so a shutdown refuses them:\n  %s",
			strings.Join(above, "\n  "))
	}
}

// TestDurableWrites_EveryStoreWriteCarriesARuling is the census the site lists
// cannot be on their own. It walks every store write in both packages and fails
// when the enclosing function appears in none of the three lists — which is the
// omission class the whole gate exists for, since a whitelist only ever inspects
// functions somebody already thought of.
func TestDurableWrites_EveryStoreWriteCarriesARuling(t *testing.T) {
	ruled := map[string]string{}
	for _, list := range []struct {
		name  string
		sites []durableSite
	}{
		{"durableWriteSites", durableWriteSites},
		{"abandonableWriteSites", abandonableWriteSites},
		{"inheritedWriteSites", inheritedWriteSites},
	} {
		for _, site := range list.sites {
			key := site.file + "/" + site.fn
			if other, dup := ruled[key]; dup {
				t.Errorf("%s is listed in both %s and %s; one site carries one ruling, "+
					"and two of them means the gate asserts both a detach and an attach", key, other, list.name)
			}
			ruled[key] = list.name
		}
	}

	var writes []storeWrite
	for _, dir := range storeWritePackages {
		writes = append(writes, censusStoreWrites(t, dir)...)
	}
	if len(writes) < storeWriteFloor {
		t.Fatalf("the census found %d store writes across %v, fewer than the floor of %d: "+
			"the walk stopped finding them rather than the class shrinking",
			len(writes), storeWritePackages, storeWriteFloor)
	}

	var unruled []string
	seen := map[string]bool{}
	for _, w := range writes {
		seen[w.site] = true
		if _, ok := ruled[w.site]; !ok {
			unruled = append(unruled, w.name+" in "+w.site+" at "+w.at)
		}
	}
	if len(unruled) > 0 {
		t.Errorf("these store writes carry no ruling; rule each one durable, abandonable or inherited "+
			"before it ships, because a shutdown decides it either way:\n  %s", strings.Join(unruled, "\n  "))
	}
	for key, list := range ruled {
		if !seen[key] {
			t.Errorf("%s is listed in %s and performs no store write; drop the row rather than "+
				"leaving a ruling for code that has moved", key, list)
		}
	}
}

// TestDurableWrites_EverySiteCarryingAConversationalRecordIsDetached is the
// non-finalize half of the class. One seam cannot reach it: these handlers are
// called from the translate cascade and the Forward goroutine, not from
// finalizeTurn.
func TestDurableWrites_EverySiteCarryingAConversationalRecordIsDetached(t *testing.T) {
	for _, site := range durableWriteSites {
		t.Run(site.file+"/"+site.fn, func(t *testing.T) {
			found, attached := siteWrites(t, site)
			if missing := missingWrites(found, site.calls); len(missing) > 0 {
				t.Fatalf("%s makes none of these writes it is listed for (%v); the list describes "+
					"code that has moved", site.fn, missing)
			}
			if len(attached) > 0 {
				t.Errorf("these writes run on an attached context, so a shutdown discards %s:\n  %s",
					site.because, strings.Join(formatWrites(attached), "\n  "))
			}
		})
	}
}

// TestDurableWrites_TheAbandonableSitesAreExemptedNotForgotten holds the other
// direction. Each of these was ruled abandonable, and asserting the ruling still
// describes real code is what stops the list quietly emptying out under a rename:
// a site that vanished from here would read as a site nobody had to judge.
func TestDurableWrites_TheAbandonableSitesAreExemptedNotForgotten(t *testing.T) {
	for _, site := range abandonableWriteSites {
		t.Run(site.file+"/"+site.fn, func(t *testing.T) {
			found, attached := siteWrites(t, site)
			if missing := missingWrites(found, site.calls); len(missing) > 0 {
				t.Fatalf("%s makes none of these writes it is exempted for (%v); re-judge it rather "+
					"than leaving a stale exemption", site.fn, missing)
			}
			if len(attached) != len(found) {
				t.Errorf("%s was detached without its exemption being revisited; it writes %s, "+
					"so the detach buys nothing and moves the site out of this list silently",
					site.fn, site.because)
			}
		})
	}
}

// TestDurableWrites_TheInheritedSitesDecideNothing holds the third ruling. A
// persist helper shared between the finalize path and the segment seal must take
// the caller's context unwrapped: a detach inside persistTurn would change the
// seal's shutdown behaviour from refused to written, which is probably right and
// definitely not what anyone reviewed.
func TestDurableWrites_TheInheritedSitesDecideNothing(t *testing.T) {
	for _, site := range inheritedWriteSites {
		t.Run(site.file+"/"+site.fn, func(t *testing.T) {
			found, attached := siteWrites(t, site)
			if missing := missingWrites(found, site.calls); len(missing) > 0 {
				t.Fatalf("%s makes none of these writes it is listed for (%v); the list describes "+
					"code that has moved", site.fn, missing)
			}
			if len(attached) != len(found) {
				t.Errorf("%s wraps a context it does not own; %s decides it, and a wrap here changes "+
					"every caller at once instead of the one under review", site.fn, site.because)
			}
		})
	}
}

// storeWrite is one store write the census found, keyed by the function that
// performs it.
type storeWrite struct {
	site string
	name string
	at   string
}

// censusStoreWrites reports every store write in one package directory's
// production files, whatever function performs it.
func censusStoreWrites(t *testing.T, dir string) []storeWrite {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(moduleRoot(t), dir))
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var out []storeWrite
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		rel := dir + "/" + name
		fset, file := parseModuleFile(t, rel)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			for _, w := range writeCallsIn(fset, fn.Body) {
				out = append(out, storeWrite{site: rel + "/" + fn.Name.Name, name: w.name, at: w.at})
			}
		}
	}
	return out
}

// writeCall is one store write inside a function body.
type writeCall struct {
	name     string
	at       string
	detached bool
}

// siteWrites reports one listed site's writes and the subset running on an
// ATTACHED context.
func siteWrites(t *testing.T, site durableSite) (found, attached []writeCall) {
	t.Helper()
	fset, file := parseModuleFile(t, site.file)
	fn := funcDeclNamed(t, file, site.file, site.fn)
	found = writeCallsIn(fset, fn.Body)
	for _, w := range found {
		if !w.detached {
			attached = append(attached, w)
		}
	}
	return found, attached
}

// writeCallsIn reports every store write in body, in source order. A write is
// detached when its own context argument is durable.Context(...), or when the body
// reassigned ctx from it beforehand.
func writeCallsIn(fset *token.FileSet, body *ast.BlockStmt) []writeCall {
	want := make(map[string]bool, len(storeWriteNames))
	for _, n := range storeWriteNames {
		want[n] = true
	}
	var out []writeCall
	reassigned := token.NoPos
	ast.Inspect(body, func(n ast.Node) bool {
		if as, ok := n.(*ast.AssignStmt); ok && isDurableReassign(as) && !reassigned.IsValid() {
			reassigned = as.Pos()
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !want[sel.Sel.Name] {
			return true
		}
		out = append(out, writeCall{
			name:     sel.Sel.Name,
			at:       fset.Position(call.Pos()).String(),
			detached: isDurableCall(call.Args) || (reassigned.IsValid() && reassigned < call.Pos()),
		})
		return true
	})
	return out
}

// missingWrites reports which of want no write in found is named for.
func missingWrites(found []writeCall, want []string) []string {
	have := make(map[string]bool, len(found))
	for _, w := range found {
		have[w.name] = true
	}
	var missing []string
	for _, n := range want {
		if !have[n] {
			missing = append(missing, n)
		}
	}
	return missing
}

// formatWrites renders writes for a failure message.
func formatWrites(calls []writeCall) []string {
	out := make([]string, 0, len(calls))
	for _, w := range calls {
		out = append(out, w.name+" at "+w.at)
	}
	return out
}

// isDurableReassign reports whether stmt is `ctx = durable.Context(…)`.
func isDurableReassign(stmt *ast.AssignStmt) bool {
	if stmt.Tok != token.ASSIGN || len(stmt.Lhs) != 1 || len(stmt.Rhs) != 1 {
		return false
	}
	id, ok := stmt.Lhs[0].(*ast.Ident)
	if !ok || id.Name != "ctx" {
		return false
	}
	return isDurableCall(stmt.Rhs)
}

// isDurableCall reports whether the first expression is a durable.Context call.
func isDurableCall(args []ast.Expr) bool {
	if len(args) == 0 {
		return false
	}
	call, ok := args[0].(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "durable" && sel.Sel.Name == "Context"
}

// parseModuleFile parses one production file by its module-relative path.
func parseModuleFile(t *testing.T, rel string) (*token.FileSet, *ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join(moduleRoot(t), rel), nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}
	return fset, file
}

// funcDeclNamed returns the function or method declared as name.
func funcDeclNamed(t *testing.T, file *ast.File, rel, name string) *ast.FuncDecl {
	t.Helper()
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == name && fn.Body != nil {
			return fn
		}
	}
	t.Fatalf("%s declares no %s; the site list names code that no longer exists", rel, name)
	return nil
}

// exprText renders an expression back to source, for comparing a switch tag.
func exprText(fset *token.FileSet, expr ast.Expr) string {
	start := fset.Position(expr.Pos())
	end := fset.Position(expr.End())
	data, err := os.ReadFile(start.Filename)
	if err != nil || end.Offset > len(data) {
		return ""
	}
	return string(data[start.Offset:end.Offset])
}

// moduleRoot walks up from the test's working directory to the module root.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", dir)
		}
		dir = parent
	}
}
