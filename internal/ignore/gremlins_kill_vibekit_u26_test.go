package ignore

// Gremlins mutant-killing tests for unit vibekit-u26 (internal/ignore).
// Targets in ignore.go (doRefresh cache guards + size cap) and match.go
// (segment matcher). Helpers are prefixed gk_vibekit_u26_. The existing
// writeIgnoreSettings / writeIgnoreFile helpers (ignore_test.go) are reused.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- ignore.go:238:18 CONDITIONALS_BOUNDARY (info.Size() > maxIgnoreFileSize) ---

func Test_gk_vibekit_u26_doRefresh_exactCapFileIsParsed(t *testing.T) {
	configDir := t.TempDir()
	workDir := t.TempDir()
	ignorePath := filepath.Join(workDir, "big.gitignore")

	// Build a file of EXACTLY maxIgnoreFileSize bytes: one real rule, the rest
	// a single comment line (parseIgnoreLine drops '#' lines).
	rule := "secret\n"
	body := rule + strings.Repeat("#", int(maxIgnoreFileSize)-len(rule))
	if int64(len(body)) != int64(maxIgnoreFileSize) {
		t.Fatalf("setup: body len = %d, want %d", len(body), maxIgnoreFileSize)
	}
	if err := os.WriteFile(ignorePath, []byte(body), 0o600); err != nil {
		t.Fatalf("write ignore file: %v", err)
	}
	writeIgnoreSettings(t, configDir, []string{ignorePath})

	m := NewMatcher(configDir, workDir)
	// Original (size > cap, false at ==) parses the rule -> "secret" is ignored.
	// The boundary mutant (size >= cap) skips the file -> no rules -> false.
	if !m.Matches(context.Background(), "secret", false) {
		t.Errorf("Matches(\"secret\") with exactly-cap ignore file = false, want true")
	}
}

// --- ignore.go:196:17 CONDITIONALS_NEGATION (doRefresh: settingsErr == nil) ---

func Test_gk_vibekit_u26_doRefresh_configDeletedDoesNotPanic(t *testing.T) {
	configDir := t.TempDir()
	workDir := t.TempDir()
	ignorePath := filepath.Join(workDir, ".gitignore")
	writeIgnoreFile(t, ignorePath, "secret\n")
	writeIgnoreSettings(t, configDir, []string{ignorePath})

	m := NewMatcher(configDir, workDir)
	ctx := context.Background()
	// First refresh caches a non-zero settings mtime.
	if !m.Matches(ctx, "secret", false) {
		t.Fatalf("setup: first Matches(\"secret\") = false, want true")
	}
	// Delete config.json so os.Stat fails and settingsInfo is nil.
	if err := os.Remove(filepath.Join(configDir, "config.json")); err != nil {
		t.Fatalf("remove config.json: %v", err)
	}

	// Original short-circuits on `settingsErr == nil` (false) and never touches
	// settingsInfo. The negated mutant (settingsErr != nil) proceeds, with a
	// cached non-zero mtime, to settingsInfo.ModTime() on a nil FileInfo -> panic.
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Matches after config.json delete panicked: %v", r)
		}
	}()
	if got := m.Matches(ctx, "secret", false); got {
		t.Errorf("Matches(\"secret\") after config delete = true, want false (rules cleared)")
	}
}

// --- ignore.go:198:23 CONDITIONALS_NEGATION (doRefresh: Size() == cachedSize) ---

func Test_gk_vibekit_u26_doRefresh_sizeChangeBypassesFastPath(t *testing.T) {
	configDir := t.TempDir()
	workDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.json")

	// Two ignore files with DIFFERENT path lengths so the two config.json
	// versions differ in byte size. v1 ignores "foo", v2 ignores only "bar".
	ignoreA := filepath.Join(workDir, "ia.gitignore")
	ignoreB := filepath.Join(workDir, "ib-substantially-longer-name.gitignore")
	writeIgnoreFile(t, ignoreA, "foo\n")
	writeIgnoreFile(t, ignoreB, "bar\n")

	fixed := time.Now().Add(-time.Hour).Truncate(time.Second)

	// v1 -> lists ignoreA; pin mtime to a fixed value.
	writeIgnoreSettings(t, configDir, []string{ignoreA})
	if err := os.Chtimes(configPath, fixed, fixed); err != nil {
		t.Fatalf("chtimes v1: %v", err)
	}
	info1, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("stat v1: %v", err)
	}
	size1 := info1.Size()

	m := NewMatcher(configDir, workDir)
	ctx := context.Background()
	if !m.Matches(ctx, "foo", false) {
		t.Fatalf("setup: Matches(\"foo\") with v1 = false, want true")
	}

	// v2 -> lists ignoreB (does NOT ignore "foo"); reset mtime to the SAME fixed
	// time so ONLY Size() differs between cached and current config.json.
	writeIgnoreSettings(t, configDir, []string{ignoreB})
	info2, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("stat v2: %v", err)
	}
	if info2.Size() == size1 {
		t.Fatalf("setup: config sizes equal (%d); need different sizes", size1)
	}
	if err := os.Chtimes(configPath, fixed, fixed); err != nil {
		t.Fatalf("chtimes v2: %v", err)
	}

	// Original: mtime equal but `Size() == cachedSize` is false -> settingsChanged
	// -> re-reads v2 -> "foo" no longer ignored -> false.
	// Mutant (`Size() != cachedSize` is true) -> all four conjuncts true -> fast
	// path reuses the cached v1 file list -> "foo" still ignored -> true.
	if got := m.Matches(ctx, "foo", false); got {
		t.Errorf("Matches(\"foo\") after config size change = true, want false (must re-read v2)")
	}
}

// --- match.go:115:18 CONDITIONALS_NEGATION (segMatchBounded: len(rest) == 0) ---

func Test_gk_vibekit_u26_segMatchBounded_trailingDoubleStarShortCircuits(t *testing.T) {
	x := make([]string, maxSegMatchSteps+5)
	for i := range x {
		x[i] = "seg"
	}
	// Pattern is a lone "**" (rest empty). Original short-circuits true at
	// `len(rest) == 0`. The negated mutant falls through into the bounded for-j
	// loop, which exhausts the step budget on this long path and returns false.
	if !segMatchBounded([]string{"**"}, x) {
		t.Errorf("segMatchBounded([**], len=%d) = false, want true (** matches everything)", len(x))
	}
}

// --- match.go:121:11 INCREMENT_DECREMENT (segMatchBounded: steps++ in for-j) ---

func Test_gk_vibekit_u26_segMatchBounded_doubleStarBudgetBlocksDeepMatch(t *testing.T) {
	x := make([]string, maxSegMatchSteps+1)
	for i := range x {
		x[i] = "Y"
	}
	x[len(x)-1] = "X" // the only matching segment, at the far end

	// Pattern ["**", "X"]: the for-j loop pushes one split point per remaining
	// path segment, incrementing steps each time (line 121). On this long path
	// the original exhausts the budget DURING the push and returns false.
	// The decrement mutant drives steps negative so the budget never fires; the
	// DFS then pops smallest-xi frames first, reaches "X" at the end -> true.
	if segMatchBounded([]string{"**", "X"}, x) {
		t.Errorf("segMatchBounded([** X], len=%d) = true, want false (budget exhausts before match)", len(x))
	}
}

// --- match.go:120:21 ARITHMETIC_BASE & INVERT_NEGATIVES (for j := len(x) - xi) ---

func Test_gk_vibekit_u26_segMatchBounded_doubleStarSplitStartExact(t *testing.T) {
	const k = 4000  // literal prefix segments before the "**"
	const mm = 3000 // "Y" filler between the prefix and the trailing "X"
	p := make([]string, 0, k+2)
	for i := 0; i < k; i++ {
		p = append(p, "a")
	}
	p = append(p, "**", "X")
	x := make([]string, 0, k+mm+1)
	for i := 0; i < k; i++ {
		x = append(x, "a")
	}
	for i := 0; i < mm; i++ {
		x = append(x, "Y")
	}
	x = append(x, "X")
	// At the "**", xi == k. Original splits over j := len(x) - xi, staying well
	// under the step budget and finding "X" at the end -> true. A `+` mutation
	// (len(x)+xi) over-runs the budget during the push (len(x)+xi pushes exceed
	// maxSegMatchSteps) and a sign-flip yields a negative start that pushes no
	// split points; either way the match is missed -> false.
	if !segMatchBounded(p, x) {
		t.Errorf("segMatchBounded(prefix+**+X, k=%d mm=%d) = false, want true", k, mm)
	}
}

// --- match.go:104:12 CONDITIONALS_BOUNDARY (outer: steps > maxSegMatchSteps) ---

func Test_gk_vibekit_u26_segMatchBounded_outerBudgetBoundary(t *testing.T) {
	// Tuned so the matching frame is popped exactly when steps == maxSegMatchSteps
	// (2*mm+4 == maxSegMatchSteps). Original (steps > max, false at ==) processes
	// that frame and matches -> true; the boundary mutant (steps >= max) bails one
	// comparison earlier -> false.
	mm := (maxSegMatchSteps - 4) / 2
	x := make([]string, 0, mm+1)
	for i := 0; i < mm; i++ {
		x = append(x, "Y")
	}
	x = append(x, "X")
	if !segMatchBounded([]string{"**", "X"}, x) {
		t.Errorf("segMatchBounded([** X], mm=%d) = false, want true (match at budget boundary)", mm)
	}
}

// --- Coverage for matchSegments / matchAnchored / segMatchBounded happy paths.
// (match.go:86 boundary and match.go:122 inner-budget boundary are equivalent;
// these positive ** / prefix matches exercise that code.) ---

func Test_gk_vibekit_u26_matchSegments_basics(t *testing.T) {
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
