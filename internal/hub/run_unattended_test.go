package hub

import (
	"testing"
	"time"
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

// TestIsScheduledRun_TracksTheMark pins the read the run_started event's
// `scheduled` flag is derived from.
//
// The flag exists because the CLIENT cannot make this distinction: a parentless
// run's lifecycle frames are workspace-global with an empty chat id, and a manual
// launch is parentless too, so watching events cannot separate the two. Only the
// launch path knows, and this map is where it says so.
func TestIsScheduledRun_TracksTheMark(t *testing.T) {
	t.Parallel()
	h := &Hub{}

	if h.IsScheduledRun("wf_1") {
		t.Error("an unknown run reported scheduled; a manual launch must never be marked")
	}
	h.markUnattended("wf_1", "sched-1")
	if !h.IsScheduledRun("wf_1") {
		t.Error("a marked run did not report scheduled")
	}
	if h.IsScheduledRun("wf_2") {
		t.Error("the mark leaked to another run")
	}
	// A terminal run_complete clears the mark, and the flag must go with it: the
	// run is over, so a later frame naming it is not a scheduled run starting.
	h.clearUnattended("wf_1")
	if h.IsScheduledRun("wf_1") {
		t.Error("a cleared run still reported scheduled")
	}
	// An empty id is not a run. Answering true here would mark every frame that
	// arrived without one.
	if h.IsScheduledRun("") {
		t.Error("the empty workflow id reported scheduled")
	}
}
