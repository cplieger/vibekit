package agent

import (
	"os"
	"testing"
	"time"
)

// Both automatic-retry ladders are parked past any test run. UP rather than down
// because nothing WAITS on either: each installs an untracked timer, so at the
// production delay it outlives its own test and re-attempts against an unrelated
// test's fixture. A test that needs a ladder to FIRE lowers the base itself.
func TestMain(m *testing.M) {
	cancelRetryBaseDelay = time.Hour
	healBaseDelay = time.Hour
	os.Exit(m.Run())
}
