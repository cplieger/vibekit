package hub

import (
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/runlease"
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
	h := &runPlane{}

	if h.IsScheduledRun("wf_1") {
		t.Error("an unknown run reported scheduled; a manual launch must never be marked")
	}
	h.grantLease(t.Context(), "wf_manual", "publish", manualLaunch())
	if h.IsScheduledRun("wf_manual") {
		t.Error("a manual run reported scheduled")
	}
	h.grantLease(t.Context(), "wf_1", "publish", scheduledLaunch("sched-1", time.Time{}))
	if !h.IsScheduledRun("wf_1") {
		t.Error("a scheduled run did not report scheduled")
	}
	if h.IsScheduledRun("wf_2") {
		t.Error("the origin leaked to another run")
	}
	// A terminal run_complete releases the lease, and the flag must go with it: the
	// run is over, so a later frame naming it is not a scheduled run starting.
	h.releaseLease(t.Context(), "wf_1")
	if h.IsScheduledRun("wf_1") {
		t.Error("a released run still reported scheduled")
	}
	// An empty id is not a run. Answering true here would mark every frame that
	// arrived without one.
	if h.IsScheduledRun("") {
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
	h := &runPlane{leases: store}
	h.grantLease(t.Context(), "wf_1", "nightly", scheduledLaunch("sched-1", time.Time{}))

	// The restart: a brand-new store over the same directory, and a hub that
	// launched nothing.
	reopened, err := runlease.NewStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	after := &runPlane{leases: reopened}

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
	if !after.IsScheduledRun("wf_1") {
		t.Error("the origin did not survive")
	}
}
