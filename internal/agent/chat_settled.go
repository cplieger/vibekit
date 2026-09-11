package agent

import (
	"github.com/cplieger/vibekit/internal/runlease"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// chatHoldsLiveRun reports whether any of `leases` belongs to a run launched by
// `chatID` — the server's half of "is everything this chat started actually over?",
// asked before an off-screen notification claims a turn's work is finished.
//
// PRESENCE over vibekit's own leases, so it costs no KAS round trip and is cheap
// enough to consult on the turn-finalize path. One lease exists per run vibekit put
// on the wire, which is the whole population that can produce the defect: a
// TUI-launched run has no lease, and an agent-launched one has one (that is how
// handleLiveRuns reports it).
//
// `Bounded()` is deliberately NOT consulted. It answers whether THIS process holds a
// deadline for the run, so it reads false for a run parked on a person and for a
// lease read back from disk — and a parked run is precisely the case a "the agent
// finished" push must not claim is over.
//
// A lease with an EMPTY ChatID is parentless (a manual or scheduled launch) and
// matches nothing, so it can never withhold another chat's notification. An empty
// `chatID` matches nothing for the same reason.
//
// Takes the slice rather than the store, so it is a pure function testable without
// one.
func chatHoldsLiveRun(leases []runlease.Lease, chatID vibekit.ChatID) bool {
	if chatID == "" {
		return false
	}
	for i := range leases {
		if leases[i].ChatID != "" && vibekit.ChatID(leases[i].ChatID) == chatID {
			return true
		}
	}
	return false
}
