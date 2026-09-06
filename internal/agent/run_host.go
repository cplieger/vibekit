package agent

// The run host: launching a PARENTLESS workflow run and the process that holds it.
// An agent-launched run is parented on its chat's session by KAS and needs nothing
// here; a manual or scheduled one gets ONE bridge of its own under the synthetic chat
// id `run:<workflowId>`, which every response path already resolves by chat id. That
// id gets no chat file. Which bridge holds what, and why the lease outlives the
// process: vibekit-runtime.md. The KAS-side shapes: vibekit-acp.md.

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/vibekit/internal/runlease"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// Workflow RPC param keys, shared across the five verbs.
const (
	keyWorkflowID     = "workflowId"
	keyWorkspacePaths = "workspacePaths"
)

// runChatPrefix namespaces the synthetic chat ids run bridges register under. Real
// chat ids are client-generated `c-*` uuids, so the namespace cannot collide.
const runChatPrefix = "run:"

// runChatID is the bridge-manager key for a run's bridge.
func runChatID(workflowID string) vibekit.ChatID {
	return vibekit.ChatID(runChatPrefix + workflowID)
}

// isRunChat reports whether a chat id names a run bridge.
func isRunChat(chatID vibekit.ChatID) bool {
	return strings.HasPrefix(string(chatID), runChatPrefix)
}

// workflowIDOf recovers the run a bridge hosts from its synthetic chat id, or "" for
// anything that is not one.
func workflowIDOf(chatID vibekit.ChatID) string {
	if !isRunChat(chatID) {
		return ""
	}
	return strings.TrimPrefix(string(chatID), runChatPrefix)
}

// launchTimeout bounds the launch handshake. Generous because a first launch may
// unpack a ~240 MB KAS runtime tree before the process answers.
const launchTimeout = 120 * time.Second

// errRecipeBusy reports the single-run rule: one live run per recipe definition,
// globally. Concurrency is declared INSIDE a workflow.
var errRecipeBusy = errors.New("this recipe already has a live run")

// Launch starts one parentless ATTENDED run of the recipe with the given source key
// and returns its workflow id and name.
func (rs *Runs) Launch(ctx context.Context, source string, inputs map[string]string) (id, name string, err error) {
	return rs.launch(ctx, source, inputs, manualLaunch())
}

// LaunchScheduled launches a run on behalf of a schedule, marking it UNATTENDED for
// the duration. slotAt is when this run's own next slot comes due; zero means the idle
// window and backstop alone bound it.
func (rs *Runs) LaunchScheduled(ctx context.Context, source, scheduleID string, slotAt time.Time) (id, name string, err error) {
	return rs.launch(ctx, source, nil, scheduledLaunch(scheduleID, slotAt))
}

// launch is the shared body of both public launch verbs.
func (rs *Runs) launch(ctx context.Context, source string, inputs map[string]string, o launchOrigin) (id, name string, err error) {
	cctx, cancel := context.WithTimeout(ctx, launchTimeout)
	defer cancel()

	recipe, err := rs.recipeBySource(cctx, source)
	if err != nil {
		return "", "", err
	}
	if o.origin == runlease.OriginManual {
		// A manual run yields to its recipe's next slot, known only once resolved.
		o.slotAt = rs.manualSlot(recipe.Source)
	}
	if bErr := rs.recipeIdle(cctx, recipe.Name); bErr != nil {
		return "", "", bErr
	}

	// Started OUTSIDE the manager: the map key is the workflow id, which only `new`'s
	// reply knows. Call replies ride the readLoop, so Forward can attach afterwards.
	bridge := rs.bridges.factory()
	if sErr := bridge.Start(cctx, &vibekit.StartOpts{
		Lifetime: rs.lifecycle.shutdownCtx,
		// Named rather than inherited from buildACPArgs's default, so a change there
		// cannot launch a run bridge on the legacy engine. Same argv today.
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

	// Register BEFORE invoke: the first lifecycle frame follows it immediately, and
	// a frame arriving before the map entry has no bridge to answer through.
	rs.bridges.insert(runChatID(wfID), &sharedBridge{bridge: bridge, state: bridgeIdle})
	// The run's envelope, before anything can execute — see runlease.Lease.
	rs.grantLease(cctx, wfID, recipe.Name, o)
	go rs.coord.Forward(runChatID(wfID), bridge)

	if _, err := bridge.Call(cctx, methodKiroWorkflowInvoke, map[string]any{keyWorkflowID: wfID}); err != nil {
		// The run was created but never started, so nothing is executing.
		rs.releaseLease(cctx, wfID)
		rs.coord.CloseBridge(runChatID(wfID))
		return "", "", fmt.Errorf("workflow invoke: %w", err)
	}
	// After invoke, so a run that never started leaves no timer; idempotent with
	// the `run_start` frame's own arm.
	rs.armDeadline(cctx, wfID)
	slog.Info("workflow run launched", "workflow_id", wfID, "recipe", recipe.Name)
	return wfID, recipe.Name, nil
}

// Cancel asks a run to stop, on the USER's behalf. A LOST termination claim returns
// nil — something else got there first — and a refused RPC is handed back, with
// retryTermination re-attempting it on its own budget.
func (rs *Runs) Cancel(ctx context.Context, workflowID string) error {
	return rs.cancelOn(ctx, workflowID, nil)
}

// cancelOn is Cancel for a caller that has ALREADY resolved the run's carrier, so
// stopping N runs costs one inventory read instead of N+1 (CancelForSessions). A nil
// carrier means resolve it here, which is every other caller.
func (rs *Runs) cancelOn(ctx context.Context, workflowID string, carrier *sharedBridge) error {
	if workflowID == "" {
		return errors.New("missing workflow id")
	}
	if !rs.claimTermination(workflowID) {
		return nil
	}
	return rs.finishTermination(ctx, workflowID, "", carrier)
}

// cancelRPC issues the cancel VERB and nothing else — no claim, no record. The bridge
// is NOT closed here: the owning process must live to the node boundary to certify the
// cancelled state, so the terminal `run_complete` closes it.
func (rs *Runs) cancelRPC(ctx context.Context, workflowID string, carrier *sharedBridge) error {
	return rs.control(ctx, workflowID, methodKiroWorkflowCancel, "workflow cancel call", carrier)
}

// Delete removes a run and everything either side keeps about it, on the USER's
// behalf. The only run verb that is not recoverable; a failed verb leaves everything
// in place, because the run still exists in KAS.
//
// Deliberately unlike cancel: no termination claim (KAS's delete cancels a
// non-terminal run itself) and the bridge IS closed.
func (rs *Runs) Delete(ctx context.Context, workflowID string) error {
	if workflowID == "" {
		return errors.New("missing workflow id")
	}
	if err := rs.control(ctx, workflowID, methodKiroWorkflowDelete, "workflow delete call", nil); err != nil {
		return err
	}
	rs.coord.CloseBridge(runChatID(workflowID))
	rs.forgetBounds(ctx, workflowID)
	rs.clearEnd(workflowID)
	slog.Info("workflow run deleted", "workflow_id", workflowID)
	return nil
}

// Pause asks a running run to stop at its next node boundary, keeping its state
// resumable. The in-flight node runs to completion, so the reply confirms the ASK
// rather than a paused state; a re-hosted pause is expected to be REFUSED, because
// KAS throws for a run its own process forgot.
func (rs *Runs) Pause(ctx context.Context, workflowID string) error {
	return rs.hostedControl(ctx, workflowID, methodKiroWorkflowPause)
}

// Resume re-drives a paused run, re-arming the wall clock the pause parked: each
// arm bounds EXECUTING time, so a resumed run gets a FRESH budget.
func (rs *Runs) Resume(ctx context.Context, workflowID string) error {
	err := rs.hostedControl(ctx, workflowID, methodKiroWorkflowResume)
	if err == nil {
		rs.armDeadline(ctx, workflowID)
	}
	return err
}

// Retry re-hosts a finished run and resets its failed work. Only reachable for a
// PARENTLESS run; an agent-parented run's recovery is the agent's own.
func (rs *Runs) Retry(ctx context.Context, workflowID string) error {
	if workflowID == "" {
		return errors.New("missing workflow id")
	}
	chatID := runChatID(workflowID)

	// Not the expected path — retry's window implies a closed bridge — but a run
	// aborted without a terminal frame can still be registered.
	if sb := rs.bridges.get(chatID); sb != nil {
		rs.carriers.enter(sb)
		defer rs.carriers.leave(sb)
		recipe := rs.recipeOf(ctx, workflowID)
		_, err := sb.Call(ctx, methodKiroWorkflowRetry, map[string]any{keyWorkflowID: workflowID})
		if err == nil {
			// Only on success: a retry KAS refused re-drove nothing.
			rs.rearmRetried(ctx, workflowID, recipe)
		}
		return err
	}

	cctx, cancel := context.WithTimeout(ctx, launchTimeout)
	defer cancel()

	// BEFORE anything is re-driven: see recipeOf.
	recipe := rs.recipeOf(cctx, workflowID)

	sb, discard, err := rs.rehost(cctx, workflowID)
	if err != nil {
		return err
	}
	rs.carriers.enter(sb)
	defer rs.carriers.leave(sb)

	// Before the verb, as a launch grants between `new` and `invoke`: retry's own
	// `run_start` can arrive before the call returns.
	minted := false
	if _, held := rs.lease(workflowID); !held {
		rs.grantLease(cctx, workflowID, recipe, manualLaunch())
		minted = true
	}

	if _, err := sb.bridge.Call(cctx, methodKiroWorkflowRetry, map[string]any{keyWorkflowID: workflowID}); err != nil {
		// A CONTEXT error keeps BOTH the carrier and the lease, under the one
		// unknown-outcome rule: armDeadline returns when there is no lease, so a
		// retry KAS did take would execute with no deadline and nothing to arm one.
		if minted && !isCtxErr(err) {
			rs.releaseLease(cctx, workflowID)
		}
		discard(err)
		return fmt.Errorf("workflow retry: %w", err)
	}
	// Only now the retry has landed: the client lets a recognised end_reason outrank
	// live status.
	rs.rearmRetried(cctx, workflowID, recipe)
	slog.Info("workflow run retried", "workflow_id", workflowID, "recipe", recipe)
	return nil
}

// recipeOf reads a run's recipe NAME off KAS's own run list. Best-effort: "" when the
// list cannot be read, because a retry must not fail over a name.
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

// errStepStatusRefused means KAS resolved the run and DECLINED the update instead of
// throwing: the run is terminal, or holds no running-or-paused step to mark. A state
// of the world rather than a fault, so the REST layer answers 409 with KAS's sentence.
var errStepStatusRefused = errors.New("this run's step status was not changed")

// updateStatusAction is `_kiro/workflow/update`'s action id for a step-status write.
// Sent EXPLICITLY although any non-`replace_remaining` value works: the tool schema
// declares `action` required, so an absent value is accepted only by accident.
const updateStatusAction = "update_status"

// errStepStatusMistargeted means the node the caller named is not the node KAS's
// resolver would mark, so the write was WITHHELD. Wraps errStepStatusRefused, so the
// REST layer's 409 arm covers it too.
var errStepStatusMistargeted = fmt.Errorf(
	"%w: this run is not waiting at the step you asked to mark", errStepStatusRefused,
)

// errStepStatusUnreadable means the pre-send read failed, so whether the caller's node
// is the one KAS would mark is UNKNOWN. Withheld rather than sent: a `completed` on the
// wrong node publishes its capture and stamps it finished, which nothing undoes.
var errStepStatusUnreadable = fmt.Errorf(
	"%w: this run's state could not be read just now, so try again in a moment",
	errStepStatusRefused,
)

// SetStepStatus marks a step completed, failed, or running so a wedged run can
// advance. The verb carries NO node id, so KAS resolves its target positionally and a
// client naming node X can have KAS mark node Y — the tree is READ first and the write
// withheld unless the two agree. Schema and resolver: vibekit-acp.md.
func (rs *Runs) SetStepStatus(ctx context.Context, workflowID, nodeID, status string) error {
	if nodeID == "" {
		return errors.New("missing node id")
	}
	if !slices.Contains(runStepStatuses, status) {
		return fmt.Errorf("step status must be one of %v", runStepStatuses)
	}
	// BEFORE hostOrRehost, unlike AnswerInput's read: `inspect` runs on the utility
	// session, so a withheld write spends no process start. Cost: the target can go
	// stale across the spawn, bounded by an unhosted run advancing nothing itself.
	if err := rs.stepStatusAddress(ctx, workflowID, nodeID); err != nil {
		return err
	}
	sb, discard, err := rs.hostOrRehost(ctx, workflowID)
	if err != nil {
		return err
	}
	rs.carriers.enter(sb)
	defer rs.carriers.leave(sb)
	resp, cErr := sb.bridge.Call(ctx, methodKiroWorkflowUpdate, map[string]any{
		keyWorkflowID: workflowID,
		"action":      updateStatusAction,
		"status":      status,
	})
	if callErr := runCallErr(resp, cErr); callErr != nil {
		discard(callErr)
		return callErr
	}
	// The REPLY is read, because a decline is not a throw: both declines answer
	// {updated:false} with a 200, which a caller ignoring it reports as landed.
	if refusal := stepStatusRefusal(resp); refusal != "" {
		// The carrier goes on this path too: a decline means nothing was written, so
		// no run_complete follows and a re-host's process would outlive the verb.
		discard(errStepStatusRefused)
		return fmt.Errorf("%w: %s", errStepStatusRefused, refusal)
	}
	if status == runStepRunning {
		// Re-driven WITHOUT the user's words, so whatever it asked is no longer
		// answerable. SettledByUser because this IS the reader's decision.
		rs.settleAskForNode(ctx, workflowID, nodeID, vibekit.SettledByUser)
	}
	return nil
}

// stepStatusRefusal returns KAS's reason when it declined the update, "" when it took
// it. `Updated` is a *bool so ABSENT reads as taken: an unstated field must not make a
// verb that worked report a refusal. A queued update was taken, so `queued` is unread.
func stepStatusRefusal(resp *vibekit.RPCResponse) string {
	if resp == nil || len(resp.Result) == 0 {
		return ""
	}
	var reply struct {
		Updated *bool  `json:"updated"`
		Message string `json:"message"`
	}
	if json.Unmarshal(resp.Result, &reply) != nil {
		return ""
	}
	if reply.Updated == nil || *reply.Updated {
		return ""
	}
	if reply.Message == "" {
		return "KAS gave no reason"
	}
	return reply.Message
}

// stepStatusAddress reports whether the node a caller named is the node KAS would
// mark, and REFUSES rather than falling back when it cannot tell: here a wrong answer
// costs a positional WRITE that publishes a capture and stamps endedAt, which nothing
// undoes.
func (rs *Runs) stepStatusAddress(ctx context.Context, workflowID, nodeID string) error {
	raw, err := rs.rawInspect(ctx, workflowID)
	if err != nil {
		slog.Warn("could not read a run's state, so its step status was left alone",
			"workflow_id", workflowID, "error", err)
		return errStepStatusUnreadable
	}
	var res askInspect
	if json.Unmarshal(raw, &res) != nil || res.State == nil {
		slog.Warn("a run's state did not decode, so its step status was left alone",
			"workflow_id", workflowID)
		return errStepStatusUnreadable
	}
	target := statusUpdateTarget(res.State.Root)
	if target == nil {
		slog.Info("a run holds no running or paused step, so nothing was marked",
			"workflow_id", workflowID, "node_id", scrubLog(nodeID),
			"status", scrubLog(res.State.Status))
		return errStepStatusMistargeted
	}
	if target.NodeID != nodeID {
		slog.Info("a run's current step is not the one being marked, "+
			"so the status write was withheld", "workflow_id", workflowID,
			"asked_node_id", scrubLog(nodeID), "target_node_id", scrubLog(target.NodeID))
		return errStepStatusMistargeted
	}
	return nil
}

// stepNodeType is the state tree's `type` for a STEP node — the only kind KAS's
// resolver considers, which keeps a paused `parallel` container out of the answer.
const stepNodeType = "step"

// statusUpdateTarget resolves the node `_kiro/workflow/update` would mark, mirroring
// KAS's own resolver: the first RUNNING step node in pre-order, else the first PAUSED
// one. Not pausedLeaf, which answers where the run is WAITING, over leaves.
func statusUpdateTarget(root *askNode) *askNode {
	running, paused := stepTargets(root)
	if running != nil {
		return running
	}
	return paused
}

// stepTargets is statusUpdateTarget's ONE pre-order pass, returning the first RUNNING
// step node and the first PAUSED one. Both in one traversal because the resolver's own
// arms share one, and each node is judged BEFORE its children so "first" means the same
// thing here as there; a running step returns immediately and never descends.
func stepTargets(n *askNode) (running, paused *askNode) {
	if n == nil {
		return nil, nil
	}
	if n.Type == stepNodeType {
		if n.Status == runStatusRunning {
			return n, nil
		}
		if n.Status == runStatusPaused {
			paused = n
		}
	}
	for i := range n.Children {
		hitRunning, hitPaused := stepTargets(&n.Children[i])
		if hitRunning != nil {
			return hitRunning, nil
		}
		if paused == nil {
			paused = hitPaused
		}
	}
	return nil, paused
}

// The step statuses a human may set. `running` is the CONTINUE-WITHOUT-ANSWERING
// verb rather than a mark, and plain Resume cannot substitute for it — the KAS-side
// mechanics and what `update` also carries are in vibekit-acp.md.
const (
	runStepCompleted = "completed"
	runStepFailed    = "failed"
	runStepRunning   = "running"
)

// runStepStatuses is the allowlist, in the order the refusal names them.
var runStepStatuses = []string{runStepCompleted, runStepFailed, runStepRunning}

// errAskAlreadySettled means the ask an answer names is no longer open. Distinct
// from a KAS refusal so the REST layer answers 409 rather than 500.
var errAskAlreadySettled = errors.New(
	"that question has already been answered, or the step it belonged to has moved on",
)

// AnswerInput answers one parked step with the user's words, as a plain
// `session/prompt` addressed to the PAUSED STEP's own session.
//
// THE ORDER IS THE CONTRACT: carrier, then claim, then address, then send. Each
// step guards a window the next one would open — see vibekit-runtime.md's
// liveness-split block. A failed send puts the claim back.
func (rs *Runs) AnswerInput(ctx context.Context, workflowID, askID, text string) error {
	if workflowID == "" || askID == "" {
		return errors.New("missing workflow id or ask id")
	}
	if strings.TrimSpace(text) == "" {
		return errors.New("an answer cannot be empty")
	}
	// BEFORE the claim, so no instant has the registry empty AND nothing reporting
	// an answer in flight — see pendingRunAsks.beginAnswer.
	rs.asks.beginAnswer(workflowID)
	defer rs.asks.endAnswer(workflowID)
	sb, discard, err := rs.hostOrRehost(ctx, workflowID)
	if err != nil {
		return err
	}
	// From HERE, not from the Call: the address read below is a round trip, and a
	// bound coming due inside it would close the carrier this answer is about to use.
	rs.carriers.enter(sb)
	defer rs.carriers.leave(sb)
	a, ok := rs.asks.TakeIfPresent(workflowID, askID)
	if !ok {
		discard(errAskAlreadySettled)
		return errAskAlreadySettled
	}
	session, verdict := rs.answerAddress(ctx, workflowID, a)
	if verdict == answerMoot {
		// Nobody has to answer this any more, so it is MOOT rather than restored:
		// re-offering a card for a step that has moved on asks the reader to
		// answer a question KAS has stopped waiting on.
		rs.announceSettled(ctx, a, vibekit.SettledByMoot)
		discard(errAskAlreadySettled)
		return errAskAlreadySettled
	}
	if verdict == answerBusy {
		// EARLY, not stale: the run is between steps, so the words are still
		// wanted. The card goes back and the reader is told to retry — settling
		// here would discard what they typed and tell them the step had moved on.
		rs.restoreAsk(ctx, a)
		discard(errRunNotParked)
		return errRunNotParked
	}
	if session == "" {
		unaddressable := errors.New("the step that asked cannot be addressed on this server")
		rs.restoreAsk(ctx, a)
		discard(unaddressable)
		return unaddressable
	}
	resp, cErr := sb.bridge.Call(ctx, vibekit.MethodPrompt, map[string]any{
		vibekit.KeySessionID: session,
		vibekit.KeyPrompt:    []any{vibekit.TextBlock(text)},
	})
	if callErr := runCallErr(resp, cErr); callErr != nil {
		rs.restoreAsk(ctx, a)
		discard(callErr)
		return callErr
	}
	// A FRESH budget, the rule Resume follows: each arm bounds EXECUTING time.
	rs.armDeadline(ctx, workflowID)
	rs.announceSettled(ctx, a, vibekit.SettledByUser)
	slog.Info("answered a parked workflow step", "workflow_id", workflowID,
		"node_id", a.payload.NodeID, "ask_id", askID)
	return nil
}

// errRunNotParked means the run is executing rather than waiting, so there is no
// parked step for KAS to reroute the answer into YET. Distinct from
// errAskAlreadySettled because it is retryable and the card survives it.
var errRunNotParked = errors.New(
	"this run is not waiting on an answer right now, so try again in a moment",
)

// answerVerdict is what a fresh read of the run says about the ask being answered.
type answerVerdict int

const (
	// answerSend: the ask's own step is parked, so KAS reroutes the prompt into it.
	answerSend answerVerdict = iota
	// answerMoot: nothing is waiting on this question any more.
	answerMoot
	// answerBusy: the run is BETWEEN steps, so the answer is early rather than stale.
	answerBusy
)

// answerAddress resolves where one ask's answer must be sent, and grades what the
// run's state says about the question.
//
// THE FRESH READ LEADS and the ask's own address is only the fallback, because a
// prompt KAS does not reroute runs as an ordinary turn on that session —
// vibekit-acp.md "A step's answer is a plain `session/prompt`". An UNREADABLE run
// falls back rather than refusing: a failed read never destroys work here. The three
// verdicts: vibekit-runtime.md's liveness-split block.
func (rs *Runs) answerAddress(
	ctx context.Context, workflowID string, a *runAsk,
) (session string, verdict answerVerdict) {
	raw, err := rs.rawInspect(ctx, workflowID)
	if err != nil {
		slog.Warn("could not read a parked run's state, so its ask answers to the address it carries",
			"workflow_id", workflowID, "error", err)
		return a.payload.StepSessionID, answerSend
	}
	var res askInspect
	if json.Unmarshal(raw, &res) != nil || res.State == nil {
		return a.payload.StepSessionID, answerSend
	}
	if step := askedStep(res.State.Root, a.payload.NodeID); step != nil {
		return cmp.Or(step.SessionID, a.payload.StepSessionID), answerSend
	}
	if parked, _ := pausedLeaf(res.State.Root, nil); parked != nil {
		slog.Info("a run is parked at a different step than the one being answered, "+
			"so the answer was withheld", "workflow_id", workflowID,
			"asked_node_id", scrubLog(a.payload.NodeID), "parked_node_id", scrubLog(parked.NodeID))
		return "", answerMoot
	}
	if terminalRunStatus(res.State.Status) {
		return "", answerMoot
	}
	slog.Info("a run is not parked on any step right now, so its answer was held back "+
		"rather than discarded", "workflow_id", workflowID,
		"asked_node_id", scrubLog(a.payload.NodeID), "status", scrubLog(res.State.Status))
	return "", answerBusy
}

// askedStep finds the PARKED step one ask belongs to, or nil when that step is not
// parked right now. Addressed by the ask's own NODE ID rather than by pausedLeaf's
// first depth-first match, which is what makes a parallel run's second parked branch
// answerable — the divergence is in vibekit-runtime.md's liveness-split block.
//
// An EMPTY id matches any parked step: such an ask was minted before that field
// existed, so it keeps pausedLeaf's older behaviour rather than being refused. A
// repeat's iterations SHARE an id, so any parked instance of it answers yes.
func askedStep(n *askNode, nodeID string) *askNode {
	if n == nil {
		return nil
	}
	if len(n.Children) == 0 {
		if n.Status == runStatusPaused && (nodeID == "" || n.NodeID == nodeID) {
			return n
		}
		return nil
	}
	for i := range n.Children {
		if hit := askedStep(&n.Children[i], nodeID); hit != nil {
			return hit
		}
	}
	return nil
}

// hostedControl issues a verb that must run on a process holding the run, never on
// the utility bridge: that session denies every permission request and errors every
// fs/terminal call, so a run resumed there would grind with no tools.
func (rs *Runs) hostedControl(ctx context.Context, workflowID, method string) error {
	sb, discard, err := rs.hostOrRehost(ctx, workflowID)
	if err != nil {
		return err
	}
	// Held for the whole span, so a kept carrier's bound cannot come due under a
	// verb still using it — carrierUse.
	rs.carriers.enter(sb)
	defer rs.carriers.leave(sb)
	resp, cErr := sb.bridge.Call(ctx, method, map[string]any{keyWorkflowID: workflowID})
	if callErr := runCallErr(resp, cErr); callErr != nil {
		discard(callErr)
		return callErr
	}
	return nil
}

// errRunHostStart marks a failure to START a carrier for a run: a spawn fault on
// this server, not a statement about the run. The REST layer keys on it to answer
// 500 rather than echoing an internal path as though the caller had asked wrongly.
var errRunHostStart = errors.New("a process for this run could not be started")

// hostOrRehost resolves the process holding a run, STARTING one when nothing does,
// and hands back the teardown its caller owes on a FAILED verb.
//
// `discard` is a no-op for an already-hosted run — a bridge THIS call started must
// go, one that was already there belongs to a launch or a conversation — and it
// takes the CAUSE, because a context error keeps the carrier. Both asymmetries are
// in vibekit-runtime.md's liveness-split block. It never releases the LEASE.
func (rs *Runs) hostOrRehost(
	ctx context.Context, workflowID string,
) (sb *sharedBridge, discard func(error), err error) {
	if held := rs.hostBridge(ctx, workflowID); held != nil {
		return held, func(error) {}, nil
	}
	return rs.rehost(ctx, workflowID)
}

// rehost starts a process for a run nothing hosts and registers it under the run's
// synthetic `run:<id>` chat id. KAS rehydrates from disk, so vibekit supplies the
// carrier and nothing else.
//
// Registration precedes the verb, whose first lifecycle frame can arrive before the
// call returns, and the bridge OUTLIVES the bounded start — StartOpts.Lifetime is
// what it lives on. A LOST race hands back the incumbent with a no-op discard, for
// the three reasons in vibekit-runtime.md's liveness-split block.
func (rs *Runs) rehost(
	ctx context.Context, workflowID string,
) (sb *sharedBridge, discard func(error), err error) {
	cctx, cancel := context.WithTimeout(ctx, launchTimeout)
	defer cancel()
	bridge := rs.bridges.factory()
	if sErr := bridge.Start(cctx, &vibekit.StartOpts{
		Lifetime:    rs.lifecycle.shutdownCtx,
		AgentEngine: resolveAgentEngine(),
		Presets:     securityPresets(cctx, rs.lifecycle.configDir),
		ToolSearch:  toolSearchEnabled(cctx, rs.lifecycle.configDir),
		Knowledge:   knowledgeEnabled(cctx, rs.lifecycle.configDir),
		Memory:      memoryEnabled(cctx, rs.lifecycle.configDir),
	}); sErr != nil {
		return nil, nil, fmt.Errorf("%w: %w", errRunHostStart, sErr)
	}
	chatID := runChatID(workflowID)
	resident, inserted := rs.bridges.insert(chatID, &sharedBridge{bridge: bridge, state: bridgeIdle})
	if !inserted {
		bridge.Stop()
		slog.Info("another call had already re-hosted this run, so the second process was stopped",
			"workflow_id", workflowID)
		return resident, func(error) {}, nil
	}
	go rs.coord.Forward(chatID, bridge)
	slog.Info("re-hosted a run nothing was holding", "workflow_id", workflowID)
	return resident, func(cause error) {
		if isCtxErr(cause) {
			slog.Warn("a run verb ended with its context cancelled, so its carrier is kept: "+
				"whether KAS took the verb is unknown, and frames from a run it drove would go nowhere",
				"workflow_id", workflowID, "error", cause)
			rs.boundKeptCarrier(chatID, workflowID, resident)
			return
		}
		rs.coord.CloseBridge(chatID)
	}, nil
}

// carrierUse counts the run verbs currently HOLDING each carrier, so the kept-carrier
// bound asks rather than inferring — the premise it replaces held for Retry's call
// alone, and the key is the CARRIER because a lost insert race hands back the
// incumbent. Both: vibekit-runtime.md's liveness-split block.
type carrierUse struct {
	held map[*sharedBridge]int
	// onIdle is the close a lifecycle frame deferred because a verb was holding the
	// carrier. See whenIdle.
	onIdle map[*sharedBridge]func()
	mu     sync.Mutex
}

// enter records that a verb is using sb.
//
// Paired with leave over the WHOLE span from resolving the carrier to returning,
// never around the Call alone: AnswerInput's span contains an `inspect` round trip
// between taking the carrier and sending, so a Call-scoped count would leave the
// reader's carrier closable for the length of that read.
func (c *carrierUse) enter(sb *sharedBridge) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.held == nil {
		c.held = make(map[*sharedBridge]int)
	}
	c.held[sb]++
}

// leave records that a verb has finished with sb, and runs a close a lifecycle frame
// deferred. The entry is DELETED at zero rather than left holding 0: the key is a live
// pointer, so a retained entry would hold that bridge for the process's life.
//
// The deferred close runs OUTSIDE the lock — it tears a process down, so holding c.mu
// across it would block every other verb's enter and leave.
func (c *carrierUse) leave(sb *sharedBridge) {
	c.mu.Lock()
	if c.held[sb] > 1 {
		c.held[sb]--
		c.mu.Unlock()
		return
	}
	delete(c.held, sb)
	closeFn := c.onIdle[sb]
	delete(c.onIdle, sb)
	c.mu.Unlock()
	if closeFn != nil {
		closeFn()
	}
}

// busy reports whether any run verb is holding sb right now.
func (c *carrierUse) busy(sb *sharedBridge) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.held[sb] > 0
}

// whenIdle runs closeFn once no verb is holding sb — immediately when none is.
//
// It is what lets a LIFECYCLE frame ask the kept-carrier bound's question without a
// timer, and TWO INDEPENDENT terminators end the wait: the caller's context, and KAS
// answering — likely here, a terminal frame just arrived. Bridge exit is NOT a third,
// being the close being deferred. The residual: vibekit-runtime.md. A repeat frame
// REPLACES the pending closer rather than queueing beside it — one carrier, one close,
// so exactly-once is the whole requirement.
func (c *carrierUse) whenIdle(sb *sharedBridge, closeFn func()) {
	c.mu.Lock()
	if c.held[sb] == 0 {
		c.mu.Unlock()
		closeFn()
		return
	}
	if c.onIdle == nil {
		c.onIdle = make(map[*sharedBridge]func())
	}
	c.onIdle[sb] = closeFn
	c.mu.Unlock()
}

// keptCarrierGrace is how long a carrier kept on an unknown outcome is given before
// its run is re-read. Still STRICTLY longer than launchTimeout, which covers Retry's
// own bounded call, but the grace is no longer the safety argument — carrierUse is.
// A `var` so a test drives the path in milliseconds; never reassigned in production.
var keptCarrierGrace = 10 * time.Minute

// carrierVerdict is what one firing of the kept-carrier bound decided.
type carrierVerdict int

const (
	// carrierClosed: the carrier was stopped.
	carrierClosed carrierVerdict = iota
	// carrierSpared: not this bound's to close, or its run is still executing.
	// Nothing further to wait for, so the bound ends here.
	carrierSpared
	// carrierBusy: a run verb is still holding it, so the answer is "ask again".
	carrierBusy
)

// boundKeptCarrier gives a carrier kept on a context error an END, because the reason
// it was kept is also the reason nothing else would ever close it.
//
// Untracked like healPaused's own AfterFunc and safe for the same reason: the guards
// below make a late firing cost one inspect instead of a wrong close. A BUSY verdict
// RE-ARMS, and that re-arm is NOT self-terminating — while a verb blocks on this
// carrier nothing here closes it, so the loop ends at that verb's own end or at
// shutdown. Why each of those ends it, plus the residual: vibekit-runtime.md.
func (rs *Runs) boundKeptCarrier(chatID vibekit.ChatID, workflowID string, kept *sharedBridge) {
	time.AfterFunc(keptCarrierGrace, func() {
		if rs.closeKeptCarrier(chatID, workflowID, kept) == carrierBusy {
			rs.boundKeptCarrier(chatID, workflowID, kept)
		}
	})
}

// closeKeptCarrier is the bound's whole decision, split out so it is answerable
// without a timer.
//
// IDENTITY leads, so a stale bound neither closes a later re-host's carrier nor
// re-arms forever over one nothing will close; USE comes next because it is a local
// read and needs no RPC to decline.
func (rs *Runs) closeKeptCarrier(
	chatID vibekit.ChatID, workflowID string, kept *sharedBridge,
) carrierVerdict {
	if rs.bridges.get(chatID) != kept {
		return carrierSpared
	}
	if rs.carriers.busy(kept) {
		slog.Info("a run verb is still holding a carrier its bound came due on, "+
			"so the carrier is kept and re-read later", "workflow_id", workflowID)
		return carrierBusy
	}
	res, ok := rs.inspect(rs.lifecycle.shutdownCtx, workflowID)
	if !ok || res.WorkflowID != workflowID {
		return carrierSpared
	}
	if !terminalRunStatus(res.State.Status) && res.State.Status != runStatusPaused {
		return carrierSpared
	}
	slog.Info("closing a run carrier kept for a verb KAS never took",
		"workflow_id", workflowID, "status", res.State.Status)
	rs.coord.CloseBridge(chatID)
	return carrierClosed
}

// isCtxErr reports whether an error is a cancellation or a deadline, at any
// wrapping depth. It is the ONE condition under which a failed verb keeps the
// carrier this call started (hostOrRehost).
func isCtxErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// hostBridge resolves the bridge whose process holds the run's registry entry.
//
// Two ways a run is hosted (vibekit-runtime.md): its own `run:<id>` bridge, or the
// LAUNCHING CHAT's, since KAS parents an agent-launched run on that session. Costs
// one `workflow/list` round trip on the second path only.
func (rs *Runs) hostBridge(ctx context.Context, workflowID string) *sharedBridge {
	_, sb := rs.hostBridgeChat(ctx, workflowID)
	return sb
}

// hostBridgeChat is hostBridge plus the CHAT ID that bridge belongs to, or "" when
// nothing here hosts the run. The id is what a synthesised ask is keyed to
// (askChatID), so a reconstructed question lands in the launching conversation's
// dock. One function rather than two, to pay the `workflow/list` trip once.
func (rs *Runs) hostBridgeChat(
	ctx context.Context, workflowID string,
) (vibekit.ChatID, *sharedBridge) {
	if sb := rs.runOwnBridge(workflowID); sb != nil {
		return runChatID(workflowID), sb
	}
	return rs.bridgeForSession(ctx, rs.parentSession(ctx, workflowID))
}

// runOwnBridge is the process holding the run under its OWN `run:<id>` chat id, or
// nil when nothing re-hosted it there.
//
// ONE owner for a three-line preference, because the preference is load-bearing and
// has two composition sites (hostBridgeChat and CancelForSessions): a re-hosted run's
// registry entry lives in THAT process, so consulting the chat's bridge first would
// send the verb to a process which has forgotten the run and be refused.
func (rs *Runs) runOwnBridge(workflowID string) *sharedBridge {
	return rs.bridges.get(runChatID(workflowID))
}

// bridgeForSession is the live bridge whose chat launched from this KAS session, the
// half hostBridgeChat reaches once the parent session is known. No RPC.
//
// It needs the chat RECORD, which is what makes it unusable on the delete-grade
// teardown — see CancelForSessions.
func (rs *Runs) bridgeForSession(
	ctx context.Context, parent string,
) (vibekit.ChatID, *sharedBridge) {
	if parent == "" {
		return "", nil
	}
	// The bridge map rather than the chat store: a chat with no bridge is no
	// carrier, so resolving its id answers a question nobody can act on.
	for chatID, sb := range rs.bridges.all() {
		chat, ok := rs.chats.Get(ctx, chatID)
		if !ok {
			continue
		}
		// The whole CHAIN, not the live id: a chat changes session routinely, and
		// matching only the current one would strand exactly those runs.
		if slices.Contains(chat.SessionChain(), parent) {
			return chatID, sb
		}
	}
	return "", nil
}

// parentSession reports the KAS session that launched a run, or "" when the run is
// parentless, unknown, or the inventory cannot be read. `workflow/list` is the only
// source — `inspect` omits the field, and the frame that carries it is not retained.
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

// control issues a verb that is safe on either connection, PREFERRING the process
// that holds the run. Only cancel and delete qualify: both only WRITE state, so a
// text-only session carries them; executing verbs use hostedControl.
//
// The utility session is REFUSED while the owner lives — KAS checks ownership on every
// branch but its own registry hit — so routing is what makes these two land. Resolving
// it needs no re-host (hostBridgeChat), so the fallback costs one `workflow/list` trip
// and a carrier the CALLER resolved (cancelOn) costs none. Bundle: vibekit-acp.md.
func (rs *Runs) control(
	ctx context.Context, workflowID, method, logLabel string, carrier *sharedBridge,
) error {
	params := map[string]any{keyWorkflowID: workflowID}
	if carrier == nil {
		_, carrier = rs.hostBridgeChat(ctx, workflowID)
	}
	if carrier != nil {
		resp, err := carrier.bridge.Call(ctx, method, params)
		return runCallErr(resp, err)
	}
	_, err := rs.utility().session.rawCall(ctx, logLabel, method, callerParams(params))
	return err
}

// dispatch is translateACPEvent's branch for frames arriving on a RUN bridge.
//
// Host requests and the three ASK kinds reuse the chat handlers, keyed by the
// synthetic id. LIFECYCLE frames go out workspace-global with an EMPTY chat id,
// because a parentless run is owned by no chat. session/update is PROJECTED as
// `run_step` and never buffered — there is no transcript to buffer into.
func (rt *Runtime) dispatch(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	if msg.ID != nil {
		rt.dispatchRequest(ctx, chatID, msg)
		return
	}
	// BEFORE the prefix test, which this method is not under. It keeps the run
	// bridge's OWN chat id rather than going workspace-global: an ask is
	// answerable, so it must land on a surface, and `run:<id>` is that dock key.
	if msg.Method == methodKiroSessionNotify {
		rt.runs.handleSessionNotify(ctx, chatID, msg)
		return
	}
	if strings.HasPrefix(msg.Method, "_kiro/workflow/") {
		if fn, ok := rt.chatHandlers[msg.Method]; ok {
			fn(ctx, "", msg)
		}
		if msg.Method == methodWFRunComplete {
			rt.closeStoppedBridge(chatID, msg)
		}
		return
	}
	if msg.Method == vibekit.MethodSessionUpdate {
		rt.translator.HandleRunStepFrame(ctx, workflowIDOf(chatID), msg.Params)
		return
	}
	slog.Debug("run bridge: unhandled notification", "method", msg.Method, "chat_id", chatID)
}

// dispatchRequest answers an A→C request on a run bridge, mirroring
// translateACPEvent's request half minus the chat-only concerns. An unmatched
// request is REFUSED rather than dropped: an unanswered one wedges the step's turn.
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

// closeStoppedBridge closes a run bridge once its run STOPPED EXECUTING, terminal or
// paused alike; hostOrRehost re-hosts a parked one on demand. Run bridges only, no
// lease released, an unrecognised status kept: vibekit-runtime.md for each.
//
// The close is a goroutine because this runs FROM the forward loop, whose channel
// CloseBridge → Stop closes. It ASKS about a verb in flight, closeKeptCarrier's own
// question, and DEFERS rather than re-arming: it holds a terminal frame, so the held
// span's end is a signal it can wait on. That and the identity re-check: same doc.
func (rt *Runtime) closeStoppedBridge(chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	var p struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(msg.Params, &p) != nil {
		return
	}
	if !terminalRunStatus(p.Status) && p.Status != runStatusPaused {
		return
	}
	sb := rt.bridge.mgr.get(chatID)
	if sb == nil {
		return
	}
	slog.Info("run stopped executing, closing its bridge", "chat_id", chatID, "status", p.Status)
	rt.runs.carriers.whenIdle(sb, func() {
		if rt.bridge.mgr.get(chatID) != sb {
			return
		}
		go rt.coord.CloseBridge(chatID)
	})
}

// terminalRunStatus mirrors KAS's isTerminalWorkflowStatus: paused is the one
// non-terminal run_complete status (an onMaxIterations policy stop).
//
// `cancelled` is KEPT even though it is not in KAS's own status enum — it is
// reachable, and one value too WIDE is the safe direction here. Bundle evidence and
// the cost either way: vibekit-acp.md.
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
// KAS's list is the source of truth, deliberately: it is the only thing that sees
// the runs vibekit did not launch. The leases add the ability to EXPLAIN a blocking
// row — ask whether it is an orphan vibekit itself left behind before refusing.
func (rs *Runs) recipeIdle(ctx context.Context, name string) error {
	runs, err := rs.list(ctx, nil)
	if err != nil {
		// Launching blind would let a second live run exist, which the
		// Run ⇄ Cancel row cannot represent.
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

// stalePauseReason is KAS's STALE_RUNNING_PAUSE_REASON, stamped by its read-path
// reconcile on a run whose owning process died. Matched as a LITERAL: several sites
// set pauseReason and only this one licenses a cancel.
const stalePauseReason = "Interrupted by agent restart; the previously running step was paused for resume."

// The pause reasons KAS records when a run stopped for a cause NOBODY CHOSE.
//
// THE RESUME SIDE ONLY: stalePauseReason licenses a CANCEL, this wider set only a
// resume. The network one is matched by PREFIX because KAS interpolates the error
// code into it, and that prefix cannot reach a reason that must be left alone.
const (
	interruptedPauseReason  = "Step interrupted (agent shutdown or connection reset); will resume."
	modelServicePauseReason = "Transient model service error (service 5xx/throttling); will resume."
	networkPausePrefix      = "Transient connection error ("
)

// transientErrorClass is the `pauseDetail.class` KAS stamps for every transient
// fault, the MACHINE-READABLE half of the reasons above. It reaches a pause a REASON
// cannot, because a parallel branch's prose is re-rendered around it — see
// vibekit-acp.md.
const transientErrorClass = "transient-error"

// pauseDetail is KAS's machine-readable pause CLASSIFICATION, on both the
// run-level `paused` frame and `inspect`'s state. ONE declaration for the whole
// predicate path, so no second Go shape can disagree with the wire.
//
// `occurredAt` is on the wire and is deliberately NOT declared here: every field
// here is one a predicate decodes, and no predicate reads a timestamp. Its one
// reader is the need-input reconcile, off its own narrow shape (run_ask.go).
type pauseDetail struct {
	Class string `json:"class"`
	Code  string `json:"code"`
}

// resumablePause reports whether a pause means the run stopped for a cause nobody
// chose, and is therefore vibekit's to resume unasked.
//
// REASON **OR** DETAIL, and both arms are load-bearing: the literals cover the
// pauses that carry no detail, the detail arm covers the ones whose prose is
// re-rendered. THE RESUME SIDE ONLY — `restartPaused` (run_orphan.go) cancels and
// never reads a detail, an asymmetry preserved by construction rather than comment.
func resumablePause(reason string, detail *pauseDetail) bool {
	if detail != nil && detail.Class == transientErrorClass {
		return true
	}
	switch reason {
	case stalePauseReason, interruptedPauseReason, modelServicePauseReason:
		return true
	}
	return strings.HasPrefix(reason, networkPausePrefix)
}

// resumeInterruptedRuns resumes the runs a chat's rehydrated bridge should pick
// back up: the ones ITS sessions launched that stopped for a cause nobody chose. A
// run heals WITH its chat, which is why an agent-launched run has no Resume button.
//
// Scoped twice: to this chat's session chain (never resumeAll, which would sweep
// runs another chat or the TUI paused on purpose), and to the involuntary reasons.
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

// healBaseDelay is the wait before the FIRST automatic resume, doubling per attempt
// (5s, 10s, 20s). Not zero, so a deliberate cancel can land first. A `var` so a test
// can drive the path in milliseconds; never reassigned in production.
var healBaseDelay = 5 * time.Second

// healPaused resumes a run KAS has just parked for a reason nobody chose, off the
// `_kiro/workflow/paused` frame the launching chat's bridge already receives — so
// the timing is exact with no polling and no timer per chat.
//
// Runs AFTER `next`, so the client renders the pause before anything undoes it.
func (rs *Runs) healPaused(
	next func(context.Context, vibekit.ChatID, *vibekit.RPCResponse),
) func(context.Context, vibekit.ChatID, *vibekit.RPCResponse) {
	return func(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
		next(ctx, chatID, msg)
		f := decodePauseFrame(msg)
		if f.WorkflowID == "" || chatID == "" {
			return
		}
		if !resumablePause(f.PauseReason, f.PauseDetail) {
			// Without this line the decline is silent and `observePaused` writes
			// nothing either, so the frame's arrival had to be inferred. Debug
			// because a run parked ON PURPOSE takes this branch too.
			slog.Debug("a paused run was left alone: its pause is not one vibekit resumes unasked",
				"workflow_id", scrubLog(f.WorkflowID), "chat_id", chatID,
				"pause_reason", scrubLog(f.PauseReason),
				"pause_class", scrubLog(pauseClassOf(f.PauseDetail)),
				"pause_code", scrubLog(pauseCodeOf(f.PauseDetail)))
			return
		}
		attempt, ok := rs.claimHeal(f.WorkflowID)
		if !ok {
			slog.Warn("a run keeps pausing for a cause nobody chose; leaving it paused",
				"workflow_id", scrubLog(f.WorkflowID), "chat_id", chatID,
				"pause_reason", scrubLog(f.PauseReason), "attempts", attempt)
			return
		}
		delay := healBaseDelay * time.Duration(1<<(attempt-1))
		slog.Info("scheduling the automatic resume of an involuntarily paused run",
			"workflow_id", scrubLog(f.WorkflowID), "chat_id", chatID,
			"pause_reason", scrubLog(f.PauseReason), "delay", delay)
		// NOT tracked, unlike `bounds.timers`: a fired deadline CANCELS a run,
		// while this re-reads the state and does nothing unless still parked.
		time.AfterFunc(delay, func() {
			hctx, cancel := rs.lifecycle.derivedContext()
			defer cancel()
			rs.resumeIfInterrupted(hctx, chatID, f.WorkflowID)
		})
	}
}

// pauseFrame is the three fields the heal reads off `_kiro/workflow/paused`. Its
// own minimal decode rather than a share of `lifecycleFrame`, which carries the
// status the BOUNDS read and no pause reason at all. Read the DETAIL, not the
// prose. Pointer first for govet's fieldalignment; the tags carry the wire names.
type pauseFrame struct {
	PauseDetail *pauseDetail `json:"pauseDetail"`
	WorkflowID  string       `json:"workflowId"`
	PauseReason string       `json:"pauseReason"`
}

// pauseClassOf names a pause's class for a log line, or says there was none.
// LOG-ONLY: the predicates compare the field directly, because a sentinel string
// is a value KAS could theoretically send.
func pauseClassOf(d *pauseDetail) string {
	if d == nil {
		return "(none)"
	}
	return d.Class
}

// pauseCodeOf is pauseClassOf's twin, and it is what makes `code` a field this
// path READS rather than one it merely declares — see pauseDetail.
func pauseCodeOf(d *pauseDetail) string {
	if d == nil {
		return "(none)"
	}
	return d.Code
}

// unmarshalKeepingReadable decodes data into dst and reports whether the result is
// usable, KEEPING every field that decoded when one field's wire TYPE has drifted.
//
// `encoding/json` finishes the object on a type mismatch, so a drifted `pauseDetail`
// still leaves `workflowId` and `pauseReason` intact; a bare `err != nil` discarded
// them and blinded the heal silently. A SYNTAX error is NOT tolerated — there the
// bytes are not JSON, so nothing in dst is a partial answer.
func unmarshalKeepingReadable(data []byte, dst any) bool {
	err := json.Unmarshal(data, dst)
	if err == nil {
		return true
	}
	var typeErr *json.UnmarshalTypeError
	return errors.As(err, &typeErr)
}

func decodePauseFrame(msg *vibekit.RPCResponse) pauseFrame {
	var f pauseFrame
	if msg == nil || len(msg.Params) == 0 {
		return f
	}
	if !unmarshalKeepingReadable(msg.Params, &f) {
		return pauseFrame{}
	}
	return f
}

// healProgress gives a run back both of its per-stall budgets when a node completes,
// and retires the ask that node was holding. Progress is the only honest evidence the
// fault cleared.
//
// The ASK clear is NODE-scoped, not run-scoped: a parallel branch's node can complete
// while a sibling's step still waits, and clearing the run would take that live ask
// with it. It also covers the step answered from the TUI, which no path here claimed.
// Both budgets are keyed on the RUN, so they are refilled per frame.
func (rs *Runs) healProgress(
	next func(context.Context, vibekit.ChatID, *vibekit.RPCResponse),
) func(context.Context, vibekit.ChatID, *vibekit.RPCResponse) {
	return func(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
		if f := decodeNodeFrame(msg); f.WorkflowID != "" {
			rs.clearHeals(f.WorkflowID)
			// The cancel ladder too: a refusal is evidence about a MOMENT, and a
			// run that has since completed a node makes our earlier refusals
			// stale. Without it, three refused Cancel presses leave the run
			// clock's own ladder spent hours later — see maxCancelRetries.
			rs.clearCancelRetries(f.WorkflowID)
			// And the idle window, which is the same evidence spent on the third
			// budget: a completed node is the strongest progress signal there is.
			// BOTH populations reach it — the run bridge reuses chatHandlers for
			// every `_kiro/workflow/*` method — so this is the one progress site a
			// parentless run and a chat-parented one share.
			rs.refillDeadline(ctx, f.WorkflowID)
			// SettledByMoot: this frame says the node MOVED ON — completion,
			// failure or abort — and vibekit's answer path already settled
			// anything it accepted, so nothing here was answered through us.
			rs.settleAskForNode(ctx, f.WorkflowID, f.NodeID, vibekit.SettledByMoot)
		}
		next(ctx, chatID, msg)
	}
}

// nodeFrame is the two fields the node-scoped ask clear reads off
// `_kiro/workflow/node_complete`, its own minimal decode for pauseFrame's reason.
//
// It keeps the STRICT decode its sibling dropped, and the asymmetry is the
// consumer's: `settleAskForNode` needs BOTH ids, so a frame missing either would
// settle the wrong node rather than degrade.
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
	// The wider involuntary set, because this RESUMES; the orphan sweep's
	// narrower `restartPaused` cancels.
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
	// Resume's own arm, for its reason: each arm bounds EXECUTING time. Needed HERE
	// because this path calls the verb directly, so only the `run_start` frame covers
	// it — and a lost frame leaves the run executing unbounded with nothing noticing.
	rs.armDeadline(ctx, workflowID)
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

// CancelForChat cancels every non-terminal run this chat's sessions launched. A run
// is durable state, so killing the chat's process only PAUSES it — each must be told
// to cancel while the owning bridge is alive to say it. Begins with a record read,
// so it no-ops on a deleted chat.
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
// blocking a tab close on an RPC would invert the gesture's meaning.
//
// ONE inventory read, not N+1, and NO chat-record read: the carrier comes off the two
// things this loop already knows. A plain Cancel re-reads the inventory per run for
// the parent session, and the record-matching resolver cannot answer here at all.
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
		carrier := rs.runOwnBridge(r.WorkflowID)
		if carrier == nil {
			// THIS chat's bridge, by id: the loop filtered on this chat's own
			// chain, so it is the launching chat for every run it reaches, and a
			// deleted record is no obstacle to a map lookup.
			carrier = rs.bridges.get(chatID)
		}
		if cErr := rs.cancelOn(ctx, r.WorkflowID, carrier); cErr != nil {
			slog.Warn("close: run cancel failed", "workflow_id", r.WorkflowID, "chat_id", chatID, "error", cErr)
			continue
		}
		slog.Info("close: cancelled chat's run", "workflow_id", r.WorkflowID, "chat_id", chatID)
	}
}
