package agent

// The run host: launching a PARENTLESS workflow run, and the bridge that
// hosts it.
//
// There are two ways a run starts. The agent calling `run_workflow` parents
// the run on ITS chat's session — KAS hardcodes that — so the run lives on
// the chat's bridge and vibekit builds nothing. The USER clicking Run on the
// Workflows tab deliberately touches no chat (user decision): no launch
// record in any transcript, no progress rows on any conversation, no
// completion wake. That run needs a process of its own, because a chat's
// bridge would couple the run's life to a conversation it has nothing to do
// with, and the utility bridge REFUSES host requests by design (text-only).
//
// So: ONE bridge per launched run, registered under the synthetic chat id
// `run:<workflowId>`. Every response path (permission answers, fs replies,
// terminal I/O) already resolves its bridge from that map by chat id, so a
// run ask answered from the client routes back with ZERO new addressing
// machinery.
//
// What the synthetic id does NOT get: a chat file. Invariant 3 ("a live
// bridge implies a live chat record") exists so a CONVERSATION survives
// restarts; a run bridge is deliberately ephemeral — its loss is a PAUSE (KAS
// reconciles a dead owner's run to paused on the next read), and closing the
// tab is a CANCEL.
//
// Lifecycle: the bridge lives until its run reaches a terminal
// `run_complete`, which the dispatcher watches for and answers by closing the
// bridge. Closing right after the cancel VERB would be wrong: cancel is a
// node-boundary verb, and the terminal state is certified by the OWNING
// process at the node boundary, so killing that process on the verb's reply
// can leave the run `paused` instead of `cancelled` on the next read.

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/cplieger/vibekit/internal/runlease"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// Workflow RPC param keys, shared across the five verbs.
const (
	keyWorkflowID     = "workflowId"
	keyWorkspacePaths = "workspacePaths"
)

// runChatPrefix namespaces the synthetic chat ids run bridges register under.
// Real chat ids are client-generated `c-*` uuids, so the namespace cannot
// collide with one.
const runChatPrefix = "run:"

// runChatID is the bridge-manager key for a run's bridge.
func runChatID(workflowID string) vibekit.ChatID {
	return vibekit.ChatID(runChatPrefix + workflowID)
}

// isRunChat reports whether a chat id names a run bridge.
func isRunChat(chatID vibekit.ChatID) bool {
	return strings.HasPrefix(string(chatID), runChatPrefix)
}

// workflowIDOf recovers the run a bridge hosts from its synthetic chat id.
//
// The bridge hosts exactly ONE run, so its identity is known before any frame is
// decoded — which is what lets a step's content be addressed to a run without
// trusting the frame to name it. Empty for anything that is not a run chat id.
func workflowIDOf(chatID vibekit.ChatID) string {
	if !isRunChat(chatID) {
		return ""
	}
	return strings.TrimPrefix(string(chatID), runChatPrefix)
}

// launchTimeout bounds the launch handshake (process start + initialize +
// session/new + new + invoke). Generous because a first launch may unpack a
// KAS runtime tree (~240 MB) before the process answers.
const launchTimeout = 120 * time.Second

// errRecipeBusy reports the single-run rule: one live run per recipe
// definition, globally (user decision). Concurrency is declared INSIDE a
// workflow, never by launching the same recipe twice.
var errRecipeBusy = errors.New("this recipe already has a live run")

// Launch starts one parentless run of the recipe with the given source key
// and returns its workflow id and name.
//
// The source is re-validated against a fresh listRecipes call rather than
// trusted, since echoing an arbitrary path into `new` unchecked would let a
// client aim this endpoint at a file that is not a recipe.
//
// A manual run of a SCHEDULED recipe is bounded by that recipe's next slot as
// well as the ceiling, so clicking Run shortly before 02:00 cannot hold the
// recipe past 02:00. It stays ATTENDED either way.
func (rs *Runs) Launch(ctx context.Context, source string, inputs map[string]string) (id, name string, err error) {
	return rs.launch(ctx, source, inputs, manualLaunch())
}

// LaunchScheduled launches a run on behalf of a schedule, marking it
// UNATTENDED for the duration.
//
// The mark rides the lease granted between `new` and `invoke` — the earliest
// point the workflow id exists and still before anything can execute, so no
// permission request can slip through unmarked.
//
// slotAt is the instant this run's own next slot comes due; zero means the
// schedule cannot name its next slot, and the run is then bounded by the
// ceiling alone.
func (rs *Runs) LaunchScheduled(ctx context.Context, source, scheduleID string, slotAt time.Time) (id, name string, err error) {
	return rs.launch(ctx, source, nil, scheduledLaunch(scheduleID, slotAt))
}

// launch is the shared body of both public launch verbs. The origin carries
// everything that differs between them.
func (rs *Runs) launch(ctx context.Context, source string, inputs map[string]string, o launchOrigin) (id, name string, err error) {
	cctx, cancel := context.WithTimeout(ctx, launchTimeout)
	defer cancel()

	recipe, err := rs.recipeBySource(cctx, source)
	if err != nil {
		return "", "", err
	}
	if o.origin == runlease.OriginManual {
		// A manual run of a recipe that ALSO has a schedule must yield to that
		// recipe's next slot, only knowable once the recipe is resolved.
		o.slotAt = rs.manualSlot(recipe.Source)
	}
	if bErr := rs.recipeIdle(cctx, recipe.Name); bErr != nil {
		return "", "", bErr
	}

	// The run's own process. Started OUTSIDE the manager: the map key is the
	// workflow id, which only `new`'s reply knows. Call replies ride the
	// bridge's readLoop (matched by request id), not NotifCh, so the forward
	// goroutine can attach after `new`.
	bridge := rs.bridges.factory()
	// Presets carry the active security profile, same as a chat bridge: a run
	// executes agent work with the same tools, and the unattended path is
	// where that matters most, since nobody is watching to answer a prompt
	// the profile was meant to remove.
	if sErr := bridge.Start(cctx, &vibekit.StartOpts{
		Lifetime: rs.lifecycle.shutdownCtx,
		// Named rather than left to buildACPArgs's empty-string default, so every
		// spawn in this process takes the engine from the one pin. The default
		// resolves to v3 too, so this changes no argv; what it changes is that a
		// future edit to the default cannot silently launch a run bridge on the
		// legacy engine while every chat bridge stays on v3.
		AgentEngine: resolveAgentEngine(),
		Presets:     securityPresets(cctx, rs.lifecycle.configDir),
		ToolSearch:  toolSearchEnabled(cctx, rs.lifecycle.configDir),
		Knowledge:   knowledgeEnabled(cctx, rs.lifecycle.configDir),
		Memory:      memoryEnabled(cctx, rs.lifecycle.configDir),
	}); sErr != nil {
		return "", "", fmt.Errorf("run bridge start: %w", sErr)
	}

	wfID, err := rs.workflowNew(cctx, bridge, recipe.Source, inputs)
	if err != nil {
		bridge.Stop()
		return "", "", err
	}

	// Register BEFORE invoke: the first lifecycle frame follows invoke
	// immediately, and a frame arriving before the map entry would find no
	// bridge to answer through.
	rs.bridges.insert(runChatID(wfID), &sharedBridge{bridge: bridge, state: bridgeIdle})
	// The run's envelope, before anything can execute: the single-run rule's
	// evidence that this row is vibekit's own, the deadline's inputs, and the
	// unattended mark the permission floor reads.
	rs.grantLease(cctx, wfID, recipe.Name, o)
	go rs.coord.Forward(runChatID(wfID), bridge)

	if _, err := bridge.Call(cctx, methodKiroWorkflowInvoke, map[string]any{keyWorkflowID: wfID}); err != nil {
		// The run was created but never started; nothing is executing. Tear the
		// bridge down and surface the failure rather than leaving a zombie row.
		rs.releaseLease(cctx, wfID)
		rs.coord.CloseBridge(runChatID(wfID))
		return "", "", fmt.Errorf("workflow invoke: %w", err)
	}
	// The wall clock, for every run this path launches. After invoke, so a run
	// that never started leaves no timer, and idempotent with the `run_start`
	// frame's own arm.
	rs.armDeadline(cctx, wfID)
	slog.Info("workflow run launched", "workflow_id", wfID, "recipe", recipe.Name)
	return wfID, recipe.Name, nil
}

// Cancel asks a run to stop, on the USER's behalf.
//
// It takes the run's termination claim like every bound does, which is what
// keeps a deliberate stop from being relabelled: a cancel pressed seconds
// before a ceiling or a schedule deadline used to race a bound callback that
// still saw a live gate, and the row afterwards read "overran" for a run the
// user stopped on purpose. Winning records NOTHING, and that absence is the
// third value the row's one field carries.
//
// A LOST claim returns nil rather than an error, because something else got
// there first. When the claim is won and the RPC then fails, the claim is
// handed back so a later Cancel is not silently refused.
func (rs *Runs) Cancel(ctx context.Context, workflowID string) error {
	if workflowID == "" {
		return errors.New("missing workflow id")
	}
	if !rs.claimTermination(workflowID) {
		return nil
	}
	return rs.finishTermination(ctx, workflowID, "")
}

// cancelRPC issues the cancel VERB and nothing else — no claim, no record.
//
// On the run's own bridge when one is live (the manual-launch case); through
// the utility session otherwise — an agent-launched run, or one from a
// previous boot, has no run bridge and cancel is an ordinary client RPC that
// any connection may issue. The bridge is NOT closed here: the terminal
// `run_complete` the cancel eventually produces closes it, because the
// owning process must live to the node boundary to certify the cancelled
// state.
func (rs *Runs) cancelRPC(ctx context.Context, workflowID string) error {
	return rs.control(ctx, workflowID, methodKiroWorkflowCancel, "workflow cancel call")
}

// Delete removes a run and everything either side keeps about it, on the
// USER's behalf. It is the History row's delete, and the only run verb that
// is not recoverable.
//
// Order matters and is the opposite of cancel's. KAS's delete cancels a
// non-terminal run itself before removing the run directory, so vibekit must
// NOT pre-cancel: taking the termination claim first would record an end
// reason for a row that is about to stop existing. So the verb goes first,
// and vibekit's bookkeeping is dropped only once KAS reports the run gone.
//
// The bridge IS closed here, unlike cancel's path: a deleted run has no
// state left to certify and no directory to write it to, so waiting for a
// terminal frame that may never come would leak a kiro-cli subprocess.
//
// A failed verb leaves everything in place: the run still exists in KAS, so
// forgetting the lease would strand a row vibekit could no longer bound or
// cancel.
func (rs *Runs) Delete(ctx context.Context, workflowID string) error {
	if workflowID == "" {
		return errors.New("missing workflow id")
	}
	if err := rs.control(ctx, workflowID, methodKiroWorkflowDelete, "workflow delete call"); err != nil {
		return err
	}
	rs.coord.CloseBridge(runChatID(workflowID))
	rs.forgetBounds(ctx, workflowID)
	rs.clearEnd(workflowID)
	slog.Info("workflow run deleted", "workflow_id", workflowID)
	return nil
}

// errRunNotHosted is returned by a control verb that needs the run's OWN
// bridge when there is none. Distinct from a KAS refusal so the REST layer
// can answer 409 with an explanation rather than 500.
//
// It no longer says an agent-launched run is ALWAYS in this state: hostBridge
// resolves the launching chat's bridge, so such a run is reachable whenever
// that chat has one.
var errRunNotHosted = errors.New(
	"this run has no live bridge on this server, so it cannot be paused or resumed from here; " +
		"cancel still works. A run from before the last restart is in this state, " +
		"and so is an agent-launched run whose chat is closed -- open that chat to bring it back",
)

// Pause asks a running run to stop at its next node boundary, keeping its
// state resumable. KAS sets `control.pauseRequested` and the in-flight node
// runs to completion, so the reply confirms the ASK rather than a paused
// state.
//
// Requires the run's own bridge: KAS's pause reaches `registry.require`,
// which throws for a run not in the live in-memory registry and does NOT
// rehydrate from disk the way cancel, resume and retry do.
func (rs *Runs) Pause(ctx context.Context, workflowID string) error {
	return rs.hostedControl(ctx, workflowID, methodKiroWorkflowPause)
}

// Resume re-drives a paused run.
//
// Re-arms the wall clock, because the pause parked it: a resumed run is
// executing again and gets a FRESH budget rather than the remainder of the
// one it was holding when it parked.
func (rs *Runs) Resume(ctx context.Context, workflowID string) error {
	err := rs.hostedControl(ctx, workflowID, methodKiroWorkflowResume)
	if err == nil {
		rs.armDeadline(ctx, workflowID)
	}
	return err
}

// retryTimeout bounds the whole retry handshake, process start included.
//
// BELOW the browser's own 30s request budget (@cplieger/fetch's API_TIMEOUT_MS,
// hard-wired into every apiAction), deliberately. The handler used to run on the
// request context's 120s, so a slow engine start meant the BROWSER aborted first
// and that cancellation tore down a bridge which had just been minted, releasing
// the lease with nobody watching. Answering inside the client's window turns that
// into a 503 the reader can act on.
//
// DERIVED from the client's budget rather than written as a number beside a
// comment claiming it is smaller, so the relationship is structural and cannot be
// broken by editing one of the two.
//
// A `var` so a test can drive the expiry in milliseconds instead of waiting out a
// real handshake. Never reassigned in production.
var retryTimeout = clientRequestBudget - 5*time.Second

// clientRequestBudget is the ceiling retryTimeout has to fit under: the timeout
// @cplieger/fetch hard-wires into every apiAction, which the browser applies to
// this request whatever the server thinks its own deadline is.
const clientRequestBudget = 30 * time.Second

// errRetryEngineSlow reports that the retry could not be handed off inside
// retryTimeout. Distinct from a KAS refusal so the REST layer can answer 503
// with "try again" rather than reporting a fault the reader cannot act on.
var errRetryEngineSlow = errors.New(
	"the run's engine did not start in time, so nothing was retried; try again",
)

// errRetryOutcomeUnreadable reports that KAS ACCEPTED the retry and its report
// could not be read.
//
// Its own class because the remedy inverts. Every other retry failure means
// nothing was re-driven, so the reader may try again and this process may tear
// down what it started; here the run may be executing, so retrying would ask for
// the work twice and killing the bridge would kill it mid-node. What the reader
// can act on is refreshing, and the sentence says so.
var errRetryOutcomeUnreadable = errors.New(
	"the retry was accepted but its report could not be read, so which steps it reset " +
		"is unknown; refresh the run to see where it is",
)

// kasRetryOutcome is `_kiro/workflow/retry`'s reply, in KAS's own spelling. The
// decode target only; the verb answers vibekit.RunRetriedResponse, which is the
// shape the route forwards and the client decodes.
//
// RetriedNodeIDs is why Retry returns a value at all. A retry that reset five
// nodes and one that reset none are indistinguishable otherwise, and the second
// is exactly what "I pressed Retry and nothing happened" looks like from the
// outside — so the reply IS the report, and discarding it made a no-op retry
// the design's expected appearance.
type kasRetryOutcome struct {
	WorkflowID     string   `json:"workflowId"`
	Status         string   `json:"status"`
	RetriedNodeIDs []string `json:"retriedNodeIds"`
}

// Retry resets a finished run's failed work and reports what it reset.
//
// Retry is legal only from `failed` or `aborted`, and `closeFinishedBridge`
// tears a run's bridge down on exactly those statuses — so retry is the one
// control verb that must be able to reach a run nothing hosts, which is what
// makes it the recovery path for a run neither product can otherwise resume.
//
// THE HOST IS RESOLVED, not assumed. Keying on `run:<id>` alone meant a
// chat-parented run always took the re-host branch, so vibekit spawned a second
// engine for a run whose parent session lives in a process that is frequently
// still alive — the same defect SetStepStatus was fixed for. The affordance
// carries the launching chat the route's gate already resolved, so hostBridgeFor
// answers who holds the run without asking KAS again.
//
// AND THE RE-HOST BRANCH LOADS FIRST. `_kiro/workflow/retry` against a process
// that has never seen the run fails with "not registered. Load or create it
// first."; recovery after a process death is `load` then `retry`, which is what
// kiro-cli's own client does before it touches a run it does not hold. Without
// the load this branch could only ever have worked for a run that was somehow
// still registered, which is the one case it is not for.
func (rs *Runs) Retry(
	ctx context.Context, workflowID string, aff runAffordance,
) (vibekit.RunRetriedResponse, error) {
	if workflowID == "" {
		return vibekit.RunRetriedResponse{}, errors.New("missing workflow id")
	}
	cctx, cancel := context.WithTimeout(ctx, retryTimeout)
	defer cancel()

	// The run's real host, whichever process that is: its own bridge for a run
	// vibekit launched, the LAUNCHING CHAT's for an agent-launched one. Already
	// registered there, so no load is needed and no second engine is started.
	if _, sb := rs.hostBridgeFor(workflowID, aff.ParentChat); sb != nil {
		recipe := rs.recipeOf(cctx, workflowID)
		out, err := rs.retryCall(cctx, sb.bridge, workflowID)
		if err != nil {
			// The verb LANDED and only its report is unusable, so the run may be
			// executing: re-arm it as a retried run rather than leaving the page
			// showing a termination that is no longer a fact about it.
			if errors.Is(err, errRetryOutcomeUnreadable) {
				rs.rearmRetried(cctx, workflowID, recipe)
			}
			return vibekit.RunRetriedResponse{}, err
		}
		// Only on success: a retry KAS refused re-drove nothing, so the
		// run's previous terminal reason is still the truth about it.
		rs.rearmRetried(cctx, workflowID, recipe)
		slog.Info("workflow run retried on its host",
			"workflow_id", workflowID, "recipe", recipe,
			"retried_nodes", len(out.RetriedNodeIDs), "status", out.Status)
		return out, nil
	}
	return rs.retryRehosted(cctx, workflowID)
}

// retryRehosted retries a run NOTHING in this process holds: start a bridge,
// load the run into it, then retry.
func (rs *Runs) retryRehosted(
	ctx context.Context, workflowID string,
) (vibekit.RunRetriedResponse, error) {
	chatID := runChatID(workflowID)
	// The recipe name, read off KAS's run list BEFORE anything is re-driven —
	// the only place a re-hosted run's recipe is available to this process.
	recipe := rs.recipeOf(ctx, workflowID)

	bridge := rs.bridges.factory()
	if err := bridge.Start(ctx, &vibekit.StartOpts{
		Lifetime:    rs.lifecycle.shutdownCtx,
		AgentEngine: resolveAgentEngine(),
		Presets:     securityPresets(ctx, rs.lifecycle.configDir),
		ToolSearch:  toolSearchEnabled(ctx, rs.lifecycle.configDir),
		Knowledge:   knowledgeEnabled(ctx, rs.lifecycle.configDir),
		Memory:      memoryEnabled(ctx, rs.lifecycle.configDir),
	}); err != nil {
		return vibekit.RunRetriedResponse{}, rs.retryStartErr(ctx, err)
	}
	// Register BEFORE the call: retry's first lifecycle frame follows it
	// immediately, and a frame arriving before the map entry would find no
	// bridge to answer through.
	rs.bridges.insert(chatID, &sharedBridge{bridge: bridge, state: bridgeIdle})
	go rs.coord.Forward(chatID, bridge)

	// The lease before the verb, exactly as a fresh launch grants between
	// `new` and `invoke`: retry's own `run_start` can arrive before the call
	// returns.
	minted := false
	if _, held := rs.lease(workflowID); !held {
		rs.grantLease(ctx, workflowID, recipe, manualLaunch())
		minted = true
	}

	out, err := rs.loadThenRetry(ctx, bridge, workflowID)
	if errors.Is(err, errRetryOutcomeUnreadable) {
		// KAS ACCEPTED the retry, so the run may be re-driving inside the bridge
		// this call just started: it keeps its process and its lease, and only the
		// report is lost. Tearing it down here would kill the work mid-node
		// because a reply could not be parsed.
		rs.rearmRetried(ctx, workflowID, recipe)
		return vibekit.RunRetriedResponse{}, err
	}
	if err != nil {
		// Nothing is executing: tear the bridge down rather than leaving a
		// process hosting a run it failed to restart, and give back a lease this
		// call minted for a run that never re-drove.
		if minted {
			rs.releaseLease(ctx, workflowID)
		}
		rs.coord.CloseBridge(chatID)
		return vibekit.RunRetriedResponse{}, err
	}
	// A fresh clock and a clean row, both only now that the retry has landed:
	// the run is executing again, so its recorded termination is no longer a
	// fact about it, and the client lets a recognised end_reason outrank live
	// status.
	rs.rearmRetried(ctx, workflowID, recipe)
	slog.Info("workflow run re-hosted and retried",
		"workflow_id", workflowID, "recipe", recipe,
		"retried_nodes", len(out.RetriedNodeIDs), "status", out.Status)
	return out, nil
}

// loadThenRetry registers the run in a fresh process and retries it.
//
// The `load` is not optional and not defensive: KAS's retry handler requires the
// run in its live registry and says so ("not registered. Load or create it
// first."). Its workspacePaths argument is the same one `new` and `list` take.
func (rs *Runs) loadThenRetry(
	ctx context.Context, bridge acpCaller, workflowID string,
) (vibekit.RunRetriedResponse, error) {
	resp, err := bridge.Call(ctx, methodKiroWorkflowLoad, map[string]any{
		keyWorkflowID:     workflowID,
		keyWorkspacePaths: []string{rs.lifecycle.workDir},
	})
	if cErr := runCallErr(resp, err); cErr != nil {
		return vibekit.RunRetriedResponse{}, fmt.Errorf("workflow load: %w", rs.retryDeadlineErr(ctx, cErr))
	}
	return rs.retryCall(ctx, bridge, workflowID)
}

// retryCall issues the verb and decodes its outcome report.
//
// Folded through runCallErr like every other verb: before this, retry read only
// the transport error, so a JSON-RPC refusal that arrived as a well-formed
// response with an `error` member was reported as a success.
func (rs *Runs) retryCall(
	ctx context.Context, bridge acpCaller, workflowID string,
) (vibekit.RunRetriedResponse, error) {
	none := vibekit.RunRetriedResponse{}
	resp, err := bridge.Call(ctx, methodKiroWorkflowRetry, map[string]any{keyWorkflowID: workflowID})
	if cErr := runCallErr(resp, err); cErr != nil {
		return none, fmt.Errorf("workflow retry: %w", rs.retryDeadlineErr(ctx, cErr))
	}
	// Past this point KAS has ACCEPTED the verb, so every failure below is a lost
	// REPORT rather than a retry that did not happen — errRetryOutcomeUnreadable
	// first, and the detail behind it for the log.
	if resp == nil || len(resp.Result) == 0 {
		return none, fmt.Errorf("%w: the reply carried no outcome", errRetryOutcomeUnreadable)
	}
	var out kasRetryOutcome
	if uErr := json.Unmarshal(resp.Result, &out); uErr != nil {
		return none, fmt.Errorf("%w: undecodable outcome: %w", errRetryOutcomeUnreadable, uErr)
	}
	// The reply must be ABOUT the run that was asked for. The orphan predicate
	// applies the same rule to `inspect` and for the same reason: a reply carrying
	// one run's outcome while naming another would be reported to the reader as
	// theirs.
	if out.WorkflowID != "" && out.WorkflowID != workflowID {
		return none, fmt.Errorf("%w: the reply names run %q, not %q",
			errRetryOutcomeUnreadable, out.WorkflowID, workflowID)
	}
	// Never nil on the wire, so a caller reporting the count does not have to
	// distinguish "none" from "absent".
	nodes := out.RetriedNodeIDs
	if nodes == nil {
		nodes = []string{}
	}
	return vibekit.RunRetriedResponse{Status: out.Status, RetriedNodeIDs: nodes}, nil
}

// retryStartErr labels a failed bridge start: the deadline when this call's own
// budget ran out, the start failure otherwise.
func (rs *Runs) retryStartErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return errRetryEngineSlow
	}
	return fmt.Errorf("retry bridge start: %w", err)
}

// retryDeadlineErr rewrites a call that failed because THIS call's budget
// expired into errRetryEngineSlow, so the reader is told to try again rather
// than shown a cancellation they did not cause.
func (rs *Runs) retryDeadlineErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return errRetryEngineSlow
	}
	return err
}

// recipeOf reads a run's recipe NAME off KAS's own run list — the only place
// this process can learn the recipe of a run it did not launch.
//
// Best-effort — "" when the list cannot be read or does not carry the run —
// because a retry must not fail over a name.
func (rs *Runs) recipeOf(ctx context.Context, workflowID string) string {
	runs, err := rs.listRaw(ctx)
	if err != nil {
		slog.Warn("could not read a retried run's recipe, so its lease carries none",
			"workflow_id", workflowID, "error", err)
		return ""
	}
	for i := range runs {
		if runs[i].WorkflowID == workflowID {
			return runs[i].Name
		}
	}
	return ""
}

// SetStepStatus marks an in-flight step completed, failed, or running so a
// wedged run can advance.
//
// Resolved through hostBridge rather than the run's OWN bridge: an
// agent-launched run has none and never will (KAS parents it on the calling
// chat's session), so keying on `run:<id>` alone answered errRunNotHosted for
// that whole population unconditionally — which is exactly the population an
// agent creates. It still does NOT re-host, because this verb acts on a step
// that is IN FLIGHT and a run with no live process has none.
func (rs *Runs) SetStepStatus(ctx context.Context, workflowID, nodeID, status string) error {
	if nodeID == "" {
		return errors.New("missing node id")
	}
	if !slices.Contains(runStepStatuses, status) {
		return fmt.Errorf("step status must be one of %v", runStepStatuses)
	}
	sb := rs.hostBridge(ctx, workflowID)
	if sb == nil {
		return errRunNotHosted
	}
	_, err := sb.bridge.Call(ctx, methodKiroWorkflowUpdate, map[string]any{
		keyWorkflowID: workflowID,
		"update": map[string]any{
			"type":   "set_step_status",
			"nodeId": nodeID,
			"status": status,
		},
	})
	if err == nil && status == runStepRunning {
		// The step is being re-driven WITHOUT the user's words, so whatever it
		// asked is no longer answerable and every surface still showing that
		// question has to be told. SettledByUser because this IS the reader's
		// decision — they clicked Continue-without-answering.
		rs.settleAskForNode(ctx, workflowID, nodeID, vibekit.SettledByUser)
	}
	return err
}

// The step statuses a human may set. Narrowed deliberately: KAS's `update` verb
// also carries `replace_remaining`, which is a plan editor and not proposed.
//
// `running` is the CONTINUE-WITHOUT-ANSWERING verb, and what it does is worth
// stating precisely because the name does not say it: KAS clears the step node's
// `completionSignal` and re-invokes the run, so the step proceeds with its
// DEFAULT continuation prompt rather than with anything the user typed. That is
// the honest escape hatch for a step parked on a question whose text this process
// lost across a restart — plain Resume cannot do it, because resume clears the
// run's pauseReason and leaves the signal, so the next `executeStep` re-parks
// under a different sentence.
const (
	runStepCompleted = "completed"
	runStepFailed    = "failed"
	runStepRunning   = "running"
)

// runStepStatuses is the allowlist, in the order the refusal names them.
var runStepStatuses = []string{runStepCompleted, runStepFailed, runStepRunning}

// errAskAlreadySettled means the ask an answer names is no longer open:
// somebody else answered it, the step moved on, or the run ended. Distinct from
// a KAS refusal so the REST layer can answer 409 naming the situation rather
// than 500.
var errAskAlreadySettled = errors.New(
	"that question has already been answered, or the step it belonged to has moved on",
)

// AnswerInput answers one parked step with the user's words.
//
// THE CLAIM COMES FIRST, and the order is the contract — TakePendingPerm's rule
// applied to a durable ask. Two surfaces are offered the same question and KAS
// accepts exactly one answer: `tryResumeStepWithMessage` returns false once the
// step is no longer paused, and the loser's `session/prompt` would then fall
// through to an ORDINARY prompt on the step's own session, injecting a message
// into a step nobody asked to steer. A caller that loses the claim must not send.
//
// The answer verb is a plain `session/prompt` addressed to the PAUSED STEP's own
// session, not a workflow verb. KAS's prompt path checks
// `tryResumeStepWithMessage` before it reaches the model: on a match it clears the
// step's `need_input` signal, re-drives the step with the text, and answers
// `end_turn` immediately. `residentSessionState` hydrates a session from disk on
// demand and the reroute re-registers a run the runner had forgotten, so this
// works on a run rehydrated after a restart — which is what makes the reconciled
// ask answerable rather than merely visible.
//
// A FAILED prompt puts the claim back. Without that, a transport blip would leave
// the run parked with its card gone from every surface and no way to bring it
// back short of another restart.
func (rs *Runs) AnswerInput(ctx context.Context, workflowID, askID, text string) error {
	if workflowID == "" || askID == "" {
		return errors.New("missing workflow id or ask id")
	}
	if strings.TrimSpace(text) == "" {
		return errors.New("an answer cannot be empty")
	}
	// BEFORE the claim, and that ordering is the whole guard: from here until this
	// call returns there is no instant at which the registry holds nothing for the
	// run AND nothing reports an answer in flight, so the reconcile below cannot
	// mint a text-less twin in the gap. See pendingRunAsks.beginAnswer.
	rs.asks.beginAnswer(workflowID)
	defer rs.asks.endAnswer(workflowID)
	a, ok := rs.asks.TakeIfPresent(workflowID, askID)
	if !ok {
		return errAskAlreadySettled
	}
	session := cmp.Or(a.payload.StepSessionID, rs.pausedStepSession(ctx, workflowID))
	sb := rs.hostBridge(ctx, workflowID)
	if session == "" || sb == nil {
		rs.restoreAsk(ctx, a)
		if session == "" {
			return errors.New("the step that asked cannot be addressed on this server")
		}
		return errRunNotHosted
	}
	resp, err := sb.bridge.Call(ctx, vibekit.MethodPrompt, map[string]any{
		vibekit.KeySessionID: session,
		vibekit.KeyPrompt:    []any{vibekit.TextBlock(text)},
	})
	if cErr := runCallErr(resp, err); cErr != nil {
		rs.restoreAsk(ctx, a)
		return cErr
	}
	// The run is executing again, so it gets a FRESH budget rather than the
	// remainder of whatever it held when it parked — the same rule Resume follows,
	// and for the same reason: each arm bounds EXECUTING time.
	rs.armDeadline(ctx, workflowID)
	rs.announceSettled(ctx, a, vibekit.SettledByUser)
	slog.Info("answered a parked workflow step", "workflow_id", workflowID,
		"node_id", a.payload.NodeID, "ask_id", askID)
	return nil
}

// pausedStepSession resolves the answer address from a fresh `inspect`, for an
// ask that carries none.
//
// Only the reconciled ask reaches this: a live notify frame arrives carrying its
// own `callerSessionId`, which is why the happy path costs no round trip. Best
// effort — "" when the run cannot be read — because the caller turns that into a
// refusal naming the situation rather than a fault.
func (rs *Runs) pausedStepSession(ctx context.Context, workflowID string) string {
	raw, err := rs.rawInspect(ctx, workflowID)
	if err != nil {
		slog.Warn("could not read a parked run's state, so its step has no answer address",
			"workflow_id", workflowID, "error", err)
		return ""
	}
	var res askInspect
	if json.Unmarshal(raw, &res) != nil || res.State == nil {
		return ""
	}
	leaf, _ := pausedLeaf(res.State.Root, nil)
	if leaf == nil {
		return ""
	}
	return leaf.SessionID
}

// hostedControl issues a verb that must run on the run's OWN bridge, and
// refuses rather than falling back to the utility bridge.
//
// The fallback would be actively harmful for two of the three: resume and
// retry make the run EXECUTE, and the utility bridge is a text-only session
// that denies every permission request and errors every fs/terminal call —
// so a run resumed there would grind through its steps with no tools. Pause
// cannot use it either.
//
// The remaining cost: these verbs reach a run only while SOME live bridge in
// this process holds its registry entry, so a run orphaned by a container
// restart can still only be cancelled (retry does re-host, and its legality
// window is the two statuses where that is the only option).
func (rs *Runs) hostedControl(ctx context.Context, workflowID, method string) error {
	sb := rs.hostBridge(ctx, workflowID)
	if sb == nil {
		return errRunNotHosted
	}
	resp, err := sb.bridge.Call(ctx, method, map[string]any{keyWorkflowID: workflowID})
	return runCallErr(resp, err)
}

// hostBridge resolves the bridge whose process holds the run's registry
// entry.
//
// Two ways a run is hosted. A run vibekit LAUNCHED has a bridge under its
// synthetic `run:<id>` chat id. An AGENT-launched run has none: KAS parents
// it on the calling chat's session, so the LAUNCHING CHAT's bridge is the
// process that registered it.
//
// Costs one `workflow/list` round trip on the agent-launched path only —
// acceptable for a deliberate user action, not a hot path.
func (rs *Runs) hostBridge(ctx context.Context, workflowID string) *sharedBridge {
	_, sb := rs.hostBridgeChat(ctx, workflowID)
	return sb
}

// hostBridgeChat is hostBridge plus the CHAT ID that bridge belongs to:
// `run:<workflowId>` for a run vibekit launched, the LAUNCHING CHAT's own id for
// an agent-launched one, and "" when nothing in this process hosts the run.
//
// The id is what a synthesised ask is keyed to (askChatID), so a reconstructed
// question lands in the dock of the conversation the run was launched from rather
// than only in the run tab. It is one function rather than two because deriving
// the id separately would repeat the `workflow/list` round trip the bridge lookup
// already paid for.
func (rs *Runs) hostBridgeChat(
	ctx context.Context, workflowID string,
) (vibekit.ChatID, *sharedBridge) {
	if sb := rs.bridges.get(runChatID(workflowID)); sb != nil {
		return runChatID(workflowID), sb
	}
	// One resolution for both questions vibekit asks about a run's parent, in
	// run_affordance.go: which chat owns it, and — below — whether that chat is a
	// live carrier.
	chatID, _ := rs.chatForSession(ctx, rs.parentSession(ctx, workflowID))
	return rs.hostBridgeFor(workflowID, chatID)
}

// hostBridgeFor is hostBridgeChat for a caller that ALREADY KNOWS the run's parent
// chat: no RPC, no chat-store read, and only a LIVE bridge answers.
//
// The resolution above costs a `workflow/list` round trip plus a full chat-directory
// scan, and a route whose affordance gate resolved the same parent one step earlier
// would spend both twice per click, before the engine starts. Two reads can also
// DISAGREE, and then the verb acts on a different answer than the gate approved.
func (rs *Runs) hostBridgeFor(
	workflowID string, parentChat vibekit.ChatID,
) (vibekit.ChatID, *sharedBridge) {
	if sb := rs.bridges.get(runChatID(workflowID)); sb != nil {
		return runChatID(workflowID), sb
	}
	if parentChat == "" {
		return "", nil
	}
	sb := rs.bridges.get(parentChat)
	if sb == nil {
		return "", nil
	}
	return parentChat, sb
}

// parentSession reports the KAS session that launched a run, or "" when the
// run is parentless, unknown, or the inventory cannot be read.
//
// `workflow/list` is the only source: `inspect` does not carry the field, and
// the notification that does arrives once at run_start and is not retained.
func (rs *Runs) parentSession(ctx context.Context, workflowID string) string {
	if workflowID == "" {
		return ""
	}
	runs, err := rs.listRaw(ctx)
	if err != nil {
		slog.Warn("could not read the run inventory, so a run's parent chat is unknown",
			"workflow_id", workflowID, "error", err)
		return ""
	}
	for i := range runs {
		if runs[i].WorkflowID == workflowID {
			return runs[i].ParentSessionID
		}
	}
	return ""
}

// control issues a verb that is safe on either connection, preferring the
// run's OWN bridge and falling back to the utility bridge.
//
// Only cancel qualifies: cancel rehydrates from disk and only WRITES state,
// so a text-only session is a sufficient carrier for it. The verbs that
// execute use hostedControl instead.
func (rs *Runs) control(ctx context.Context, workflowID, method, logLabel string) error {
	params := map[string]any{keyWorkflowID: workflowID}
	if sb := rs.bridges.get(runChatID(workflowID)); sb != nil {
		resp, err := sb.bridge.Call(ctx, method, params)
		return runCallErr(resp, err)
	}
	_, err := rs.utility().session.rawCall(ctx, logLabel, method, callerParams(params))
	return err
}

// dispatch is translateACPEvent's branch for frames arriving on a RUN
// bridge. The population differs from a chat's in both directions:
//
//   - host REQUESTS (fs, terminals, auth, secrets) go through the SAME
//     handlers a chat's do, keyed by the synthetic chat id.
//   - the three ASK kinds (permission, elicitation, user input) go through
//     their chat handlers too, broadcast keyed `run:<id>`.
//   - workflow LIFECYCLE frames go to the run translate handlers with an
//     EMPTY chat id (workspace-global): a parentless run is not owned by any
//     chat.
//   - session/update is PROJECTED as run-scoped `run_step` events, never
//     into a transcript — there is no chat to buffer into. See
//     translate/workflow_step_content.go.
func (rt *Runtime) dispatch(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	if msg.ID != nil {
		rt.dispatchRequest(ctx, chatID, msg)
		return
	}
	// BEFORE the workflow prefix test, because this method is not under it and
	// would otherwise reach the Debug tail. It keeps the run bridge's OWN chat id
	// rather than being flattened to workspace-global like the lifecycle frames:
	// an ask is answerable, so it has to land on a surface, and `run:<id>` is the
	// run tab's dock key.
	if msg.Method == methodKiroSessionNotify {
		rt.runs.handleSessionNotify(ctx, chatID, msg)
		return
	}
	if strings.HasPrefix(msg.Method, "_kiro/workflow/") {
		if fn, ok := rt.chatHandlers[msg.Method]; ok {
			fn(ctx, "", msg)
		}
		if msg.Method == methodWFRunComplete {
			rt.closeFinishedBridge(chatID, msg)
		}
		return
	}
	if msg.Method == vibekit.MethodSessionUpdate {
		rt.translator.HandleRunStepFrame(ctx, workflowIDOf(chatID), msg.Params)
		return
	}
	slog.Debug("run bridge: unhandled notification", "method", msg.Method, "chat_id", chatID)
}

// dispatchRequest answers an A→C request on a run bridge. The ladder
// mirrors translateACPEvent's request half minus the chat-only concerns; an
// unmatched request is REFUSED rather than dropped, because an unanswered
// request wedges the step's turn.
func (rt *Runtime) dispatchRequest(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	switch {
	case rt.inbound.handleFSRequest(ctx, chatID, msg),
		rt.inbound.handleKiroFSRequest(ctx, chatID, msg),
		rt.inbound.handleKiroClientRequest(ctx, chatID, msg),
		rt.inbound.handleKiroSecretRequest(ctx, chatID, msg):
		return
	case strings.HasPrefix(msg.Method, methodTermPrefix):
		rt.handleTerminalRequest(ctx, chatID, msg.Method, msg)
		return
	}
	if fn, ok := rt.chatHandlers[msg.Method]; ok &&
		(msg.Method == vibekit.MethodRequestPermission ||
			msg.Method == vibekit.MethodElicitationCreate ||
			msg.Method == vibekit.MethodKiroUserInput) {
		fn(ctx, chatID, msg)
		return
	}
	slog.Warn("run bridge: refusing unexpected request", "method", msg.Method, "chat_id", chatID)
	_ = rt.BridgeRespond(ctx, chatID, *msg.ID, nil, &vibekit.RPCError{
		Code:    vibekit.RPCCodeMethodNotFound,
		Message: "unsupported on a run bridge: " + msg.Method,
	})
}

// closeFinishedBridge closes a run bridge once its run reached a TERMINAL
// state. One rule covers completion, failure and cancel — they all end in
// run_complete — and a non-terminal run_complete (a policy pause reports
// through the same frame) keeps the bridge, because the run is still this
// process's to resume.
//
// The close runs in a goroutine because this is called FROM the bridge's own
// forward loop, and CloseBridge → Stop closes the channel that loop ranges
// over.
func (rt *Runtime) closeFinishedBridge(chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	var p struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(msg.Params, &p) != nil || !terminalRunStatus(p.Status) {
		return
	}
	slog.Info("run finished, closing its bridge", "chat_id", chatID, "status", p.Status)
	// The run's LEASE is not released here. forgetBounds owns that, on the same
	// terminal frame, because it is the one site every origin reaches — an
	// agent-parented run has no bridge of its own to close.
	go rt.coord.CloseBridge(chatID)
}

// terminalRunStatus mirrors KAS's isTerminalWorkflowStatus: paused is the one
// non-terminal run_complete status (an onMaxIterations policy stop).
func terminalRunStatus(s string) bool {
	return s == "completed" || s == "failed" || s == "aborted" || s == "cancelled"
}

// recipeBySource resolves a launch source against the CURRENT recipe list.
func (rs *Runs) recipeBySource(ctx context.Context, source string) (vibekit.Recipe, error) {
	if source == "" {
		return vibekit.Recipe{}, errors.New("missing recipe source")
	}
	recipes, err := rs.listRecipes(ctx)
	if err != nil {
		return vibekit.Recipe{}, err
	}
	for _, r := range recipes {
		if r.Source == source {
			return r, nil
		}
	}
	return vibekit.Recipe{}, fmt.Errorf("unknown recipe source %q", source)
}

// recipeIdle enforces the single-run rule against the current run list.
//
// KAS's list stays the source of truth here, deliberately: it is the only
// thing that sees the runs vibekit did not launch — an agent's, and the
// TUI's — so keying the rule on vibekit's own leases instead would let a
// second live run of one recipe start.
//
// What the leases add is the ability to EXPLAIN a blocking row rather than
// refuse it blindly: before returning busy, ask whether that row is an
// orphan vibekit itself owns and left behind.
func (rs *Runs) recipeIdle(ctx context.Context, name string) error {
	runs, err := rs.list(ctx, nil)
	if err != nil {
		// The guard needs the list; launching blind would let a second live
		// run of the recipe exist, which the Run ⇄ Cancel row cannot represent.
		return fmt.Errorf("run list unavailable: %w", err)
	}
	for i := range runs {
		if runs[i].Name != name || terminalRunStatus(runs[i].Status) {
			continue
		}
		if rs.clearBlockingOrphan(ctx, runs[i].WorkflowID, runs[i].Status) {
			slog.Info("cleared a restart-orphaned run that was holding its recipe",
				"workflow_id", runs[i].WorkflowID, "recipe", name)
			continue
		}
		return errRecipeBusy
	}
	return nil
}

// kasRecipe is one listRecipes entry as KAS reports it (probe 26). `plan` rides
// through as raw JSON — see vibekit.Recipe.
type kasRecipe struct {
	Inputs      map[string]string `json:"inputs"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Source      string            `json:"source"`
	Plan        json.RawMessage   `json:"plan"`
	BuiltIn     bool              `json:"builtIn"`
}

// listRecipes fetches the launchable recipe list (bundled + workspace) through
// the utility session — a pure read, safe on the shared connection.
func (rs *Runs) listRecipes(ctx context.Context) ([]vibekit.Recipe, error) {
	u := rs.utility()
	cctx, cancel := context.WithTimeout(ctx, sessionListTimeout)
	defer cancel()
	raw, err := u.session.rawCall(cctx, "workflow listRecipes call", methodKiroWorkflowListRecipes,
		callerParams(map[string]any{keyWorkspacePaths: []string{rs.lifecycle.workDir}}))
	if err != nil {
		return nil, err
	}
	var list struct {
		Recipes []kasRecipe `json:"recipes"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	out := make([]vibekit.Recipe, 0, len(list.Recipes))
	for _, r := range list.Recipes {
		if r.Name == "" || r.Source == "" {
			continue
		}
		out = append(out, vibekit.Recipe{
			Name:        r.Name,
			Description: r.Description,
			Source:      r.Source,
			Inputs:      r.Inputs,
			Plan:        r.Plan,
			BuiltIn:     r.BuiltIn,
		})
	}
	return out, nil
}

// workflowNew creates the run on the given bridge and returns its id.
func (rs *Runs) workflowNew(ctx context.Context, bridge acpCaller, source string, inputs map[string]string) (string, error) {
	// inputs is always a map, never nil: KAS requires the key ("inputs is not
	// iterable" without it), and an input-less recipe takes {}.
	in := map[string]any{}
	for k, v := range inputs {
		in[k] = v
	}
	resp, err := bridge.Call(ctx, methodKiroWorkflowNew, map[string]any{
		"workflowPath":    source,
		keyWorkspacePaths: []string{rs.lifecycle.workDir},
		"inputs":          in,
	})
	if cErr := runCallErr(resp, err); cErr != nil {
		return "", fmt.Errorf("workflow new: %w", cErr)
	}
	var res struct {
		WorkflowID string `json:"workflowId"`
	}
	if err := json.Unmarshal(resp.Result, &res); err != nil || res.WorkflowID == "" {
		return "", errors.New("workflow new: reply carried no workflowId")
	}
	return res.WorkflowID, nil
}

// runCallErr folds a bridge Call's two failure channels into one error.
func runCallErr(resp *vibekit.RPCResponse, err error) error {
	if err != nil {
		return err
	}
	if resp != nil && resp.Error != nil {
		return resp.Error
	}
	return nil
}

// --- Restart recovery -------------------------------------------------------

// stalePauseReason is KAS's STALE_RUNNING_PAUSE_REASON — the pauseReason its
// read-path reconcile stamps on a run whose owning process died. Matched as a
// LITERAL: at least five sites set pauseReason and only this one means "the
// process died under it"; every other reason must be left alone.
const stalePauseReason = "Interrupted by agent restart; the previously running step was paused for resume."

// The pause reasons KAS records when a run stopped for a cause NOBODY CHOSE,
// beside the reconcile literal above.
//
// THESE ARE THE RESUME SIDE ONLY, and the asymmetry with stalePauseReason is
// the point: that literal licenses the orphan sweep to CANCEL a run; this
// wider set only ever licenses a RESUME. Resuming a run that did not need it
// costs nothing; cancelling one that did not need it costs the work.
//
// A network pause is matched by PREFIX because KAS interpolates the error
// code into it (`Transient connection error (EAI_AGAIN); …`). That prefix
// carries the whole distinguishing phrase, so it cannot reach any reason that
// must be left alone (a step waiting on a human, a policy stop, a recorded
// failure).
const (
	interruptedPauseReason  = "Step interrupted (agent shutdown or connection reset); will resume."
	modelServicePauseReason = "Transient model service error (service 5xx/throttling); will resume."
	networkPausePrefix      = "Transient connection error ("
)

// resumablePause reports whether a pause reason means the run stopped for a
// cause nobody chose, and is therefore vibekit's to resume unasked.
//
// Pure and reason-only, so the table test is the reason list rather than a
// set of RPC fixtures; the status and identity conditions live with the
// caller that reads them off one reply.
func resumablePause(reason string) bool {
	switch reason {
	case stalePauseReason, interruptedPauseReason, modelServicePauseReason:
		return true
	}
	return strings.HasPrefix(reason, networkPausePrefix)
}

// resumeInterruptedRuns resumes the runs a chat's rehydrated bridge should
// pick back up: the ones ITS sessions launched, that stopped for a cause
// nobody chose.
//
// This is why there is no Resume button anywhere: a chat's bridge dying
// pauses its runs, and the user's next message respawns the bridge — so
// resuming here makes the run heal WITH the chat.
//
// Scoped twice: to THIS chat's session chain (never resumeAll, which would
// sweep runs other chats or the TUI paused on purpose), and to the
// involuntary pause reasons.
func (rs *Runs) resumeInterruptedRuns(ctx context.Context, chatID vibekit.ChatID) {
	chat, ok := rs.chats.Get(ctx, chatID)
	if !ok {
		return
	}
	chain := make(map[string]bool, len(chat.PriorACPSessionIDs)+1)
	chain[chat.ACPSessionID] = true
	for _, id := range chat.PriorACPSessionIDs {
		chain[id] = true
	}

	runs, err := rs.listRaw(ctx)
	if err != nil {
		slog.Warn("rehydrate: run list unavailable, skipping resume sweep", "chat_id", chatID, "error", err)
		return
	}
	for i := range runs {
		r := &runs[i]
		if r.Status != runStatusPaused || !chain[r.ParentSessionID] {
			continue
		}
		rs.resumeIfInterrupted(ctx, chatID, r.WorkflowID)
	}
}

// maxAutoHeals bounds the automatic resumes one run may spend between two
// pieces of progress. Three, because a fourth attempt against a dead network
// tells nobody anything the third did not.
const maxAutoHeals = 3

// healBaseDelay is the wait before the FIRST automatic resume, doubling per
// attempt (5s, 10s, 20s). Not zero, so a deliberate cancel can land first —
// the callback re-reads the run's state rather than trusting the frame that
// scheduled it.
//
// A `var` so a test can drive the whole path in milliseconds instead of
// waiting out a real backoff. Never reassigned in production.
var healBaseDelay = 5 * time.Second

// healPaused resumes a run KAS has just parked for a reason nobody chose.
//
// This is the trigger the recovery model was missing: resumeInterruptedRuns
// only fires when a chat's bridge comes BACK, so a run pausing on a transient
// network error while its bridge stays alive had no trigger at all.
//
// KAS emits `_kiro/workflow/paused` with `{workflowId, pauseReason}` on the
// LAUNCHING CHAT's bridge the moment it parks a run, so the timing is exact
// with no polling.
//
// Runs AFTER `next`, so the client renders the pause before anything undoes
// it.
func (rs *Runs) healPaused(
	next func(context.Context, vibekit.ChatID, *vibekit.RPCResponse),
) func(context.Context, vibekit.ChatID, *vibekit.RPCResponse) {
	return func(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
		next(ctx, chatID, msg)
		f := decodePauseFrame(msg)
		if f.WorkflowID == "" || chatID == "" || !resumablePause(f.PauseReason) {
			return
		}
		attempt, ok := rs.claimHeal(f.WorkflowID)
		if !ok {
			slog.Warn("a run keeps pausing for a cause nobody chose; leaving it paused",
				"workflow_id", f.WorkflowID, "chat_id", chatID,
				"pause_reason", f.PauseReason, "attempts", attempt)
			return
		}
		delay := healBaseDelay * time.Duration(1<<(attempt-1))
		slog.Info("scheduling the automatic resume of an involuntarily paused run",
			"workflow_id", f.WorkflowID, "chat_id", chatID,
			"pause_reason", f.PauseReason, "delay", delay)
		// The timer handle is deliberately NOT tracked, unlike the deadline
		// timers in `bounds.timers`: those must be stoppable because a fired
		// one CANCELS a run, while this one re-reads the run's state first and
		// does nothing unless it is still involuntarily paused.
		time.AfterFunc(delay, func() {
			hctx, cancel := rs.lifecycle.derivedContext()
			defer cancel()
			rs.resumeIfInterrupted(hctx, chatID, f.WorkflowID)
		})
	}
}

// pauseFrame is the two fields the heal reads off `_kiro/workflow/paused`.
//
// Its own minimal decode rather than a share of `lifecycleFrame`, which
// carries the status and name the BOUNDS read and no pause reason at all.
type pauseFrame struct {
	WorkflowID  string `json:"workflowId"`
	PauseReason string `json:"pauseReason"`
}

func decodePauseFrame(msg *vibekit.RPCResponse) pauseFrame {
	var f pauseFrame
	if msg == nil || len(msg.Params) == 0 {
		return f
	}
	if json.Unmarshal(msg.Params, &f) != nil {
		return pauseFrame{}
	}
	return f
}

// healProgress gives a run its heal budget back when a node completes.
//
// Progress is the only honest evidence that whatever paused the run has
// cleared; without this the budget is per-process and a long-running job
// would spend its three attempts on one morning blip.
// It also retires the ask that node was holding, if it was holding one.
//
// A node completing is the honest end of ITS OWN wait, and it covers the case
// nothing else does: the step was answered from somewhere vibekit is not — the
// TUI, another ACP client — so no answer path here ever claimed the entry, and
// without this the card would sit live until the run ended. Node-SCOPED rather
// than run-scoped precisely because a parallel branch's node can complete while
// a sibling branch's step is still waiting, and clearing the run would take that
// sibling's live ask with it.
func (rs *Runs) healProgress(
	next func(context.Context, vibekit.ChatID, *vibekit.RPCResponse),
) func(context.Context, vibekit.ChatID, *vibekit.RPCResponse) {
	return func(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
		if f := decodeNodeFrame(msg); f.WorkflowID != "" {
			rs.clearHeals(f.WorkflowID)
			// SettledByMoot rather than SettledByUser: this frame says the node
			// moved on, which covers a completion, a failure and an abort alike,
			// and vibekit's own answer path already settled anything it accepted
			// before it sent. So nothing reaching here was answered through this
			// server, and claiming it was is a sentence the reader can disprove.
			rs.settleAskForNode(ctx, f.WorkflowID, f.NodeID, vibekit.SettledByMoot)
		}
		next(ctx, chatID, msg)
	}
}

// nodeFrame is the two fields the node-scoped ask clear reads off
// `_kiro/workflow/node_complete`. Its own minimal decode, for the reason
// pauseFrame beside it has one: lifecycleFrame carries the status and name the
// BOUNDS read and no node id at all.
type nodeFrame struct {
	WorkflowID string `json:"workflowId"`
	NodeID     string `json:"nodeId"`
}

func decodeNodeFrame(msg *vibekit.RPCResponse) nodeFrame {
	var f nodeFrame
	if msg == nil || len(msg.Params) == 0 {
		return f
	}
	if json.Unmarshal(msg.Params, &f) != nil {
		return nodeFrame{}
	}
	return f
}

// resumeIfInterrupted inspects one paused run and resumes it when its pause
// reason means the stop was involuntary. Resumed on the CHAT's bridge, so the
// chat's process becomes the run's owner again.
func (rs *Runs) resumeIfInterrupted(ctx context.Context, chatID vibekit.ChatID, workflowID string) {
	// The orphan sweep's predicate is the NARROWER `restartPaused`,
	// deliberately: it cancels, and only "the owning process died" licenses
	// that. This one resumes, so it reads the wider involuntary set.
	if !rs.involuntarilyPaused(ctx, workflowID) {
		return
	}
	sb := rs.bridges.get(chatID)
	if sb == nil {
		return
	}
	resp, err := sb.bridge.Call(ctx, methodKiroWorkflowResume, map[string]any{keyWorkflowID: workflowID})
	if cErr := runCallErr(resp, err); cErr != nil {
		slog.Warn("rehydrate: resume failed", "workflow_id", workflowID, "chat_id", chatID, "error", cErr)
		return
	}
	slog.Info("rehydrate: resumed restart-paused run", "workflow_id", workflowID, "chat_id", chatID)
}

// listRaw lists runs with their raw parent session ids, for callers
// that scope by session chain rather than by resolved chat.
func (rs *Runs) listRaw(ctx context.Context) ([]kasWorkflowRun, error) {
	u := rs.utility()
	cctx, cancel := context.WithTimeout(ctx, sessionListTimeout)
	defer cancel()
	raw, err := u.session.rawCall(cctx, "workflow list call", methodKiroWorkflowList,
		callerParams(map[string]any{keyWorkspacePaths: []string{rs.lifecycle.workDir}}))
	if err != nil {
		return nil, err
	}
	var list kasWorkflowRuns
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	return list.Runs, nil
}

// CancelForChat cancels every non-terminal run this chat's sessions launched.
// The tab-close half of the run lifecycle: a run is durable state, so
// killing the chat's process only PAUSES it — it must be told to cancel, per
// run, while the owning bridge is still alive to say it.
//
// It BEGINS with a record read, so it no-ops on a deleted chat.
func (rs *Runs) CancelForChat(ctx context.Context, chatID vibekit.ChatID) {
	chat, ok := rs.chats.Get(ctx, chatID)
	if !ok {
		return
	}
	rs.CancelForSessions(ctx, chatID, chat.SessionChain())
}

// CancelForSessions cancels every non-terminal run launched by one of these
// sessions. The chain-shaped half of CancelForChat: it reads no chat record,
// so it works from a CAPTURED chain after the record is gone.
//
// Best-effort throughout: blocking a tab close on a workflow RPC would
// invert the gesture's meaning — the user said stop, not wait.
func (rs *Runs) CancelForSessions(ctx context.Context, chatID vibekit.ChatID, sessionChain []string) {
	if len(sessionChain) == 0 {
		return
	}
	chain := make(map[string]bool, len(sessionChain))
	for _, id := range sessionChain {
		chain[id] = true
	}
	runs, err := rs.listRaw(ctx)
	if err != nil {
		slog.Warn("close: run list unavailable, skipping run cancel", "chat_id", chatID, "error", err)
		return
	}
	for i := range runs {
		r := &runs[i]
		if terminalRunStatus(r.Status) || !chain[r.ParentSessionID] {
			continue
		}
		if cErr := rs.Cancel(ctx, r.WorkflowID); cErr != nil {
			slog.Warn("close: run cancel failed", "workflow_id", r.WorkflowID, "chat_id", chatID, "error", cErr)
			continue
		}
		slog.Info("close: cancelled chat's run", "workflow_id", r.WorkflowID, "chat_id", chatID)
	}
}
