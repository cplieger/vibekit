package agent

// The run read surface: GET /api/runs/{id}.
//
// KAS's `state` and `nodePlan` pass through VERBATIM: the node tree is KAS's
// structure and already carries every execution fact a reader wants, so a projected
// copy here would be a second representation of a thing vibekit does not own. The
// four run-control verbs are KAS's own too; this file only routes to them.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/vibekit/internal/rpcerr"
	"github.com/cplieger/vibekit/internal/vibekit"
	"github.com/cplieger/vibekit/internal/workflow"
	"github.com/cplieger/webhttp/v2"
)

// handleRun: GET /api/runs/{workflowId} → one run's full state.
//
// Two things happen besides the passthrough: the step sessions in the returned tree
// are recorded, which is the only recovery path for step-frame attribution after a
// restart empties that registry while the run carries on; and a missing VERB is
// distinguished from a missing RUN, so "this build has no workflow engine" does not
// report as "your run was deleted".
func (rr *runRoutes) handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		httpreply.BadRequest(w, "missing workflow id")
		return
	}
	raw, err := rr.runs.rawInspect(r.Context(), id)
	if err != nil {
		if errors.Is(err, workflow.ErrUnknownMethod) {
			slog.Warn("workflow inspect: engine not available on this kiro-cli",
				"workflow_id", id, "detail", rpcerr.Details(err))
			webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable,
				map[string]string{"error": "the workflow engine is not available on this kiro-cli build"})
			return
		}
		slog.Warn("workflow inspect failed", "workflow_id", id,
			"error", err, "detail", rpcerr.Details(err))
		httpreply.NotFound(w, "workflow run not found")
		return
	}
	rr.runs.translate.RecordRunSteps(raw)
	// The container-restart path: the ask registry is in memory while the run is
	// not. The response stays a VERBATIM passthrough — a synthesised ask travels on
	// the `run_input_needed` SSE instead of being spliced into `raw`.
	rr.runs.reconcileNeedInput(r.Context(), id, raw)
	httpreply.WriteRawJSON(w, raw)
}

// handleLiveRuns: GET /api/runs/live → every live lease, projected to
// `{workflow_id, chat_id}`.
//
// PRESENCE-based, over vibekit-local state only: a lease exists if and only if
// vibekit put the run on the wire and no terminal transition released it, so no KAS
// call is needed. Staleness errs toward keeping — a missed terminal frame leaves the
// lease live until the next boot sweep, which costs memory, never correctness.
func (rr *runRoutes) handleLiveRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	held := rr.runs.leaseStore().List()
	out := vibekit.LiveRunsResponse{Runs: make([]vibekit.LiveRun, 0, len(held))}
	for i := range held {
		out.Runs = append(out.Runs, vibekit.LiveRun{
			WorkflowID: held[i].WorkflowID,
			ChatID:     held[i].ChatID,
		})
	}
	webhttp.WriteJSON(w, out)
}

// status reads one run's current status via `_kiro/workflow/inspect`, returning ""
// when the run is unknown, which the caller turns into a 404. Its own inspect call
// rather than a shared cache: a control decision must rest on the status as of NOW.
func (rr *runRoutes) status(ctx context.Context, workflowID string) (string, error) {
	raw, err := rr.runs.rawInspect(ctx, workflowID)
	if err != nil {
		// An unknown run and an unavailable engine are both "no status to gate on"
		// rather than a fault: the caller 404s and the verb is not attempted.
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
func (rr *runRoutes) handleRecipes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	recipes, err := rr.runs.listRecipes(r.Context())
	if err != nil {
		slog.Warn("recipe list failed", "error", err, "detail", rpcerr.Details(err))
		httpreply.InternalError(w, errors.New("recipe list unavailable"))
		return
	}
	webhttp.WriteJSON(w, vibekit.RecipesResponse{Recipes: recipes})
}

// handleLaunch: POST /api/runs → launch one PARENTLESS run and answer with its id
// and name. 409 when the recipe already has a live run — the wire shape of the
// single-run rule, which keeps the Workflows row's Run ⇄ Cancel button naming one run.
func (rr *runRoutes) handleLaunch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	var req vibekit.RunLaunchRequest
	if !httpreply.DecodeJSON(w, r, &req) {
		return
	}
	id, name, err := rr.runs.Launch(r.Context(), req.Source, req.Inputs)
	if err != nil {
		if errors.Is(err, errRecipeBusy) {
			httpreply.Conflict(w, errRecipeBusy.Error())
			return
		}
		slog.Warn("run launch failed", "source", req.Source, "error", err, "detail", rpcerr.Details(err))
		// KAS's launch-time validation names the problem precisely (a bad input
		// set, an unregistered agent), and the fix is the user's, so forward it.
		httpreply.BadRequest(w, rpcerr.Text(err))
		return
	}
	webhttp.WriteJSON(w, vibekit.RunLaunchedResponse{WorkflowID: id, Name: name})
}

// handleCancel: POST /api/runs/{id}/cancel → ask the run to stop. The response
// confirms the ASK, not the stop: cancel is a node-boundary verb, so the terminal
// run_complete follows at the in-flight node's end.
func (rr *runRoutes) handleCancel(w http.ResponseWriter, r *http.Request) {
	rr.controlHandler(w, r, runVerbCancel)
}

// handleControls: GET /api/runs/{id}/controls → what may be done to this run, and
// one sentence per verb it refuses. See vibekit.RunControlsResponse for why the
// server owns the answer.
//
// Its own route rather than a field on the passthrough above, which must stay a
// verbatim relay. The client fetches it on pointing at a run and again on that
// run's `run_finished`, so it is one request per tab open, not per repaint.
func (rr *runRoutes) handleControls(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		httpreply.BadRequest(w, "missing workflow id")
		return
	}
	status, err := rr.status(r.Context(), id)
	if err != nil {
		slog.Warn("run controls: status read failed",
			"workflow_id", id, "error", err, "detail", rpcerr.Details(err))
		httpreply.InternalError(w, errors.New("run controls unavailable"))
		return
	}
	if status == "" {
		httpreply.NotFound(w, "run not found")
		return
	}
	aff := rr.runs.affordance(r.Context(), id, status)
	webhttp.WriteJSON(w, vibekit.RunControlsResponse{
		Verbs:        aff.Verbs,
		Refused:      aff.Refused,
		ParentChatID: string(aff.ParentChat),
	})
}

// handlePause and handleResume are the same shape as cancel; the difference is
// which statuses permit them, which the affordance table carries.
func (rr *runRoutes) handlePause(w http.ResponseWriter, r *http.Request) {
	rr.controlHandler(w, r, runVerbPause)
}

func (rr *runRoutes) handleResume(w http.ResponseWriter, r *http.Request) {
	rr.controlHandler(w, r, runVerbResume)
}

// handleRetry: POST /api/runs/{id}/retry → reset the run's failed work and report
// WHAT WAS RESET. Its own handler rather than a runVerb, for handleStepStatus's
// reason inverted: the verb table's issue signature answers `error` alone, and
// retry's reply is the outcome — collapsed to `{"ok":true}`, a retry that reset zero
// nodes is indistinguishable from one that reset five.
func (rr *runRoutes) handleRetry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		httpreply.BadRequest(w, "missing workflow id")
		return
	}
	aff, ok := rr.permits(w, r, verbRetry, id)
	if !ok {
		return
	}
	// The gate's own answer, forwarded: it carries the run's parent chat, which is
	// what the verb needs to find the process that holds the run.
	out, err := rr.runs.Retry(r.Context(), id, aff)
	if err != nil {
		rr.writeControlErr(w, verbRetry, id, err)
		return
	}
	webhttp.WriteJSON(w, out)
}

// handleDelete: DELETE /api/runs/{id} — the History row's delete. Removes the run
// from KAS (its directory included) and drops vibekit's lease, timer and recorded
// end reason. Not recoverable, which is why the client confirms it first.
func (rr *runRoutes) handleDelete(w http.ResponseWriter, r *http.Request) {
	rr.controlHandler(w, r, runVerbDelete)
}

// handleStepStatus: POST /api/runs/{id}/step — mark an IN-FLIGHT step completed or
// failed so a wedged run advances. Its own handler rather than a runVerb because it
// carries a body, where the verb table's issue signature is id-only.
func (rr *runRoutes) handleStepStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		httpreply.BadRequest(w, "missing workflow id")
		return
	}
	var body struct {
		NodeID string `json:"node_id"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpreply.BadRequest(w, "invalid step-status payload")
		return
	}
	if err := rr.runs.SetStepStatus(r.Context(), id, body.NodeID, body.Status); err != nil {
		if errors.Is(err, errRunNotHosted) {
			httpreply.Conflict(w, err.Error())
			return
		}
		httpreply.BadRequest(w, err.Error())
		return
	}
	webhttp.Ok(w)
}

// handleAnswer: POST /api/runs/{id}/answer — answer a step parked on a question.
// Its own handler rather than a runVerb because it carries a body.
//
// A REST call on the run surface rather than a `/api/command` envelope: every other
// run mutation is REST, and this ask is not chat-scoped — a parentless run's ask has
// no chat id to put in that envelope.
func (rr *runRoutes) handleAnswer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		httpreply.BadRequest(w, "missing workflow id")
		return
	}
	var body vibekit.RunAnswerRequest
	if !httpreply.DecodeJSON(w, r, &body) {
		return
	}
	err := rr.runs.AnswerInput(r.Context(), id, body.AskID, body.Text)
	switch {
	case err == nil:
		webhttp.Ok(w)
	case errors.Is(err, errAskAlreadySettled):
		// A state of the world rather than a fault: another surface got there
		// first, or the step moved on.
		httpreply.Conflict(w, err.Error())
	case errors.Is(err, errRunNotHosted):
		httpreply.Conflict(w, err.Error())
	default:
		slog.Warn("run answer failed", "workflow_id", id, "ask_id", body.AskID,
			"error", err, "detail", rpcerr.Details(err))
		httpreply.BadRequest(w, rpcerr.Text(err))
	}
}

// runVerb describes one run-control verb: how to issue it, and whether it is gated
// on the run's affordance. The gate exists because KAS's own refusals are throws
// surfacing as a -32603 with the reason buried in `error.data`, and two of them are
// ordinary user timing rather than faults. WHICH statuses a verb is legal from lives
// in run_affordance.go, so one table decides both what the client draws and what
// this route accepts. KAS remains the authority: this is a pre-check against a
// status read one round trip earlier, and its refusal is forwarded, not anonymised.
type runVerb struct {
	name  string
	issue func(*Runs, context.Context, string) error
	// method is set EXPLICITLY on every verb rather than defaulting to POST, so the
	// lone DELETE is not the only one whose method is stated.
	method string
	// gated asks the affordance before issuing. False means unrestricted.
	gated bool
}

var (
	runVerbCancel = runVerb{
		name: verbCancel,
		// Deliberately unrestricted: cancel is the tab-close gesture and must never
		// be the verb that fails; KAS is idempotent on an already-terminal run.
		issue:  (*Runs).Cancel,
		method: http.MethodPost,
	}
	runVerbPause = runVerb{
		name:   verbPause,
		issue:  (*Runs).Pause,
		method: http.MethodPost,
		gated:  true,
	}
	runVerbResume = runVerb{
		name:   verbResume,
		issue:  (*Runs).Resume,
		method: http.MethodPost,
		gated:  true,
	}
	// Delete is unrestricted for the same reason cancel is, plus one of its own: it
	// is the only way a row leaves the History page.
	runVerbDelete = runVerb{
		name:   verbDelete,
		issue:  (*Runs).Delete,
		method: http.MethodDelete,
	}
)

// verbDelete is the History row's delete. Deliberately outside run_affordance.go's
// table: it is never offered as a control, so listing it there would put a verb in
// the row's vocabulary that the row must not draw.
const verbDelete = "delete"

// permits gates one verb on the run's affordance, writing the refusal itself, and
// hands the affordance back so the verb can act on the ANSWER rather than resolving
// the same facts again. The refusal carries the affordance's own sentence when it
// has one, and falls back to naming the status when the status alone is the reason.
func (rr *runRoutes) permits(
	w http.ResponseWriter, r *http.Request, verb, id string,
) (runAffordance, bool) {
	status, err := rr.status(r.Context(), id)
	if err != nil {
		slog.Warn("run control: status read failed",
			"verb", verb, "workflow_id", id, "error", err, "detail", rpcerr.Details(err))
		httpreply.InternalError(w, errors.New(verb+" failed"))
		return runAffordance{}, false
	}
	if status == "" {
		httpreply.NotFound(w, "run not found")
		return runAffordance{}, false
	}
	aff := rr.runs.affordance(r.Context(), id, status)
	if aff.permits(verb) {
		return aff, true
	}
	slog.Info("run control refused", "verb", verb, "workflow_id", id, "status", status)
	if sentence := aff.refusal(verb); sentence != "" {
		httpreply.Conflict(w, sentence)
		return aff, false
	}
	httpreply.Conflict(w, verb+" is not available for a "+status+" run")
	return aff, false
}

// writeControlErr answers a verb that reached KAS and failed, distinguishing a
// refusal from a fault. InternalError is kept for what it is for: a fault that is
// not the reader's to act on. Anonymising the rest sent the reader the constant
// "internal error" while the actionable sentence went only to the container log.
func (rr *runRoutes) writeControlErr(w http.ResponseWriter, verb, id string, err error) {
	switch {
	case errors.Is(err, errRunNotHosted):
		// A state of the world, not a fault.
		slog.Info("run control unavailable: run not hosted here", "verb", verb, "workflow_id", id)
		httpreply.Conflict(w, err.Error())
	case errors.Is(err, errRetryEngineSlow):
		slog.Warn("run control timed out starting an engine", "verb", verb, "workflow_id", id)
		webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable, httpreply.ErrorJSON(err.Error()))
	case errors.Is(err, errRetryOutcomeUnreadable):
		// The engine ACCEPTED the verb and only its report is unusable, so 502: the
		// sentence sends the reader to a refresh rather than to a second retry of
		// work that may already be running.
		slog.Warn("run control landed but its report could not be read",
			"verb", verb, "workflow_id", id, "error", err)
		webhttp.WriteJSONStatus(w, http.StatusBadGateway,
			httpreply.ErrorJSON(errRetryOutcomeUnreadable.Error()))
	case isRPCRefusal(err):
		// KAS declined. Its sentence names the reason and the fix is frequently the
		// reader's, so it is forwarded rather than replaced by a sentinel.
		slog.Info("run control refused by the workflow engine",
			"verb", verb, "workflow_id", id, "detail", rpcerr.Details(err))
		httpreply.Conflict(w, rpcerr.Text(err))
	default:
		slog.Warn("run control failed",
			"verb", verb, "workflow_id", id, "error", err, "detail", rpcerr.Details(err))
		httpreply.InternalError(w, errors.New(verb+" failed"))
	}
}

// isRPCRefusal reports whether the failure is KAS's own answer rather than a fault
// on this side. Matched at any wrapping depth, because the verb helpers wrap with
// fmt.Errorf on the way up.
func isRPCRefusal(err error) bool {
	_, ok := errors.AsType[*vibekit.RPCError](err)
	return ok
}

func (rr *runRoutes) controlHandler(w http.ResponseWriter, r *http.Request, verb runVerb) {
	if r.Method != verb.method {
		httpreply.MethodNotAllowed(w, verb.method)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		httpreply.BadRequest(w, "missing workflow id")
		return
	}
	if verb.gated {
		if _, ok := rr.permits(w, r, verb.name, id); !ok {
			return
		}
	}
	if err := verb.issue(rr.runs, r.Context(), id); err != nil {
		rr.writeControlErr(w, verb.name, id, err)
		return
	}
	webhttp.Ok(w)
}
