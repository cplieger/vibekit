package composition

// The sweep needs the utility bridge, which needs an active kiro-cli, so a boot
// that finds the pinned version still downloading answers `run list unavailable`.
// Without a retry that process keeps its stale leases for its whole life.

import (
	"context"
	"testing"
	"time"
)

// sweepRecorder reports each invocation on a channel, so a test synchronises on
// the sweep itself rather than on a sleep.
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

func (s *sweepRecorder) awaitCall(t *testing.T, what string) {
	t.Helper()
	select {
	case <-s.calls:
	case <-time.After(5 * time.Second):
		t.Fatalf("no sweep call for %s within the budget", what)
	}
}

// refuseCall is bounded by a real deadline because the property is an ABSENCE with
// nothing to synchronise on; short, because the goroutine's other arm is a closed
// channel it would take immediately.
func (s *sweepRecorder) refuseCall(t *testing.T, what string) {
	t.Helper()
	select {
	case <-s.calls:
		t.Fatalf("a sweep call arrived for %s, which must not happen", what)
	case <-time.After(100 * time.Millisecond):
	}
}

// The retry waits on the INSTALL rather than polling readiness: the installer knows
// the instant a version goes active, where a poll must invent an interval and a ceiling.
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

// The retry exists for ONE failure: a sweep that read the run list has already decided
// every lease it holds, so retrying would spend a second pass over the lease store per boot.
func TestStartOrphanSweep_DoesNotRetryASweepThatReachedKAS(t *testing.T) {
	t.Parallel()
	rec := newSweepRecorder(true)
	installed := make(chan struct{})
	close(installed) // already active, which is every ordinary boot

	t.Cleanup(startOrphanSweep(t.Context(), rec.sweep, installed))

	rec.awaitCall(t, "the boot attempt")
	rec.refuseCall(t, "a retry after a sweep that reached KAS")
}

// On every ordinary boot the pinned version is already on the volume, so waiting for
// the install signal would delay the sweep past the point a schedule can launch a run.
func TestStartOrphanSweep_SweepsImmediatelyRatherThanWaitingForTheInstall(t *testing.T) {
	t.Parallel()
	rec := newSweepRecorder(true)

	// Never closed: an install still in flight.
	t.Cleanup(startOrphanSweep(t.Context(), rec.sweep, make(chan struct{})))

	rec.awaitCall(t, "the boot attempt with the install still pending")
}

// No pins at all (a bare `go run`) and pins the manager refused can never complete an
// install, so there is no second condition to wait for and the first attempt is all
// there is. A nil channel needs no special case: `select` takes the ctx arm the stop joins on.
func TestStartOrphanSweep_WithNoInstallToWaitFor(t *testing.T) {
	t.Parallel()
	rec := newSweepRecorder(false)

	stop := startOrphanSweep(t.Context(), rec.sweep, nil)
	t.Cleanup(stop)

	rec.awaitCall(t, "the boot attempt")
	rec.refuseCall(t, "a retry with no install channel to wait on")
}

// Asserted on the STOP, not on the absence of a call: a goroutine parked forever on the
// install signal makes no call either, so a call-counting test passes with the ctx arm
// deleted. The join duration is what discriminates — runBackground's stop cancels and then
// waits out `backgroundStopGrace`, so a goroutine with no ctx arm turns a millisecond into
// five seconds. On a container whose install never completes the park IS forever.
func TestStartOrphanSweep_StopsAtShutdown(t *testing.T) {
	t.Parallel()
	rec := newSweepRecorder(false)

	// Never closed: an install that will not finish.
	stop := startOrphanSweep(t.Context(), rec.sweep, make(chan struct{}))
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
