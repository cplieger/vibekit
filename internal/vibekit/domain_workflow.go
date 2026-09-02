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
// each other's rows. Joined with "/", and NOT byte-identical to what a client
// derives from `inspect`'s state tree: KAS spells a repeat's iteration container
// `iter-<n>` here and `<repeatId>#<n>` there, so the client translates the tree
// into this spelling (`static-src/run-store.ts`'s nodePathSegment).
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

// RunInputNeededPayload is the payload for type="run_input_needed": a workflow
// STEP asked a question and the run is parked until somebody answers it.
//
// The FIFTH run event, and the second that is not an invalidation. It carries its
// payload for the same reason run_step does and the three invalidations do not:
// the question text is on no endpoint. KAS parks the run with one fixed literal
// in `state.pauseReason` and an empty `pauseDetail`, so `inspect` can say a step
// wants input and can never say what it asked.
//
// WorkflowID is the IDENTITY, never a chat id. The envelope's chat id is the
// launching chat for an agent-parented run and empty for a parentless one, so the
// ask must be findable from the run in both cases.
//
// Question MAY BE EMPTY, and a consumer has to render that case. The ask registry
// is in memory, so a restart loses the text while the run stays parked; the read
// path then synthesises an ask from the paused leaf rather than stranding the
// user, and an empty question is what that ask carries.
//
// There is deliberately no Severity field. KAS's `send_message` carries four, and
// only `warning` parks a run — the other three advance, complete or fail the step
// with no wait — so the translator drops them and a severity here would be a
// field with one possible value.
type RunInputNeededPayload struct {
	WorkflowID string `json:"workflow_id"`
	// AskID is the ask's identity within its run, and the value an answer names.
	// Composed server-side with keyenc so it cannot be forged by a separator
	// inside one of its parts: the notification's own id for a live ask, the
	// paused leaf's node PATH for one synthesised after a restart. The second
	// spelling has to be deterministic, because the read path that mints it runs
	// on every refetch and a fresh id per read would stack duplicate cards.
	AskID string `json:"ask_id"`
	// NodeID addresses the asking step, and it is what every ask surface in this
	// app already joins on (the run card marks the row whose node id an ask
	// names). MAY BE EMPTY: KAS puts the node id on the notification only when the
	// caller is a step, and a run blocked by an unattributable ask is still
	// blocked, so the count travels separately from the row.
	//
	// There is deliberately no NodePath. The step-session registry holds a node
	// ID and no path, so a live ask could never carry one, and a field populated
	// on the restart-reconcile path alone would invite a reader to rely on it.
	NodeID string `json:"node_id"`
	// StepSessionID is the ANSWER ADDRESS: a `session/prompt` sent to the paused
	// step's own session is what KAS reroutes into the run. Empty when the notify
	// frame carried no caller session, in which case the answer path resolves it
	// from a fresh `inspect`.
	StepSessionID string `json:"step_session_id"`
	AgentName     string `json:"agent_name"`
	Question      string `json:"question"`
	AskedAt       string `json:"asked_at"`
}

// RunInputSettledPayload is the payload for type="run_input_settled": the ask is
// answered or waived, so every surface still showing it must retire the card.
//
// Its own event rather than a member of decision_settled, whose payload is keyed
// by an int64 JSON-RPC request id. A run ask has no open request behind it, so a
// second identity field on that payload would make one shape carry two mutually
// exclusive keys — one per family — and neither consumer could tell which to read
// without first knowing the kind.
type RunInputSettledPayload struct {
	WorkflowID string    `json:"workflow_id"`
	AskID      string    `json:"ask_id"`
	SettledBy  SettledBy `json:"settled_by"`
}

// RunAnswerRequest is POST /api/runs/{id}/answer's body: answer one parked step.
//
// Text empty is a 400 rather than a waive. Continuing without an answer is a
// DIFFERENT verb (POST /api/runs/{id}/step with status `running`), because it
// drives the step with KAS's default continuation instead of the user's words and
// a reader must not reach it by submitting an empty box.
type RunAnswerRequest struct {
	AskID string `json:"ask_id"`
	Text  string `json:"text"`
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

// LiveRun is one row of GET /api/runs/live: a run vibekit's own lease registry
// says is in flight, named with the chat whose agent launched it. ChatID is
// empty for a parentless run (manual, scheduled) and for a lease written
// before the field existed — both mean "no chat to exempt" to the consumer,
// the client's eviction sweep.
type LiveRun struct {
	WorkflowID string `json:"workflow_id"`
	ChatID     string `json:"chat_id"`
}

// LiveRunsResponse is GET /api/runs/live's reply. An envelope rather than a
// bare array, the GET /api/tabs precedent.
type LiveRunsResponse struct {
	Runs []LiveRun `json:"runs"`
}
