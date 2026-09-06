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
// agent-launched run is excluded from the sweep's cancel arm by its own origin.
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
//     and belongs to the chat rehydrate's resume sweep — which is why the orphan
//     sweep's CANCEL arm excludes it rather than merely being unlikely to match.
//     Its lease is still released like any other once the run is over.
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
	// MUTABLE in two ways: it is re-stamped on every start, cleared on every
	// pause, and rolled FORWARD by the run's own observable progress, because the
	// primary bound is on time spent making none. A run deliberately parked for a
	// week must not be cancelled for having been parked, and a run producing
	// steps for nine hours must not be cancelled for being long — so a deadline
	// computed once at admission would be the wrong semantic twice over (that is
	// wall time, and it cannot be rolled forward).
	//
	// The instant it holds is therefore whichever of three inputs is tighter (see
	// Bounds): the idle window from the last progress, the run's own next slot,
	// and the absolute executing-time backstop that stops a runaway loop from
	// refreshing the window forever.
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
	// ChatID is the chat whose agent launched the run, stamped where the origin
	// is observed (the run_start frame's carrier). Empty means "no chat to
	// exempt": the parentless launch verbs (manual, scheduled) mint with no
	// chat BY DESIGN, and a lease written before this field existed decodes
	// the same way. ADDITIVE at Version 1 deliberately — a version bump would
	// make NewStore discard the whole file at load, stripping every live lease
	// of its deadline for a field whose absence already means the right thing.
	ChatID string `json:"chat_id,omitempty"`
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
	// TabOffered records that the run's tab has been offered to its launching
	// chat, so it is offered exactly once for the life of the run.
	//
	// DURABLE rather than process memory: `run_start` re-fires on every resume
	// and a restart re-reads the lease, and a reader's close has to stay final
	// across both. Additive at Version 1 — absent decodes false, so a lease
	// written before this field earns one re-offer.
	TabOffered bool `json:"tab_offered,omitempty"`
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

// Bounds is the input set NextDeadline composes into one deadline.
//
// A STRUCT rather than four positional parameters: `ceiling, floor` were already
// an undetectable transposition — two same-typed adjacent durations whose swap
// compiles and produces a plausible-looking answer — and a third duration makes
// that strictly worse. A keyed literal names each value at the call site.
//
// Field order is govet fieldalignment's: the times first, each carrying a
// *Location in its tail.
type Bounds struct {
	// SlotAt is the instant the run's own next scheduled slot comes due. Zero
	// means "no slot to respect", not "due at the epoch".
	SlotAt time.Time
	// BackstopAt is the instant the run's absolute EXECUTING-time budget is
	// spent. Zero means "this input does not bound the run".
	//
	// An INSTANT rather than a remaining duration, for SlotAt's reason: the zero
	// value has to mean "absent", and an omitted duration would read as "already
	// spent" — which OUTRANKS the floor, so a keyed literal that forgot the field
	// would cancel every run the instant it was armed.
	BackstopAt time.Time
	// Idle is how long a run may execute without observable progress. It is the
	// input a refill rolls forward, which is what makes the primary bound a
	// STALL bound rather than a total-duration one.
	Idle time.Duration
	// Floor is the smallest budget any run may be handed. It outranks the idle
	// window and the slot, and it does NOT outrank ANY BackstopAt tighter than the
	// composed value — a backstop already spent is the sharpest instance, not the
	// only one: with BackstopAt at now+2m and this floor at 5m the answer is now+2m.
	// A floor answers "this bound is too small to finish anything inside", which is a
	// claim about how much budget a run should get, and the backstop answers how much
	// it has left. The remainder wins even when it is smaller than any useful budget.
	Floor time.Duration
}

// NextDeadline computes the ONE deadline a run gets, from the three inputs that
// bound it. They are composed here rather than enforced separately because a run
// gets one timer, and a timer can only be armed for one instant.
//
//	min(now+Idle, SlotAt?)   — the tighter of the two bounds a run can work its way past
//	max(that, now+Floor)     — never hand a run an absurd budget
//	min(that, BackstopAt?)   — and never one that outlives its absolute budget
//
// THE ORDER IS THE PRECEDENCE, and the backstop is last because it outranks the
// floor. `min` on the slot is what makes a manual run of a scheduled recipe yield
// to the schedule instead of holding it for the whole idle window. `max` is what
// stops a slot that fired late, or an interval edited below the schedule's own
// floor, from producing a two-minute run budget — a bound too small to finish
// anything in is a guaranteed failure on every slot, and the floor is derived from
// the tightest schedule interval this app accepts, so the SLOT is the input it
// exists to answer for.
//
// A BACKSTOP ALREADY SPENT IS A STATEMENT THAT THE RUN IS OVER, so no floor may
// lift a deadline back above one: an instant in the past is returned AS ITSELF, and
// the timer armed for it fires immediately. Let the floor answer instead and the
// composed value is now+Floor on EVERY stamp, so each progress frame hands out a
// fresh floor, the refill throttle admits the later instant, and the absolute bound
// degrades into an unbounded rolling window — the one thing the backstop exists to
// stop. The clamp binds on any backstop TIGHTER than the composed value, of which a
// spent one is only the sharpest case.
//
// Clamping last is also what makes the backstop terminal ONCE IT BINDS: from that
// stamp on the answer is BackstopAt, which is fixed for the whole executing stretch,
// so every later stamp computes the same instant. Before it binds the answer is the
// idle window and the backstop is inert.
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
