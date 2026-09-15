package agent

import (
	"encoding/json"
	"log/slog"

	"github.com/cplieger/vibekit/internal/subject"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// mintPending bumps the workspace-wide `pending` counter for one of the three
// pending stores (permissions, run asks, steers). Called with the store's own mutex
// held, so the bump and the mutation it certifies are one critical section; a nil
// registry is replaced by a private one so an unwired store stays honest.
func mintPending(versions **subject.Versions) {
	if *versions == nil {
		*versions = &subject.Versions{}
	}
	(*versions).BumpCounter(subject.KindPending, "")
}

// pendingSnapshotStamped is the pending_snapshot payload with its `pending` stamp,
// for the v3 connect hook: every unresolved permission, run ask and steer across
// every chat, each as the complete envelope the live path would have published.
//
// THE COUNTER IS READ FIRST, then the three stores in sequence under their own
// locks. There is no single critical section over the three, and none is claimed:
// a mutation landing between the counter read and a store read puts its item in
// the set and its bump OUTSIDE the stamp, so the client holds a set at least as
// new as its version and the next digest answers changed. Reading the counter
// last would allow a stamp newer than the set and a false unchanged across a
// connection loss. The live frame such a mutation publishes has an offset above
// the connection's hello head and is applied after this snapshot, so the client
// converges on it either way.
func (rt *Runtime) pendingSnapshotStamped() (vibekit.PendingSnapshotPayload, *vibekit.SubjectStamp) {
	return rt.pendingSnapshot(nil)
}

// pendingSnapshot is pendingSnapshotStamped with a seam between its four reads:
// afterRead, when non-nil, runs after read n (0 the counter, then the three
// stores), which is how the interleaving property drives a mutation into every
// gap of the real procedure rather than a copy of it.
func (rt *Runtime) pendingSnapshot(afterRead func(n int)) (vibekit.PendingSnapshotPayload, *vibekit.SubjectStamp) {
	step := func(n int) {
		if afterRead != nil {
			afterRead(n)
		}
	}
	version, _ := rt.versions.Current(subject.KindPending, "")
	step(0)
	events := rt.bus.pendingPerms.List("")
	step(1)
	events = append(events, rt.runs.asks.List("")...)
	step(2)
	events = append(events, rt.bus.steers.List("")...)
	step(3)
	items := make([]json.RawMessage, 0, len(events))
	for i := range events {
		data, err := json.Marshal(events[i])
		if err != nil {
			slog.Error("pending snapshot: marshal item", "type", events[i].Type, "error", err)
			continue
		}
		items = append(items, data)
	}
	return vibekit.PendingSnapshotPayload{Items: items}, vibekit.NewSubjectStamp(string(subject.KindPending), "", version)
}
