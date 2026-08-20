package agent

// Tests for the runtime side of a run lease: what a launch records, what a terminal
// frame releases, and what survives a restart.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/runlease"
	"github.com/cplieger/vibekit/internal/schedule"
)

// launchableRecipe is the fake-bridge reply set a launch needs: one recipe, an
// empty run list (so admission passes), and ids for new/invoke.
func launchableRecipe(br *fakeBridge, workflowID string) {
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowListRecipes: json.RawMessage(
			`{"recipes":[{"name":"publish","source":"bundled://publish"}]}`),
		methodKiroWorkflowList:   json.RawMessage(`{"runs":[]}`),
		methodKiroWorkflowNew:    json.RawMessage(`{"workflowId":"` + workflowID + `"}`),
		methodKiroWorkflowInvoke: json.RawMessage(`{}`),
	}
}

// TestLaunchRun_GrantsTheRunsEnvelope pins what a launch records, per origin.
//
// All four facts are on ONE record because they are read together and were
// previously kept apart: the recipe (the single-run rule's key), the origin and
// schedule id (attribution), the slot (an input to the deadline), and the
// unattended mark (the permission floor's authority to answer for an absent
// user).
func TestLaunchRun_GrantsTheRunsEnvelope(t *testing.T) {
	slot := time.Now().Add(30 * time.Minute)

	t.Run("a manual launch is attended and has no slot", func(t *testing.T) {
		h, _, br := newTestHub()
		launchableRecipe(br, "wf_manual")

		if _, _, err := h.runs.Launch(t.Context(), "bundled://publish", nil); err != nil {
			t.Fatalf("Launch: %v", err)
		}
		l, ok := h.runs.lease("wf_manual")
		if !ok {
			t.Fatal("the manual launch granted no lease, so nothing bounds or explains the run")
		}
		if l.Origin != runlease.OriginManual {
			t.Errorf("origin = %q, want manual", l.Origin)
		}
		if l.Recipe != "publish" {
			t.Errorf("recipe = %q, want publish (the single-run rule's key)", l.Recipe)
		}
		if l.Unattended {
			t.Error("a manual run was marked unattended; the user clicked Run and can answer")
		}
		if !l.SlotAt.IsZero() {
			t.Errorf("SlotAt = %v; a manual launch has no interval to be bounded by", l.SlotAt)
		}
		if l.ScheduleID != "" {
			t.Errorf("ScheduleID = %q on a manual run", l.ScheduleID)
		}
		if l.StartedAt.IsZero() {
			t.Error("StartedAt is zero")
		}
	})

	t.Run("a scheduled launch carries its row, its slot and the floor's mark", func(t *testing.T) {
		h, _, br := newTestHub()
		launchableRecipe(br, "wf_sched")

		if _, _, err := h.runs.LaunchScheduled(t.Context(), "bundled://publish", "sched-1", slot); err != nil {
			t.Fatalf("LaunchScheduled: %v", err)
		}
		l, ok := h.runs.lease("wf_sched")
		if !ok {
			t.Fatal("the scheduled launch granted no lease")
		}
		if l.Origin != runlease.OriginScheduled {
			t.Errorf("origin = %q, want scheduled", l.Origin)
		}
		if l.ScheduleID != "sched-1" {
			t.Errorf("ScheduleID = %q, want sched-1; an unattended denial could not be attributed", l.ScheduleID)
		}
		if !l.Unattended {
			t.Error("a scheduled run was not marked unattended; a 03:00 permission ask would park forever")
		}
		if !l.SlotAt.Equal(slot) {
			t.Errorf("SlotAt = %v, want %v", l.SlotAt, slot)
		}
	})
}

// TestLaunchRun_ManualRunOfAScheduledRecipeYieldsToItsNextSlot is the manual-run
// bug, end to end, against a real schedule entry.
//
// The ratified design says a manual Run of a recipe that ALSO has a schedule must
// yield to that recipe's next slot. It did not: manualLaunch carried a zero slot and
// nothing consulted the schedule store, so the run took the universal ceiling as its
// whole bound — it held the recipe for up to an hour while the single-run rule
// refused every slot underneath it, up to ELEVEN at the 5-minute schedule floor.
//
// Driven through Launch rather than through the arm, because that is where the
// defect lived: the test this replaces asserted the arithmetic with a
// scheduledLaunch substituted into every nonzero-slot case, so it passed while the
// manual path resolved no slot at all.
//
// The slot is derived through schedule.NextRunFrom here too, from the entry's own
// spec and anchor, so the assertion cannot drift from the one derivation the runner
// and the REST row use.
func TestLaunchRun_ManualRunOfAScheduledRecipeYieldsToItsNextSlot(t *testing.T) {
	h, _, br := newTestHub()
	launchableRecipe(br, "wf_manual")

	st, err := schedule.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("schedule.NewStore: %v", err)
	}
	// Every 5 minutes, the schedule floor: the tightest repeat the form accepts and
	// the interval the manual run used to refuse eleven of.
	spec := schedule.Spec{Freq: schedule.FreqMinutely, Interval: 5}
	anchor := time.Now().Add(-time.Hour)
	entry := schedule.Entry{
		ID: "sched-1", Source: "bundled://publish", Enabled: true, Spec: spec, Anchor: anchor,
	}
	if pErr := st.Put(t.Context(), &entry); pErr != nil {
		t.Fatalf("Put schedule: %v", pErr)
	}
	h.runs.schedules = st

	before := time.Now()
	if _, _, lErr := h.runs.Launch(t.Context(), "bundled://publish", nil); lErr != nil {
		t.Fatalf("Launch: %v", lErr)
	}
	wantSlot, err := schedule.NextRunFrom(spec, anchor, before)
	if err != nil {
		t.Fatalf("NextRunFrom: %v", err)
	}

	l, ok := h.runs.lease("wf_manual")
	if !ok {
		t.Fatal("the manual launch granted no lease")
	}
	if l.SlotAt.IsZero() {
		t.Fatal("the manual run carried NO slot, so it is bounded by the hour-long ceiling and " +
			"will refuse every scheduled slot underneath it — the bug this change exists to close")
	}
	// Within a tick of the derivation: the launch reads its own clock, and a 5-minute
	// grid means anything further out is a different slot entirely.
	if l.SlotAt.Sub(wantSlot).Abs() > time.Minute {
		t.Errorf("SlotAt = %v, want the schedule's own next slot %v", l.SlotAt, wantSlot)
	}
	// The bound is the SCHEDULE's, not the hour: on a 5-minute grid the next slot is
	// always inside the ceiling, so the deadline is the slot — floored up to
	// minRunBudget when the slot is nearer than that, since a bound too small to
	// finish inside is no bound (runlease.NextDeadline, tested there).
	want := runlease.NextDeadline(before, runCeiling, minRunBudget, l.SlotAt)
	if l.Deadline.Sub(want).Abs() > time.Second {
		t.Errorf("deadline = %v, want the one derivation's answer %v for slot %v",
			l.Deadline, want, l.SlotAt)
	}
	if budget := l.Deadline.Sub(before); budget > minRunBudget+time.Second {
		t.Errorf("the manual run got %v of budget against a 5-minute schedule; it must not hold "+
			"the recipe for the whole %v ceiling and refuse the slots underneath it",
			budget.Round(time.Second), runCeiling)
	}
	// Still a MANUAL run in every other respect: the slot bounds it, and nothing else
	// about it changed.
	if l.Origin != runlease.OriginManual {
		t.Errorf("origin = %q, want manual", l.Origin)
	}
	if l.Unattended {
		t.Error("the manual run was marked unattended; the user clicked Run and can answer, and " +
			"the deny-fast permission floor must not reach it")
	}
	if l.ScheduleID != "" {
		t.Errorf("ScheduleID = %q; no row asked for this run, so no row may be blamed for its "+
			"outcome", l.ScheduleID)
	}
}

// TestLaunchRun_ManualSlotIgnoresWhatCannotBindThisRun pins the negative half, so
// the slot lookup cannot quietly bound a run by somebody else's schedule.
func TestLaunchRun_ManualSlotIgnoresWhatCannotBindThisRun(t *testing.T) {
	for name, entry := range map[string]schedule.Entry{
		"a DISABLED schedule for this very recipe": {
			ID: "s1", Source: "bundled://publish", Enabled: false,
			Spec: schedule.Spec{Freq: schedule.FreqMinutely, Interval: 5},
		},
		"an enabled schedule for another recipe": {
			ID: "s2", Source: "bundled://other", Enabled: true,
			Spec: schedule.Spec{Freq: schedule.FreqMinutely, Interval: 5},
		},
	} {
		t.Run(name, func(t *testing.T) {
			h, _, br := newTestHub()
			launchableRecipe(br, "wf_manual")
			st, err := schedule.NewStore(t.TempDir())
			if err != nil {
				t.Fatalf("schedule.NewStore: %v", err)
			}
			e := entry
			if pErr := st.Put(t.Context(), &e); pErr != nil {
				t.Fatalf("Put schedule: %v", pErr)
			}
			h.runs.schedules = st

			if _, _, lErr := h.runs.Launch(t.Context(), "bundled://publish", nil); lErr != nil {
				t.Fatalf("Launch: %v", lErr)
			}
			l, _ := h.runs.lease("wf_manual")
			if !l.SlotAt.IsZero() {
				t.Errorf("SlotAt = %v; this schedule cannot bind this run, so the ceiling is the "+
					"whole bound", l.SlotAt)
			}
		})
	}

	t.Run("no schedule store at all", func(t *testing.T) {
		h, _, br := newTestHub()
		launchableRecipe(br, "wf_manual")
		if _, _, err := h.runs.Launch(t.Context(), "bundled://publish", nil); err != nil {
			t.Fatalf("Launch: %v", err)
		}
		if l, _ := h.runs.lease("wf_manual"); !l.SlotAt.IsZero() {
			t.Errorf("SlotAt = %v with scheduling switched off", l.SlotAt)
		}
	})
}

// TestLaunchRun_ReleasesTheLeaseWhenInvokeFails: the run was created but never
// started, so nothing is executing. A lease left behind would make the recipe
// look busy to the admission backstop and hand a deadline to a run that has none.
func TestLaunchRun_ReleasesTheLeaseWhenInvokeFails(t *testing.T) {
	h, _, br := newTestHub()
	launchableRecipe(br, "wf_1")
	delete(br.callResults, methodKiroWorkflowInvoke)
	br.callErrs = map[string]error{methodKiroWorkflowInvoke: errRecipeBusy}

	if _, _, err := h.runs.Launch(t.Context(), "bundled://publish", nil); err == nil {
		t.Fatal("a failed invoke reported success")
	}
	if _, ok := h.runs.lease("wf_1"); ok {
		t.Error("the lease of a run that never started survived its failed launch")
	}
}

// TestObserveRunComplete_ReleasesTheLeaseOfATerminalRun pins where the lease's
// life ends, and why it is here rather than beside the bridge teardown: this is
// the one site every origin reaches. An agent-parented run has no bridge of its
// own to close, and its lease must be released all the same.
func TestObserveRunComplete_ReleasesTheLeaseOfATerminalRun(t *testing.T) {
	for name, tc := range map[string]struct {
		status    string
		stillHeld bool
	}{
		"completed": {"completed", false},
		"failed":    {"failed", false},
		"aborted":   {"aborted", false},
		// A policy pause reports through the same frame, and that run is still
		// this process's to resume — so its envelope must survive.
		"a policy pause": {"paused", true},
	} {
		t.Run(name, func(t *testing.T) {
			h, _, _ := newTestHub()
			const id = "wf_1"
			h.runs.grantLease(t.Context(), id, "publish", manualLaunch())

			h.runs.observeComplete(t.Context(), "", runNotif(methodWFRunComplete, map[string]any{
				"workflowId": id, "status": tc.status,
			}))

			_, held := h.runs.lease(id)
			if held != tc.stillHeld {
				t.Errorf("lease held = %v after status %q, want %v", held, tc.status, tc.stillHeld)
			}
		})
	}
}

// TestLeaseStore_FallsBackToMemory pins the accommodation every unit test relies
// on: a Runtime built without the durable store still has a lease registry, because a
// lease carries the run's wall clock. There is no "leases off" mode.
func TestLeaseStore_FallsBackToMemory(t *testing.T) {
	t.Parallel()
	h := &Runs{}
	h.grantLease(t.Context(), "wf_1", "publish", manualLaunch())
	if _, ok := h.lease("wf_1"); !ok {
		t.Fatal("a runtime with no durable store lost the lease, so the run would be unbounded")
	}
}
