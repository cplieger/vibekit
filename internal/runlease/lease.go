// Package runlease holds what vibekit knows about a workflow run KAS owns.
//
// KAS owns the run: it survives a vibekit restart, it has its own status, and
// vibekit cannot see inside it. What vibekit owns is the ENVELOPE around it —
// whether it may start, how long it may execute, whether it runs unattended,
// and what its ending is called. Those four questions used to be answered in
// four unrelated places, none of which knew the others existed. A lease answers
// all four from one record.
//
// One lease per run vibekit itself put on the wire. That scoping is the whole
// safety property of the restart-orphan sweep: a lease exists if and only if
// vibekit minted one, so a run launched from the TUI can never be swept, and an
// agent-launched run is excluded by its own origin.
package runlease

import "time"

// Origin says which launch produced the run, and each value carries a different
// envelope rather than being a label.
//
//   - OriginScheduled: bounded by its own next slot as well as the ceiling,
//     unattended (the permission floor applies), and sweepable.
//   - OriginManual: bounded by the ceiling, attended, sweepable.
//   - OriginAgent: bounded by the ceiling and NOTHING else. KAS parents such a
//     run on the calling chat's session, so it is chat-parented by construction
//     and belongs to the chat rehydrate's resume sweep — which is why it is
//     excluded from the orphan sweep rather than merely unlikely to match.
type Origin string

// The three launches vibekit knows about. There is deliberately no `tui` value:
// a TUI-launched run never reaches this app, so it has no lease at all, and that
// absence is what keeps the orphan sweep off it.
const (
	OriginScheduled Origin = "scheduled"
	OriginManual    Origin = "manual"
	OriginAgent     Origin = "agent"
)

// Valid reports whether an origin is one this build understands. A lease read
// back with an unknown origin is not a lease this build can reason about: it
// cannot know whether the run is sweepable or unattended.
func (o Origin) Valid() bool {
	return o == OriginScheduled || o == OriginManual || o == OriginAgent
}

// Lease is vibekit's record of one run.
//
// Field order is govet fieldalignment's, not the reader's: times first (each
// carries a *Location in its tail), then strings, then the bool.
type Lease struct {
	// StartedAt is when vibekit put the run on the wire. Display and diagnosis
	// only; nothing branches on it.
	StartedAt time.Time `json:"started_at"`
	// Deadline is the ONE instant at which this run is cancelled, and it is
	// MUTABLE: it is re-stamped on every start and cleared on every pause,
	// because the bound is on EXECUTING time. A run deliberately parked for a
	// week must not be cancelled for having been parked, so a deadline computed
	// once at admission would be the wrong semantic (that is wall time).
	//
	// ZERO means vibekit is not currently bounding the run — it is parked, or it
	// was read back from disk, where a deadline is a fact about a process that no
	// longer exists. It is the successor of "no arm in the map", and the same
	// three readers key on it: the timer callback (as its own liveness test), the
	// step cap (as its authority to act), and the re-arm.
	Deadline time.Time `json:"deadline,omitzero"`
	// SlotAt is the instant this run's own NEXT scheduled slot comes due, and it
	// is an INPUT to Deadline rather than a second deadline: a scheduled run may
	// execute up to its own repeat interval and no longer. Immutable for the
	// life of the lease, which is what lets a re-arm re-apply the same bound
	// instead of forgetting it on the first pause. Zero for every origin but
	// scheduled, and zero for a schedule that cannot name its next slot.
	SlotAt time.Time `json:"slot_at,omitzero"`
	// WorkflowID is KAS's id for the run, and the lease's key.
	WorkflowID string `json:"workflow_id"`
	// Recipe is the run's recipe NAME, which is what the single-run rule keys
	// on (KAS's run list reports the same string).
	Recipe string `json:"recipe"`
	Origin Origin `json:"origin"`
	// ScheduleID names the row that asked for the run, so an unattended denial
	// can be attributed back to it. Set if and only if Origin is scheduled.
	ScheduleID string `json:"schedule_id,omitempty"`
	// Unattended marks a run with nobody to answer a permission request, which
	// is what arms the deny-fast floor. A field rather than `Origin ==
	// scheduled` because the two are different claims: the origin says who
	// launched the run, this says whether a human is watching it.
	Unattended bool `json:"unattended"`
}

// Bounded reports whether vibekit is currently bounding the run — that is,
// whether it believes the run to be EXECUTING under a deadline it set.
func (l *Lease) Bounded() bool { return !l.Deadline.IsZero() }

// expired reports whether the lease's deadline has passed. A parked lease is
// never expired: there is no deadline to pass.
//
// UNEXPORTED, and the reason is the mechanism above it. Nothing outside this
// package asks whether a deadline has passed, because nothing needs to: the runtime
// enforces the bound with a `time.AfterFunc` armed for exactly this instant, so
// the timer FIRING is that question, and its callback then asks the sharper one
// (is the stored deadline still the one I was armed for — armDeadline's
// generation check). Substituting a wall-clock comparison there would be a
// downgrade, not a tightening: AfterFunc measures monotonic elapsed time, so a
// backwards clock adjustment would make an expired() gate refuse to cancel a run
// whose budget genuinely ran out. NewStore parks every deadline it reads back,
// so a loaded lease is never expired either, which is why the orphan sweep keys
// on KAS's own status and pause reason instead.
//
// It stays as the statement of the invariant Bounded()'s three readers rely on —
// that a parked lease is not merely unbounded but uncancellable — and
// TestLeaseBounded_IsTheSuccessorOfTheArmMap is what holds both halves together.
func (l *Lease) expired(now time.Time) bool {
	return l.Bounded() && !now.Before(l.Deadline)
}

// NextDeadline computes the ONE deadline a run gets, from the two bounds that
// used to be two independent mechanisms.
//
//	min(now+ceiling, slotAt)   — never outlive your own slot
//	max(that, now+floor)       — never hand a run an absurd budget
//
// The order is the meaning. `min` is what makes a manual run of a scheduled
// recipe yield to the schedule instead of holding it for the whole ceiling, and
// `max` is what stops a slot that fired late, or an interval edited below the
// schedule's own floor, from producing a two-minute run budget. The floor
// therefore WINS over the slot when they conflict: a bound too small to finish
// anything in is not a bound, it is a guaranteed failure on every slot.
//
// A zero slotAt means "no slot to respect", not "due at the epoch".
func NextDeadline(now time.Time, ceiling, floor time.Duration, slotAt time.Time) time.Time {
	deadline := now.Add(ceiling)
	if !slotAt.IsZero() && slotAt.Before(deadline) {
		deadline = slotAt
	}
	if minimum := now.Add(floor); deadline.Before(minimum) {
		deadline = minimum
	}
	return deadline
}
