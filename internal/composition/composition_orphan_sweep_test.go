package composition

// The boot orphan sweep's retry policy.
//
// The sweep needs the utility bridge and the utility bridge needs an active
// kiro-cli, so a boot that finds the pinned version still downloading answers
// `run list unavailable` and stops. That happened seven times in ten days on the
// live volume and nothing retried it, so every one of those processes kept its
// stale leases for its whole life — each of which keeps advertising a dead run on
// /api/runs/live and holds its chat exempt from the client's eviction sweep.

import (
	"context"
	"testing"
	"time"
)

// sweepRecorder is a fake run sweep: it answers a scripted verdict per call and
// reports each invocation on a channel, so a test synchronises on the sweep itself
// rather than on a sleep.
type sweepRecorder struct {
	calls    chan context.Context
	verdicts []bool
}

func newSweepRecorder(verdicts ...bool) *sweepRecorder {
	return &sweepRecorder{calls: make(chan context.Context, len(verdicts)+1), verdicts: verdicts}
}

func (s *sweepRecorder) sweep(ctx context.Context) bool {
	s.calls <- ctx
	if len(s.verdicts) == 0 {
		return true
	}
	v := s.verdicts[0]
	s.verdicts = s.verdicts[1:]
	return v
}

// awaitCall waits for the next sweep invocation, failing with a diagnostic rather
// than hanging the package's whole test binary.
func (s *sweepRecorder) awaitCall(t *testing.T, what string) {
	t.Helper()
	select {
	case <-s.calls:
	case <-time.After(5 * time.Second):
		t.Fatalf("no sweep call for %s within the budget", what)
	}
}

// refuseCall fails when a sweep call arrives inside a short window. Bounded by a
// real deadline because the property is an ABSENCE and there is nothing to
// synchronise on; the window is short because the goroutine's other arm is a
// closed channel it would take immediately.
func (s *sweepRecorder) refuseCall(t *testing.T, what string) {
	t.Helper()
	select {
	case <-s.calls:
		t.Fatalf("a sweep call arrived for %s, which must not happen", what)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestStartOrphanSweep_RetriesOnceTheInstallCompletes is Fix B: the seven measured
// skips become seven recoveries.
//
// It waits on the INSTALL rather than polling readiness, because the installer knows
// the instant a version goes active and a poll would have to invent an interval and
// a ceiling for it.
func TestStartOrphanSweep_RetriesOnceTheInstallCompletes(t *testing.T) {
	t.Parallel()
	rec := newSweepRecorder(false, true)
	installed := make(chan struct{})

	t.Cleanup(startOrphanSweep(t.Context(), rec.sweep, installed))

	rec.awaitCall(t, "the boot attempt")
	rec.refuseCall(t, "a retry before the install completed")

	close(installed)

	rec.awaitCall(t, "the retry after the install completed")
}

// TestStartOrphanSweep_DoesNotRetryASweepThatReachedKAS: the retry exists for ONE
// failure, and a sweep that read the run list has already decided every lease it
// holds. Retrying would spend a second pass over the whole lease store per boot.
func TestStartOrphanSweep_DoesNotRetryASweepThatReachedKAS(t *testing.T) {
	t.Parallel()
	rec := newSweepRecorder(true)
	installed := make(chan struct{})
	close(installed) // already active, which is every ordinary boot

	t.Cleanup(startOrphanSweep(t.Context(), rec.sweep, installed))

	rec.awaitCall(t, "the boot attempt")
	rec.refuseCall(t, "a retry after a sweep that reached KAS")
}

// TestStartOrphanSweep_SweepsImmediatelyRatherThanWaitingForTheInstall pins the
// ordering the call site's own comment protects: on every ordinary boot the pinned
// version is already on the volume, so waiting for the install signal would delay
// the sweep past the point the schedule runner can launch a run.
func TestStartOrphanSweep_SweepsImmediatelyRatherThanWaitingForTheInstall(t *testing.T) {
	t.Parallel()
	rec := newSweepRecorder(true)

	// Never closed: an install still in flight.
	t.Cleanup(startOrphanSweep(t.Context(), rec.sweep, make(chan struct{})))

	rec.awaitCall(t, "the boot attempt with the install still pending")
}

// TestStartOrphanSweep_WithNoInstallToWaitFor covers the two runtimes that can
// never complete an install — no pins at all (a bare `go run`), and pins the manager
// refused. There is no second condition for a retry to wait for, so the first
// attempt is all there is.
//
// A nil channel needs no special case: `select` takes the ctx arm, which is what the
// stop below joins on.
func TestStartOrphanSweep_WithNoInstallToWaitFor(t *testing.T) {
	t.Parallel()
	rec := newSweepRecorder(false)

	stop := startOrphanSweep(t.Context(), rec.sweep, nil)
	t.Cleanup(stop)

	rec.awaitCall(t, "the boot attempt")
	rec.refuseCall(t, "a retry with no install channel to wait on")
}

// TestStartOrphanSweep_StopsAtShutdown is the goroutine's LIFETIME, and it is
// asserted on the stop rather than on the absence of a call.
//
// The absence of a call is what the shape claims and not what it proves: a goroutine
// PARKED forever on the install signal makes no call either, so a test that only
// counts calls passes with the ctx arm deleted. This one measures how long the join
// takes — runBackground's stop cancels and then waits out `backgroundStopGrace`
// before giving up, so a goroutine with no ctx arm turns a millisecond into five
// seconds and says so in a log line nobody reads.
//
// On a container whose install never completes the park IS forever, which is why the
// arm exists and why the call site holds the stop on App.
func TestStartOrphanSweep_StopsAtShutdown(t *testing.T) {
	t.Parallel()
	rec := newSweepRecorder(false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Never closed: an install that will not finish.
	stop := startOrphanSweep(ctx, rec.sweep, make(chan struct{}))
	rec.awaitCall(t, "the boot attempt")

	start := time.Now()
	stop()
	took := time.Since(start)

	if took >= backgroundStopGrace {
		t.Errorf("the stop took %s, at or past the %s grace: the sweep goroutine is parked "+
			"with no shutdown arm, so it outlives App.Shutdown and can still be mid-RPC on a "+
			"utility bridge the teardown has closed", took, backgroundStopGrace)
	}
	rec.refuseCall(t, "a retry after shutdown")
}
