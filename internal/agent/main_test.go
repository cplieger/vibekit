package agent

import (
	"os"
	"testing"
	"time"
)

// TestMain points the package's two automatic-retry ladders at a delay no test run
// reaches, so a stray one cannot fire mid-suite.
//
// UP rather than down, because nothing WAITS on either: a refused cancel and a failed
// heal each install an untracked time.AfterFunc, so at the production 5s the timer
// outlives its own test and re-attempts a cancel or a resume while an unrelated test
// asserts on the same fixture. Three tests drive a refused cancel and each left one
// behind. A test that needs a ladder to FIRE overrides the base downward for itself
// (restoreCancelRetryDelay).
func TestMain(m *testing.M) {
	cancelRetryBaseDelay = time.Hour
	healBaseDelay = time.Hour
	os.Exit(m.Run())
}
