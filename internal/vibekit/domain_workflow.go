package vibekit

// Workflow-run domain types: the EVENT surface only.
//
// A run is KAS's entity — its state lives under ~/.kiro/sessions/<hash>/workflows/
// and vibekit persists nothing about it — so there is deliberately no Run struct,
// node tree or plan model here: `_kiro/workflow/inspect` returns all three, and
// GET /api/runs/{id} passes its `state` and `nodePlan` through verbatim.

import "encoding/json"

// RunProgressKind is the KAS lifecycle kind behind a run_progress event: seven of
// KAS's nine notification kinds. run_start and run_complete are their own events,
// because one inserts a row and the other is terminal and fires a push.
type RunProgressKind string

// Spelled exactly as KAS's method suffixes, so one string greps across both codebases.
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
// chat. Name is carried because a client that has never fetched this run has
// nothing to label the row with.
//
// Scheduled exists because the client cannot derive it: a MANUAL launch is
// parentless too, so `parentSessionId` is empty for both and events cannot
// separate them — only the launch path knows. Absent on the wire rather than
// false for a manual run, so an older client and a manual launch read alike.
type RunStartedPayload struct {
	WorkflowID string `json:"workflow_id"`
	Name       string `json:"name,omitempty"`
	Scheduled  bool   `json:"scheduled,omitempty"`
}

// RunProgressPayload is the payload for type="run_progress": what happened to ONE
// node of a run, addressed by its path.
//
// Path, not id, because KAS re-fires `run_start` on every resume and duplicates
// progress frames across one, and `node_complete` carries neither `iteration` nor
// `branchId` — so a client accumulating by node id alone cannot tell two passes of
// a loop body apart. An idempotent write addressed by path replays safely.
// `loop_iteration`, `steps_queued` and `paused` carry no node state and refetch.
type RunProgressPayload struct {
	WorkflowID string `json:"workflow_id"`
	NodeID     string `json:"node_id,omitempty"`
	// NodePath addresses ONE execution of a node, joined with "/" — the same
	// spelling RunStepPayload.NodePath uses. Empty on the run-level and
	// shape-changing kinds, which is what tells the client to refetch instead.
	NodePath string `json:"node_path,omitempty"`
	// Status is the node's status after this frame, in KAS's own NodeState
	// vocabulary so it drops straight onto the cached tree.
	Status string `json:"status,omitempty"`
	// StartedAt and EndedAt are RFC 3339, stamped by the SERVER at frame arrival:
	// KAS puts no timestamp on either lifecycle frame. A later refetch overwrites
	// both with KAS's own values.
	StartedAt string `json:"started_at,omitempty"`
	EndedAt   string `json:"ended_at,omitempty"`
	// FailureReason is KAS's own explanation for a node that failed. Empty on
	// every other outcome.
	FailureReason string          `json:"failure_reason,omitempty"`
	Kind          RunProgressKind `json:"kind"`
}

// RunFinishedPayload is the payload for type="run_finished": terminal. Status is
// KAS's own run-level status (completed / failed / aborted / paused — a policy
// pause at `onMaxIterations` reports through here too, since KAS emits
// `run_complete` for it). There is no aborted_by_restart flag: a restart PAUSES a
// run, so there is nothing for one to mean.
//
// Name is read out of KAS's `finalState` for a client that never saw the start
// frame; empty when KAS sends no state, and the consumer falls back to a label.
type RunFinishedPayload struct {
	WorkflowID string `json:"workflow_id"`
	Status     string `json:"status"`
	Name       string `json:"name,omitempty"`
}

// RunStepKind discriminates what a run_step frame carries: the same three block
// kinds a transcript already renders, because a step's content IS a transcript.
type RunStepKind string

// The three run-step kinds.
const (
	// RunStepText is a delta of the step agent's own prose.
	RunStepText RunStepKind = "text"
	// RunStepThinking is a delta of its reasoning.
	RunStepThinking RunStepKind = "thinking"
	// RunStepTool is one tool call, whole: sent on create AND on every update,
	// folded server-side, so a client renders from the frame it holds.
	RunStepTool RunStepKind = "tool"
)

// RunStepPayload is the payload for type="run_step".
//
// NodePath, not NodeID: a repeat's iterations share a node id, so an id cannot
// address one execution. Joined with "/" and NOT byte-identical to what a client
// derives from `inspect`'s tree — KAS spells a repeat's iteration container
// `iter-<n>` here and `<repeatId>#<n>` there, so the client translates. ToolCall
// is whole rather than a delta because a parentless run has no chat, so nothing
// at this end accumulates its content; the translator folds and sends the value.
type RunStepPayload struct {
	ToolCall   *ToolCall   `json:"tool_call,omitempty"`
	WorkflowID string      `json:"workflow_id"`
	NodePath   string      `json:"node_path"`
	Kind       RunStepKind `json:"kind"`
	Delta      string      `json:"delta,omitempty"`
}

// RunInputNeededPayload is the payload for type="run_input_needed": a workflow
// STEP asked a question and the run is parked until somebody answers.
//
// It carries its payload because the question text is on no endpoint — KAS parks
// with one fixed literal in `state.pauseReason` and an empty `pauseDetail`.
// Question MAY BE EMPTY and a consumer must render that: the ask registry is in
// memory, so a restart loses the text while the run stays parked, and the read
// path then synthesises an ask from the paused leaf rather than stranding anyone.
type RunInputNeededPayload struct {
	WorkflowID string `json:"workflow_id"`
	// AskID is the ask's identity within its run and the value an answer names.
	// Composed server-side with keyenc so a separator inside one of its parts
	// cannot forge it. The post-restart spelling is deterministic, because the
	// read path mints it on every refetch and a fresh id would stack duplicates.
	AskID string `json:"ask_id"`
	// NodeID addresses the asking step and is what every ask surface joins on. MAY
	// BE EMPTY: KAS puts the node id on the notification only when the caller is a
	// step, and a run blocked by an unattributable ask is still blocked.
	NodeID string `json:"node_id"`
	// StepSessionID is the ANSWER ADDRESS: a `session/prompt` sent to the paused
	// step's own session is what KAS reroutes into the run. Empty when the notify
	// frame carried no caller session; the answer path resolves it from `inspect`.
	StepSessionID string `json:"step_session_id"`
	AgentName     string `json:"agent_name"`
	Question      string `json:"question"`
	AskedAt       string `json:"asked_at"`
}

// RunInputSettledPayload is the payload for type="run_input_settled": the ask is
// answered or waived, so every surface still showing it must retire the card.
// Separate from decision_settled, whose payload is keyed by a JSON-RPC request id
// a run ask does not have.
type RunInputSettledPayload struct {
	WorkflowID string    `json:"workflow_id"`
	AskID      string    `json:"ask_id"`
	SettledBy  SettledBy `json:"settled_by"`
}

// RunAnswerRequest is POST /api/runs/{id}/answer's body: answer one parked step.
// Text empty is a 400 rather than a waive — continuing without an answer is a
// different verb (POST /api/runs/{id}/step with status `running`), which drives
// the step with KAS's default continuation instead of the user's words.
type RunAnswerRequest struct {
	AskID string `json:"ask_id"`
	Text  string `json:"text"`
}

// RunLaunchRequest is POST /api/runs's body: launch one recipe, PARENTLESS. The
// recipe is named by its `source` exactly as `_kiro/workflow/listRecipes` reported
// it. The server re-validates that value against a fresh listRecipes call rather
// than trusting it, so the endpoint cannot be steered at an arbitrary file even
// though the wire value looks like a path.
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
	// Plan is KAS's node plan for the recipe, forwarded VERBATIM: an array of node
	// descriptors saying what the recipe will do and on which model. Raw rather
	// than modelled, for the reason at the top of this file. Last of the
	// pointer-bearing fields so the slice's len/cap words end the GC scan region.
	Plan    json.RawMessage `json:"plan,omitempty"`
	BuiltIn bool            `json:"built_in,omitempty"`
}

// RecipesResponse is GET /api/recipes's reply.
type RecipesResponse struct {
	Recipes []Recipe `json:"recipes"`
}

// LiveRun is one row of GET /api/runs/live: a run vibekit's own lease registry
// says is in flight, named with the chat whose agent launched it. ChatID is empty
// for a parentless run and for a lease written before the field existed — both
// mean "no chat to exempt" to the client's eviction sweep.
type LiveRun struct {
	WorkflowID string `json:"workflow_id"`
	ChatID     string `json:"chat_id"`
}

// LiveRunsResponse is GET /api/runs/live's reply.
type LiveRunsResponse struct {
	Runs []LiveRun `json:"runs"`
}

// RunControlsResponse is GET /api/runs/{id}/controls's reply: what may be done to
// one run, and why not for the rest.
//
// Its own route rather than a field on GET /api/runs/{id}, a verbatim KAS
// passthrough. The client used to decide this from a status table plus an SSE-fed
// cache of which chat launched the run, so any reloaded client read a
// chat-parented run as parentless. Only the server sees all three inputs.
type RunControlsResponse struct {
	// Refused maps a verb this run does not offer to the one sentence a reader
	// needs, and carries only a verb whose absence would otherwise be unexplained.
	Refused map[string]string `json:"refused,omitempty"`
	// ParentChatID names the chat whose agent launched the run, empty for a
	// parentless one. Read from the chat store here rather than from an event-fed
	// client cache, which is empty after a reload.
	ParentChatID string `json:"parent_chat_id"`
	// Verbs are the offered controls, in row order. Strings rather than a
	// registered enum because the client's label table is the narrowing point: an
	// unlabelled verb is dropped, so a future one degrades to a missing button.
	Verbs []string `json:"verbs"`
}

// RunRetriedResponse is POST /api/runs/{id}/retry's reply: KAS's own outcome
// report, forwarded rather than collapsed to `{"ok":true}`. RetriedNodeIDs is why
// the route exists — a retry that resets five nodes and one that resets none are
// otherwise the same HTTP result, and the second is what "nothing happened" is.
type RunRetriedResponse struct {
	// Status is the run's status after the reset, as KAS reports it.
	Status         string   `json:"status"`
	RetriedNodeIDs []string `json:"retried_node_ids"`
}
