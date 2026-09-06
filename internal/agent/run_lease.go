package agent

// The runtime side of a run lease: minting one at launch, releasing it when the
// run is over, and reading the facts every other run path used to keep its own
// copy of.
//
// One record answers four questions that used to live in four unrelated
// places: whether a blocking row is vibekit's own orphan (admission), when the
// run must end (the deadline), whether it runs unattended (the permission
// floor), and which schedule row to attribute an outcome to.

import (
	"context"
	"log/slog"
	"time"

	"github.com/cplieger/vibekit/internal/runlease"
	"github.com/cplieger/vibekit/internal/schedule"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// launchOrigin is what a launch verb knows about the run it is about to start —
// the lease's whole input set beyond the recipe name.
//
// A value rather than two more parameters on the shared launch body: the
// scheduled fields only make sense together, and a non-empty scheduleID
// standing in for "this launch is a schedule's" could not express the third
// origin at all.
type launchOrigin struct {
	// slotAt is the instant this run's own next slot comes due, and it is an
	// INPUT to the lease's deadline. Zero for a schedule that cannot name its
	// next slot, and zero for a manual launch of a recipe nothing schedules —
	// but not for a manual launch as such: launch fills it in from the
	// recipe's own enabled schedules, which is what stops a manual run from
	// holding a scheduled recipe until it stalls or spends its backstop (see
	// manualSlot).
	slotAt     time.Time
	scheduleID string
	// chatID is the launching chat for an agent's run (runStartLaunch);
	// empty for every parentless launch. See runlease.Lease.ChatID.
	chatID string
	origin runlease.Origin
}

// manualLaunch is the Workflows tab's Run button and the retry path: attended,
// with no schedule row to attribute an outcome to.
//
// The SLOT is not set here because it is not knowable here: it depends on the
// resolved recipe, which only launch has. A manual launch that reaches
// grantLease with a zero slot is therefore either a retry (whose recipe source
// this process never saw) or a recipe nothing schedules.
func manualLaunch() launchOrigin {
	return launchOrigin{origin: runlease.OriginManual}
}

// scheduledLaunch is the scheduler's, carrying the row to attribute outcomes
// to and the slot the run may not outlive.
func scheduledLaunch(scheduleID string, slotAt time.Time) launchOrigin {
	return launchOrigin{origin: runlease.OriginScheduled, scheduleID: scheduleID, slotAt: slotAt}
}

// manualSlot is the next slot a MANUAL run of this recipe must yield to.
//
// The slot is an input to the ONE deadline every run gets
// (runlease.NextDeadline), so a manual run finding its recipe's next slot here
// is what makes it yield instead of holding the recipe for as long as it keeps
// making progress — which, with an idle window rolled forward by every step, is
// potentially the whole backstop.
//
// Matched on the launch SOURCE, the key both affordances share: a schedule row
// and the Run button on the same Workflows row carry the same string. Two
// distinct sources that happen to share a recipe NAME are the single-run rule's
// business rather than the deadline's.
//
// Floored at NOW through schedule.NextRunFrom, the same call the REST view
// makes, so the bound agrees with the "next run" the user is reading off the
// row instead of naming a slot that has already gone. (The runner passes a
// zero floor, because it alone has to SEE a past slot to tell one it may still
// fire from one missed while the container was down.)
//
// Zero when there is no schedule store, no enabled schedule for this source, or
// no computable next slot. Such a run is bounded by the idle window and the
// backstop alone.
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

// leaseStore returns the lease registry, creating an in-memory one if the
// runtime was built without the durable store.
//
// The fallback is what a Runtime assembled without a config dir (every unit
// test) needs, because a lease carries the run's wall clock. Durability is the
// part WithRunLeases adds.
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
// Called between `new` and `invoke` for a launched run — the earliest point the
// workflow id exists and still before anything can execute, so no permission
// request can slip through unmarked. UNATTENDED is set for a scheduled run
// only: a manual run is attended by definition, and an agent-launched run is
// the agent's own, on its chat's bridge.
//
// A persist failure is logged, not returned: the run is on the wire either
// way, and the lease is kept in memory, so this process still bounds and
// attributes it.
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

// releaseLease forgets a run's envelope, for a run that is over or was never
// started. Idempotent: the terminal frame and the cancel path both release,
// and neither knows which arrived first.
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

// RunChat answers which chat's agent launched a run, satisfying
// command.RunOwner so a run tab opened with no parent still nests under its
// conversation. `ok` reports whether a lease exists at all, which differs from
// an empty chat id: a parentless run HAS a lease and no chat.
//
// Lock order: the coordinator calls this holding its own operation lock, so the
// order is Membership.mu -> Runs.mu -> the lease store's. Acyclic because
// offerRunTab releases Runs.mu before it opens anything.
func (rs *Runs) RunChat(workflowID string) (vibekit.ChatID, bool) {
	l, ok := rs.lease(workflowID)
	if !ok {
		return "", false
	}
	return vibekit.ChatID(l.ChatID), true
}
