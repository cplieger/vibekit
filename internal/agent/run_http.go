package agent

// The run surface: the two reads, and the routes that forward to KAS's own control
// verbs. vibekit adds no control of its own — no scheduling, no retry policy.
//
// The RUN read passes `state` and `nodePlan` through VERBATIM: a projected copy
// would be a second representation of a structure vibekit does not own.

// The STEP read is what passthrough cannot answer, since `inspect` carries only
// what a step chose to DECLARE. It loads that step's own KAS session and projects
// the replay (step_transcript.go), and its three-valued verdict is what makes a
// LIVE step and a reclaimed one distinguishable — see vibekit.md, `run_step`.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/vibekit/internal/rpcerr"
	"github.com/cplieger/vibekit/internal/vibekit"
	"github.com/cplieger/vibekit/internal/workflow"
	"github.com/cplieger/webhttp/v2"
)

// handleRun: GET /api/runs/{workflowId} → one run's full state.
//
// Two things happen besides the passthrough. The step sessions in the returned tree
// are RECORDED, the only recovery path for step-frame attribution after a restart
// empties that registry mid-run; and a missing VERB is distinguished from a missing
// RUN, so a build with no workflow engine does not report as a deleted run.
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

// handleStepTranscript: GET /api/runs/{id}/steps/{path...} → one step's transcript.
//
// The path arrives with RAW separators and decoded segments, so it is compared
// against the joined `StepSession.Path` as it stands; a node id containing a `/` is
// therefore not addressable. The workflow id is its FIRST segment and the mismatch is
// ASSERTED, or a path from another run would 404 confidently. EVERY 4xx here is
// SETTLED — asking again cannot change it — so a `gone` or `unavailable` verdict is a
// 200 instead: those answer ABOUT the transcript, where a 5xx is transient.
func (rr *runRoutes) handleStepTranscript(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		httpreply.BadRequest(w, "missing workflow id")
		return
	}
	nodePath := r.PathValue("path")
	if nodePath == "" {
		httpreply.BadRequest(w, "missing step path")
		return
	}
	if first, _, _ := strings.Cut(nodePath, "/"); first != id {
		httpreply.BadRequest(w, "the step path does not belong to this run")
		return
	}
	out, err := rr.runs.StepTranscript(r.Context(), id, nodePath)
	if err != nil {
		if errors.Is(err, errStepUnknown) {
			httpreply.NotFound(w, errStepUnknown.Error())
			return
		}
		slog.Warn("step transcript failed", "workflow_id", id, "node_path", nodePath,
			"error", err, "detail", rpcerr.Details(err))
		httpreply.InternalError(w, errors.New("step transcript unavailable"))
		return
	}
	webhttp.WriteJSON(w, out)
}

// handleLiveRuns: GET /api/runs/live → every live lease, projected to
// `{workflow_id, chat_id, executing}`.
//
// PRESENCE-based over vibekit-local state, so it costs no KAS call: `executing` is
// Lease.Bounded(), and a real status would mean one `inspect` per lease behind a
// page load. Why a field rather than a filter here: vibekit.LiveRun. Staleness errs
// toward KEEPING, which costs memory and not correctness.
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
			Executing:  held[i].Bounded(),
		})
	}
	webhttp.WriteJSON(w, out)
}

// status reads one run's current status via `_kiro/workflow/inspect`, or "" when the
// run is unknown, which the caller turns into a 404. Its own call rather than a
// shared cache: a control decision is made against the status as of NOW.
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

// handleCancel: POST /api/runs/{id}/cancel → ask the run to stop. The response
// confirms the ASK, not the stop: cancel is a node-boundary verb, so the terminal
// frame follows at the in-flight node's end.
func (rr *runRoutes) handleCancel(w http.ResponseWriter, r *http.Request) {
	rr.controlHandler(w, r, runVerbCancel)
}

// handlePause / handleResume are cancel's shape; the difference is which statuses
// permit them, which runVerb carries.
func (rr *runRoutes) handlePause(w http.ResponseWriter, r *http.Request) {
	rr.controlHandler(w, r, runVerbPause)
}

func (rr *runRoutes) handleResume(w http.ResponseWriter, r *http.Request) {
	rr.controlHandler(w, r, runVerbResume)
}

func (rr *runRoutes) handleRetry(w http.ResponseWriter, r *http.Request) {
	rr.controlHandler(w, r, runVerbRetry)
}

// handleDelete: DELETE /api/runs/{id} — the History row's delete. Removes the run
// from KAS and drops vibekit's lease, timer and recorded end reason. Not
// recoverable, which is why the client confirms it before sending.
func (rr *runRoutes) handleDelete(w http.ResponseWriter, r *http.Request) {
	rr.controlHandler(w, r, runVerbDelete)
}

// handleStepStatus: POST /api/runs/{id}/step — mark a step completed, failed or
// running so a wedged run advances. Its own handler rather than a runVerb because it
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
	// Three arms, because the re-host put a SERVER fault on a path that had only ever
	// carried a caller's own mistake: `errRunHostStart` is a failed spawn here, and
	// answering 400 with its text tells the reader they asked wrongly and hands them an
	// internal path to read. `errStepStatusRefused` covers every state-of-the-world
	// refusal and takes the 409 the answer route's own do — a KAS decline, and vibekit
	// withholding a write it cannot address. Everything else is a validation refusal or
	// a KAS throw: 400.
	err := rr.runs.SetStepStatus(r.Context(), id, body.NodeID, body.Status)
	switch {
	case err == nil:
		webhttp.Ok(w)
	case errors.Is(err, errRunHostStart):
		slog.Warn("run step status: could not host the run", "workflow_id", id,
			"node_id", body.NodeID, "error", err)
		httpreply.InternalError(w, errors.New("step status update failed"))
	case errors.Is(err, errStepStatusRefused):
		httpreply.Conflict(w, rpcerr.Text(err))
	default:
		httpreply.BadRequest(w, rpcerr.Text(err))
	}
}

// handleAnswer: POST /api/runs/{id}/answer — answer a step parked on a question.
// Its own handler for handleStepStatus's reason: it carries a body.
//
// REST rather than an `/api/command` envelope, because every other run mutation is
// REST and a parentless run's ask has no chat id to put in that envelope.
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
		// first. The client retires its card on `run_input_settled` either way.
		httpreply.Conflict(w, err.Error())
	case errors.Is(err, errRunNotParked):
		// Also a state of the world, and the one refusal the reader can ACT on: the
		// card is back (restoreAsk re-offered it), so 409 carries the retry sentence
		// rather than a 400 telling them they asked wrongly.
		httpreply.Conflict(w, err.Error())
	case errors.Is(err, errRunHostStart):
		// handleStepStatus's arm, same reason: a failed spawn is this server's
		// fault, so it must not read as the caller's.
		slog.Warn("run answer: could not host the run", "workflow_id", id,
			"ask_id", body.AskID, "error", err)
		httpreply.InternalError(w, errors.New("answer failed"))
	default:
		slog.Warn("run answer failed", "workflow_id", id, "ask_id", body.AskID,
			"error", err, "detail", rpcerr.Details(err))
		httpreply.BadRequest(w, rpcerr.Text(err))
	}
}

// runVerb describes one run-control verb: how to issue it, and which run statuses
// it is legal from.
//
// The gate exists because KAS's refusals are throws surfacing as -32603 with the
// reason buried in `error.data`, and two of them are ordinary user timing. Gating
// here turns those into a 409 naming the status and leaves -32603 meaning a fault.
// KAS remains the authority — this is a pre-check one round trip stale.
type runVerb struct {
	name  string
	issue func(*Runs, context.Context, string) error
	// method is set EXPLICITLY on every verb rather than defaulting to POST: an
	// implicit default would leave the one DELETE as the only stated method.
	method string
	// from lists the statuses the verb is legal from. Empty means unrestricted.
	from []string
}

// The workflow status vocabulary is KAS's WorkflowStatusSchema:
// running | paused | completed | failed | aborted.
var (
	runVerbCancel = runVerb{
		name: "cancel",
		// Deliberately unrestricted: cancel is the tab-close gesture and must
		// never be the verb that fails; KAS is idempotent on an
		// already-terminal run.
		issue:  (*Runs).Cancel,
		method: http.MethodPost,
	}
	runVerbPause = runVerb{
		name:   "pause",
		issue:  (*Runs).Pause,
		method: http.MethodPost,
		from:   []string{"running"},
	}
	runVerbResume = runVerb{
		name:   "resume",
		issue:  (*Runs).Resume,
		method: http.MethodPost,
		from:   []string{runStatusPaused},
	}
	// Retry's window is exactly the two statuses at which a run's own bridge has
	// already been closed, which is why Retry re-hosts instead of requiring
	// one.
	runVerbRetry = runVerb{
		name:   "retry",
		issue:  (*Runs).Retry,
		method: http.MethodPost,
		from:   []string{"failed", "aborted"},
	}
	// Delete is unrestricted for the same reason cancel is, plus one of its
	// own: it is the only way a row leaves the History page.
	runVerbDelete = runVerb{
		name:   "delete",
		issue:  (*Runs).Delete,
		method: http.MethodDelete,
	}
)

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
	if len(verb.from) > 0 {
		status, err := rr.status(r.Context(), id)
		if err != nil {
			slog.Warn("run control: status read failed",
				"verb", verb.name, "workflow_id", id, "error", err, "detail", rpcerr.Details(err))
			httpreply.InternalError(w, errors.New(verb.name+" failed"))
			return
		}
		if status == "" {
			httpreply.NotFound(w, "run not found")
			return
		}
		if !slices.Contains(verb.from, status) {
			httpreply.Conflict(w, verb.name+" is not available for a "+status+" run")
			return
		}
	}
	if err := verb.issue(rr.runs, r.Context(), id); err != nil {
		slog.Warn("run control failed",
			"verb", verb.name, "workflow_id", id, "error", err, "detail", rpcerr.Details(err))
		// A START failure is tested FIRST, and the order is the whole point: a
		// re-host whose handshake KAS itself refused puts an *RPCError UNDER
		// errRunHostStart, so the type test below matches it and would report this
		// server's failed spawn as a state of the run, carrying KAS's session-door
		// text. The two body-carrying routes test it first for the same reason.
		if errors.Is(err, errRunHostStart) {
			httpreply.InternalError(w, errors.New(verb.name+" failed"))
			return
		}
		// KAS's OWN refusal reaches the client, because the re-host made it the
		// answer a reader gets most often: the `from` gate is one round trip stale,
		// so a verb reaching a run whose state has since moved is refused BY KAS,
		// and "pause failed" names neither the run's state nor a remedy. 409 for
		// the same reason the gate above uses it — a state of the world, not a
		// fault.
		if _, fromKAS := errors.AsType[*vibekit.RPCError](err); fromKAS {
			httpreply.Conflict(w, rpcerr.Text(err))
			return
		}
		httpreply.InternalError(w, errors.New(verb.name+" failed"))
		return
	}
	webhttp.Ok(w)
}
