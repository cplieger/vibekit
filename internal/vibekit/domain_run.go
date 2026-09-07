package vibekit

// RunStatus is KAS's run-level workflow status.
//
// The values match kiro-cli 2.21.1's adjacent run-status enum in
// acp-server.js at byte 401215.
type RunStatus string

// Run-level workflow statuses.
//
// RunStatusCancelled is NOT in KAS's own enum and is a member anyway: cancel writes
// `targetStatus ?? "aborted"` verbatim with no enum check, so another client of the
// workspace can put it there and `_kiro/workflow/list` reads it back. One value too
// WIDE releases a lease early for a run nothing will resume; one too NARROW strands
// the lease AND wedges the recipe with no clearing path.
const (
	RunStatusRunning   RunStatus = "running"
	RunStatusPaused    RunStatus = "paused"
	RunStatusCompleted RunStatus = "completed"
	RunStatusFailed    RunStatus = "failed"
	RunStatusAborted   RunStatus = "aborted"
	RunStatusCancelled RunStatus = "cancelled"
)

// Terminal reports whether the run has settled. Unknown values stay live so a
// producer addition cannot make the server release a bridge or lease early.
func (s RunStatus) Terminal() bool {
	switch s {
	case RunStatusCompleted, RunStatusFailed, RunStatusAborted, RunStatusCancelled:
		return true
	case RunStatusRunning, RunStatusPaused:
		return false
	}
	return false
}

// Active reports whether the run must still be treated as live.
func (s RunStatus) Active() bool {
	return !s.Terminal()
}

// RunNodeStatus is KAS's node-level workflow status.
//
// The values match kiro-cli 2.21.1's adjacent node-status enum in
// acp-server.js at byte 401215.
type RunNodeStatus string

// Node-level workflow statuses.
const (
	RunNodeStatusPending   RunNodeStatus = "pending"
	RunNodeStatusRunning   RunNodeStatus = "running"
	RunNodeStatusPaused    RunNodeStatus = "paused"
	RunNodeStatusCompleted RunNodeStatus = "completed"
	RunNodeStatusFailed    RunNodeStatus = "failed"
	RunNodeStatusAborted   RunNodeStatus = "aborted"
	RunNodeStatusSkipped   RunNodeStatus = "skipped"
)

// The node vocabulary deliberately carries NO Terminal/Active predicate, unlike
// RunStatus. Nothing folds a node status that way: production compares two of these
// constants directly (paused, running) and the client's exec view owns the fold onto
// its own presentation states. The constants exist for the census, which asserts this
// declaration against KAS's own enum; a predicate with no consumer would be surface
// the census cannot justify.
