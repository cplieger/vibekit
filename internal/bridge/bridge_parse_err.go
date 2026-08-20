package bridge

// Parse-error tracking state machine.
//
// parseErrTracker implements burst-then-summarize-then-circuit-break
// logic for malformed JSON lines from kiro-cli. It is a pure state
// machine with no external dependencies or I/O, making it
// independently testable with table-driven cases.

import "time"

// parseErrAction describes what readLoop should do after recording a parse error.
type parseErrAction int

const (
	parseErrLog          parseErrAction = iota // emit the error verbatim
	parseErrSuppress                           // within window, suppress
	parseErrSummarize                          // emit a summary line
	parseErrCircuitBreak                       // consecutive ceiling hit, tear down
)

// parseErrBurst is the first N parse-error lines readLoop emits
// verbatim before switching to summary-only mode. parseErrWindow is
// the summary-line cadence; parseErrMaxConsecutive is the consecutive-
// failure ceiling that triggers bridge teardown so the runtime recreates
// a fresh subprocess on the next prompt.
const (
	parseErrBurst          = 10
	parseErrWindow         = 30 * time.Second
	parseErrMaxConsecutive = 1000
)

// parseErrTracker encapsulates the burst-then-summary parse-error
// logging and circuit-breaker logic previously inlined in readLoop.
type parseErrTracker struct {
	windowStart time.Time
	lastErrorAt time.Time
	total       int
	consecutive int
}

// parseErrDecay is how long the storm window survives without a new error. Past
// it the burst/summary accounting starts over, so a long-lived bridge that saw
// one storm hours ago gets its verbatim burst back instead of staying in
// summary-only mode for the life of the process.
//
// It does NOT touch the circuit breaker, and it must not: the breaker counts
// CONSECUTIVE failures, which Reset clears on every frame that parses, so
// parseErrMaxConsecutive frames with not one valid frame between them is a dead
// stream at any pace. Decaying that count would make the breaker unable to fire
// on a stream that fails totally but slowly.
const parseErrDecay = 5 * time.Minute

// Record notes a parse error and returns the action readLoop should take.
func (t *parseErrTracker) Record() parseErrAction {
	now := time.Now()
	// Decay: if the last error was long ago, reset the storm window.
	if !t.lastErrorAt.IsZero() && now.Sub(t.lastErrorAt) > parseErrDecay {
		t.total = 0
		t.windowStart = time.Time{}
	}
	t.lastErrorAt = now
	t.total++
	t.consecutive++
	if t.consecutive >= parseErrMaxConsecutive {
		return parseErrCircuitBreak
	}
	if t.total <= parseErrBurst {
		if t.total == parseErrBurst {
			t.windowStart = now
		}
		return parseErrLog
	}
	// now, not a second time.Now(): one decision must not straddle two readings
	// of the clock, or the decay above and the window here answer about
	// different instants.
	if now.Sub(t.windowStart) > parseErrWindow {
		t.windowStart = now
		return parseErrSummarize
	}
	return parseErrSuppress
}

// Reset clears the consecutive counter on a successful parse.
func (t *parseErrTracker) Reset() { t.consecutive = 0 }

// SummaryCount returns the number of suppressed errors since the last
// summary (total minus the initial burst).
func (t *parseErrTracker) SummaryCount() int { return t.total - parseErrBurst }
