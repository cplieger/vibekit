package schedule

import (
	"context"
	"log/slog"
	"time"
)

// TickInterval is how often the runner looks for due schedules. One shared
// ticker rather than a timer per schedule: there is no per-entry lifecycle to
// leak, an edit needs no rescheduling, and the whole loop is one readable pass.
const TickInterval = time.Minute

// MissGrace is how late a due slot may be and still fire. Anything later was
// missed while the container was down and is SKIPPED — by decision, because
// waking up to a batch of overdue runs firing at once is worse than missing
// one, especially when they write files. The user can always launch manually.
//
// It must exceed TickInterval, or a slot landing between ticks would be
// classified as missed and never run at all.
const MissGrace = 3 * time.Minute

// Launcher starts one workflow run on behalf of a schedule. Runtime satisfies this;
// the narrow interface is what lets the runner be tested without a bridge or a
// subprocess.
//
// The scheduleID travels with the launch so the host can attribute an UNATTENDED
// run back to the row that asked for it. That attribution is what makes a
// silent nightly failure visible: the host records the outcome against this id
// when the run is denied a permission nobody was there to answer.
//
// slotAt is the instant this run's own NEXT slot comes due, and it is computed
// here because it is a property of the SCHEDULE rather than of the run: a
// scheduled run may take up to its own repeat interval and no longer. The interval
// IS the number, which is what makes this bound a rule rather than a timeout
// somebody had to pick.
//
// It is an INPUT to the run's bound, not the bound itself. The host takes the
// tighter of this and its own universal ceiling and applies a floor, so zero (a
// spec whose next slot cannot be computed) means "no slot to respect" rather than
// "unbounded" — such a run is still bounded by the ceiling.
type Launcher interface {
	LaunchScheduledRun(ctx context.Context, source, scheduleID string, slotAt time.Time) (id, name string, err error)
}

// Runner fires due schedules. Construct with NewRunner and call Run in a
// goroutine; it returns when ctx is cancelled.
type Runner struct {
	store    *Store
	launcher Launcher
	now      func() time.Time
	tick     time.Duration
	grace    time.Duration
}

// NewRunner wires a runner over the store and launcher.
func NewRunner(store *Store, launcher Launcher) *Runner {
	return &Runner{store: store, launcher: launcher, now: time.Now, tick: TickInterval, grace: MissGrace}
}

// Run polls until ctx is cancelled. It does NOT sweep on entry: a schedule due
// during a restart is a missed slot, and firing it at boot is the behaviour this
// deliberately avoids.
func (r *Runner) Run(ctx context.Context) {
	t := time.NewTicker(r.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.sweep(ctx)
		}
	}
}

// sweep fires every schedule whose slot is due and recent.
func (r *Runner) sweep(ctx context.Context) {
	now := r.now()
	list := r.store.List()
	for i := range list {
		e := &list[i]
		if !e.Enabled {
			continue
		}
		// No floor: the runner is the one caller that must SEE a slot which has
		// already gone, because the two branches below tell a slot it may still
		// fire from one missed while the container was down. The REST view calls
		// the same helper with `now` as the floor, which is what stops a row from
		// naming a next run in the past.
		due, err := NextRunFrom(e.Spec, e.Anchor, time.Time{})
		if err != nil {
			// A stored spec that cannot be computed would otherwise be
			// retried every tick forever; say so once per tick and move on
			// rather than disabling something the user configured.
			slog.Warn("schedule has no next run", "id", e.ID, "error", err)
			continue
		}
		if now.Before(due) {
			continue
		}
		if now.Sub(due) > r.grace {
			// Missed while down. Advance past it silently-but-logged; the next
			// slot is computed from here, so the schedule resumes normally.
			slog.Info("schedule slot missed while offline, skipping",
				"id", e.ID, "due", due, "late_by", now.Sub(due).Round(time.Second))
			if err := r.store.skipTo(ctx, e.ID, now); err != nil {
				slog.Error("schedule skip failed", "id", e.ID, "error", err)
			}
			continue
		}
		r.fire(ctx, e, due)
	}
}

// fire launches one run and records the outcome. The anchor advances either
// way: a schedule whose launch keeps failing must not retry every tick.
func (r *Runner) fire(ctx context.Context, e *Entry, due time.Time) {
	// Bound the run by its own repeat interval: the next slot after the one that
	// just fired. Measured from `due` rather than from now so a late fire inside
	// the grace window does not extend the run's budget past the slot it would
	// collide with.
	//
	// An uncomputable next slot yields no SLOT input rather than a launch failure.
	// The run still being launched is the point: refusing to run because one of the
	// two bound inputs cannot be derived would turn a display-level defect into an
	// outage, and the host's own ceiling still bounds it.
	//
	// DEFENSIVE, and unreachable through the store today: Put validates the spec
	// (an unknown frequency is rejected there), so every persisted entry can name
	// its next slot. Left in because NextRun returns an error and swallowing it
	// silently is how an unbounded run would become invisible if a future
	// frequency ever parses but cannot be projected. Deliberately not tested —
	// reaching it needs a state the store refuses to hold.
	slotAt, dErr := NextRun(e.Spec, due)
	if dErr != nil {
		slog.Warn("schedule cannot name its next slot, so its run is bounded by the ceiling alone",
			"id", e.ID, "source", e.Source, "error", dErr)
		slotAt = time.Time{}
	}
	runID, name, err := r.launcher.LaunchScheduledRun(ctx, e.Source, e.ID, slotAt)
	result := "started"
	if err != nil {
		// An overlap is the expected, already-implemented refusal (one live run
		// per recipe), not a fault: the previous run is still going, so this
		// slot is simply skipped.
		result = "failed: " + err.Error()
		slog.Warn("scheduled run did not start", "id", e.ID, "source", e.Source, "error", err)
	} else {
		slog.Info("scheduled run started", "id", e.ID, "run_id", runID, "recipe", name, "due", due)
	}
	if err := r.store.recordFire(ctx, e.ID, due, result); err != nil {
		slog.Error("schedule record failed", "id", e.ID, "error", err)
	}
}
