package ignore

import (
	"strings"
	"testing"
)

// --- parseIgnoreLine ---

func TestParseIgnoreLine(t *testing.T) {
	tests := []struct {
		line    string
		name    string
		pattern string
		wantOK  bool
		anchor  bool
		dirOnly bool
		negate  bool
	}{
		{"", "empty", "", false, false, false, false},
		{"   ", "whitespace only", "", false, false, false, false},
		{"# comment", "comment", "", false, false, false, false},
		{"node_modules", "basename", "node_modules", true, false, false, false},
		{"/node_modules", "anchored root", "node_modules", true, true, false, false},
		{"node_modules/", "dir-only", "node_modules", true, false, true, false},
		{"/node_modules/", "anchored dir-only", "node_modules", true, true, true, false},
		{"!.env.example", "negate basename", ".env.example", true, false, false, true},
		{"!/secret", "negate anchored", "secret", true, true, false, true},
		{"src/foo", "inner slash is anchored", "src/foo", true, true, false, false},
		{"*.log", "basename glob", "*.log", true, false, false, false},
		{"foo  \t", "trailing whitespace trimmed", "foo", true, false, false, false},
		{"build\r", "trailing CR trimmed", "build", true, false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, ok := parseIgnoreLine(tt.line)
			if ok != tt.wantOK {
				t.Fatalf("parseIgnoreLine(%q) ok = %v, want %v", tt.line, ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if r.pattern != tt.pattern || r.anchored != tt.anchor || r.dirOnly != tt.dirOnly || r.negate != tt.negate {
				t.Errorf("parseIgnoreLine(%q) = {pattern:%q anchored:%v dirOnly:%v negate:%v}, want {pattern:%q anchored:%v dirOnly:%v negate:%v}",
					tt.line, r.pattern, r.anchored, r.dirOnly, r.negate,
					tt.pattern, tt.anchor, tt.dirOnly, tt.negate)
			}
		})
	}
}

func TestParseIgnoreLine_RejectsPathologicalDoubleStars(t *testing.T) {
	// segMatch's `**` handler recurses over every suffix of the
	// remaining path, so N stars × M segments costs O(M^N). A
	// pattern with 5+ `**` against a 20-segment path pins CPU for
	// seconds per Matches call. parseIgnoreLine caps `**` count at
	// 4; patterns beyond that are rejected at load time with a
	// single warn.
	if _, ok := parseIgnoreLine("**/**/**/**/**/**/target"); ok {
		t.Error("pattern with 6 '**' must be rejected to bound backtracking")
	}
	if _, ok := parseIgnoreLine("**/a/**/b/**/c/**/d/**/e/target"); ok {
		t.Error("pattern with 5 '**' must be rejected")
	}
	// Sane patterns still accepted.
	for _, p := range []string{"**/foo", "a/**/b", "a/**/b/**/c", "**/a/**/b/**/c/**/d"} {
		if _, ok := parseIgnoreLine(p); !ok {
			t.Errorf("parseIgnoreLine(%q) should parse (under the 4-star cap)", p)
		}
	}
}

// --- matchSegments / matchAnchored / segMatchBounded ---

func TestMatchSegments(t *testing.T) {
	tests := []struct {
		pattern  string
		path     string
		name     string
		anchored bool
		want     bool
	}{
		// Anchored exact and glob
		{"node_modules", "node_modules", "anchored exact", true, true},
		{"node_modules", "src/node_modules", "anchored does not float", true, false},
		{"*.log", "build.log", "anchored single glob", true, true},
		{"*.log", "build.txt", "anchored glob miss", true, false},
		{"?.log", "a.log", "anchored ? single char", true, true},
		{"?.log", "ab.log", "anchored ? exactly one char", true, false},

		// Unanchored matches any segment
		{"node_modules", "src/node_modules", "unanchored inner", false, true},
		{"node_modules", "a/b/c/node_modules", "unanchored deep", false, true},
		{"node_modules", "node_modules", "unanchored root", false, true},
		{"node_modules", "src/node_modules_v2", "unanchored segment boundary", false, false},
		{"*.log", "logs/build.log", "unanchored glob", false, true},

		// ** spans directories
		{"**/secrets", "a/b/secrets", "double star prefix", true, true},
		{"**/secrets", "secrets", "double star zero segments", true, true},
		{"src/**/*.go", "src/pkg/a.go", "double star middle", true, true},
		{"src/**/*.go", "src/a/b/c/a.go", "double star multi-level", true, true},
		{"src/**/*.go", "other/a.go", "double star prefix miss", true, false},
		{"**", "any/thing", "double star alone matches everything", true, true},
		{"a/**", "a/b/c", "double star trailing", true, true},
		{"a/**", "a", "double star trailing zero", true, true},

		// Empty pattern
		{"", "foo", "empty pattern never matches", false, false},
		{"", "foo", "empty pattern anchored never matches", true, false},

		// Segment count mismatch without **
		// Note: "a/b" against "a/b/c" matches via descendant-match
		// semantics (standard gitignore: a directory entry covers its
		// subtree). See TestIgnoreMatcher_AnchoredDirCoversDescendants.
		{"a/b/c", "a/b", "too many pattern segs", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchSegments(strings.Split(tt.pattern, "/"), strings.Split(tt.path, "/"), tt.anchored)
			if got != tt.want {
				t.Errorf("matchSegments(%q, %q, anchored=%v) = %v, want %v",
					tt.pattern, tt.path, tt.anchored, got, tt.want)
			}
		})
	}
}

func TestMatchSegments_Basics(t *testing.T) {
	// matchSegments happy-paths plus the empty-rule guard (nil ruleSegs
	// never matches), exercised with pre-split segment slices.
	tests := []struct {
		name     string
		ruleSegs []string
		pathSegs []string
		anchored bool
		want     bool
	}{
		{"exact match", []string{"node_modules"}, []string{"node_modules"}, true, true},
		{"anchored prefix matches descendant", []string{"secrets"}, []string{"secrets", "api.key"}, true, true},
		{"double star spans dirs", []string{"**", "x"}, []string{"a", "b", "x"}, true, true},
		{"double star with non-empty prefix", []string{"a", "**", "z"}, []string{"a", "b", "c", "z"}, true, true},
		{"trailing double star matches all", []string{"a", "**"}, []string{"a", "b", "c"}, true, true},
		{"no match", []string{"foo"}, []string{"bar"}, true, false},
		{"empty rule never matches", nil, []string{"a"}, true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchSegments(tc.ruleSegs, tc.pathSegs, tc.anchored); got != tc.want {
				t.Errorf("matchSegments(%v, %v, %v) = %v, want %v",
					tc.ruleSegs, tc.pathSegs, tc.anchored, got, tc.want)
			}
		})
	}
}

// --- segMatchBounded step-budget behavior ---
//
// The bounded matcher caps backtracking at maxSegMatchSteps and fails open
// (returns false) when the budget is exhausted, so crafted "**" patterns
// against deep paths can't pin CPU. These cases pin both the short-circuits
// and the inclusive budget boundary.

func TestSegMatchBounded_TrailingDoubleStarShortCircuits(t *testing.T) {
	// A lone trailing "**" matches everything, including paths far longer
	// than the step budget — the short-circuit fires before any bounded scan.
	x := make([]string, maxSegMatchSteps+5)
	for i := range x {
		x[i] = "seg"
	}
	if !segMatchBounded([]string{"**"}, x) {
		t.Errorf("segMatchBounded([**], len=%d) = false, want true (** matches everything)", len(x))
	}
}

func TestSegMatchBounded_DoubleStarBudgetBlocksDeepMatch(t *testing.T) {
	// "**/X" against a long path whose only "X" is at the far end: the "**"
	// expansion exhausts the step budget during the push, so the matcher
	// fails open (false) before it can reach the deep match.
	x := make([]string, maxSegMatchSteps+1)
	for i := range x {
		x[i] = "Y"
	}
	x[len(x)-1] = "X" // the only matching segment, at the far end

	if segMatchBounded([]string{"**", "X"}, x) {
		t.Errorf("segMatchBounded([** X], len=%d) = true, want false (budget exhausts before match)", len(x))
	}
}

func TestSegMatchBounded_BudgetFailsOpenDuringPops(t *testing.T) {
	// The budget must also fail open during the stack-pop phase, not just
	// the push phase. Pattern "**/a" against a long run of "x" ending in the
	// lone "a": the "**" push stays under budget, but reaching the tail "a"
	// takes ~2N steps total, so the budget trips mid-pop and the matcher
	// returns false rather than scanning to the deep tail match.
	const n = 6000
	x := make([]string, n)
	for i := range x {
		x[i] = "x"
	}
	x[n-1] = "a"

	if segMatchBounded([]string{"**", "a"}, x) {
		t.Fatalf("segMatchBounded([** a], len=%d) = true, want false "+
			"(the %d-step budget must fail open before the deep tail match)", n, maxSegMatchSteps)
	}
}

func TestSegMatchBounded_DoubleStarSplitStart(t *testing.T) {
	// A literal prefix before "**" must split the path at the correct offset
	// (xi == prefix length), staying under budget and finding the trailing "X".
	const k = 4000  // literal prefix segments before the "**"
	const mm = 3000 // "Y" filler between the prefix and the trailing "X"
	p := make([]string, 0, k+2)
	for range k {
		p = append(p, "a")
	}
	p = append(p, "**", "X")
	x := make([]string, 0, k+mm+1)
	for range k {
		x = append(x, "a")
	}
	for range mm {
		x = append(x, "Y")
	}
	x = append(x, "X")
	if !segMatchBounded(p, x) {
		t.Errorf("segMatchBounded(prefix+**+X, k=%d mm=%d) = false, want true", k, mm)
	}
}

func TestSegMatchBounded_OuterBudgetBoundary(t *testing.T) {
	// The budget check is inclusive: a match whose deciding frame is popped
	// exactly when steps == maxSegMatchSteps still succeeds (the guard trips
	// only strictly above the cap). Tuned so 2*mm+4 == maxSegMatchSteps.
	mm := (maxSegMatchSteps - 4) / 2
	x := make([]string, 0, mm+1)
	for range mm {
		x = append(x, "Y")
	}
	x = append(x, "X")
	if !segMatchBounded([]string{"**", "X"}, x) {
		t.Errorf("segMatchBounded([** X], mm=%d) = false, want true (match at budget boundary)", mm)
	}
}
