package kascap

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"slices"
	"strconv"
	"testing"
)

var (
	runStatusEnumRe     = regexp.MustCompile(`\.enum\(\[("running","paused"(?:,"[^"]+")*)\]\)`)
	runNodeStatusEnumRe = regexp.MustCompile(`\.enum\(\[("pending","running"(?:,"[^"]+")*)\]\)`)
	enumMemberRe        = regexp.MustCompile(`"[^"]+"`)
)

func extractedStatusValues(t *testing.T, name string, re *regexp.Regexp, src string) []string {
	t.Helper()
	matches := re.FindAllStringSubmatch(src, -1)
	if len(matches) != 1 {
		t.Fatalf("%s enum: found %d literal arrays, want exactly 1", name, len(matches))
	}
	members := enumMemberRe.FindAllString(matches[0][1], -1)
	values := make([]string, 0, len(members))
	for _, member := range members {
		value, err := strconv.Unquote(member)
		if err != nil {
			t.Fatalf("%s enum member %s: %v", name, member, err)
		}
		values = append(values, value)
	}
	return values
}

func declaredStatusValues(t *testing.T, typeName string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "../vibekit/domain_run.go", nil, 0)
	if err != nil {
		t.Fatalf("parse run-status declaration: %v", err)
	}
	var values []string
	ast.Inspect(file, func(node ast.Node) bool {
		spec, ok := node.(*ast.ValueSpec)
		if !ok || len(spec.Values) != 1 {
			return true
		}
		ident, ok := spec.Type.(*ast.Ident)
		if !ok || ident.Name != typeName {
			return true
		}
		literal, ok := spec.Values[0].(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		value, unquoteErr := strconv.Unquote(literal.Value)
		if unquoteErr != nil {
			t.Fatalf("decode %s declaration %s: %v", typeName, literal.Value, unquoteErr)
		}
		values = append(values, value)
		return true
	})
	if len(values) == 0 {
		t.Fatalf("%s declaration has no string constants", typeName)
	}
	return values
}

func fixtureStatusValues(t *testing.T) (runs, nodes []string) {
	t.Helper()
	raw, err := os.ReadFile("../vibekit/testdata/run_statuses.json")
	if err != nil {
		t.Fatalf("read run-status fixture: %v", err)
	}
	var fixture struct {
		Runs []struct {
			Status string `json:"status"`
		} `json:"runs"`
		Nodes []struct {
			Status string `json:"status"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("decode run-status fixture: %v", err)
	}
	for _, row := range fixture.Runs {
		runs = append(runs, row.Status)
	}
	for _, row := range fixture.Nodes {
		nodes = append(nodes, row.Status)
	}
	return runs, nodes
}

func TestRunStatusEnumExtractor(t *testing.T) {
	const src = `a.enum(["running","paused","completed","failed","aborted","quiesced"]),b.enum(["pending","running","paused","completed","failed","aborted","skipped","blocked"])`
	if got := extractedStatusValues(t, "run", runStatusEnumRe, src); !slices.Equal(got, []string{"running", "paused", "completed", "failed", "aborted", "quiesced"}) {
		t.Errorf("run enum = %v, want the planted member", got)
	}
	if got := extractedStatusValues(t, "node", runNodeStatusEnumRe, src); !slices.Equal(got, []string{"pending", "running", "paused", "completed", "failed", "aborted", "skipped", "blocked"}) {
		t.Errorf("node enum = %v, want the planted member", got)
	}
}

// declaredExtras are the statuses vibekit declares that KAS's own enum does not,
// each with the reason it is admitted. The allowlist is what keeps this a DRIFT
// test rather than a rubber stamp: a bundle member renamed, reordered or removed
// still fails, and an undeclared extra fails too.
var declaredExtras = map[string]map[string]string{
	"RunStatus": {
		"cancelled": "cancel writes `targetStatus ?? \"aborted\"` verbatim with no enum " +
			"check, so another client of the workspace can put it on a run and " +
			"`_kiro/workflow/list` reads it back",
	},
	"RunNodeStatus": {},
}

func TestRunStatusEnumsMatchBundle(t *testing.T) {
	src := loadBundle(t)
	fixtureRuns, fixtureNodes := fixtureStatusValues(t)
	for _, check := range []struct {
		name     string
		re       *regexp.Regexp
		typeName string
		fixture  []string
	}{
		{name: "run", re: runStatusEnumRe, typeName: "RunStatus", fixture: fixtureRuns},
		{name: "node", re: runNodeStatusEnumRe, typeName: "RunNodeStatus", fixture: fixtureNodes},
	} {
		got := extractedStatusValues(t, check.name, check.re, src)
		declared := declaredStatusValues(t, check.typeName)
		extras := declaredExtras[check.typeName]
		// Every bundle member is declared, in the bundle's own order, ahead of the
		// extras — so a rename or a removal upstream still fails.
		if len(declared) < len(got) || !slices.Equal(got, declared[:min(len(got), len(declared))]) {
			t.Errorf("%s bundle enum = %v, Go declaration = %v", check.name, got, declared)
			continue
		}
		for _, extra := range declared[len(got):] {
			if _, ok := extras[extra]; !ok {
				t.Errorf("%s declares %q, which the bundle does not: add it to declaredExtras "+
					"with the reason, or drop it", check.name, extra)
			}
		}
		if !slices.Equal(got, check.fixture) {
			t.Errorf("%s bundle enum = %v, shared fixture = %v", check.name, got, check.fixture)
		}
	}
}
