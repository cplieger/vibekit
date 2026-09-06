// Package runlease holds what vibekit knows about a workflow run KAS owns.
//
// KAS owns the run; vibekit owns the ENVELOPE — whether it may start, how long it
// may execute, whether it runs unattended, and what its ending is called.
//
// One lease per run vibekit itself put on the wire. That scoping is the safety
// property of the restart-orphan sweep: a lease exists if and only if vibekit
// minted one, so a TUI-launched run can never be swept.
package runlease

import "time"

// Origin says which launch produced the run, and each value carries a different
// envelope: scheduled is bounded by its own next slot as well as the ceiling,
// unattended, and sweepable; manual is bounded by the ceiling, attended, and
// sweepable; agent is bounded by the ceiling alone, chat-parented by construction,
// and excluded from the orphan sweep's CANCEL arm.
type Origin string

// The three launches vibekit knows about. There is deliberately no `tui` value: a
// TUI-launched run has no lease at all, and that absence keeps the sweep off it.
const (
	OriginScheduled Origin = "scheduled"
	OriginManual    Origin = "manual"
	OriginAgent     Origin = "agent"
)

// Valid reports whether an origin is one this build understands. An unknown origin
// cannot be reasoned about: sweepable and unattended are both unanswerable.
func (o Origin) Valid() bool {
	return o == OriginScheduled || o == OriginManual || o == OriginAgent
}

// Lease is vibekit's record of one run. Field order is govet fieldalignment's:
// times first (each carries a *Location in its tail), then strings, then the bool.
type Lease struct {
	// StartedAt is when vibekit put the run on the wire; diagnosis only.
	StartedAt time.Time `json:"started_at"`
	// Deadline is the ONE instant at which this run is cancelled, MUTABLE: re-stamped
	// on every start, cleared on every pause, and rolled forward by observable
	// progress, so the primary bound is on time spent making none. ZERO means vibekit
	// is not bounding the run — parked, or read back from disk.
	Deadline time.Time `json:"deadline,omitzero"`
	// SlotAt is when this run's own next scheduled slot comes due — an INPUT to
	// Deadline, immutable, so a re-arm re-applies it. Zero for every other origin.
	SlotAt time.Time `json:"slot_at,omitzero"`
	// WorkflowID is KAS's id for the run, and the lease's key.
	WorkflowID string `json:"workflow_id"`
	// ChatID is the chat whose agent launched the run. Empty means "no chat to
	// exempt": the parentless launch verbs mint with no chat by design.
	ChatID string `json:"chat_id,omitempty"`
	// Recipe is the run's recipe NAME, which is what the single-run rule keys on.
	Recipe string `json:"recipe"`
	Origin Origin `json:"origin"`
	// ScheduleID names the row that asked for the run. Set iff Origin is scheduled.
	ScheduleID string `json:"schedule_id,omitempty"`
	// Unattended marks a run with nobody to answer a permission request, which arms
	// the deny-fast floor. Distinct from the origin: who launched it vs who watches.
	Unattended bool `json:"unattended"`
	// TabOffered records that the run's tab has been offered, exactly once for the
	// life of the run. DURABLE: run_start re-fires on resume and a restart re-reads.
	TabOffered bool `json:"tab_offered,omitempty"`
}

// Bounded reports whether vibekit believes the run to be EXECUTING under a deadline it set.
func (l *Lease) Bounded() bool { return !l.Deadline.IsZero() }

// expired reports whether the lease's deadline has passed; a parked lease is never
// expired. Unexported because a `time.AfterFunc` armed for this instant IS the
// question — and AfterFunc measures monotonic time, where a wall-clock gate would
// let a backwards clock adjustment refuse to cancel a run whose budget ran out.
func (l *Lease) expired(now time.Time) bool {
	return l.Bounded() && !now.Before(l.Deadline)
}

// Bounds is the input set NextDeadline composes into one deadline. A STRUCT rather
// than positional durations: two adjacent same-typed durations are an undetectable
// transposition. Field order is govet fieldalignment's.
type Bounds struct {
	// SlotAt is when the run's next slot comes due. Zero means "no slot to respect".
	SlotAt time.Time
	// BackstopAt is when the run's absolute EXECUTING-time budget is spent. Zero means
	// this input does not bound the run — an INSTANT rather than a remaining duration,
	// because an omitted duration would read as "already spent" and cancel every run.
	BackstopAt time.Time
	// Idle is how long a run may execute without observable progress; a refill rolls it
	// forward, which makes the primary bound a STALL bound, not a total-duration one.
	Idle time.Duration
	// Floor is the smallest budget any run may be handed. It outranks the idle window
	// and the slot, and does NOT outrank a BackstopAt tighter than the composed value:
	// the floor says how much budget a run should get, the backstop how much is left.
	Floor time.Duration
}

// NextDeadline computes the ONE deadline a run gets: one timer can be armed for
// one instant, so the three bounds compose rather than being enforced separately.
//
//	min(now+Idle, SlotAt?) -> max(that, now+Floor) -> min(that, BackstopAt?)
//
// THE ORDER IS THE PRECEDENCE. Clamping the backstop LAST keeps it terminal: let
// the floor answer instead and each progress frame hands out a fresh floor,
// degrading the absolute bound into the unbounded rolling window it exists to stop.
func NextDeadline(now time.Time, b Bounds) time.Time {
	deadline := now.Add(b.Idle)
	if !b.SlotAt.IsZero() && b.SlotAt.Before(deadline) {
		deadline = b.SlotAt
	}
	if minimum := now.Add(b.Floor); deadline.Before(minimum) {
		deadline = minimum
	}
	if !b.BackstopAt.IsZero() && b.BackstopAt.Before(deadline) {
		deadline = b.BackstopAt
	}
	return deadline
}
