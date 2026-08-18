package hub

// The run read surface: GET /api/runs/{id}.
//
// ONE route, and the shape of what it returns is a deliberate non-decision: KAS's
// `state` and `nodePlan` pass through VERBATIM. The node tree is KAS's structure,
// it already carries every execution fact a reader wants (status, timings,
// agentName, modelId, sessionId, completionSignal, capturedOutput on each step),
// and a projected copy here would be a second representation of a thing vibekit
// does not own — one more contract to keep in sync for no reader benefit.
//
// TWO routes an earlier design specified are absent, each for a measured reason:
//
//   - `GET /api/runs` (the cross-chat list) existed for a run BOARD that is
//     deleted. The history surface already lists runs, from the same
//     `_kiro/workflow/list` call, beside previous chats — so a second endpoint
//     would return the same rows to no additional consumer.
//   - `GET /api/runs/{id}/steps/{nodeId}` (a step's own transcript) cannot be
//     served without new bridge machinery AND is not needed. A step session can
//     only be read with `session/load`, which on a chat's bridge would mount a
//     second session whose replay frames feed that CHAT's projection — splicing a
//     step's transcript into the conversation — and on the utility bridge would
//     displace the session the six generators share. Meanwhile a LIVE step's
//     content now reaches the launching chat's transcript correctly attributed
//     (see translate.ACPWorkflowMeta.SubtaskID), and a FINISHED step's product is
//     its `capturedOutput`, which `inspect` returns and the run view renders. The
//     transcript would be a third copy of content already in two places.
//
// The mutations here are the four run-control verbs: cancel, pause, resume and
// retry. All four are KAS's own, and this file only routes to them.
//
// That reverses a recorded decision, deliberately and with a reason. The
// previous note read "Runs have no controls (user decision): no Retry, no
// Continue, no Pause, no Resume, no Delete. The agent orchestrates and decides."
// It was taken when the assumption was that offering a control meant BUILDING
// one — a state machine vibekit would own, competing with the agent's own
// orchestration. The 2.16.1 sweep established that every verb is already a live
// handler in the pinned binary, so the choice was never build-or-not; it was
// route-or-not, and withholding the route left a paused run with no way forward
// except deleting the chat.
//
// What the original decision was right about is kept: vibekit adds no control of
// its own, no scheduling, no retry policy and no delete. Each route is one call
// to one native verb, gated on the statuses KAS itself accepts.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/workflow"
)

// rawInspectRun issues `_kiro/workflow/inspect` for one run and TYPES its failure
// at the boundary: an unregistered verb comes back wrapping
// workflow.ErrUnknownMethod, so callers ask errors.Is instead of re-reading KAS's
// error text. One helper rather than a copy per caller because the classification
// has to happen where the RPC error still carries its `error.data` — a caller
// that wrapped first and asked second would be sniffing its own message.
func (h *Hub) rawInspectRun(ctx context.Context, workflowID string) (json.RawMessage, error) {
	u := h.ensureUtility()
	cctx, cancel := context.WithTimeout(ctx, sessionListTimeout)
	defer cancel()
	raw, err := u.session.rawCall(cctx, "workflow inspect call", methodKiroWorkflowInspect,
		callerParams(map[string]any{keyWorkflowID: workflowID}))
	if err != nil {
		return nil, workflow.Classify(err)
	}
	return raw, nil
}

// handleRun: GET /api/runs/{workflowId} → one run's full state.
//
// Two things happen besides the passthrough, and both are about telling the user
// something true:
//
//  1. The step sessions in the returned tree are recorded, which is the ONLY
//     recovery path for step-frame attribution. `node_start` announces a step's
//     session id live, but a container restart empties that registry while the
//     run carries on — so a resumed run's frames would arrive on the chat's
//     connection with session ids nothing in this process ever announced, and be
//     classified as a subagent's. Reading a run is exactly when the durable copy
//     of that mapping is in hand.
//  2. A missing VERB is distinguished from a missing RUN. KAS answers an
//     unregistered `_kiro/workflow/*` name with a -32603 whose `error.data`
//     carries its persistence classifier's text, so "this build has no workflow
//     engine" is detectable — and reporting it as 404 told the user their run had
//     been deleted, which is a different and alarming thing.
func (h *Hub) handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		api.MethodNotAllowed(w, http.MethodGet)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		api.BadRequest(w, "missing workflow id")
		return
	}
	raw, err := h.rawInspectRun(r.Context(), id)
	if err != nil {
		if errors.Is(err, workflow.ErrUnknownMethod) {
			slog.Warn("workflow inspect: engine not available on this kiro-cli",
				"workflow_id", id, "detail", api.RPCDetails(err))
			api.WriteJSONStatus(w, http.StatusServiceUnavailable,
				map[string]string{"error": "the workflow engine is not available on this kiro-cli build"})
			return
		}
		slog.Warn("workflow inspect failed", "workflow_id", id,
			"error", err, "detail", api.RPCDetails(err))
		api.NotFound(w, "workflow run not found")
		return
	}
	h.translator.RecordRunSteps(raw)
	api.WriteRawJSON(w, raw)
}

// runStatus reads one run's current status via `_kiro/workflow/inspect`.
// Returns "" when the run is unknown, which the caller turns into a 404.
//
// Its own inspect call rather than a shared cache: a control decision must be
// made against the status as of NOW, and the run list the client rendered its
// buttons from is by definition older than the click. This is one round trip on
// a deliberate user action, not a hot path.
func (h *Hub) runStatus(ctx context.Context, workflowID string) (string, error) {
	raw, err := h.rawInspectRun(ctx, workflowID)
	if err != nil {
		// An unknown run and an unavailable engine are both "no status to gate
		// on" rather than a fault to report: the caller 404s, and the verb is
		// not attempted.
		if errors.Is(err, workflow.ErrUnknownMethod) {
			return "", nil
		}
		return "", err
	}
	var res struct {
		State struct {
			Status string `json:"status"`
		} `json:"state"`
	}
	if uErr := json.Unmarshal(raw, &res); uErr != nil {
		return "", uErr
	}
	return res.State.Status, nil
}

// handleRecipes: GET /api/recipes → the launchable recipe list, bundled +
// workspace, projected to the fields the Workflows tab renders.
func (h *Hub) handleRecipes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		api.MethodNotAllowed(w, http.MethodGet)
		return
	}
	recipes, err := h.listRecipes(r.Context())
	if err != nil {
		slog.Warn("recipe list failed", "error", err, "detail", api.RPCDetails(err))
		api.InternalError(w, errors.New("recipe list unavailable"))
		return
	}
	api.WriteJSON(w, api.RecipesResponse{Recipes: recipes})
}

// handleRunLaunch: POST /api/runs → launch one PARENTLESS run and answer with
// its id and name. 409 when the recipe already has a live run — the wire shape
// of the single-run rule, which is what keeps the Workflows row's Run ⇄ Cancel
// button able to name one run.
func (h *Hub) handleRunLaunch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		api.MethodNotAllowed(w, http.MethodPost)
		return
	}
	var req api.RunLaunchRequest
	if !api.DecodeJSON(w, r, &req) {
		return
	}
	id, name, err := h.LaunchRun(r.Context(), req.Source, req.Inputs)
	if err != nil {
		if errors.Is(err, errRecipeBusy) {
			api.Conflict(w, errRecipeBusy.Error())
			return
		}
		slog.Warn("run launch failed", "source", req.Source, "error", err, "detail", api.RPCDetails(err))
		// KAS's launch-time validation is precise (a bad input set, an
		// unregistered agent) and the message names the problem; forward it
		// rather than a generic sentinel, because the fix is the user's.
		api.BadRequest(w, api.RPCErrorText(err))
		return
	}
	api.WriteJSON(w, api.RunLaunchedResponse{WorkflowID: id, Name: name})
}

// handleRunCancel: POST /api/runs/{id}/cancel → ask the run to stop. The
// response confirms the ASK, not the stop: cancel is a node-boundary verb, so
// the terminal run_complete (and the run_finished SSE it becomes) follows at
// the in-flight node's end.
func (h *Hub) handleRunCancel(w http.ResponseWriter, r *http.Request) {
	h.runControlHandler(w, r, runVerbCancel)
}

// handleRunPause / handleRunResume are the two run-control verbs added once the
// sweep established they were already live server-side. Each is the same shape as
// cancel; the difference is which statuses permit them, which runVerb carries.
//
// Retry is absent by design -- see run_host.go, where the reason lives with the
// mechanism that causes it.
func (h *Hub) handleRunPause(w http.ResponseWriter, r *http.Request) {
	h.runControlHandler(w, r, runVerbPause)
}

func (h *Hub) handleRunResume(w http.ResponseWriter, r *http.Request) {
	h.runControlHandler(w, r, runVerbResume)
}

func (h *Hub) handleRunRetry(w http.ResponseWriter, r *http.Request) {
	h.runControlHandler(w, r, runVerbRetry)
}

// handleRunDelete: DELETE /api/runs/{id} — the History row's delete. Removes the
// run from KAS (its directory included) and drops vibekit's lease, timer and
// recorded end reason. Not recoverable, which is why it is the one run verb the
// client confirms before sending.
func (h *Hub) handleRunDelete(w http.ResponseWriter, r *http.Request) {
	h.runControlHandler(w, r, runVerbDelete)
}

// handleRunStepStatus: POST /api/runs/{id}/step — mark an IN-FLIGHT step
// completed or failed so a wedged run advances.
//
// Its own handler rather than a runVerb because it carries a body (which step,
// which status) and the verb table's issue signature is id-only.
func (h *Hub) handleRunStepStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		api.MethodNotAllowed(w, http.MethodPost)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		api.BadRequest(w, "missing workflow id")
		return
	}
	var body struct {
		NodeID string `json:"node_id"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		api.BadRequest(w, "invalid step-status payload")
		return
	}
	if err := h.SetRunStepStatus(r.Context(), id, body.NodeID, body.Status); err != nil {
		if errors.Is(err, errRunNotHosted) {
			api.Conflict(w, err.Error())
			return
		}
		api.BadRequest(w, err.Error())
		return
	}
	api.Ok(w)
}

// runVerb describes one run-control verb: how to issue it, and which run
// statuses it is legal from.
//
// The status gate exists because KAS's own refusals are throws that surface as a
// -32603 with the reason buried in `error.data`, and two of them are ordinary
// user timing rather than faults: clicking Retry on a run that just resumed, or
// Pause on one that just finished. Gating here turns those into a 409 naming the
// current status, and leaves -32603 to mean something actually went wrong.
//
// KAS remains the authority. The gate is a pre-check against a status read one
// round trip earlier, so a run that changes state in between still gets KAS's
// refusal, forwarded verbatim.
type runVerb struct {
	name  string
	issue func(*Hub, context.Context, string) error
	// method is the HTTP method the verb answers. Set EXPLICITLY on every verb
	// rather than defaulting to POST when empty: four of the five are POSTs and
	// the fifth is a DELETE, so an implicit default would make the odd one out the
	// only verb whose method is stated, which is the wrong way round.
	method string
	// from lists the statuses the verb is legal from. Empty means unrestricted.
	from []string
}

// The workflow status vocabulary is KAS's WorkflowStatusSchema:
// running | paused | completed | failed | aborted.
var (
	runVerbCancel = runVerb{
		name: "cancel",
		// Deliberately unrestricted. Cancel is the tab-close gesture and must
		// never be the verb that fails; KAS is idempotent on an
		// already-terminal run (it answers ok with the previous status).
		issue:  (*Hub).CancelRun,
		method: http.MethodPost,
	}
	runVerbPause = runVerb{
		name:   "pause",
		issue:  (*Hub).PauseRun,
		method: http.MethodPost,
		from:   []string{"running"},
	}
	runVerbResume = runVerb{
		name:   "resume",
		issue:  (*Hub).ResumeRun,
		method: http.MethodPost,
		from:   []string{"paused"},
	}
	// Retry's window is exactly the two statuses at which a run's own bridge has
	// already been closed, which is why RetryRun re-hosts instead of requiring
	// one. The gate still earns its place: it turns a click on an
	// already-restarted run into a 409 naming the status.
	runVerbRetry = runVerb{
		name:   "retry",
		issue:  (*Hub).RetryRun,
		method: http.MethodPost,
		from:   []string{"failed", "aborted"},
	}
	// Delete is unrestricted for the same reason cancel is, plus one of its own:
	// it is the only way a row leaves the History page, so a status that refused
	// it would be a row the user cannot get rid of. KAS cancels a non-terminal
	// run itself before removing it.
	runVerbDelete = runVerb{
		name:   "delete",
		issue:  (*Hub).DeleteRun,
		method: http.MethodDelete,
	}
)

func (h *Hub) runControlHandler(w http.ResponseWriter, r *http.Request, verb runVerb) {
	if r.Method != verb.method {
		api.MethodNotAllowed(w, verb.method)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		api.BadRequest(w, "missing workflow id")
		return
	}
	if len(verb.from) > 0 {
		status, err := h.runStatus(r.Context(), id)
		if err != nil {
			slog.Warn("run control: status read failed",
				"verb", verb.name, "workflow_id", id, "error", err, "detail", api.RPCDetails(err))
			api.InternalError(w, errors.New(verb.name+" failed"))
			return
		}
		if status == "" {
			api.NotFound(w, "run not found")
			return
		}
		if !slices.Contains(verb.from, status) {
			api.Conflict(w, verb.name+" is not available for a "+status+" run")
			return
		}
	}
	if err := verb.issue(h, r.Context(), id); err != nil {
		// A run with no live bridge here is a state of the world, not a fault, so
		// it earns a 409 naming the situation rather than a 500. See
		// hostedRunControl for which runs fall outside and why the utility bridge
		// is not a fallback for the verbs that execute.
		if errors.Is(err, errRunNotHosted) {
			slog.Info("run control unavailable: run not hosted here",
				"verb", verb.name, "workflow_id", id)
			api.Conflict(w, err.Error())
			return
		}
		slog.Warn("run control failed",
			"verb", verb.name, "workflow_id", id, "error", err, "detail", api.RPCDetails(err))
		api.InternalError(w, errors.New(verb.name+" failed"))
		return
	}
	api.Ok(w)
}
