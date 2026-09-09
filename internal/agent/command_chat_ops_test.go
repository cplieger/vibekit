package agent

// Tests for the archive teardown path (OnChatArchiving / OnChatArchived) and
// the delete teardown (CleanupChatState): archiving a chat with a live bridge
// tears it down (bridge closed, pending perms + supervised trust cleared,
// assistant buffer dropped) but PRESERVES checkpoints (archive is reversible), while
// a delete reaps them.

import (
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// TestCleanupChatState_ForgetsThePrimeNote: takePrimeFrom is a claim-and-delete
// handoff, so a fork's note is spent by the session it primes — but a chat closed or
// deleted before it was ever prompted spends nothing, and the entry then outlives the
// chat for the process's life.
func TestCleanupChatState_ForgetsThePrimeNote(t *testing.T) {
	rt, _, _ := newTestHub()
	rt.coord.PrimeFromChat("c2", "c1")

	rt.cleanupChatState(t.Context(), "c2", false)

	if got := rt.coord.takePrimeFrom("c2"); got != vibekit.ChatID("") {
		t.Errorf("the prime note survived the teardown as %q", got)
	}
}
