package agent

// Tests for the runtime side of a run lease: what a launch records, what a terminal
// frame releases, and what survives a restart.

import (
	"encoding/json"
	"strings"
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
			`{"recipes":[{"name":"publish","source":"bundled://publish"}]}`,
		),
		methodKiroWorkflowList:   json.RawMessage(`{"runs":[]}`),
		methodKiroWorkflowNew:    json.RawMessage(`{"workflowId":"` + workflowID + `"}`),
		methodKiroWorkflowInvoke: json.RawMessage(`{}`),
	}
}

// TestLaunchRun_GrantsTheRunsEnvelope pins what a launch records, per origin. All four
// facts are on ONE record because they are read together: the recipe (the single-run rule's
// key), the origin and schedule id (attribution), the slot (an input to the deadline), and
// the unattended mark (the permission floor's authority to answer for an absent user).
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

// TestLaunchRun_ManualRunOfAScheduledRecipeYieldsToItsNextSlot: a manual Run of a recipe
// that also has a schedule must yield to that recipe's next slot. It did not — manualLaunch
// carried a zero slot, so the run took the universal ceiling and held the recipe while the
// single-run rule refused up to eleven slots underneath it. Driven through Launch because
// that is where the defect lived, with the slot derived through schedule.NextRunFrom so the
// assertion cannot drift from the derivation the runner and the REST row use.
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
	// The bound is the SCHEDULE's, not the idle window: on a 5-minute grid the next slot
	// is always inside the window, floored up to minRunBudget. BackstopAt is zero because
	// the launch is this run's first arm, so it cannot be the tightest input.
	want := runlease.NextDeadline(before, runlease.Bounds{
		SlotAt: l.SlotAt, Idle: runIdleWindow, Floor: minRunBudget,
	})
	if l.Deadline.Sub(want).Abs() > time.Second {
		t.Errorf("deadline = %v, want the one derivation's answer %v for slot %v",
			l.Deadline, want, l.SlotAt)
	}
	if budget := l.Deadline.Sub(before); budget > minRunBudget+time.Second {
		t.Errorf("the manual run got %v of budget against a 5-minute schedule; it must not hold "+
			"the recipe for the whole %v idle window and refuse the slots underneath it",
			budget.Round(time.Second), runIdleWindow)
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

// TestGrantLease_ReportsAnEnvelopeItCouldNotPersist pins the compensation for not failing a
// launch over a bookkeeping error: the run is on the wire and only THIS process bounds it,
// so a restart finds a run with no deadline, recipe or origin that the orphan sweep cannot
// recognise and nothing ends. That line is the only warning anyone gets, and a guard flipped
// here emits it on every successful launch instead, which is the same as not having it.
func TestGrantLease_ReportsAnEnvelopeItCouldNotPersist(t *testing.T) {
	const wantLine = "run lease not persisted; this run's envelope will not survive a restart"

	t.Run("a lease that could not be written is reported", func(t *testing.T) {
		logs := captureLogs(t)
		h := &Runs{leases: undurableLeaseStore(t)}

		h.grantLease(t.Context(), "wf_1", "publish", manualLaunch())

		if _, held := h.lease("wf_1"); !held {
			t.Fatal("the failed persist also dropped the in-memory lease, so this process " +
				"no longer bounds the run either")
		}
		out := logs.String()
		if !strings.Contains(out, `"msg":"`+wantLine+`"`) {
			t.Errorf("a run whose envelope was lost said nothing; want a line reading %q. Got: %s",
				wantLine, out)
		}
		if !strings.Contains(out, `"recipe":"publish"`) {
			t.Errorf("the line does not name the recipe whose run is now unbounded: %s", out)
		}
	})

	t.Run("an ordinary launch is quiet about it", func(t *testing.T) {
		logs := captureLogs(t)
		h := &Runs{}

		h.grantLease(t.Context(), "wf_1", "publish", manualLaunch())

		if out := logs.String(); strings.Contains(out, `"msg":"`+wantLine+`"`) {
			t.Errorf("a lease that persisted fine was reported as lost: %s", out)
		}
	})
}

// TestReleaseLease_ReportsAReleaseItCouldNotWrite is the release's half of the
// same durability split, and the failure runs the other way: the envelope stays on
// disk for a run that is over, so a restart resurrects a stale lease and the run
// reads as still executing to the single-run rule.
func TestReleaseLease_ReportsAReleaseItCouldNotWrite(t *testing.T) {
	const wantLine = "run lease not released on disk"

	t.Run("a release that could not be written is reported", func(t *testing.T) {
		logs := captureLogs(t)
		h := &Runs{leases: undurableLeaseStore(t)}
		h.grantLease(t.Context(), "wf_1", "publish", manualLaunch())

		h.releaseLease(t.Context(), "wf_1")

		if out := logs.String(); !strings.Contains(out, `"msg":"`+wantLine+`"`) {
			t.Errorf("a release that stayed on disk said nothing; want a line reading %q. Got: %s",
				wantLine, out)
		}
	})

	t.Run("an ordinary release is quiet about it", func(t *testing.T) {
		logs := captureLogs(t)
		h := &Runs{}
		h.grantLease(t.Context(), "wf_1", "publish", manualLaunch())

		h.releaseLease(t.Context(), "wf_1")

		if out := logs.String(); strings.Contains(out, `"msg":"`+wantLine+`"`) {
			t.Errorf("a release that worked was reported as failed: %s", out)
		}
	})
}
