package ignore

// Round-2 mutant-killing test for package internal/ignore.
// Test-only; identifier prefixed TestGKVibekitR2_.

import "testing"

// Kills match.go:103:8 (INCREMENT_DECREMENT on the OUTER-loop `steps++`,
// one per stack pop). The round-1 u26 budget test exhausts the budget
// during the `**` PUSH phase (inner steps++ at line 121), so flipping
// the outer counter to `steps--` doesn't change its all-empty no-match
// result. To pin the outer counter we need the budget to be spent during
// the POP phase, on an input whose only match lies past the budget.
//
// Construction: pattern `**/a` against a path of n="x" segments ending
// in a single "a" (the lone segment the literal "a" can match).
//
// Step accounting (maxSegMatchSteps = 10_000, n = 6000):
//   - one initial pop of {0,0}                            ~1 step
//   - the `**` pushes n+1 frames (inner steps++)          ~6001 steps  (< budget, push phase OK)
//   - popping toward the tail match frame {1, n-1}        ~n more steps (outer steps++)
//     total to reach the match ≈ 2n ≈ 12_002 > budget.
//
// So the ORIGINAL fails open at ~step 10_001 mid-pop and returns false.
// The `steps--` mutant drives the counter DOWN during the pops, never
// trips the budget, runs the search to the tail and returns true.
// Asserting false therefore kills it; n is chosen so the push phase
// alone stays under budget while push+pop exceeds it.
func TestGKVibekitR2_SegMatchBounded_BudgetFailsOpenDuringPops(t *testing.T) {
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
