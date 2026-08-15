package schedule

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// mustStore opens a store in a temp dir.
func mustStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

// fakeLauncher records launches and can be made to fail.
type fakeLauncher struct {
	sources []string
	// schedules records the id each launch was attributed to, which is what lets
	// a later denial land on the right row.
	schedules []string
	// deadlines records the run bound each launch carried, so a test can assert
	// the interval was derived rather than merely passed.
	deadlines []time.Time
	err       error
}

func (f *fakeLauncher) LaunchScheduledRun(
	_ context.Context, source, scheduleID string, deadline time.Time,
) (string, string, error) {
	f.sources = append(f.sources, source)
	f.schedules = append(f.schedules, scheduleID)
	f.deadlines = append(f.deadlines, deadline)
	if f.err != nil {
		return "", "", f.err
	}
	return "wf-1", "recipe", nil
}

// newFixture builds a store with one daily 02:00 schedule anchored yesterday,
// plus a runner whose clock the test drives.
func newFixture(t *testing.T, now time.Time) (*Store, *fakeLauncher, *Runner) {
	t.Helper()
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	e := Entry{
		ID:      "s1",
		Source:  "bundled://demo",
		Spec:    Spec{Freq: FreqDaily, Hour: 2, Minute: 0},
		Enabled: true,
		Anchor:  at(2026, time.August, 3, 12, 0),
	}
	if err := st.Put(t.Context(), &e); err != nil {
		t.Fatalf("Put: %v", err)
	}
	l := &fakeLauncher{}
	r := NewRunner(st, l)
	r.now = func() time.Time { return now }
	return st, l, r
}

// TestSweep_FiresADueSlot is the happy path: the slot is due and recent.
func TestSweep_FiresADueSlot(t *testing.T) {
	due := at(2026, time.August, 4, 2, 0)
	st, l, r := newFixture(t, due.Add(30*time.Second))
	r.sweep(t.Context())

	if len(l.sources) != 1 {
		t.Fatalf("expected one launch, got %d", len(l.sources))
	}
	got := st.List()[0]
	if !got.Anchor.Equal(due) {
		t.Errorf("anchor must advance to the DUE time (not now) to prevent drift: got %v want %v", got.Anchor, due)
	}
	if got.LastResult != "started" {
		t.Errorf("LastResult = %q", got.LastResult)
	}
	// The schedule's id must travel with the launch, or an unattended denial has
	// no row to report itself on.
	if len(l.schedules) != 1 || l.schedules[0] != "s1" {
		t.Errorf("launch must carry the schedule id, got %v", l.schedules)
	}
}

// TestSweep_BoundsTheRunByItsOwnInterval pins the run bound to the schedule's
// INTERVAL rather than to a constant.
//
// The whole argument for this bound is that nobody has to pick a number: a
// scheduled run may take until its next slot and no longer. So the assertion is
// against NextRun's own answer for the fixture's daily 02:00 spec — exactly 24h
// after the slot that fired — and a hardcoded duration here would let the
// production side drift to any value that happened to match one fixture.
//
// Measured from DUE, not from now: the fire is 30s late inside the grace window,
// and a bound measured from now would push the run 30s past the slot it is meant
// not to collide with.
func TestSweep_BoundsTheRunByItsOwnInterval(t *testing.T) {
	due := at(2026, time.August, 4, 2, 0)
	st, l, r := newFixture(t, due.Add(30*time.Second))
	r.sweep(t.Context())

	if len(l.deadlines) != 1 {
		t.Fatalf("expected one launch, got %d", len(l.deadlines))
	}
	want, err := NextRun(st.List()[0].Spec, due)
	if err != nil {
		t.Fatalf("NextRun: %v", err)
	}
	if !l.deadlines[0].Equal(want) {
		t.Errorf("run bound = %v, want the next slot %v (the interval IS the number)", l.deadlines[0], want)
	}
	if !l.deadlines[0].Equal(due.Add(24 * time.Hour)) {
		t.Errorf("a daily schedule must bound its run at 24h after the slot, got %v", l.deadlines[0].Sub(due))
	}
}

// TestSweep_SkipsASlotMissedWhileOffline is the recorded decision: a slot late
// by more than the grace is skipped, not fired, and the schedule resumes.
func TestSweep_SkipsASlotMissedWhileOffline(t *testing.T) {
	now := at(2026, time.August, 4, 9, 0) // 7h after the 02:00 slot
	st, l, r := newFixture(t, now)
	r.sweep(t.Context())

	if len(l.sources) != 0 {
		t.Errorf("a slot missed while offline must not fire: %v", l.sources)
	}
	got := st.List()[0]
	if !got.Anchor.Equal(now) {
		t.Errorf("anchor must advance past the missed slot: got %v want %v", got.Anchor, now)
	}
	if !got.LastRunAt.IsZero() {
		t.Errorf("a skip must not record a run: LastRunAt = %v", got.LastRunAt)
	}
	// And the schedule resumes on the NEXT slot rather than firing immediately.
	next, err := NextRun(got.Spec, got.Anchor)
	if err != nil {
		t.Fatalf("NextRun: %v", err)
	}
	if want := at(2026, time.August, 5, 2, 0); !next.Equal(want) {
		t.Errorf("next slot after a skip = %v, want %v", next, want)
	}
}

// TestSweep_DoesNotFireBeforeDue keeps a schedule from running early.
func TestSweep_DoesNotFireBeforeDue(t *testing.T) {
	_, l, r := newFixture(t, at(2026, time.August, 4, 1, 59))
	r.sweep(t.Context())
	if len(l.sources) != 0 {
		t.Errorf("fired before its due time: %v", l.sources)
	}
}

// TestSweep_FiresOnlyOncePerSlot is what the anchor is for: a second tick inside
// the same grace window must not launch again.
func TestSweep_FiresOnlyOncePerSlot(t *testing.T) {
	due := at(2026, time.August, 4, 2, 0)
	_, l, r := newFixture(t, due.Add(10*time.Second))
	r.sweep(t.Context())
	r.now = func() time.Time { return due.Add(70 * time.Second) }
	r.sweep(t.Context())

	if len(l.sources) != 1 {
		t.Errorf("expected exactly one launch across two ticks, got %d", len(l.sources))
	}
}

// TestSweep_SkipsDisabled covers the enable toggle.
func TestSweep_SkipsDisabled(t *testing.T) {
	due := at(2026, time.August, 4, 2, 0)
	st, l, r := newFixture(t, due.Add(30*time.Second))
	e := st.List()[0]
	e.Enabled = false
	if err := st.Put(t.Context(), &e); err != nil {
		t.Fatalf("Put: %v", err)
	}
	r.sweep(t.Context())
	if len(l.sources) != 0 {
		t.Errorf("a disabled schedule must not fire: %v", l.sources)
	}
}

// TestSweep_AdvancesPastAFailedLaunch stops a failing recipe (or the
// one-live-run-per-recipe refusal) from retrying on every tick.
func TestSweep_AdvancesPastAFailedLaunch(t *testing.T) {
	due := at(2026, time.August, 4, 2, 0)
	st, l, r := newFixture(t, due.Add(30*time.Second))
	l.err = errors.New("this recipe already has a live run")
	r.sweep(t.Context())

	got := st.List()[0]
	if !got.Anchor.Equal(due) {
		t.Errorf("anchor must advance even on failure: got %v", got.Anchor)
	}
	if got.LastResult == "started" {
		t.Errorf("a failed launch must not record success")
	}
	r.sweep(t.Context())
	if len(l.sources) != 1 {
		t.Errorf("a failed launch must not be retried on the next tick, got %d attempts", len(l.sources))
	}
}

// TestMissGraceExceedsTick pins the relationship the classification depends on:
// were the grace shorter than the tick, a slot landing between ticks would be
// called missed and never run.
func TestMissGraceExceedsTick(t *testing.T) {
	if MissGrace <= TickInterval {
		t.Errorf("MissGrace (%v) must exceed TickInterval (%v) or in-window slots are misclassified as missed",
			MissGrace, TickInterval)
	}
}

func TestStore_RoundTripsAndRejects(t *testing.T) {
	dir := t.TempDir()
	st, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := t.Context()
	if err := st.Put(ctx, &Entry{ID: "a", Source: "bundled://x", Spec: Spec{Freq: FreqDaily, Hour: 1}, Enabled: true}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// An invalid spec must never reach disk.
	if err := st.Put(ctx, &Entry{ID: "b", Source: "bundled://y", Spec: Spec{Freq: "weekly"}}); err == nil {
		t.Errorf("a weekly spec with no weekdays must be rejected")
	}
	if err := st.Put(ctx, &Entry{ID: "", Source: "bundled://y", Spec: Spec{Freq: FreqDaily}}); err == nil {
		t.Errorf("an empty id must be rejected")
	}

	reopened, err := NewStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got := reopened.List()
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("round trip lost the entry: %+v", got)
	}
	if got[0].Anchor.IsZero() {
		t.Errorf("Put must stamp an anchor so a new schedule does not fire for every past slot")
	}

	if err := reopened.Delete(ctx, "a"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(reopened.List()) != 0 {
		t.Errorf("Delete left the entry behind")
	}
	if err := reopened.Delete(ctx, "gone"); err != nil {
		t.Errorf("deleting a missing schedule must not error: %v", err)
	}
}

// TestStore_PutPreservesHistory covers an edit from the UI, which sends no
// history fields.
func TestStore_PutPreservesHistory(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := t.Context()
	e := Entry{ID: "a", Source: "bundled://x", Spec: Spec{Freq: FreqDaily, Hour: 1}, Enabled: true}
	if err := st.Put(ctx, &e); err != nil {
		t.Fatalf("Put: %v", err)
	}
	due := at(2026, time.August, 4, 1, 0)
	if err := st.recordFire(ctx, "a", due, "started"); err != nil {
		t.Fatalf("recordFire: %v", err)
	}

	edited := Entry{ID: "a", Source: "bundled://x", Spec: Spec{Freq: FreqDaily, Hour: 5}, Enabled: true}
	if err := st.Put(ctx, &edited); err != nil {
		t.Fatalf("Put edit: %v", err)
	}
	got := st.List()[0]
	if got.Spec.Hour != 5 {
		t.Errorf("edit did not apply: hour = %d", got.Spec.Hour)
	}
	if got.LastResult != "started" || got.LastRunAt.IsZero() {
		t.Errorf("edit dropped run history: %+v", got)
	}
}

// TestNewStore_RefusesAMalformedFile: silently resetting would drop the user's
// schedules with no signal.
func TestNewStore_RefusesAMalformedFile(t *testing.T) {
	dir := t.TempDir()
	if err := writeFile(dir, "{not json"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := NewStore(dir); err == nil {
		t.Errorf("a malformed store must be an error, not a silent reset")
	}
}

// writeFile seeds a raw store file for the malformed-input test.
func writeFile(dir, body string) error {
	return os.WriteFile(filepath.Join(dir, FileName), []byte(body), 0o600)
}

// TestRecordOutcome_DoesNotMoveTheAnchor is the whole reason this is separate
// from recordFire. An unattended denial lands minutes after the run started, and
// moving the anchor then would push the next run out by however long the failure
// took — a schedule that quietly drifts later every time it fails.
func TestRecordOutcome_DoesNotMoveTheAnchor(t *testing.T) {
	st := mustStore(t)
	ctx := t.Context()
	e := Entry{ID: "s1", Source: "bundled://x", Spec: Spec{Freq: FreqDaily, Hour: 2}, Enabled: true}
	if err := st.Put(ctx, &e); err != nil {
		t.Fatalf("Put: %v", err)
	}
	due := at(2026, time.August, 4, 2, 0)
	if err := st.recordFire(ctx, "s1", due, "started"); err != nil {
		t.Fatalf("recordFire: %v", err)
	}

	const reason = "failed: needed approval for Write with nobody watching"
	if err := st.RecordOutcome(ctx, "s1", reason); err != nil {
		t.Fatalf("RecordOutcome: %v", err)
	}
	got := st.List()[0]
	if got.LastResult != reason {
		t.Errorf("LastResult = %q, want the denial reason", got.LastResult)
	}
	if !got.Anchor.Equal(due) {
		t.Errorf("anchor moved: got %v want %v — the next run would drift", got.Anchor, due)
	}
}

func TestRecordOutcome_UnknownSchedule(t *testing.T) {
	st := mustStore(t)
	if err := st.RecordOutcome(t.Context(), "gone", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}
