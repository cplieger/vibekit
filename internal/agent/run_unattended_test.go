package agent

import (
	"context"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/runlease"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// TestUnattendedBudget_MatchesTheDisclaimer pins a cross-language constant.
//
// The schedule form states the budget in words ("the ask is refused after 3
// minutes"), because a user deciding whether to schedule a job needs to know what
// happens when it asks for something nobody is there to answer. There is no
// endpoint carrying the number, so the client holds its own copy —
// UNATTENDED_BUDGET_MINUTES in static-src/schedule-picker.ts — and two copies of
// one fact drift silently in exactly the direction that matters: the form would
// keep promising three minutes after the server moved to thirty.
//
// This is the whole mechanism keeping them together. Change both.
func TestUnattendedBudget_MatchesTheDisclaimer(t *testing.T) {
	t.Parallel()
	const disclaimerMinutes = 3 // UNATTENDED_BUDGET_MINUTES, schedule-picker.ts
	if unattendedApprovalBudget != disclaimerMinutes*time.Minute {
		t.Errorf("unattendedApprovalBudget = %v, but the schedule form tells the user %d minutes; "+
			"update UNATTENDED_BUDGET_MINUTES in static-src/schedule-picker.ts to match",
			unattendedApprovalBudget, disclaimerMinutes)
	}
}

// TestIsScheduledRun_ReadsTheLeasesOrigin pins the read the run_started event's
// `scheduled` flag is derived from.
//
// The flag exists because the CLIENT cannot make this distinction: a parentless
// run's lifecycle frames are workspace-global with an empty chat id, and a manual
// launch is parentless too, so watching events cannot separate the two. Only the
// launch path knows, and the lease is where it says so.
func TestIsScheduledRun_ReadsTheLeasesOrigin(t *testing.T) {
	h := &Runs{}

	if h.IsScheduled("wf_1") {
		t.Error("an unknown run reported scheduled; a manual launch must never be marked")
	}
	h.grantLease(t.Context(), "wf_manual", "publish", manualLaunch())
	if h.IsScheduled("wf_manual") {
		t.Error("a manual run reported scheduled")
	}
	h.grantLease(t.Context(), "wf_1", "publish", scheduledLaunch("sched-1", time.Time{}))
	if !h.IsScheduled("wf_1") {
		t.Error("a scheduled run did not report scheduled")
	}
	if h.IsScheduled("wf_2") {
		t.Error("the origin leaked to another run")
	}
	// A terminal run_complete releases the lease, and the flag must go with it: the
	// run is over, so a later frame naming it is not a scheduled run starting.
	h.releaseLease(t.Context(), "wf_1")
	if h.IsScheduled("wf_1") {
		t.Error("a released run still reported scheduled")
	}
	// An empty id is not a run. Answering true here would mark every frame that
	// arrived without one.
	if h.IsScheduled("") {
		t.Error("the empty workflow id reported scheduled")
	}
}

// TestUnattendedFloor_ArmsFromTheLeaseAndSurvivesARestart is the risk the durable
// lease closes, at the surface that has teeth.
//
// The mark used to be in memory, so a restart while a scheduled run was parked
// removed the deny-fast budget with no trace: the run's next permission ask at
// 03:00 waited for a human indefinitely, and under the single-run rule that parked
// the whole recipe. The floor reads the lease now, and the lease is on disk.
func TestUnattendedFloor_ArmsFromTheLeaseAndSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	store, err := runlease.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	h := &Runs{leases: store}
	h.grantLease(t.Context(), "wf_1", "nightly", scheduledLaunch("sched-1", time.Time{}))

	// The restart: a brand-new store over the same directory, and a runtime that
	// launched nothing.
	reopened, err := runlease.NewStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	after := &Runs{leases: reopened}

	l, held := after.lease("wf_1")
	if !held {
		t.Fatal("the lease did not survive, so the 03:00 ask would wait for a human forever")
	}
	if !l.Unattended {
		t.Error("the unattended mark did not survive, so the floor would not arm")
	}
	if l.ScheduleID != "sched-1" {
		t.Errorf("ScheduleID = %q; the denial could not be attributed to a row", l.ScheduleID)
	}
	if !after.IsScheduled("wf_1") {
		t.Error("the origin did not survive")
	}
}

// TestUnattendedFloor_ArmsNothingForAnUnanswerableAsk pins the third clause of the
// floor's guard, which is the one protecting the process rather than the policy.
//
// The floor answers a scheduled run's permission ask after the budget by
// REQUEST ID, so an ask that carries none cannot be answered at all — there is
// nothing to route a reply to. Arming for it would dereference an id that is not
// there, taking down the whole runtime over one malformed frame from KAS. The
// ordinary handler still runs either way: dropping the frame is the permission
// path's decision, not the floor's.
func TestUnattendedFloor_ArmsNothingForAnUnanswerableAsk(t *testing.T) {
	rs := &Runs{}
	rs.grantLease(t.Context(), "wf_1", "nightly",
		scheduledLaunch("sched-1", time.Now().Add(30*time.Second)))
	if l, held := rs.lease("wf_1"); !held || !l.Unattended {
		t.Fatal("the fixture did not produce the unattended lease the floor reaches through")
	}

	inner := 0
	noteAsk := func(context.Context, vibekit.ChatID, *vibekit.RPCResponse) { inner++ }
	wrapped := rs.permissionWithUnattendedFloor(noteAsk)

	// A permission frame with no id: nothing can answer it, and nothing may try.
	wrapped(t.Context(), runChatID("wf_1"), &vibekit.RPCResponse{
		Method: vibekit.MethodRequestPermission,
		ID:     nil,
	})

	if inner != 1 {
		t.Errorf("the ordinary permission handler ran %d times, want 1: the wrapper swallowed the "+
			"frame instead of passing it on", inner)
	}
}
