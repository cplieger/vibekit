package vibekit

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
// THREE INVALIDATION events, where an earlier design said six, plus one content
// event added later for the runs no chat can carry. Each of the three removals is
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
//
// `run_notify`'s reasoning above still holds and RunStepPayload does not weaken
// it: there is still no step-NARRATION frame, because KAS emits no prose about a
// step. What run_step carries is the step agent's OWN output, off the ordinary
// `session/update` channel every other agent's content travels on, which is a
// producer that does exist and was simply being discarded on run bridges.

import "encoding/json"

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
//
// Scheduled marks a run the SCHEDULER launched, and it exists because the client
// cannot work this out. A parentless run's lifecycle frames are workspace-global
// with an empty chat id, and a MANUAL launch is parentless too, so watching
// events cannot separate the two; `parentSessionId` separates agent-parented from
// parentless and is empty for both of these. Only the launch path knows, so the
// distinction travels from there.
//
// Its one consumer is the client's start signal: a manual launch already has the
// user's attention (they clicked Run, and a run tab opened), while a scheduled one
// began with nobody looking. Absent on the wire for a manual run rather than
// false, so an older client and a manual launch read alike.
type RunStartedPayload struct {
	WorkflowID string `json:"workflow_id"`
	Name       string `json:"name,omitempty"`
	Scheduled  bool   `json:"scheduled,omitempty"`
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
//
// Name is here for RunStartedPayload's reason, arriving at the other end of the
// run: an outcome signal has to say WHICH run finished, and a client that never
// saw this run's start frame (a page opened mid-run, another device) has nothing
// else to name it with. Read out of KAS's `finalState`, which this frame already
// decodes for its log line, so it costs one field and no new decode. Empty when
// KAS sends no state, and the consumer falls back to a generic label.
type RunFinishedPayload struct {
	WorkflowID string `json:"workflow_id"`
	Status     string `json:"status"`
	Name       string `json:"name,omitempty"`
}

// RunStepKind discriminates what a run_step frame carries. Three members,
// matching the three block kinds a transcript already renders, because a step's
// content IS a transcript and inventing a fourth vocabulary for it would make a
// reader learn the same thing twice.
type RunStepKind string

// The three run-step kinds.
const (
	// RunStepText is a delta of the step agent's own prose.
	RunStepText RunStepKind = "text"
	// RunStepThinking is a delta of its reasoning.
	RunStepThinking RunStepKind = "thinking"
	// RunStepTool is one tool call, whole. Sent on create AND on every update,
	// folded server-side, so a client renders from the frame it holds rather than
	// accumulating partials — the same rule the run lifecycle follows, applied to
	// the one surface that has no endpoint to refetch.
	RunStepTool RunStepKind = "tool"
)

// RunStepPayload is the payload for type="run_step".
//
// NodePath, not NodeID, and that is the same choice ACPWorkflowMeta.SubtaskID
// makes for the transcript: a repeat's iterations share a node id, so an id
// cannot address one execution and two passes of a loop body would write into
// each other's rows. Joined with "/" so it is the key the client already builds
// from `inspect`'s tree (`nodePathOf(...).join("/")`).
//
// ToolCall is whole rather than a delta because there is no buffer at this end to
// fold into: a parentless run has no chat, so nothing accumulates its content.
// The translator holds the in-flight calls per run instead, bounded by the same
// `run_complete` that bounds the step-session registry, and sends the folded
// value. A client can therefore render the last frame it received and be right.
type RunStepPayload struct {
	ToolCall   *ToolCall   `json:"tool_call,omitempty"`
	WorkflowID string      `json:"workflow_id"`
	NodePath   string      `json:"node_path"`
	Kind       RunStepKind `json:"kind"`
	Delta      string      `json:"delta,omitempty"`
}

// RunLaunchRequest is POST /api/runs's body: launch one recipe, PARENTLESS.
//
// The recipe is named by its `source` exactly as `_kiro/workflow/listRecipes`
// reported it — `bundled://<name>` for a compiled-in recipe, an absolute
// `*.workflow.json` path for a workspace one. The server re-validates the value
// against a fresh listRecipes call rather than trusting it, so this endpoint
// cannot be steered at an arbitrary file even though the wire value looks like
// a path.
type RunLaunchRequest struct {
	Inputs map[string]string `json:"inputs,omitempty"`
	Source string            `json:"source"`
}

// RunLaunchedResponse is POST /api/runs's reply. Name is the recipe's, so the
// client can label the tab it opens without waiting for the first refetch.
type RunLaunchedResponse struct {
	WorkflowID string `json:"workflow_id"`
	Name       string `json:"name"`
}

// Recipe is one launchable workflow definition, projected from
// `_kiro/workflow/listRecipes` for GET /api/recipes.
type Recipe struct {
	// Inputs maps input name → declared type (`string`, `prompt`, `file`).
	Inputs      map[string]string `json:"inputs,omitempty"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	// Source is the launch key: `bundled://<name>` or a workspace
	// `*.workflow.json` path. Echoed back verbatim in RunLaunchRequest.
	Source string `json:"source"`
	// Plan is KAS's node plan for the recipe, forwarded VERBATIM. It is an array
	// of node descriptors: `{nodeId, type, agentName, modelId?, effortLevel?}`
	// for a step, and a nested `steps` / `branches` array for a sequence, repeat
	// or parallel node. So it says what the recipe will do and on which model,
	// which is what a reader needs BEFORE launching one.
	//
	// KAS sends it on every listRecipes reply whether or not a client reads it,
	// so forwarding costs one field. Raw rather than modelled, for the reason
	// stated at the top of this file: the node plan is KAS's structure, and a
	// second representation of it here would be one more thing to keep in sync
	// for no gain. Last of the pointer-bearing fields on purpose — a slice's
	// len/cap words end the GC scan region, where a trailing string would extend
	// it (the vibekit.ToolCall.Checkpoint note records the same rule).
	Plan    json.RawMessage `json:"plan,omitempty"`
	BuiltIn bool            `json:"built_in,omitempty"`
}

// RecipesResponse is GET /api/recipes's reply.
type RecipesResponse struct {
	Recipes []Recipe `json:"recipes"`
}
