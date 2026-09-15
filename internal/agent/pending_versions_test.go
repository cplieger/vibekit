package agent

import (
	"testing"

	"github.com/cplieger/vibekit/internal/subject"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// pendingVersion reads the shared `pending` counter.
func pendingVersion(t *testing.T, v *subject.Versions) string {
	t.Helper()
	cur, _ := v.Current(subject.KindPending, "")
	return cur
}

// mustMove asserts that step moved the `pending` counter exactly once past before.
func mustMove(t *testing.T, v *subject.Versions, name string, step func()) {
	t.Helper()
	before := pendingVersion(t, v)
	step()
	after := pendingVersion(t, v)
	if after == before {
		t.Errorf("%s did not move the pending counter (still %q)", name, before)
	}
}

// mustHold asserts that step left the `pending` counter where it was.
func mustHold(t *testing.T, v *subject.Versions, name string, step func()) {
	t.Helper()
	before := pendingVersion(t, v)
	step()
	if after := pendingVersion(t, v); after != before {
		t.Errorf("%s moved the pending counter %q -> %q; it changed no item", name, before, after)
	}
}

func TestPendingPermsTracker_EveryMutationMovesTheSharedCounter(t *testing.T) {
	v := &subject.Versions{}
	tr := newPendingPermsTracker()
	tr.versions = v
	perm := func(chat vibekit.ChatID, id int64, run string) vibekit.ServerEvent {
		return vibekit.NewEvent(vibekit.EventPermissionNeeded, chat, vibekit.PermissionNeededPayload{
			RequestID: id, RunID: run,
			Options: []vibekit.PermissionOption{{OptionID: "allow"}},
		})
	}
	mustMove(t, v, "Add", func() { tr.Add(1, perm("c1", 1, "")) })
	mustMove(t, v, "Add second", func() { tr.Add(2, perm("c1", 2, "wf-1")) })
	mustMove(t, v, "Add third", func() { tr.Add(3, perm("c2", 3, "")) })
	mustMove(t, v, "TakeIfPresent", func() {
		if _, ok := tr.TakeIfPresent("c1", 1); !ok {
			t.Fatal("TakeIfPresent(c1, 1) = false, want the tracked request")
		}
	})
	mustHold(t, v, "TakeIfPresent on a missing request", func() { tr.TakeIfPresent("c1", 1) })
	mustHold(t, v, "TakePermissionOption with an off-list option", func() {
		if _, pending, offered := tr.TakePermissionOption("c1", 2, "nope"); !pending || offered {
			t.Fatalf("TakePermissionOption(off-list) = (pending %v, offered %v), want (true, false)", pending, offered)
		}
	})
	mustMove(t, v, "TakePermissionOption", func() {
		if _, _, offered := tr.TakePermissionOption("c1", 2, "allow"); !offered {
			t.Fatal("TakePermissionOption(allow) = not offered, want the claim")
		}
	})
	tr.Add(4, perm("c2", 4, "wf-2"))
	mustMove(t, v, "ClearForRun", func() { tr.ClearForRun("wf-2") })
	mustHold(t, v, "ClearForRun with nothing to drop", func() { tr.ClearForRun("wf-2") })
	mustMove(t, v, "ClearForChat", func() { tr.ClearForChat("c2") })
	mustHold(t, v, "ClearForChat with nothing to drop", func() { tr.ClearForChat("c2") })
}

func TestPendingRunAsks_EveryMutationMovesTheSharedCounter(t *testing.T) {
	v := &subject.Versions{}
	var r pendingRunAsks
	r.versions = v
	mustMove(t, v, "Add", func() { r.Add(askOf("c1", "wf-1", "a1", "n1")) })
	mustHold(t, v, "Add of a duplicate", func() { r.Add(askOf("c1", "wf-1", "a1", "n1")) })
	mustMove(t, v, "TakeIfPresent", func() {
		if _, ok := r.TakeIfPresent("wf-1", "a1"); !ok {
			t.Fatal("TakeIfPresent = false, want the ask")
		}
	})
	mustHold(t, v, "TakeIfPresent on a missing ask", func() { r.TakeIfPresent("wf-1", "a1") })
	r.Add(askOf("c1", "wf-1", "a2", "n2"))
	mustMove(t, v, "TakeNode", func() {
		if got := r.TakeNode("wf-1", "n2"); len(got) != 1 {
			t.Fatalf("TakeNode returned %d asks, want 1", len(got))
		}
	})
	mustHold(t, v, "TakeNode with nothing to claim", func() { r.TakeNode("wf-1", "n2") })
	r.Add(askOf("c1", "wf-1", "a3", "n3"))
	mustMove(t, v, "TakeRun", func() { r.TakeRun("wf-1") })
	mustHold(t, v, "TakeRun with nothing to claim", func() { r.TakeRun("wf-1") })
	r.Add(askOf("c1", "wf-2", "a4", "n4"))
	mustMove(t, v, "ClearChat", func() { r.ClearChat("c1") })
	mustHold(t, v, "ClearChat with nothing to drop", func() { r.ClearChat("c1") })
}

func TestSteerBuffer_EveryMutationMovesTheSharedCounter(t *testing.T) {
	v := &subject.Versions{}
	b := newSteerBuffer()
	b.versions = v
	mustMove(t, v, "SteerWaiting", func() { b.SteerWaiting("c1", vibekit.SteerQueuedPayload{SteerID: "s1"}) })
	mustHold(t, v, "SteerWaiting with no id", func() { b.SteerWaiting("c1", vibekit.SteerQueuedPayload{}) })
	mustMove(t, v, "SteerRead", func() { b.SteerRead("c1", "s1") })
	mustHold(t, v, "SteerForgotten with nothing held", func() { b.SteerForgotten("c1", []string{"s1"}) })
	b.SteerWaiting("c1", vibekit.SteerQueuedPayload{SteerID: "s2"})
	mustMove(t, v, "SteerForgotten", func() { b.SteerForgotten("c1", []string{"s2"}) })
	b.SteerWaiting("c1", vibekit.SteerQueuedPayload{SteerID: "s3"})
	mustMove(t, v, "ClearForChat", func() { b.ClearForChat("c1") })
	mustHold(t, v, "ClearForChat with nothing to drop", func() { b.ClearForChat("c1") })
}

// TestPendingStores_ShareOneCounter pins that the three stores bump ONE subject:
// the client learns the whole pending set from one connect frame, so a bump in any
// store must move the version that frame carries.
func TestPendingStores_ShareOneCounter(t *testing.T) {
	v := &subject.Versions{}
	tr := newPendingPermsTracker()
	tr.versions = v
	var r pendingRunAsks
	r.versions = v
	b := newSteerBuffer()
	b.versions = v
	tr.Add(1, vibekit.NewEvent(vibekit.EventPermissionNeeded, "c1", vibekit.PermissionNeededPayload{RequestID: 1}))
	r.Add(askOf("c1", "wf-1", "a1", "n1"))
	b.SteerWaiting("c1", vibekit.SteerQueuedPayload{SteerID: "s1"})
	if got := pendingVersion(t, v); got != "3" {
		t.Errorf("pending version after one mutation per store = %q, want \"3\"", got)
	}
}
