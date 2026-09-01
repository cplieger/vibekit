package testsupport

import (
	"encoding/json"
	"slices"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// cloneChat returns a chat that shares nothing with c, which is what both fakes'
// Get must hand out for "Mutate is the only write path" to mean anything.
//
// Round-trips through JSON deliberately: that is exactly how the real store
// achieves independence (chat.Store.Get decodes the file, so its result is new
// bytes every time), so this cannot drift from it the way a hand-written clone
// would — Chat has seven slice fields plus everything reachable through
// Message, and a `clone := *c` shallow copy shares every slice header inside
// it, so a caller could edit a stored message's content with no Mutate
// anywhere. The contract suite's Get_returns_an_independent_copy case fails if
// this is reverted.
//
// A marshal error is unreachable for a wire type (no channels, funcs, or
// cycles), so the shallow copy is a safe fallback rather than a real path.
func cloneChat(c *vibekit.Chat) *vibekit.Chat {
	shallow := *c
	data, err := json.Marshal(c)
	if err != nil {
		return &shallow
	}
	var out vibekit.Chat
	if err := json.Unmarshal(data, &out); err != nil {
		return &shallow
	}
	return &out
}

// turnPlanRow reports the index of the plan row belonging to the turn in flight,
// which is the newest plan-bearing message at or after the last user message.
// Shared by both fakes so neither can drift from the other, and derived the same
// way (*chat.Store).UpsertTurnPlan derives it: nothing remembers a plan message
// id, so there is no state to leave stale when a turn ends.
func turnPlanRow(msgs []vibekit.Message) (int, bool) {
	// Index-only: a vibekit.Message is 256 bytes, so binding the value would copy
	// one per iteration for two field reads.
	for i := range slices.Backward(msgs) {
		if msgs[i].Role == vibekit.RoleUser {
			return 0, false // turn boundary: this turn carries no plan row yet
		}
		if len(msgs[i].Plan) > 0 {
			return i, true
		}
	}
	return 0, false
}
