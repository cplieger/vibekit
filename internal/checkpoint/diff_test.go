package checkpoint

import (
	"context"
	"fmt"
	"strings"
	"testing"
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
			for range b.N {
				countLineDelta(context.Background(), from, to)
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
	added, removed := countLineDelta(context.Background(), buf, buf)
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
	added, removed := countLineDelta(context.Background(), buf, buf)
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
