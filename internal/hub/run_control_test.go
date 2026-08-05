package hub

import (
	"slices"
	"testing"
)

// TestRunVerbGates pins which statuses each run-control verb is legal from.
//
// The table is not arbitrary: it mirrors what KAS itself accepts, read off the
// handler bodies. `_kiro/workflow/retry` throws for a running run and throws
// again for any non-terminal status, naming completed/failed/aborted. `pause`
// sets a flag on a live run and means nothing once one has stopped. `resume`
// re-drives a paused one.
//
// Cancel is deliberately UNRESTRICTED, and that asymmetry is the part worth
// pinning. Cancel doubles as the tab-close gesture, so it must never be the verb
// that fails; KAS is idempotent on an already-terminal run, answering ok with the
// previous status. Gating it would turn closing a tab whose run just finished
// into an error toast.
func TestRunVerbGates(t *testing.T) {
	// KAS's WorkflowStatusSchema, exhaustively.
	all := []string{"running", "paused", "completed", "failed", "aborted"}

	cases := map[string]struct {
		verb  runVerb
		legal []string
	}{
		"pause is live-only":    {runVerbPause, []string{"running"}},
		"resume is paused-only": {runVerbResume, []string{"paused"}},
		// Not "completed", even though it is terminal. KAS's retry admits all
		// three terminal states in its outer check and then, on the no-nodeId
		// branch vibekit uses, throws unless the status is failed or aborted. An
		// earlier revision listed all three here and this test asserted that
		// wrong matrix, which is the failure mode a matrix test exists to prevent.
		"retry is failed-or-aborted only": {runVerbRetry, []string{"failed", "aborted"}},
		"cancel is unrestricted":          {runVerbCancel, all},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			for _, status := range all {
				want := slices.Contains(tc.legal, status)
				// An empty `from` means unrestricted, which the handler treats as
				// "skip the pre-check entirely".
				got := len(tc.verb.from) == 0 || slices.Contains(tc.verb.from, status)
				if got != want {
					t.Errorf("%s from %q: got legal=%v, want %v", tc.verb.name, status, got, want)
				}
			}
		})
	}
}

// TestRunVerbsAreWired guards the two halves that can silently drift apart: a
// verb with no issuer would 200 without doing anything, and a verb with no name
// would log and error as the empty string.
func TestRunVerbsAreWired(t *testing.T) {
	for _, verb := range []runVerb{runVerbCancel, runVerbPause, runVerbResume, runVerbRetry} {
		if verb.name == "" {
			t.Error("a run verb has no name; its 409 and its log line would both be blank")
		}
		if verb.issue == nil {
			t.Errorf("run verb %q has no issuer: the route would answer ok without calling KAS", verb.name)
		}
	}
}
