package agent

// The run surface: the two reads, and the routes that forward to KAS's own control verbs.
// vibekit adds no control of its own. The RUN read passes `state` and `nodePlan` through
// VERBATIM, rather than hold a second representation of a structure it does not own.

// The STEP read is what passthrough cannot answer, since `inspect` carries only what a
// step chose to DECLARE: it loads that step's own KAS session and projects the replay.

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

// handleRun: GET /api/runs/{workflowId} → one run's full state. Two things happen besides
// the passthrough: the step sessions in the returned tree are RECORDED, the only recovery
// path for step-frame attribution after a restart empties that registry mid-run; and a
// missing VERB is distinguished from a missing RUN.
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
	// A run parked on a person with no ask on this server gets one reconstructed from its
	// state — the container-restart path, since the ask registry is in memory. The response
	// stays VERBATIM: the synthesised ask travels on the `run_input_needed` SSE instead.
	rr.runs.reconcileNeedInput(r.Context(), id, raw)
	httpreply.WriteRawJSON(w, raw)
}

// handleStepTranscript: GET /api/runs/{id}/steps/{path...} → one step's transcript. The
// path is compared against the joined `StepSession.Path` as it stands, so a node id
// containing a `/` is not addressable, and its FIRST segment must be the workflow id.
// EVERY 4xx here is SETTLED, so a `gone` or `unavailable` verdict is a 200 instead.
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

// handleLiveRuns: GET /api/runs/live → every live lease as `{workflow_id, chat_id,
// executing}`. PRESENCE-based over vibekit-local state, so it costs no KAS call: a real
// status would mean one `inspect` per lease behind a page load. Staleness errs to KEEPING.
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

// status reads one run's current status, or "" when the run is unknown, which the caller
// turns into a 404. Its own call: a control decision is made against the status as of NOW.
func (rr *runRoutes) status(ctx context.Context, workflowID string) (string, error) {
	raw, err := rr.runs.rawInspect(ctx, workflowID)
	if err != nil {
		// Both are "no status to gate on" rather than a fault: the caller 404s.
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

// handleRecipes: GET /api/recipes → the launchable recipe list, bundled + workspace.
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

// handleLaunch: POST /api/runs → launch one PARENTLESS run. 409 when the recipe already
// has a live run: the wire shape of the single-run rule the Run ⇄ Cancel button needs.
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
		// KAS's launch-time validation names the problem (a bad input set, an
		// unregistered agent), so forward it rather than a sentinel: the fix is the user's.
		httpreply.BadRequest(w, rpcerr.Text(err))
		return
	}
	webhttp.WriteJSON(w, vibekit.RunLaunchedResponse{WorkflowID: id, Name: name})
}

// handleCancel: POST /api/runs/{id}/cancel → ask the run to stop. The response confirms
// the ASK, not the stop: cancel is a node-boundary verb, so the terminal frame follows.
func (rr *runRoutes) handleCancel(w http.ResponseWriter, r *http.Request) {
	rr.controlHandler(w, r, runVerbCancel)
}

// handlePause / handleResume are cancel's shape; only the legal statuses differ.
func (rr *runRoutes) handlePause(w http.ResponseWriter, r *http.Request) {
	rr.controlHandler(w, r, runVerbPause)
}

func (rr *runRoutes) handleResume(w http.ResponseWriter, r *http.Request) {
	rr.controlHandler(w, r, runVerbResume)
}

func (rr *runRoutes) handleRetry(w http.ResponseWriter, r *http.Request) {
	rr.controlHandler(w, r, runVerbRetry)
}

// handleDelete: DELETE /api/runs/{id} — removes the run from KAS and drops vibekit's
// lease, timer and recorded end reason. Not recoverable, so the client confirms first.
func (rr *runRoutes) handleDelete(w http.ResponseWriter, r *http.Request) {
	rr.controlHandler(w, r, runVerbDelete)
}

// handleStepStatus: POST /api/runs/{id}/step — mark a step completed, failed or running
// so a wedged run advances. Its own handler rather than a runVerb: it carries a body.
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
	// Three arms: `errRunHostStart` is a failed SPAWN, so a 400 with its text would tell
	// the reader they asked wrongly and hand them an internal path; `errStepStatusRefused`
	// is every state-of-the-world refusal, at 409; everything else is a 400.
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

// handleAnswer: POST /api/runs/{id}/answer — answer a step parked on a question. REST
// rather than an `/api/command` envelope: a parentless run's ask has no chat id.
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
		// A state of the world: another surface got there first.
		httpreply.Conflict(w, err.Error())
	case errors.Is(err, errRunNotParked):
		// Also a state of the world, and the one refusal the reader can ACT on: the card
		// is back, so 409 carries the retry sentence rather than a 400.
		httpreply.Conflict(w, err.Error())
	case errors.Is(err, errRunHostStart):
		// A failed spawn is this server's fault, so it must not read as the caller's.
		slog.Warn("run answer: could not host the run", "workflow_id", id,
			"ask_id", body.AskID, "error", err)
		httpreply.InternalError(w, errors.New("answer failed"))
	default:
		slog.Warn("run answer failed", "workflow_id", id, "ask_id", body.AskID,
			"error", err, "detail", rpcerr.Details(err))
		httpreply.BadRequest(w, rpcerr.Text(err))
	}
}

// runVerb describes one run-control verb: how to issue it, and which statuses it is legal
// from. The gate exists because KAS's refusals are throws surfacing as -32603 with the
// reason buried in `error.data`, two of them ordinary user timing; it is one trip stale.
type runVerb struct {
	name  string
	issue func(*Runs, context.Context, string) error
	// method is EXPLICIT on every verb, or the one DELETE would be the only stated method.
	method string
	// from lists the statuses the verb is legal from. Empty means unrestricted.
	from []string
}

// The status vocabulary is KAS's: running | paused | completed | failed | aborted.
var (
	runVerbCancel = runVerb{
		name: "cancel",
		// Deliberately unrestricted: cancel is the tab-close gesture and must never be
		// the verb that fails; KAS is idempotent on an already-terminal run.
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
	// Retry's window is exactly the two statuses at which a run's own bridge has already
	// been closed, which is why Retry re-hosts instead of requiring one.
	runVerbRetry = runVerb{
		name:   "retry",
		issue:  (*Runs).Retry,
		method: http.MethodPost,
		from:   []string{"failed", "aborted"},
	}
	// Delete is unrestricted like cancel, plus it is the only way a row leaves History.
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
		// A START failure is tested FIRST: a re-host whose handshake KAS itself refused
		// puts an *RPCError UNDER errRunHostStart, so the type test below would report
		// this server's failed spawn as a state of the run.
		if errors.Is(err, errRunHostStart) {
			httpreply.InternalError(w, errors.New(verb.name+" failed"))
			return
		}
		// KAS's OWN refusal reaches the client: the `from` gate is one round trip stale,
		// so a verb reaching a run whose state has moved is refused BY KAS, and "pause
		// failed" names neither the state nor a remedy. 409, a state of the world.
		if _, fromKAS := errors.AsType[*vibekit.RPCError](err); fromKAS {
			httpreply.Conflict(w, rpcerr.Text(err))
			return
		}
		httpreply.InternalError(w, errors.New(verb.name+" failed"))
		return
	}
	webhttp.Ok(w)
}
