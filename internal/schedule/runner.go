package schedule

import (
	"context"
	"log/slog"
	"time"
)

// TickInterval is how often the runner looks for due schedules. One shared ticker
// rather than a timer per schedule: there is no per-entry lifecycle to leak.
const TickInterval = time.Minute

// MissGrace is how late a due slot may be and still fire. Anything later was
// missed while the container was down and is SKIPPED by decision: waking to a
// batch of overdue runs firing at once is worse than missing one. It must exceed
// TickInterval, or a slot landing between ticks would never run at all.
const MissGrace = 3 * time.Minute

// Launcher starts one workflow run on behalf of a schedule; Runtime satisfies it.
// scheduleID travels with the launch so the host can attribute an unattended run's
// outcome back to this row. slotAt is an INPUT to the run's bound, not the bound:
// the host takes the tighter of it and its own ceiling, and zero means no slot.
type Launcher interface {
	LaunchScheduled(ctx context.Context, source, scheduleID string, slotAt time.Time) (id, name string, err error)
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
// during a restart is a missed slot, and firing it at boot is what this avoids.
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
		// No floor: the runner is the one caller that must SEE a slot already gone,
		// so the branches below can tell a due slot from one missed while down.
		due, err := NextRunFrom(e.Spec, e.Anchor, time.Time{})
		if err != nil {
			// An uncomputable stored spec would otherwise be retried every tick
			// forever; say so once per tick rather than disabling the user's row.
			slog.Warn("schedule has no next run", "id", e.ID, "error", err)
			continue
		}
		if now.Before(due) {
			continue
		}
		if now.Sub(due) > r.grace {
			// Missed while down. Advance past it; the next slot computes from here.
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
// way so a schedule whose launch keeps failing does not retry every tick.
func (r *Runner) fire(ctx context.Context, e *Entry, due time.Time) {
	// Bound the run by the next slot after the one that just fired, measured from
	// `due` so a late fire inside the grace window cannot extend the budget into
	// it. An uncomputable slot degrades to the idle window, never to unbounded.
	slotAt, dErr := NextRun(e.Spec, due)
	if dErr != nil {
		slog.Warn("schedule cannot name its next slot, so its run is bounded by its idle window alone",
			"id", e.ID, "source", e.Source, "error", dErr)
		slotAt = time.Time{}
	}
	runID, name, err := r.launcher.LaunchScheduled(ctx, e.Source, e.ID, slotAt)
	result := "started"
	if err != nil {
		// An overlap is the expected refusal (one live run per recipe), not a fault:
		// the previous run is still going, so this slot is simply skipped.
		result = "failed: " + err.Error()
		slog.Warn("scheduled run did not start", "id", e.ID, "source", e.Source, "error", err)
	} else {
		slog.Info("scheduled run started", "id", e.ID, "run_id", runID, "recipe", name, "due", due)
	}
	if err := r.store.recordFire(ctx, e.ID, due, result); err != nil {
		slog.Error("schedule record failed", "id", e.ID, "error", err)
	}
}
