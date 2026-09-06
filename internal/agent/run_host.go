package agent

// A parentless run gets ONE bridge under the synthetic chat id
// `run:<workflowId>`, so every response path resolves it by chat id. It has no
// chat file, and its loss is a PAUSE. The bridge closes on terminal
// `run_complete`, never on the cancel verb: the owning process certifies the
// terminal state at the node boundary, so an earlier kill can leave it `paused`.

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
// Real chat ids are client-generated `c-*` uuids, so the two cannot collide.
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
// Empty for anything that is not a run chat id.
func workflowIDOf(chatID vibekit.ChatID) string {
	if !isRunChat(chatID) {
		return ""
	}
	return strings.TrimPrefix(string(chatID), runChatPrefix)
}

// launchTimeout bounds the launch handshake. Generous because a first launch
// may unpack a KAS runtime tree (~240 MB) before the process answers.
const launchTimeout = 120 * time.Second

// errRecipeBusy reports the single-run rule: one live run per recipe, globally.
var errRecipeBusy = errors.New("this recipe already has a live run")

// Launch starts one parentless run of the recipe with the given source key and
// returns its workflow id and name. The source is re-validated against a fresh
// listRecipes call, so a client cannot aim this endpoint at a file that is not a
// recipe. A manual run of a SCHEDULED recipe is bounded by that recipe's next
// slot as well as the ceiling, and stays ATTENDED either way.
func (rs *Runs) Launch(ctx context.Context, source string, inputs map[string]string) (id, name string, err error) {
	return rs.launch(ctx, source, inputs, manualLaunch())
}

// LaunchScheduled launches a run on behalf of a schedule, marking it UNATTENDED
// for the duration. The mark rides the lease granted between `new` and `invoke`,
// the earliest point the workflow id exists and still before anything can
// execute, so no permission request slips through unmarked. slotAt is this run's
// own next slot; zero bounds the run by the ceiling alone.
func (rs *Runs) LaunchScheduled(ctx context.Context, source, scheduleID string, slotAt time.Time) (id, name string, err error) {
	return rs.launch(ctx, source, nil, scheduledLaunch(scheduleID, slotAt))
}

// launch is the shared body of both launch verbs; the origin carries what differs.
func (rs *Runs) launch(ctx context.Context, source string, inputs map[string]string, o launchOrigin) (id, name string, err error) {
	cctx, cancel := context.WithTimeout(ctx, launchTimeout)
	defer cancel()

	recipe, err := rs.recipeBySource(cctx, source)
	if err != nil {
		return "", "", err
	}
	if o.origin == runlease.OriginManual {
		// A manual run must yield to the recipe's own next slot, knowable only now.
		o.slotAt = rs.manualSlot(recipe.Source)
	}
	if bErr := rs.recipeIdle(cctx, recipe.Name); bErr != nil {
		return "", "", bErr
	}

	// Started OUTSIDE the manager: the map key is the workflow id, which only
	// `new`'s reply knows.
	bridge := rs.bridges.factory()
	// Same security profile as a chat bridge; an unattended run has nobody to
	// answer a prompt the profile was meant to remove.
	if sErr := bridge.Start(cctx, &vibekit.StartOpts{
		Lifetime: rs.lifecycle.shutdownCtx,
		// Named rather than left to buildACPArgs's default: a future edit to that
		// default cannot then silently put a run bridge on the legacy engine.
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

	// Register BEFORE invoke: the first lifecycle frame follows it immediately.
	rs.bridges.insert(runChatID(wfID), &sharedBridge{bridge: bridge, state: bridgeIdle})
	// The run's envelope, before anything can execute: single-run evidence, the
	// deadline's inputs, and the unattended mark the permission floor reads.
	rs.grantLease(cctx, wfID, recipe.Name, o)
	go rs.coord.Forward(runChatID(wfID), bridge)

	if _, err := bridge.Call(cctx, methodKiroWorkflowInvoke, map[string]any{keyWorkflowID: wfID}); err != nil {
		// Created but never started, so nothing is executing; leave no zombie row.
		rs.releaseLease(cctx, wfID)
		rs.coord.CloseBridge(runChatID(wfID))
		return "", "", fmt.Errorf("workflow invoke: %w", err)
	}
	// After invoke, so a run that never started leaves no timer. Idempotent with
	// the `run_start` frame's own arm.
	rs.armDeadline(cctx, wfID)
	slog.Info("workflow run launched", "workflow_id", wfID, "recipe", recipe.Name)
	return wfID, recipe.Name, nil
}

// Cancel asks a run to stop, on the USER's behalf.
//
// It takes the run's termination claim like every bound does, so a cancel pressed
// seconds before a ceiling cannot be relabelled "overran"; winning records
// NOTHING, and that absence is the third value the row's one field carries. A
// LOST claim returns nil, because something else got there first; a won claim
// whose RPC then fails is handed back, so a later Cancel is not refused.
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
// On the run's own bridge when one is live, through the utility session
// otherwise. The bridge is NOT closed here: the terminal `run_complete` the
// cancel produces closes it, because the owning process must live to the node
// boundary to certify the cancelled state.
func (rs *Runs) cancelRPC(ctx context.Context, workflowID string) error {
	return rs.control(ctx, workflowID, methodKiroWorkflowCancel, "workflow cancel call")
}

// Delete removes a run and everything either side keeps about it. The only run
// verb that is not recoverable.
//
// Order is the opposite of cancel's: KAS's delete cancels a non-terminal run
// itself, so pre-claiming termination would record an end reason for a row about
// to stop existing. The bridge IS closed here, because a deleted run has no
// state left to certify. A failed verb leaves everything in place.
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

// errRunNotHosted is returned by a control verb that needs the run's OWN bridge
// when there is none. Distinct from a KAS refusal so the REST layer can answer
// 409 with an explanation rather than 500.
var errRunNotHosted = errors.New(
	"this run has no live bridge on this server, so it cannot be paused or resumed from here; " +
		"cancel still works. A run from before the last restart is in this state, " +
		"and so is an agent-launched run whose chat is closed -- open that chat to bring it back",
)

// Pause asks a running run to stop at its next node boundary, keeping its state
// resumable. The reply confirms the ASK: KAS sets `control.pauseRequested` and
// the in-flight node runs to completion.
//
// Requires the run's own bridge: KAS's pause reaches `registry.require`, which
// throws for a run not in the live registry and does not rehydrate from disk.
func (rs *Runs) Pause(ctx context.Context, workflowID string) error {
	return rs.hostedControl(ctx, workflowID, methodKiroWorkflowPause)
}

// Resume re-drives a paused run. Re-arms the wall clock, because the pause
// parked it: a resumed run gets a FRESH budget, not the remainder of the old one.
func (rs *Runs) Resume(ctx context.Context, workflowID string) error {
	err := rs.hostedControl(ctx, workflowID, methodKiroWorkflowResume)
	if err == nil {
		rs.armDeadline(ctx, workflowID)
	}
	return err
}

// retryTimeout bounds the whole retry handshake, process start included.
//
// BELOW the browser's own request budget deliberately: on a longer deadline a
// slow engine start let the BROWSER abort first, and that cancellation tore down
// a freshly minted bridge, releasing the lease with nobody watching. DERIVED from
// clientRequestBudget so the relationship cannot be broken by editing one of the
// two. A `var` only so a test can drive the expiry in milliseconds.
var retryTimeout = clientRequestBudget - 5*time.Second

// clientRequestBudget is the ceiling retryTimeout fits under: the timeout
// @cplieger/fetch hard-wires into every apiAction, applied by the browser
// whatever the server thinks its own deadline is.
const clientRequestBudget = 30 * time.Second

// errRetryEngineSlow reports that the retry could not be handed off inside
// retryTimeout. Its own class so the REST layer can answer 503 "try again".
var errRetryEngineSlow = errors.New(
	"the run's engine did not start in time, so nothing was retried; try again",
)

// errRetryOutcomeUnreadable reports that KAS ACCEPTED the retry and its report
// could not be read.
//
// Its own class because the remedy inverts: the run may be executing, so retrying
// would ask for the work twice and killing the bridge would kill it mid-node.
// Refreshing is what the reader can act on.
var errRetryOutcomeUnreadable = errors.New(
	"the retry was accepted but its report could not be read, so which steps it reset " +
		"is unknown; refresh the run to see where it is",
)

// kasRetryOutcome is `_kiro/workflow/retry`'s reply in KAS's own spelling, the
// decode target only; the verb answers vibekit.RunRetriedResponse.
//
// RetriedNodeIDs is why Retry returns a value at all: a retry that reset five
// nodes and one that reset none are otherwise indistinguishable, which is what
// "I pressed Retry and nothing happened" looks like from outside.
type kasRetryOutcome struct {
	WorkflowID     string   `json:"workflowId"`
	Status         string   `json:"status"`
	RetriedNodeIDs []string `json:"retriedNodeIds"`
}

// Retry resets a finished run's failed work and reports what it reset.
//
// Legal only from `failed` or `aborted`, and `closeFinishedBridge` tears the
// bridge down on exactly those statuses, so retry is the one control verb that
// must reach a run nothing hosts. THE HOST IS RESOLVED, not assumed: keying on
// `run:<id>` alone sent every chat-parented run down the re-host branch, spawning
// a second engine for a run whose parent session is still alive. That branch
// LOADS FIRST, because KAS's retry refuses a run it has never seen.
func (rs *Runs) Retry(
	ctx context.Context, workflowID string, aff runAffordance,
) (vibekit.RunRetriedResponse, error) {
	if workflowID == "" {
		return vibekit.RunRetriedResponse{}, errors.New("missing workflow id")
	}
	cctx, cancel := context.WithTimeout(ctx, retryTimeout)
	defer cancel()

	// The run's real host: its own bridge, or the LAUNCHING CHAT's. Already
	// registered there, so no load is needed and no second engine is started.
	if _, sb := rs.hostBridgeFor(workflowID, aff.ParentChat); sb != nil {
		recipe := aff.Recipe
		out, err := rs.retryCall(cctx, sb.bridge, workflowID)
		if err != nil {
			// The verb LANDED and only its report is unusable, so the run may be running.
			if errors.Is(err, errRetryOutcomeUnreadable) {
				rs.rearmRetried(cctx, workflowID, recipe)
			}
			return vibekit.RunRetriedResponse{}, err
		}
		// Only on success: a refused retry re-drove nothing, so the run's previous
		// terminal reason is still the truth about it.
		rs.rearmRetried(cctx, workflowID, recipe)
		slog.Info("workflow run retried on its host",
			"workflow_id", workflowID, "recipe", recipe,
			"retried_nodes", len(out.RetriedNodeIDs), "status", out.Status)
		return out, nil
	}
	return rs.retryRehosted(cctx, workflowID, aff.Recipe)
}

// retryRehosted retries a run NOTHING in this process holds: start a bridge, load
// the run into it, then retry. recipe is the name the gate's inventory read
// carried, "" when it carried none — threaded because nothing here can learn it
// once the run is re-driving.
func (rs *Runs) retryRehosted(
	ctx context.Context, workflowID, recipe string,
) (vibekit.RunRetriedResponse, error) {
	chatID := runChatID(workflowID)
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
	// Register BEFORE the call: retry's first lifecycle frame follows it at once.
	rs.bridges.insert(chatID, &sharedBridge{bridge: bridge, state: bridgeIdle})
	go rs.coord.Forward(chatID, bridge)

	// The lease before the verb: retry's own `run_start` can arrive before the
	// call returns.
	minted := false
	if _, held := rs.lease(workflowID); !held {
		rs.grantLease(ctx, workflowID, recipe, manualLaunch())
		minted = true
	}

	out, err := rs.loadThenRetry(ctx, bridge, workflowID)
	if errors.Is(err, errRetryOutcomeUnreadable) {
		// KAS ACCEPTED the retry, so the run may be re-driving inside this bridge:
		// only the report is lost, and tearing down would kill the work mid-node.
		rs.rearmRetried(ctx, workflowID, recipe)
		return vibekit.RunRetriedResponse{}, err
	}
	if err != nil {
		// Nothing is executing: tear the bridge down, and give back a lease this
		// call minted for a run that never re-drove.
		if minted {
			rs.releaseLease(ctx, workflowID)
		}
		rs.coord.CloseBridge(chatID)
		return vibekit.RunRetriedResponse{}, err
	}
	// A fresh clock and a clean row, now that the retry has landed: the run's
	// recorded termination is no longer a fact about it.
	rs.rearmRetried(ctx, workflowID, recipe)
	slog.Info("workflow run re-hosted and retried",
		"workflow_id", workflowID, "recipe", recipe,
		"retried_nodes", len(out.RetriedNodeIDs), "status", out.Status)
	return out, nil
}

// loadThenRetry registers the run in a fresh process and retries it. The `load`
// is not optional: KAS's retry handler requires the run in its live registry and
// says so ("not registered. Load or create it first.").
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

// retryCall issues the verb and decodes its outcome report. Folded through
// runCallErr, so a JSON-RPC refusal arriving as a well-formed response with an
// `error` member is not read as a success.
func (rs *Runs) retryCall(
	ctx context.Context, bridge acpCaller, workflowID string,
) (vibekit.RunRetriedResponse, error) {
	none := vibekit.RunRetriedResponse{}
	resp, err := bridge.Call(ctx, methodKiroWorkflowRetry, map[string]any{keyWorkflowID: workflowID})
	if cErr := runCallErr(resp, err); cErr != nil {
		return none, fmt.Errorf("workflow retry: %w", rs.retryDeadlineErr(ctx, cErr))
	}
	// Past this point KAS has ACCEPTED the verb, so every failure below is a lost
	// REPORT rather than a retry that did not happen.
	if resp == nil || len(resp.Result) == 0 {
		return none, fmt.Errorf("%w: the reply carried no outcome", errRetryOutcomeUnreadable)
	}
	var out kasRetryOutcome
	if uErr := json.Unmarshal(resp.Result, &out); uErr != nil {
		return none, fmt.Errorf("%w: undecodable outcome: %w", errRetryOutcomeUnreadable, uErr)
	}
	// The reply must be ABOUT the run that was asked for: one run's outcome under
	// another's name would be reported to the reader as theirs.
	if out.WorkflowID != "" && out.WorkflowID != workflowID {
		return none, fmt.Errorf("%w: the reply names run %q, not %q",
			errRetryOutcomeUnreadable, out.WorkflowID, workflowID)
	}
	// Never nil on the wire, so a counting caller need not tell "none" from "absent".
	nodes := out.RetriedNodeIDs
	if nodes == nil {
		nodes = []string{}
	}
	return vibekit.RunRetriedResponse{Status: out.Status, RetriedNodeIDs: nodes}, nil
}

// retryStartErr labels a failed bridge start: this call's deadline, or the start
// failure itself.
func (rs *Runs) retryStartErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return errRetryEngineSlow
	}
	return fmt.Errorf("retry bridge start: %w", err)
}

// retryDeadlineErr rewrites a call that failed on THIS call's budget into
// errRetryEngineSlow, so the reader is told to try again.
func (rs *Runs) retryDeadlineErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return errRetryEngineSlow
	}
	return err
}

// SetStepStatus marks an in-flight step completed, failed, or running so a wedged
// run can advance.
//
// Resolved through hostBridge rather than the run's OWN bridge: an agent-launched
// run has none and never will, since KAS parents it on the calling chat's session.
// It does NOT re-host, because this verb acts on a step that is IN FLIGHT and a
// run with no live process has none.
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
		// The step is re-driven WITHOUT the user's words, so whatever it asked is no
		// longer answerable. SettledByUser because the reader chose it.
		rs.settleAskForNode(ctx, workflowID, nodeID, vibekit.SettledByUser)
	}
	return err
}

// The step statuses a human may set. Narrowed deliberately: KAS's `update` verb
// also carries `replace_remaining`, a plan editor.
//
// `running` is the CONTINUE-WITHOUT-ANSWERING verb: KAS clears the step node's
// `completionSignal` and re-invokes the run, so the step proceeds with its DEFAULT
// continuation prompt rather than anything the user typed. Resume cannot do it —
// it clears pauseReason and leaves the signal, so the step re-parks.
const (
	runStepCompleted = "completed"
	runStepFailed    = "failed"
	runStepRunning   = "running"
)

// runStepStatuses is the allowlist, in the order the refusal names them.
var runStepStatuses = []string{runStepCompleted, runStepFailed, runStepRunning}

// errAskAlreadySettled means the ask an answer names is no longer open. Distinct
// from a KAS refusal so the REST layer can answer 409 naming the situation.
var errAskAlreadySettled = errors.New(
	"that question has already been answered, or the step it belonged to has moved on",
)

// AnswerInput answers one parked step with the user's words.
//
// THE CLAIM COMES FIRST, and the order is the contract: two surfaces are offered
// the same question and KAS accepts exactly one answer, so the loser's
// `session/prompt` would fall through to an ORDINARY prompt on the step's own
// session, injecting a message into a step nobody asked to steer. A FAILED prompt
// puts the claim back. The verb itself is a plain `session/prompt` to the PAUSED
// STEP's session, not a workflow verb.
func (rs *Runs) AnswerInput(ctx context.Context, workflowID, askID, text string) error {
	if workflowID == "" || askID == "" {
		return errors.New("missing workflow id or ask id")
	}
	if strings.TrimSpace(text) == "" {
		return errors.New("an answer cannot be empty")
	}
	// BEFORE the claim: no instant may exist where the registry holds nothing for
	// the run AND no answer is in flight, or the reconcile mints a text-less twin.
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
	// The run is executing again, so a FRESH budget: each arm bounds EXECUTING time.
	rs.armDeadline(ctx, workflowID)
	rs.announceSettled(ctx, a, vibekit.SettledByUser)
	slog.Info("answered a parked workflow step", "workflow_id", workflowID,
		"node_id", a.payload.NodeID, "ask_id", askID)
	return nil
}

// pausedStepSession resolves the answer address from a fresh `inspect`, for an ask
// that carries none. Only a RECONCILED ask reaches this; a live notify frame
// carries its own `callerSessionId`. Best effort — "" when the run cannot be read.
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

// hostedControl issues a verb that must run on the run's OWN bridge, and refuses
// rather than falling back to the utility bridge.
//
// The fallback would be actively harmful: resume and retry make the run EXECUTE,
// and the utility bridge is a text-only session that denies every permission
// request and errors every fs/terminal call, so a run resumed there would grind
// through its steps with no tools. The cost is that a run orphaned by a restart
// can only be cancelled — retry re-hosts instead.
func (rs *Runs) hostedControl(ctx context.Context, workflowID, method string) error {
	sb := rs.hostBridge(ctx, workflowID)
	if sb == nil {
		return errRunNotHosted
	}
	resp, err := sb.bridge.Call(ctx, method, map[string]any{keyWorkflowID: workflowID})
	return runCallErr(resp, err)
}

// hostBridge resolves the bridge whose process holds the run's registry entry.
//
// A run vibekit LAUNCHED has a bridge under its synthetic `run:<id>` chat id. An
// AGENT-launched run has none: KAS parents it on the calling chat's session, so
// the LAUNCHING CHAT's bridge is the process that registered it. Costs one
// `workflow/list` round trip on that path only.
func (rs *Runs) hostBridge(ctx context.Context, workflowID string) *sharedBridge {
	_, sb := rs.hostBridgeChat(ctx, workflowID)
	return sb
}

// hostBridgeChat is hostBridge plus the CHAT ID that bridge belongs to, and ""
// when nothing in this process hosts the run.
//
// The id is what a synthesised ask is keyed to (askChatID), so a reconstructed
// question lands in the dock of the conversation the run was launched from. One
// function rather than two, because deriving the id separately would repeat the
// `workflow/list` round trip the bridge lookup already paid for.
func (rs *Runs) hostBridgeChat(
	ctx context.Context, workflowID string,
) (vibekit.ChatID, *sharedBridge) {
	if sb := rs.bridges.get(runChatID(workflowID)); sb != nil {
		return runChatID(workflowID), sb
	}
	// One resolution for both questions run_affordance.go asks about a run's parent:
	// which chat owns it, and whether that chat is a live carrier.
	chatID, _ := rs.chatForSession(ctx, rs.listedRun(ctx, workflowID).ParentSessionID)
	return rs.hostBridgeFor(workflowID, chatID)
}

// hostBridgeFor is hostBridgeChat for a caller that ALREADY KNOWS the run's parent
// chat: no RPC, no chat-store read, and only a LIVE bridge answers.
//
// The resolution above costs a `workflow/list` round trip plus a chat-directory
// scan, and two reads can DISAGREE, leaving the verb acting on a different answer
// than the gate approved.
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

// listedRun finds one run in KAS's own inventory, the only place this process can
// read a run it did not launch: `inspect` carries neither the parent session nor
// the recipe.
//
// The ZERO VALUE is the answer for a missing run, an empty field and an unreadable
// inventory alike, because none of the three may fail a control read or a retry.
func (rs *Runs) listedRun(ctx context.Context, workflowID string) kasWorkflowRun {
	if workflowID == "" {
		return kasWorkflowRun{}
	}
	runs, err := rs.listRaw(ctx)
	if err != nil {
		slog.Warn("could not read the run inventory, so a run's parent chat and recipe are unknown",
			"workflow_id", workflowID, "error", err)
		return kasWorkflowRun{}
	}
	for i := range runs {
		if runs[i].WorkflowID == workflowID {
			return runs[i]
		}
	}
	return kasWorkflowRun{}
}

// control issues a verb that is safe on either connection, preferring the run's
// OWN bridge and falling back to the utility bridge. Only cancel qualifies: it
// rehydrates from disk and only WRITES state, so a text-only session carries it.
func (rs *Runs) control(ctx context.Context, workflowID, method, logLabel string) error {
	params := map[string]any{keyWorkflowID: workflowID}
	if sb := rs.bridges.get(runChatID(workflowID)); sb != nil {
		resp, err := sb.bridge.Call(ctx, method, params)
		return runCallErr(resp, err)
	}
	_, err := rs.utility().session.rawCall(ctx, logLabel, method, callerParams(params))
	return err
}

// dispatch is translateACPEvent's branch for frames arriving on a RUN bridge.
// Host requests and the three ASK kinds go through the SAME handlers a chat's do,
// keyed by the synthetic chat id; workflow LIFECYCLE frames go through with an
// EMPTY chat id, because a parentless run is owned by no chat; and session/update
// is PROJECTED as run-scoped `run_step` events, never into a transcript.
func (rt *Runtime) dispatch(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	if msg.ID != nil {
		rt.dispatchRequest(ctx, chatID, msg)
		return
	}
	// BEFORE the workflow prefix test, because this method is not under it. It keeps
	// the bridge's OWN chat id: an ask is answerable, so it has to land on a surface.
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

// dispatchRequest answers an A→C request on a run bridge. An unmatched request is
// REFUSED rather than dropped, because an unanswered request wedges the step's turn.
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

// closeFinishedBridge closes a run bridge once its run reached a TERMINAL state.
// One rule covers completion, failure and cancel — all end in run_complete — and a
// non-terminal run_complete (a policy pause reports through the same frame) keeps
// the bridge.
//
// The close runs in a goroutine because this is called FROM the bridge's own
// forward loop, and CloseBridge → Stop closes the channel that loop ranges over.
func (rt *Runtime) closeFinishedBridge(chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	var p struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(msg.Params, &p) != nil || !terminalRunStatus(p.Status) {
		return
	}
	slog.Info("run finished, closing its bridge", "chat_id", chatID, "status", p.Status)
	// The run's LEASE is not released here: forgetBounds owns that, on the same
	// terminal frame, because it is the one site every origin reaches.
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
// KAS's list stays the source of truth here: it is the only thing that sees the
// runs vibekit did not launch, so keying the rule on vibekit's own leases would
// let a second live run of one recipe start. What the leases add is the ability to
// EXPLAIN a blocking row — ask whether it is an orphan vibekit left behind.
func (rs *Runs) recipeIdle(ctx context.Context, name string) error {
	runs, err := rs.list(ctx, nil)
	if err != nil {
		// The guard needs the list; launching blind would let a second live run exist.
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

// kasRecipe is one listRecipes entry as KAS reports it; `plan` rides through as
// raw JSON — see vibekit.Recipe.
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
	// Always a map, never nil: KAS answers "inputs is not iterable" without it.
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

// --- Restart recovery ---

// stalePauseReason is KAS's STALE_RUNNING_PAUSE_REASON, the pauseReason its
// read-path reconcile stamps on a run whose owning process died. Matched as a
// LITERAL: five sites set pauseReason and only this one means the process died.
const stalePauseReason = "Interrupted by agent restart; the previously running step was paused for resume."

// The pause reasons KAS records when a run stopped for a cause NOBODY CHOSE,
// beside the reconcile literal above.
//
// THESE ARE THE RESUME SIDE ONLY: stalePauseReason licenses the orphan sweep to
// CANCEL a run, this wider set only ever licenses a RESUME. A network pause is
// matched by PREFIX because KAS interpolates the error code into it; the prefix
// carries the whole distinguishing phrase, so it cannot reach a reason that must
// be left alone (a step waiting on a human, a policy stop, a recorded failure).
const (
	interruptedPauseReason  = "Step interrupted (agent shutdown or connection reset); will resume."
	modelServicePauseReason = "Transient model service error (service 5xx/throttling); will resume."
	networkPausePrefix      = "Transient connection error ("
)

// resumablePause reports whether a pause reason means the run stopped for a cause
// nobody chose, and is therefore vibekit's to resume unasked. Reason-only, so the
// status and identity conditions live with the caller that reads one reply.
func resumablePause(reason string) bool {
	switch reason {
	case stalePauseReason, interruptedPauseReason, modelServicePauseReason:
		return true
	}
	return strings.HasPrefix(reason, networkPausePrefix)
}

// resumeInterruptedRuns resumes the runs a chat's rehydrated bridge should pick
// back up: the ones ITS sessions launched, that stopped for a cause nobody chose.
//
// This is why there is no Resume button anywhere: a chat's bridge dying pauses its
// runs, and the user's next message respawns the bridge. Scoped twice — to THIS
// chat's session chain (never resumeAll, which would sweep runs another chat
// paused on purpose), and to the involuntary pause reasons.
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

// maxAutoHeals bounds the automatic resumes one run may spend between two pieces
// of progress. Three, because a fourth attempt against a dead network says nothing.
const maxAutoHeals = 3

// healBaseDelay is the wait before the FIRST automatic resume, doubling per
// attempt (5s, 10s, 20s). Not zero, so a deliberate cancel can land first. A `var`
// only so a test can drive the whole path in milliseconds.
var healBaseDelay = 5 * time.Second

// healPaused resumes a run KAS has just parked for a reason nobody chose.
//
// resumeInterruptedRuns only fires when a chat's bridge comes BACK, so a run
// pausing on a transient network error while its bridge stays alive has no other
// trigger. KAS emits `_kiro/workflow/paused` on the LAUNCHING CHAT's bridge the
// moment it parks a run, so the timing is exact with no polling. Runs AFTER
// `next`, so the client renders the pause before anything undoes it.
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
		// The timer handle is deliberately NOT tracked, unlike the deadline timers in
		// `bounds.timers`: a fired one there CANCELS a run, while this re-reads state.
		time.AfterFunc(delay, func() {
			hctx, cancel := rs.lifecycle.derivedContext()
			defer cancel()
			rs.resumeIfInterrupted(hctx, chatID, f.WorkflowID)
		})
	}
}

// pauseFrame is the two fields the heal reads off `_kiro/workflow/paused`. Its own
// minimal decode rather than a share of `lifecycleFrame`, which carries no pause
// reason at all.
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

// healProgress gives a run its heal budget back when a node completes, and
// retires the ask that node was holding.
//
// Progress is the only honest evidence that whatever paused the run has cleared;
// without this a long-running job would spend its three attempts on one blip. The
// ask half covers the case nothing else does: a step answered from somewhere
// vibekit is not. Node-SCOPED, because a parallel branch's node can complete while
// a sibling branch's step is still waiting.
func (rs *Runs) healProgress(
	next func(context.Context, vibekit.ChatID, *vibekit.RPCResponse),
) func(context.Context, vibekit.ChatID, *vibekit.RPCResponse) {
	return func(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
		if f := decodeNodeFrame(msg); f.WorkflowID != "" {
			rs.clearHeals(f.WorkflowID)
			// SettledByMoot rather than SettledByUser: the frame says only that the node
			// moved on, and the answer path already settled anything vibekit accepted.
			rs.settleAskForNode(ctx, f.WorkflowID, f.NodeID, vibekit.SettledByMoot)
		}
		next(ctx, chatID, msg)
	}
}

// nodeFrame is the two fields the node-scoped ask clear reads off
// `_kiro/workflow/node_complete`. Its own minimal decode, like pauseFrame beside
// it: lifecycleFrame carries no node id at all.
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

// resumeIfInterrupted inspects one paused run and resumes it when the pause was
// involuntary. Resumed on the CHAT's bridge, so that process owns the run again.
func (rs *Runs) resumeIfInterrupted(ctx context.Context, chatID vibekit.ChatID, workflowID string) {
	// The orphan sweep's predicate is the NARROWER `restartPaused`: it cancels, and
	// only "the owning process died" licenses that. This one resumes.
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

// listRaw lists runs with their raw parent session ids, for callers that scope by
// session chain rather than by resolved chat.
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

// CancelForChat cancels every non-terminal run this chat's sessions launched. A
// run is durable state, so killing the chat's process only PAUSES it — it must be
// told to cancel, per run, while the owning bridge is still alive to say it.
// BEGINS with a record read, so it no-ops on a deleted chat.
func (rs *Runs) CancelForChat(ctx context.Context, chatID vibekit.ChatID) {
	chat, ok := rs.chats.Get(ctx, chatID)
	if !ok {
		return
	}
	rs.CancelForSessions(ctx, chatID, chat.SessionChain())
}

// CancelForSessions cancels every non-terminal run launched by one of these
// sessions. The chain-shaped half of CancelForChat: it reads no chat record, so it
// works from a CAPTURED chain after the record is gone. Best-effort throughout —
// blocking a tab close on a workflow RPC would invert the gesture's meaning.
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
