package testsupport

import (
	"encoding/json"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// cloneChat returns a chat that shares nothing with c, which is what both fakes'
// Get must hand out for "Mutate is the only write path" to mean anything.
//
// It round-trips through JSON on purpose, and the mechanism is the argument
// rather than a shortcut: that is EXACTLY how the real store achieves
// independence — chat.Store.Get decodes the file, so its result is new bytes
// every time — so a fake built the same way cannot disagree with it. A
// hand-written clone would have to grow a line for each of Chat's seven slice
// fields plus everything reachable through Message (blocks, tool calls, plan
// entries, the changed-files map), and the next field added to the wire type
// would silently alias again. Nobody would notice, because the failure is a test
// that keeps passing.
//
// `clone := *c` is what this replaces. It copies a struct and shares every slice
// header inside it, so a caller could edit a stored message's Content — or a
// block inside it — with no Mutate anywhere. The contract suite's
// Get_returns_an_independent_copy case is what fails when this is reverted.
//
// A marshal error is not reachable for a wire type (no channels, no funcs, no
// cycles), and a fake is the wrong place to fail a test over it, so the shallow
// copy stands in.
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
