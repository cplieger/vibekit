package agent

// The runtime side of a run lease. One record answers four questions: is a blocking
// row vibekit's own orphan, when must the run end, does it run unattended, and which
// schedule row gets the outcome.

import (
	"context"
	"log/slog"
	"time"

	"github.com/cplieger/vibekit/internal/runlease"
	"github.com/cplieger/vibekit/internal/schedule"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// launchOrigin is what a launch verb knows about the run it is about to start — the
// lease's whole input set beyond the recipe name. A value rather than two more
// parameters: the scheduled fields only make sense together, and a non-empty
// scheduleID standing in for "this is a schedule's" cannot express the third origin.
type launchOrigin struct {
	// slotAt is the instant this run's own next slot comes due, an INPUT to the
	// lease's deadline. Zero for a launch that cannot name one — but launch fills it
	// in for a manual run from the recipe's own enabled schedules (see manualSlot).
	slotAt     time.Time
	scheduleID string
	// chatID is the launching chat for an agent's run (runStartLaunch); empty for
	// every parentless launch. See runlease.Lease.ChatID.
	chatID string
	origin runlease.Origin
}

// manualLaunch is the Workflows tab's Run button and the retry path: attended, with
// no schedule row to attribute an outcome to. The SLOT is not set here because it
// is not knowable here — it depends on the resolved recipe, which only launch has.
func manualLaunch() launchOrigin {
	return launchOrigin{origin: runlease.OriginManual}
}

// scheduledLaunch is the scheduler's, carrying the row to attribute outcomes
// to and the slot the run may not outlive.
func scheduledLaunch(scheduleID string, slotAt time.Time) launchOrigin {
	return launchOrigin{origin: runlease.OriginScheduled, scheduleID: scheduleID, slotAt: slotAt}
}

// manualSlot is the next slot a MANUAL run of this recipe must yield to, an input to
// the ONE deadline every run gets, so a manual run yields instead of holding the
// recipe for as long as it keeps making progress.
//
// Matched on the launch SOURCE, the key a schedule row and the Run button share.
// Floored at NOW through schedule.NextRunFrom, the same call the REST view makes, so
// the bound agrees with the "next run" the user is reading off the row. Zero when
// nothing schedules this source: the idle window and the backstop bound the run alone.
func (rs *Runs) manualSlot(source string) time.Time {
	if rs.schedules == nil || source == "" {
		return time.Time{}
	}
	now := time.Now()
	var earliest time.Time
	list := rs.schedules.List()
	for i := range list {
		e := &list[i]
		if !e.Enabled || e.Source != source {
			continue
		}
		next, err := schedule.NextRunFrom(e.Spec, e.Anchor, now)
		if err != nil {
			// No slot input rather than a launch failure: the idle window and
			// the backstop still bound the run.
			slog.Warn("schedule cannot name its next slot, so a manual run of its recipe "+
				"is bounded by its idle window alone", "schedule_id", e.ID, "error", err)
			continue
		}
		if earliest.IsZero() || next.Before(earliest) {
			earliest = next
		}
	}
	return earliest
}

// leaseStore returns the lease registry, creating an in-memory one if the runtime
// was built without the durable store — which every unit test is, and a lease has
// to exist because it carries the run's wall clock. WithRunLeases adds durability.
func (rs *Runs) leaseStore() *runlease.Store {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.leases == nil {
		rs.leases = runlease.NewMemory()
	}
	return rs.leases
}

// grantLease records the envelope of a run vibekit just put on the wire.
//
// Called between `new` and `invoke`: the earliest point the workflow id exists and
// still before anything can execute, so no permission request slips through
// unmarked. UNATTENDED is set for a scheduled run only. A persist failure is logged
// rather than returned — the run is on the wire either way and the lease is kept in
// memory, so this process still bounds and attributes it.
func (rs *Runs) grantLease(ctx context.Context, workflowID, recipe string, o launchOrigin) {
	if workflowID == "" {
		return
	}
	l := runlease.Lease{
		StartedAt:  time.Now(),
		SlotAt:     o.slotAt,
		WorkflowID: workflowID,
		ChatID:     o.chatID,
		Recipe:     recipe,
		Origin:     o.origin,
		ScheduleID: o.scheduleID,
		Unattended: o.origin == runlease.OriginScheduled,
	}
	if err := rs.leaseStore().Put(ctx, &l); err != nil {
		slog.Error("run lease not persisted; this run's envelope will not survive a restart",
			"workflow_id", workflowID, "recipe", recipe, "origin", o.origin, "error", err)
	}
}

// releaseLease forgets a run's envelope. Idempotent: the terminal frame and the
// cancel path both release, and neither knows which arrived first.
func (rs *Runs) releaseLease(ctx context.Context, workflowID string) {
	if workflowID == "" {
		return
	}
	if err := rs.leaseStore().Release(ctx, workflowID); err != nil {
		slog.Warn("run lease not released on disk", "workflow_id", workflowID, "error", err)
	}
}

// lease reads a run's envelope.
func (rs *Runs) lease(workflowID string) (runlease.Lease, bool) {
	return rs.leaseStore().Get(workflowID)
}

// RunChat answers which chat's agent launched a run, satisfying command.RunOwner so
// a run tab opened with no parent still nests under its conversation. `ok` reports
// whether a lease exists at all, which differs from an empty chat id: a parentless
// run HAS a lease and no chat.
//
// Lock order Membership.mu -> Runs.mu -> the lease store's, acyclic because
// offerRunTab releases Runs.mu before it opens anything.
func (rs *Runs) RunChat(workflowID string) (vibekit.ChatID, bool) {
	l, ok := rs.lease(workflowID)
	if !ok {
		return "", false
	}
	return vibekit.ChatID(l.ChatID), true
}
