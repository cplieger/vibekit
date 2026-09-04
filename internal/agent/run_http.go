package agent

// The run read surface: GET /api/runs/{id}.
//
// ONE route. KAS's `state` and `nodePlan` pass through VERBATIM: the node tree
// is KAS's structure, it already carries every execution fact a reader wants,
// and a projected copy here would be a second representation of a thing
// vibekit does not own.
//
// TWO routes an earlier design specified are absent:
//
//   - `GET /api/runs` (the cross-chat list): the run board it served is
//     deleted, and the history surface already lists runs from the same
//     `_kiro/workflow/list` call beside previous chats.
//   - `GET /api/runs/{id}/steps/{nodeId}` (a step's own transcript): cannot be
//     served without new bridge machinery, and a live step's content already
//     reaches the launching chat's transcript correctly attributed while a
//     finished step's product is its `capturedOutput`, which `inspect`
//     returns.
//
// The mutations here are the four run-control verbs: cancel, pause, resume and
// retry. All four are KAS's own; this file only routes to them. Earlier
// guidance withheld all controls on the theory that offering one meant
// BUILDING one; the 2.16.1 sweep established every verb is already a live
// handler in the pinned binary, so the choice was route-or-not, and withholding
// the route left a paused run with no way forward except deleting the chat.
// vibekit adds no control of its own, no scheduling, no retry policy and no
// delete beyond what each route forwards to one native verb.

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
// Two things happen besides the passthrough:
//
//  1. The step sessions in the returned tree are recorded — the only recovery
//     path for step-frame attribution after a container restart empties that
//     registry while the run carries on.
//  2. A missing VERB is distinguished from a missing RUN, so "this build has
//     no workflow engine" doesn't report as "your run was deleted".
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
	// A run parked on a person with no ask on this server gets one reconstructed
	// from its state — the container-restart path, since the ask registry is in
	// memory while the run is not. The response stays a VERBATIM passthrough: the
	// synthesised ask travels on the `run_input_needed` SSE every open client
	// already consumes, so nothing is spliced into `raw`.
	rr.runs.reconcileNeedInput(r.Context(), id, raw)
	httpreply.WriteRawJSON(w, raw)
}

// handleLiveRuns: GET /api/runs/live → every live lease, projected to
// `{workflow_id, chat_id}`.
//
// PRESENCE-based, over vibekit-local state only: a lease exists if and only if
// vibekit put the run on the wire and no terminal transition released it, so
// no KAS call is needed to serve it. The consumer is the client's eviction
// sweep, which must not evict a chat whose agent still has a run in flight.
//
// Staleness errs toward keeping: a missed terminal frame leaves the lease live
// until the next boot sweep releases it, which costs memory only, never
// correctness.
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

// status reads one run's current status via `_kiro/workflow/inspect`.
// Returns "" when the run is unknown, which the caller turns into a 404.
//
// Its own inspect call rather than a shared cache: a control decision must be
// made against the status as of NOW, not the older status the client rendered
// its buttons from.
func (rr *runRoutes) status(ctx context.Context, workflowID string) (string, error) {
	raw, err := rr.runs.rawInspect(ctx, workflowID)
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

// handleLaunch: POST /api/runs → launch one PARENTLESS run and answer with
// its id and name. 409 when the recipe already has a live run — the wire shape
// of the single-run rule, which is what keeps the Workflows row's Run ⇄ Cancel
// button able to name one run.
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
		// KAS's launch-time validation is precise (a bad input set, an
		// unregistered agent) and the message names the problem; forward it
		// rather than a generic sentinel, because the fix is the user's.
		httpreply.BadRequest(w, rpcerr.Text(err))
		return
	}
	webhttp.WriteJSON(w, vibekit.RunLaunchedResponse{WorkflowID: id, Name: name})
}

// handleCancel: POST /api/runs/{id}/cancel → ask the run to stop. The
// response confirms the ASK, not the stop: cancel is a node-boundary verb, so
// the terminal run_complete (and the run_finished SSE it becomes) follows at
// the in-flight node's end.
func (rr *runRoutes) handleCancel(w http.ResponseWriter, r *http.Request) {
	rr.controlHandler(w, r, runVerbCancel)
}

// handleControls: GET /api/runs/{id}/controls → what may be done to this run,
// and one sentence per verb it refuses.
//
// Its own route rather than a field on the passthrough above, which must stay a
// verbatim relay of KAS's reply. The client fetches it when it points at a run
// and again on that run's `run_finished`, so this is one request per tab open
// rather than one per repaint.
//
// It exists because the client cannot answer the question. It was deciding the
// row from a status table plus a map of which chat launched which run, and that
// map is written only by SSE frames — so any client that reloaded saw no entry,
// read a chat-parented run as parentless, and drew the parentless row. Only the
// server sees the run's status, its parentage and whether anything still hosts
// it at once.
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

// handlePause / handleResume are the two run-control verbs added once the
// sweep established they were already live server-side. Each is the same shape as
// cancel; the difference is which statuses permit them, which the affordance
// table carries.
func (rr *runRoutes) handlePause(w http.ResponseWriter, r *http.Request) {
	rr.controlHandler(w, r, runVerbPause)
}

func (rr *runRoutes) handleResume(w http.ResponseWriter, r *http.Request) {
	rr.controlHandler(w, r, runVerbResume)
}

// handleRetry: POST /api/runs/{id}/retry → reset the run's failed work and
// report WHAT WAS RESET.
//
// Its own handler rather than a runVerb, for handleStepStatus's reason inverted:
// the verb table's issue signature answers `error` alone, and retry's reply is
// the outcome. Collapsing it to `{"ok":true}` made a retry that reset zero nodes
// indistinguishable from one that reset five — and since the success path also
// emitted no notification and triggered no refetch, "nothing happened" was
// exactly what a no-op retry was designed to look like.
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

// handleDelete: DELETE /api/runs/{id} — the History row's delete. Removes the
// run from KAS (its directory included) and drops vibekit's lease, timer and
// recorded end reason. Not recoverable, which is why it is the one run verb the
// client confirms before sending.
func (rr *runRoutes) handleDelete(w http.ResponseWriter, r *http.Request) {
	rr.controlHandler(w, r, runVerbDelete)
}

// handleStepStatus: POST /api/runs/{id}/step — mark an IN-FLIGHT step
// completed or failed so a wedged run advances.
//
// Its own handler rather than a runVerb because it carries a body (which step,
// which status) and the verb table's issue signature is id-only.
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
//
// Its own handler rather than a runVerb, for handleStepStatus's reason: it carries
// a body (which ask, and the words), where the verb table's issue signature is
// id-only.
//
// A REST call on the run surface rather than a `/api/command` envelope, because
// every other run mutation is REST and this ask is not chat-scoped — a parentless
// run's ask has no chat id to put in that envelope.
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
		// first, or the step moved on. The client retires its card on the
		// `run_input_settled` frame either way.
		httpreply.Conflict(w, err.Error())
	case errors.Is(err, errRunNotHosted):
		httpreply.Conflict(w, err.Error())
	default:
		slog.Warn("run answer failed", "workflow_id", id, "ask_id", body.AskID,
			"error", err, "detail", rpcerr.Details(err))
		httpreply.BadRequest(w, rpcerr.Text(err))
	}
}

// runVerb describes one run-control verb: how to issue it, and whether it is
// gated on the run's affordance.
//
// The gate exists because KAS's own refusals are throws that surface as a -32603
// with the reason buried in `error.data`, and two of them are ordinary user
// timing rather than faults. Gating turns those into a 409 naming the situation,
// and leaves -32603 to mean something actually went wrong.
//
// WHICH statuses a verb is legal from no longer lives here: run_affordance.go
// holds the one table, so the same answer decides what the client draws and what
// this route accepts. A per-verb `from` list beside it would be a second copy of
// the rule with no way to see the other two inputs.
//
// KAS remains the authority: this is a pre-check against a status read one round
// trip earlier, so a run that changes state in between still gets KAS's refusal,
// now forwarded rather than anonymised.
type runVerb struct {
	name  string
	issue func(*Runs, context.Context, string) error
	// method is the HTTP method the verb answers. Set EXPLICITLY on every verb
	// rather than defaulting to POST when empty: four of the five are POSTs and
	// the fifth is a DELETE, so an implicit default would make the odd one out the
	// only verb whose method is stated, which is the wrong way round.
	method string
	// gated asks the affordance before issuing. False means unrestricted.
	gated bool
}

var (
	runVerbCancel = runVerb{
		name: verbCancel,
		// Deliberately unrestricted: cancel is the tab-close gesture and must
		// never be the verb that fails; KAS is idempotent on an
		// already-terminal run.
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
	// Delete is unrestricted for the same reason cancel is, plus one of its
	// own: it is the only way a row leaves the History page.
	runVerbDelete = runVerb{
		name:   verbDelete,
		issue:  (*Runs).Delete,
		method: http.MethodDelete,
	}
)

// verbDelete is the History row's delete. Not in run_affordance.go's table with
// the other four: it is never offered as a control and never gated, so putting
// it there would put a verb in the row's vocabulary that the row must not draw.
const verbDelete = "delete"

// permits gates one verb on the run's affordance, writing the refusal itself,
// and hands the affordance back so the verb can act on the ANSWER rather than
// resolving the same facts a second time.
//
// The refusal carries the affordance's own sentence when it has one — which is
// how a pause on a run whose launching chat is closed says which chat to open —
// and falls back to naming the status when the status alone is the reason.
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

// writeControlErr answers a verb that reached KAS and failed.
//
// It stops anonymising the answer. Every failure but errRunNotHosted used to
// become httpreply.InternalError, which sends the CONSTANT "internal error"
// while the actionable sentence went to the container log — so a KAS refusal
// reached the reader as "Couldn't retry the run: internal error", and a caller
// could not tell a refusal from a fault. handleLaunch already forwards a
// launch-time refusal with rpcerr.Text for exactly this reason.
//
// InternalError is kept for what it is for: a fault that is not the reader's to
// act on.
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
		// The engine ACCEPTED the verb and only its report is unusable, so this is
		// neither a refusal nor this side's fault: 502 says the upstream answer was,
		// and the sentence sends the reader to a refresh rather than to a second
		// retry of work that may already be running. The detail stays in the log,
		// where the wrapped decode error names what was unreadable.
		slog.Warn("run control landed but its report could not be read",
			"verb", verb, "workflow_id", id, "error", err)
		webhttp.WriteJSONStatus(w, http.StatusBadGateway,
			httpreply.ErrorJSON(errRetryOutcomeUnreadable.Error()))
	case isRPCRefusal(err):
		// KAS declined. Its sentence names the reason and the fix is frequently
		// the reader's, so it is forwarded rather than replaced by a sentinel.
		slog.Info("run control refused by the workflow engine",
			"verb", verb, "workflow_id", id, "detail", rpcerr.Details(err))
		httpreply.Conflict(w, rpcerr.Text(err))
	default:
		slog.Warn("run control failed",
			"verb", verb, "workflow_id", id, "error", err, "detail", rpcerr.Details(err))
		httpreply.InternalError(w, errors.New(verb+" failed"))
	}
}

// isRPCRefusal reports whether the failure is KAS's own answer rather than a
// fault on this side. Matched on the JSON-RPC error type at any wrapping depth,
// because the verb helpers wrap with fmt.Errorf on the way up.
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
