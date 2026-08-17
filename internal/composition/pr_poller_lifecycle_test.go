package composition

// The PR-status poller's shutdown, asserted through the REAL shutdown owner.
//
// The defect these cover: production calls Build with context.Background()
// (main.go), and the poller was handed that context directly, so nothing stopped
// it. App.Shutdown closed the push service and the poller kept waking once a
// minute to consult it. The process exiting immediately afterwards is what hid it,
// which is why a test that cancels a context it made up itself proves nothing —
// that is the mechanism that already worked. What had to be proven is that
// App.Shutdown reaches the loop.

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestRunBackground_StopWaitsForTheGoroutine is the mechanism: stop cancels, and
// it does not return until the loop has. The wait is the part that matters — an
// unwaited cancel lets a sweep already inside a forge subprocess keep running
// after Shutdown returned, which is the leak in a different costume.
func TestRunBackground_StopWaitsForTheGoroutine(t *testing.T) {
	var exited atomic.Bool
	started := make(chan struct{})
	stop := runBackground(context.Background(), "test loop", func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		// A real loop unwinds work here; the sleep is what makes an unwaited
		// stop observably different from a waited one.
		time.Sleep(20 * time.Millisecond)
		exited.Store(true)
	})
	<-started
	if exited.Load() {
		t.Fatal("the loop exited before it was asked to")
	}
	stop()
	if !exited.Load() {
		t.Error("stop returned while the loop was still running: the cancel is not waited on")
	}
}

// TestApp_ShutdownStopsThePRStatusPoller is the finding itself. It drives
// App.Shutdown — the function production calls — rather than a cancel the test
// created, because the bug was precisely that App.Shutdown owned no cancel.
func TestApp_ShutdownStopsThePRStatusPoller(t *testing.T) {
	var exited atomic.Bool
	started := make(chan struct{})
	// Every other App member is left nil on purpose: this asserts the poller leg of
	// the teardown, and Shutdown treats each member as optional so one absent
	// service cannot take the ordered teardown of the others down with it.
	app := &App{
		stopPRPoller: runBackground(context.Background(), "pr status poller",
			func(ctx context.Context) {
				close(started)
				<-ctx.Done()
				exited.Store(true)
			}),
	}
	<-started

	app.Shutdown()

	if !exited.Load() {
		t.Error("the PR-status poller was still running after App.Shutdown returned; " +
			"it would keep consulting the push service the same Shutdown just closed")
	}
}

// TestApp_ShutdownSurvivesAnAbsentPoller keeps the nil tolerance honest in the
// direction that matters: a degraded Build (tools is already nil on the
// root-integrity path) must still get its ordered teardown rather than a panic.
func TestApp_ShutdownSurvivesAnAbsentPoller(t *testing.T) {
	(&App{}).Shutdown()
}
