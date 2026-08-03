package api

// Workflow-run domain types.
//
// A run is KAS's entity, not vibekit's: its state lives at
// ~/.kiro/sessions/<hash>/workflows/<workflowId>/ and vibekit persists nothing
// about it. So the types here are the EVENT surface and nothing more — there is
// no Run struct, no node tree and no plan model, because `_kiro/workflow/inspect`
// already returns all three and re-modelling them here would be a second
// representation of a structure vibekit does not own. GET /api/runs/{id} passes
// KAS's `state` and `nodePlan` through verbatim for exactly that reason.
//
// THREE events, where an earlier design said six. Each of the three removals is
// measured, not a judgement call:
//
//   - `run_notify` (step narration) is dropped because there is no frame that
//     could emit it. KAS's `KIND_TO_METHOD` table (workflow-notification-bridge)
//     has exactly nine kinds — run_start, node_start, node_complete, node_paused,
//     loop_iteration, watch_poll, paused, run_complete, steps_queued — and none
//     carries a severity or a message. An event whose producer does not exist is
//     worse than a missing feature: every client branch on it is dead code.
//   - `runs_changed` is dropped as redundant. The list changes when a run starts
//     and when it finishes, which is what the other two events already say.
//   - `run_paused` is folded into RunProgress as a kind. The reason it was
//     separate — "it needs a visible explanation" — is satisfied by the refetch:
//     `pauseReason` is on `inspect`, and putting it on the event would invite the
//     accumulate-from-events model the invalidation contract exists to forbid.
//
// All three ride the LAUNCHING CHAT's topic, which costs no transport code: KAS
// parents a run on the calling chat's session (`RunWorkflowTool.handle` sets
// `parentSessionId` from the execution's chat session), so a run's frames arrive
// on that chat's bridge and `translateACPEvent` already knows the chat id. A run
// started from the TUI arrives on no vibekit bridge at all and therefore receives
// no live events — it is visible in the history inventory and nowhere else.

// RunProgressKind is the KAS lifecycle kind behind a run_progress event.
//
// Seven of KAS's nine notification kinds. The other two are their own events
// (run_start → run_started, run_complete → run_finished) because one inserts a
// row and the other is terminal and fires a push; everything between them is
// the same instruction to the client: refetch.
type RunProgressKind string

// The seven progress kinds, spelled exactly as KAS's method suffixes so a
// reader can grep one string across both codebases.
const (
	RunProgressNodeStart     RunProgressKind = "node_start"
	RunProgressNodeComplete  RunProgressKind = "node_complete"
	RunProgressNodePaused    RunProgressKind = "node_paused"
	RunProgressLoopIteration RunProgressKind = "loop_iteration"
	RunProgressWatchPoll     RunProgressKind = "watch_poll"
	RunProgressPaused        RunProgressKind = "paused"
	RunProgressStepsQueued   RunProgressKind = "steps_queued"
)

// RunStartedPayload is the payload for type="run_started": a run began on this
// chat. Carries the name because a client that has never fetched this run has
// nothing to label the row with, and a row appearing with no name reads as a
// bug rather than as a pending fetch.
type RunStartedPayload struct {
	WorkflowID string `json:"workflow_id"`
	Name       string `json:"name,omitempty"`
}

// RunProgressPayload is the payload for type="run_progress": an INVALIDATION
// signal. The client refetches `GET /api/runs/{id}`; it never reconstructs run
// state from these events, and the payload is deliberately too thin to let it.
//
// That thinness is load-bearing rather than minimalist. `run_start` re-fires on
// every resume and progress frames duplicate across a resume (probe 6 saw three
// `run_start` frames for one run), so a client accumulating them would render a
// garbled tree. `node_complete` also cannot be joined by (nodeId, iteration,
// branchId) — it carries none of the last two — so an accumulating client could
// not even tell two repeat iterations apart.
//
// NodeID is absent on `paused` (a run-level frame) and holds the loop id on
// `loop_iteration`, which is the node the frame is about in both cases.
type RunProgressPayload struct {
	WorkflowID string          `json:"workflow_id"`
	NodeID     string          `json:"node_id,omitempty"`
	Kind       RunProgressKind `json:"kind"`
}

// RunFinishedPayload is the payload for type="run_finished": terminal. Status is
// KAS's own run-level status (completed / failed / aborted / paused — a policy
// pause at `onMaxIterations` reports through here too, since KAS emits
// `run_complete` for it).
//
// There is no aborted_by_restart flag. A restart PAUSES a run — KAS's read-path
// reconcile has exactly one outcome and no path to aborted (probe 24) — so there
// is nothing for such a flag to mean.
type RunFinishedPayload struct {
	WorkflowID string `json:"workflow_id"`
	Status     string `json:"status"`
}
