package checkpoint

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

func BenchmarkCountLineDelta_RepresentativePayloads(b *testing.B) {
	makeFile := func(lines int) []byte {
		parts := make([]string, lines)
		for i := range lines {
			parts[i] = fmt.Sprintf("line %d: content here for benchmarking purposes", i)
		}
		return []byte(strings.Join(parts, "\n") + "\n")
	}

	makeModified := func(original []byte, changed int) []byte {
		lines := strings.Split(strings.TrimSuffix(string(original), "\n"), "\n")
		start := len(lines) / 3
		for i := 0; i < changed && start+i < len(lines); i++ {
			lines[start+i] = fmt.Sprintf("MODIFIED line %d: different content", start+i)
		}
		return []byte(strings.Join(lines, "\n") + "\n")
	}

	cases := []struct {
		name    string
		lines   int
		changed int
	}{
		{"small_50lines_5changed", 50, 5},
		{"medium_500lines_50changed", 500, 50},
		{"large_2000lines_200changed", 2000, 200},
	}

	for _, tc := range cases {
		from := makeFile(tc.lines)
		to := makeModified(from, tc.changed)
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				countLineDelta(b.Context(), from, to)
			}
		})
	}
}

// repeatLines builds a byte buffer of n identical "x" lines.
func repeatLines(n int) []byte {
	return []byte(strings.Repeat("x\n", n))
}

// TestCountLineDelta_ProductGateUsesMultiplication pins that the
// product-cap gate multiplies the two line counts: at 5000x5000 the
// product (25,000,000) exceeds lcsCellCap, so countLineDelta short-
// circuits to "everything changed" = (m, n) rather than running LCS.
func TestCountLineDelta_ProductGateUsesMultiplication(t *testing.T) {
	const lines = 5000 // 5000*5000 = 25,000,000 > lcsCellCap (16,777,216)
	buf := repeatLines(lines)
	added, removed := countLineDelta(t.Context(), buf, buf)
	if added != lines || removed != lines {
		t.Errorf("countLineDelta(%d identical lines) = (%d,%d), want (%d,%d): the product gate must exceed the cap and return (m,n)",
			lines, added, removed, lines, lines)
	}
}

// TestCountLineDelta_ProductCapIsExclusive pins the strict ">" on the
// product gate: at 4096x4096 the product equals lcsCellCap exactly, so
// the gate must NOT trip and the LCS path runs (finding zero changes
// for identical input).
func TestCountLineDelta_ProductCapIsExclusive(t *testing.T) {
	const lines = 4096 // 4096*4096 = 16,777,216 == lcsCellCap exactly
	buf := repeatLines(lines)
	added, removed := countLineDelta(t.Context(), buf, buf)
	if added != 0 || removed != 0 {
		t.Errorf("countLineDelta(%d identical lines) = (%d,%d), want (0,0): product==cap is not > cap, so LCS runs and finds zero changes",
			lines, added, removed)
	}
}

// TestCountLineDelta_ContextCheckCadence pins that the cancellation
// check fires only every 65536 inner iterations: a 3x3 (9-iteration)
// LCS with an already-cancelled context never reaches a check, so it
// completes normally to (0,0) instead of bailing to (m,n).
func TestCountLineDelta_ContextCheckCadence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()              // already cancelled
	buf := repeatLines(3) // 3x3 = 9 iters, far below the 65536 check cadence
	added, removed := countLineDelta(ctx, buf, buf)
	if added != 0 || removed != 0 {
		t.Errorf("countLineDelta(cancelled ctx, 3 identical lines) = (%d,%d), want (0,0): the ctx check never fires for a 9-iteration loop, so LCS completes",
			added, removed)
	}
}

// --- countLineDelta / bytesToLines: moved here from manager_test.go ---
//
// These test diff.go, not the Manager. They lived in manager_test.go because
// countLineDelta's only production caller used to be Manager.Snapshot's
// line-delta stamping; with capture delegated to KAS that caller is gone and
// diff.go is all that remains of the package, so the tests move to sit beside
// what they actually exercise.
func TestCountLineDelta_EmptyInputs(t *testing.T) {
	cases := []struct {
		name                string
		from, to            string
		wantAdd, wantRemove int
	}{
		{"both empty", "", "", 0, 0},
		{"from empty", "", "a\nb\n", 2, 0},
		{"to empty", "a\nb\n", "", 0, 2},
		{"identical", "a\nb\n", "a\nb\n", 0, 0},
		{"append one line", "a\n", "a\nb\n", 1, 0},
		{"remove one line", "a\nb\n", "a\n", 0, 1},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			gotAdd, gotRemove := countLineDelta(t.Context(), []byte(tt.from), []byte(tt.to))
			if gotAdd != tt.wantAdd || gotRemove != tt.wantRemove {
				t.Errorf("countLineDelta(%q,%q) = (%d,%d), want (%d,%d)",
					tt.from, tt.to, gotAdd, gotRemove, tt.wantAdd, tt.wantRemove)
			}
		})
	}
}

func TestCountLineDelta_ReorderIsNotUnchanged(t *testing.T) {
	// The load-bearing property: swapping two lines counts as one
	// add and one remove (LCS=1), not zero. A naive multiset/bag
	// approach would return (0,0) here.
	from := []byte("a\nb\n")
	to := []byte("b\na\n")
	add, remove := countLineDelta(t.Context(), from, to)
	if add != 1 || remove != 1 {
		t.Errorf("countLineDelta(reorder) = (%d,%d), want (1,1)", add, remove)
	}
}

func TestBytesToLines(t *testing.T) {
	cases := []struct {
		name string
		want []string
		in   []byte
	}{
		{"empty", nil, []byte("")},
		{"single without newline", []string{"a"}, []byte("a")},
		{"trailing newline dropped", []string{"a", "b"}, []byte("a\nb\n")},
		{"no trailing newline", []string{"a", "b"}, []byte("a\nb")},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := bytesToLines(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("bytesToLines(%q) = %v, want %v", tt.in, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("bytesToLines(%q)[%d] = %q, want %q", tt.in, i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestCountLineDelta_DegradedFallbackForExtremelyLargeInputs pins
// the CYCLE 2 Q9 cap. LCS is O(N*M) in memory and time; without
// the cap a pathological multi-hundred-thousand-line diff would
// allocate many GB synchronously inside Manager.Diff while holding
// m.mu, starving every other chat operation. The cap trades
// precision for bounded allocation: above the product cell cap
// each side's line count is reported verbatim as added+removed
// (degraded but truthful — everything counts as changed).
func TestCountLineDelta_DegradedFallbackForExtremelyLargeInputs(t *testing.T) {
	// Pick dimensions that exceed lcsCellCap when multiplied but
	// stay cheap to allocate for the test: 5000 × 5000 = 25 M
	// cells, well over the 16 M cap. The single-dimension (big,
	// nil) cases short-circuit via the n==0/m==0 guards before
	// the product check, so they still exercise the "empty side"
	// branch for free.
	const n = 5_000
	big := bytes.Repeat([]byte("a\n"), n)
	add, remove := countLineDelta(t.Context(), big, nil)
	if add != 0 || remove != n {
		t.Errorf("countLineDelta(big, nil) = (%d, %d), want (0, %d)", add, remove, n)
	}
	add, remove = countLineDelta(t.Context(), nil, big)
	if add != n || remove != 0 {
		t.Errorf("countLineDelta(nil, big) = (%d, %d), want (%d, 0)", add, remove, n)
	}

	// Both sides over the product cap (n*m = 25 M > 16 M):
	// fallback reports everything as changed in both directions.
	a := bytes.Repeat([]byte("a\n"), n)
	b := bytes.Repeat([]byte("b\n"), n)
	add, remove = countLineDelta(t.Context(), a, b)
	if add != n || remove != n {
		t.Errorf("countLineDelta(oversized, oversized) = (%d, %d), want (%d, %d)", add, remove, n, n)
	}
}

// BenchmarkCountLineDelta measures the LCS-based diff at typical file
// sizes and a pathological case near lcsCellCap to verify the fallback
// fires correctly. countLineDelta is called from Manager.Snapshot on
// every file snapshot (5-20 times/sec during active agent turns).
func BenchmarkCountLineDelta(b *testing.B) {
	// generate builds two slices with ~50% shared lines to exercise
	// the LCS inner loop realistically.
	generate := func(n int) ([]byte, []byte) {
		var from, to bytes.Buffer
		for i := range n {
			from.WriteString("line ")
			from.WriteRune(rune('A' + i%26))
			from.WriteByte('\n')
			if i%2 == 0 {
				to.WriteString("line ")
				to.WriteRune(rune('A' + i%26))
			} else {
				to.WriteString("new ")
				to.WriteRune(rune('a' + i%26))
			}
			to.WriteByte('\n')
		}
		return from.Bytes(), to.Bytes()
	}

	for _, size := range []int{100, 500, 2000} {
		from, to := generate(size)
		b.Run(fmt.Sprintf("%d_lines", size), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				countLineDelta(b.Context(), from, to)
			}
		})
	}

	// Pathological case: both sides just over sqrt(lcsCellCap) so
	// the product exceeds the cap and the fallback fires.
	capSize := 4097 // 4097*4097 = ~16.8M > 16M cap
	from, to := generate(capSize)
	b.Run("near_cap_fallback", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			countLineDelta(b.Context(), from, to)
		}
	})
}

func TestCountLineDelta_RapidInvariants(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		from := rapid.SliceOfN(rapid.StringMatching(`[^\n]{0,40}`), 0, 50).Draw(t, "from")
		to := rapid.SliceOfN(rapid.StringMatching(`[^\n]{0,40}`), 0, 50).Draw(t, "to")

		fromBytes := []byte(strings.Join(from, "\n"))
		toBytes := []byte(strings.Join(to, "\n"))

		added, removed := countLineDelta(t.Context(), fromBytes, toBytes)

		// Invariant 1: non-negativity.
		if added < 0 || removed < 0 {
			t.Fatalf("negative delta: added=%d removed=%d", added, removed)
		}

		// Invariant 2: bounds — added <= len(toLines), removed <= len(fromLines).
		fromLines := bytesToLines(fromBytes)
		toLines := bytesToLines(toBytes)
		if added > len(toLines) {
			t.Fatalf("added=%d > len(toLines)=%d", added, len(toLines))
		}
		if removed > len(fromLines) {
			t.Fatalf("removed=%d > len(fromLines)=%d", removed, len(fromLines))
		}

		// Invariant 3: symmetry — swap(from,to) swaps (added,removed).
		added2, removed2 := countLineDelta(t.Context(), toBytes, fromBytes)
		if added2 != removed || removed2 != added {
			t.Fatalf("symmetry: delta(%q,%q)=(%d,%d) but delta(%q,%q)=(%d,%d)",
				fromBytes, toBytes, added, removed,
				toBytes, fromBytes, added2, removed2)
		}

		// Invariant 4: identity — countLineDelta(x,x) == (0,0).
		selfAdd, selfRem := countLineDelta(t.Context(), fromBytes, fromBytes)
		if selfAdd != 0 || selfRem != 0 {
			t.Fatalf("identity: delta(x,x)=(%d,%d), want (0,0)", selfAdd, selfRem)
		}
	})
}

func FuzzCountLineDelta(f *testing.F) {
	f.Add([]byte("a\nb\n"), []byte("b\nc\n"))
	f.Add([]byte(""), []byte("x\n"))
	f.Add([]byte("x\n"), []byte(""))
	f.Add([]byte("same\n"), []byte("same\n"))
	f.Fuzz(func(t *testing.T, from, to []byte) {
		added, removed := countLineDelta(t.Context(), from, to)
		if added < 0 || removed < 0 {
			t.Fatalf("negative delta: added=%d removed=%d", added, removed)
		}
		fromLines := bytesToLines(from)
		toLines := bytesToLines(to)
		if added > len(toLines) {
			t.Fatalf("added=%d > len(toLines)=%d", added, len(toLines))
		}
		if removed > len(fromLines) {
			t.Fatalf("removed=%d > len(fromLines)=%d", removed, len(fromLines))
		}
	})
}
