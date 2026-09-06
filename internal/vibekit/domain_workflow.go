package vibekit

// Workflow-run domain types: the EVENT surface and nothing more. A run is KAS's
// entity and vibekit persists nothing about it, so there is no Run struct, no node
// tree and no plan model here — `_kiro/workflow/inspect` returns all three and GET
// /api/runs/{id} passes them through verbatim. There is deliberately no
// step-NARRATION event either: KAS's nine notification kinds carry neither a
// severity nor a message, so nothing could emit one.

import "encoding/json"

// RunProgressKind is the KAS lifecycle kind behind a run_progress event: seven of
// KAS's nine. The other two are their own events, because one inserts a row and
// the other is terminal and fires a push, while every kind between them carries
// the same instruction — refetch.
type RunProgressKind string

// The seven progress kinds, spelled as KAS's method suffixes so one string greps
// across both codebases.
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
// Scheduled marks a run the SCHEDULER launched, and it travels because only the
// launch path knows: a manual launch is parentless too, so events cannot separate
// the two. It drives the client's start signal — a manual launch already has the
// user's attention, a scheduled one began with nobody looking.
type RunStartedPayload struct {
	WorkflowID string `json:"workflow_id"`
	Name       string `json:"name,omitempty"`
	Scheduled  bool   `json:"scheduled,omitempty"`
}

// RunProgressPayload is the payload for type="run_progress": an INVALIDATION
// signal, deliberately too thin to reconstruct a run from. `run_start` re-fires on
// every resume and `node_complete` carries neither iteration nor branchId, so an
// accumulating client could not tell two repeat iterations apart. NodeID is absent
// on the run-level `paused` and holds the loop id on `loop_iteration`.
type RunProgressPayload struct {
	WorkflowID string          `json:"workflow_id"`
	NodeID     string          `json:"node_id,omitempty"`
	Kind       RunProgressKind `json:"kind"`
}

// RunFinishedPayload is the payload for type="run_finished": terminal. Status is
// KAS's own run-level status, `paused` included — a policy pause at
// `onMaxIterations` reports through here, because KAS emits `run_complete` for it.
//
// There is deliberately no aborted_by_restart flag: a restart PAUSES a run and
// KAS's read-path reconcile has no path to aborted, so nothing would fill it.
// Name is here for RunStartedPayload's reason, and empty when KAS sends no state.
type RunFinishedPayload struct {
	WorkflowID string `json:"workflow_id"`
	Status     string `json:"status"`
	Name       string `json:"name,omitempty"`
}

// RunStepKind discriminates what a run_step frame carries: the three block kinds a
// transcript already renders, because a step's content IS a transcript.
type RunStepKind string

// The three run-step kinds.
const (
	// RunStepText is a delta of the step agent's own prose.
	RunStepText RunStepKind = "text"
	// RunStepThinking is a delta of its reasoning.
	RunStepThinking RunStepKind = "thinking"
	// RunStepTool is one tool call, whole: sent on create and on every update,
	// folded server-side, so a client renders the frame it holds and accumulates
	// nothing. The one surface with no endpoint to refetch.
	RunStepTool RunStepKind = "tool"
)

// RunStepPayload is the payload for type="run_step".
//
// NodePath, not NodeID, because a repeat's iterations share a node id and two
// passes of a loop body would write into each other's rows. NOT byte-identical to
// `inspect`'s state tree: KAS spells an iteration container `iter-<n>` here and
// `<repeatId>#<n>` there, so the client translates. ToolCall is whole because a
// parentless run has no chat and so no buffer to fold into.
type RunStepPayload struct {
	ToolCall   *ToolCall   `json:"tool_call,omitempty"`
	WorkflowID string      `json:"workflow_id"`
	NodePath   string      `json:"node_path"`
	Kind       RunStepKind `json:"kind"`
	Delta      string      `json:"delta,omitempty"`
}

// RunStepTranscriptState is the verdict GET /api/runs/{id}/steps/{path...} answers
// with. A registered wire enum, so the three values have one definition across both
// languages and a client's branch over them is total. Three rather than a
// 200-with-an-empty-list, because flattening the outcomes makes every client policy
// a guess.
type RunStepTranscriptState string

// The three step-transcript verdicts.
const (
	// RunStepTranscriptReady: the transcript was read. Messages may still be EMPTY,
	// which is its own fact — the step ran and produced no prose.
	RunStepTranscriptReady RunStepTranscriptState = "ready"
	// RunStepTranscriptGone: KAS holds no session for the step, so there is nothing
	// to serve and never will be. A step that never started answers this too.
	RunStepTranscriptGone RunStepTranscriptState = "gone"
	// RunStepTranscriptUnavailable: the read failed. TRANSIENT, and the only verdict
	// a client may retry.
	RunStepTranscriptUnavailable RunStepTranscriptState = "unavailable"
)

// RunStepTranscript is GET /api/runs/{id}/steps/{path...}'s reply, projected from
// KAS's replay per request and PERSISTED BY NOTHING. It exists because a step's
// transcript is on no other endpoint: `inspect` carries what a step DECLARED.
//
// NO `omitempty` on ANY field, deliberately: the generator emits a REQUIRED
// TypeScript field without it, which stops a client inventing a fallback for the
// verdict. An optional `state` would read as "assume ready".
type RunStepTranscript struct {
	// WorkflowID and NodePath echo the request, so a client holding several reads in
	// flight tells the answers apart without correlating.
	WorkflowID string                 `json:"workflow_id"`
	NodePath   string                 `json:"node_path"`
	State      RunStepTranscriptState `json:"state"`
	// Messages is the step's transcript, filtered to the ASSISTANT rows. Empty on any
	// state but ready, and legitimately empty on ready.
	Messages []Message `json:"messages"`
}

// RunInputNeededPayload is the payload for type="run_input_needed": a workflow STEP
// asked a question and the run is parked until somebody answers it. It carries its
// payload rather than invalidating because the text is on no endpoint — KAS parks
// with one fixed literal in `state.pauseReason` — and WorkflowID is the IDENTITY,
// since the envelope's chat id is empty for a parentless run. Question MAY BE
// EMPTY: the ask registry is in memory, so a restart loses the text while the run
// stays parked. No Severity field — only `warning` parks a run.
type RunInputNeededPayload struct {
	WorkflowID string `json:"workflow_id"`
	// AskID is composed with keyenc so a separator inside one part cannot forge it.
	// The after-restart spelling must be DETERMINISTIC, or the read path that mints it
	// on every refetch stacks duplicate cards.
	AskID string `json:"ask_id"`
	// NodeID addresses the asking step. MAY BE EMPTY — KAS sets it only for a step
	// caller, and a run blocked by an unattributable ask is still blocked. No
	// NodePath: the step-session registry holds no path, so a live ask could not.
	NodeID string `json:"node_id"`
	// StepSessionID is the ANSWER ADDRESS: KAS reroutes a `session/prompt` sent to the
	// paused step's session into the run. Empty when the notify frame carried no
	// caller session, and the answer path resolves it from a fresh `inspect`.
	StepSessionID string `json:"step_session_id"`
	AgentName     string `json:"agent_name"`
	Question      string `json:"question"`
	AskedAt       string `json:"asked_at"`
}

// RunInputSettledPayload is the payload for type="run_input_settled": the ask is
// answered or waived, so every surface still showing it must retire the card.
//
// Its own event rather than a member of decision_settled, which is keyed by an
// int64 JSON-RPC request id: a run ask has no open request, so sharing that payload
// would give one shape two mutually exclusive identity keys.
type RunInputSettledPayload struct {
	WorkflowID string    `json:"workflow_id"`
	AskID      string    `json:"ask_id"`
	SettledBy  SettledBy `json:"settled_by"`
}

// RunAnswerRequest is POST /api/runs/{id}/answer's body: answer one parked step.
//
// Empty Text is a 400 rather than a waive. Continuing without an answer is a
// DIFFERENT verb (POST /api/runs/{id}/step with status `running`) — it drives the
// step with KAS's default continuation, and an empty box must not reach it.
type RunAnswerRequest struct {
	AskID string `json:"ask_id"`
	Text  string `json:"text"`
}

// RunLaunchRequest is POST /api/runs's body: launch one recipe, PARENTLESS.
//
// Source is `_kiro/workflow/listRecipes`' own value verbatim. The server
// re-validates it against a fresh listRecipes call rather than trusting it, so the
// endpoint cannot be steered at an arbitrary file even though the value looks like
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
	// Plan is KAS's node plan, forwarded VERBATIM: what the recipe will do and on
	// which model, raw for the reason at the top of this file. Last of the
	// pointer-bearing fields on purpose — a slice's len/cap words end the GC scan
	// region where a trailing string would extend it.
	Plan    json.RawMessage `json:"plan,omitempty"`
	BuiltIn bool            `json:"built_in,omitempty"`
}

// RecipesResponse is GET /api/recipes's reply.
type RecipesResponse struct {
	Recipes []Recipe `json:"recipes"`
}

// LiveRun is one row of GET /api/runs/live: a run vibekit's own lease registry says
// is in flight, named with the chat whose agent launched it. ChatID is empty for a
// parentless run and for a lease predating the field — both mean "no chat to launch
// a tab under". Executing is a FIELD rather than a filter applied here because
// three consumers ask this row three different questions, and filtering for the
// eviction sweep would take the row away from the tab dot and the tab parent.
type LiveRun struct {
	WorkflowID string `json:"workflow_id"`
	ChatID     string `json:"chat_id"`
	// Executing reports whether THIS PROCESS holds a deadline for the run. NOT
	// `status` and none of KAS's five, because serving one would mean an `inspect` per
	// lease on the client's boot path (runlease.Lease.Deadline owns the answer).
	//
	// It can read false while KAS says running — a lease read back from disk is
	// parked, and `set_step_status` advances a step without re-arming — and both are
	// accepted: the client corrects itself on the run's next progress frame.
	Executing bool `json:"executing"`
}

// LiveRunsResponse is GET /api/runs/live's reply. An envelope rather than a
// bare array, the GET /api/tabs precedent.
type LiveRunsResponse struct {
	Runs []LiveRun `json:"runs"`
}
