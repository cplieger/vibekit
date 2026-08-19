package hub

// The run host: launching a PARENTLESS workflow run, and the bridge that hosts
// it.
//
// There are exactly two ways a run starts, and they get two different homes.
// The agent calling `run_workflow` parents the run on ITS chat's session — KAS
// hardcodes that — so the run lives on the chat's bridge and vibekit builds
// nothing. The USER clicking Run on the Workflows tab is the other way, and it
// deliberately touches no chat (user decision): no launch record in any
// transcript, no progress rows on any conversation, no completion wake. That
// run needs a process of its own, because `new` + `invoke` must travel over
// SOME connection and the two existing kinds are both wrong:
//
//   - a chat's bridge couples the run's life to a conversation it has nothing
//     to do with — closing that unrelated tab would kill the run
//   - the utility bridge REFUSES host requests by design (text-only), so a
//     step's permission ask would be auto-denied and its file writes would fail
//
// So: ONE bridge per launched run, registered in the ordinary bridge manager
// under the synthetic chat id `run:<workflowId>`. The synthetic id is the whole
// trick — every response path (permission answers, fs replies, terminal I/O)
// already resolves its bridge from that map by chat id, so a run ask answered
// from the client routes back with ZERO new addressing machinery: the client
// sends `permission_response` with chat_id `run:<id>` and CmdPermission finds
// the right bridge the same way it finds a chat's.
//
// What the synthetic id does NOT get: a chat file. Invariant 3 ("a live bridge
// implies a live chat record") exists so a CONVERSATION survives restarts; a
// run bridge is deliberately ephemeral — its loss is a PAUSE (KAS reconciles a
// dead owner's run to paused on the next read), and closing the tab is a
// CANCEL. Nothing in the run dispatch path writes the chat store, which is
// what keeps the invariant scoped to chats.
//
// Lifecycle: the bridge lives until its run reaches a terminal `run_complete`,
// which the dispatcher watches for and answers by closing the bridge — covering
// completion, failure, AND cancel with one rule. Closing the bridge right after
// the cancel VERB would be wrong: cancel is a node-boundary verb (probe 15) and
// the terminal state is certified by the OWNING process at the node boundary,
// so killing that process on the verb's reply can leave the run `paused`
// instead of `cancelled` on the next read.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/runlease"
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
func runChatID(workflowID string) api.ChatID {
	return api.ChatID(runChatPrefix + workflowID)
}

// isRunChat reports whether a chat id names a run bridge.
func isRunChat(chatID api.ChatID) bool {
	return strings.HasPrefix(string(chatID), runChatPrefix)
}

// launchTimeout bounds the launch handshake (process start + initialize +
// session/new + new + invoke). Generous because a first launch may unpack a
// KAS runtime tree (~240 MB) before the process answers.
const launchTimeout = 120 * time.Second

// errRecipeBusy reports the single-run rule: one live run per recipe
// definition, globally (user decision). Concurrency is declared INSIDE a
// workflow — subagents or a `parallel` node — never by launching the same
// recipe twice; the rule is also what lets the Workflows row carry a Run ⇄
// Cancel button that unambiguously names one run.
var errRecipeBusy = errors.New("this recipe already has a live run")

// LaunchRun starts one parentless run of the recipe with the given source key
// and returns its workflow id and name.
//
// The source is re-validated against a fresh listRecipes call rather than
// trusted: the wire value looks like a path (`bundled://x` or an absolute
// *.workflow.json), and echoing it into `new` unchecked would let a client
// point this endpoint at an arbitrary file.
//
// A manual run of a SCHEDULED recipe is bounded by that recipe's next slot as
// well as the ceiling — the same two inputs a scheduled launch gets — so clicking
// Run shortly before 02:00 cannot hold the recipe past 02:00. It stays ATTENDED
// either way: the slot bounds the run, the unattended permission floor does not
// apply, and no schedule row is attributed to it.
func (h *Hub) LaunchRun(ctx context.Context, source string, inputs map[string]string) (id, name string, err error) {
	return h.launchRun(ctx, source, inputs, manualLaunch())
}

// LaunchScheduledRun launches a run on behalf of a schedule, marking it
// UNATTENDED for the duration.
//
// The mark is what gates the deny-fast permission floor, and it rides the lease
// granted between `new` and `invoke` — the earliest point the workflow id exists
// and still before anything can execute, so no permission request can slip
// through unmarked.
//
// slotAt is the instant this run's own next slot comes due (see
// schedule.Launcher). It is an INPUT to the run's single deadline rather than a
// second bound of its own, so it travels with the launch instead of arming a
// timer of its own afterwards. Zero means the schedule cannot name its next slot,
// and the run is then bounded by the ceiling alone.
func (h *Hub) LaunchScheduledRun(ctx context.Context, source, scheduleID string, slotAt time.Time) (id, name string, err error) {
	return h.launchRun(ctx, source, nil, scheduledLaunch(scheduleID, slotAt))
}

// launchRun is the shared body of both public launch verbs. The origin carries
// everything that differs between them.
func (h *Hub) launchRun(ctx context.Context, source string, inputs map[string]string, o launchOrigin) (id, name string, err error) {
	cctx, cancel := context.WithTimeout(ctx, launchTimeout)
	defer cancel()

	recipe, err := h.recipeBySource(cctx, source)
	if err != nil {
		return "", "", err
	}
	if o.origin == runlease.OriginManual {
		// A manual run of a recipe that ALSO has a schedule must yield to that
		// recipe's next slot, and the slot is only knowable once the recipe is
		// resolved. Without this the run took the ceiling as its whole bound and
		// refused every slot underneath it (run_lease.go manualSlot).
		o.slotAt = h.manualSlot(recipe.Source)
	}
	if bErr := h.recipeIdle(cctx, recipe.Name); bErr != nil {
		return "", "", bErr
	}

	// The run's own process. Started OUTSIDE the manager: the map key is the
	// workflow id, which only `new`'s reply knows. Call replies ride the
	// bridge's readLoop (matched by request id), not NotifCh, so the forward
	// goroutine can attach after `new` — NotifCh is buffered, and whatever
	// session/new-time notifications land there are drained once Forward
	// starts.
	bridge := h.bridge.mgr.factory()
	if sErr := bridge.Start(cctx, &api.StartOpts{Lifetime: h.lifecycle.shutdownCtx}); sErr != nil {
		return "", "", fmt.Errorf("run bridge start: %w", sErr)
	}

	wfID, err := h.workflowNew(cctx, bridge, recipe.Source, inputs)
	if err != nil {
		bridge.Stop()
		return "", "", err
	}

	// Register BEFORE invoke: the first lifecycle frame follows invoke
	// immediately, and a frame arriving before the map entry would find no
	// bridge to answer through.
	h.bridge.mgr.insert(runChatID(wfID), &sharedBridge{bridge: bridge, state: bridgeIdle})
	// The run's envelope, before anything can execute: the single-run rule's
	// evidence that this row is vibekit's own, the deadline's inputs, and the
	// unattended mark the permission floor reads.
	h.grantLease(cctx, wfID, recipe.Name, o)
	go h.coord.Forward(runChatID(wfID), bridge)

	if _, err := bridge.Call(cctx, methodKiroWorkflowInvoke, map[string]any{keyWorkflowID: wfID}); err != nil {
		// The run was created but never started; nothing is executing. Tear the
		// bridge down and surface the failure rather than leaving a zombie row.
		h.releaseLease(cctx, wfID)
		h.coord.CloseBridge(runChatID(wfID))
		return "", "", fmt.Errorf("workflow invoke: %w", err)
	}
	// The wall clock, for every run this path launches — manual and scheduled
	// alike (run_bounds.go). After invoke, so a run that never started leaves no
	// timer, and idempotent with the `run_start` frame's own arm.
	h.armRunDeadline(cctx, wfID)
	slog.Info("workflow run launched", "workflow_id", wfID, "recipe", recipe.Name)
	return wfID, recipe.Name, nil
}

// CancelRun asks a run to stop, on the USER's behalf.
//
// It takes the run's termination claim like every bound does (run_bounds.go), and
// that is what keeps a deliberate stop from being relabelled: a cancel pressed
// seconds before a ceiling or a schedule deadline used to race a bound callback
// that still saw a live gate, and the row afterwards read "overran" for a run the
// user stopped on purpose. Winning records NOTHING, and that absence is the third
// value the row's one field carries.
//
// A LOST claim returns nil rather than an error, because the run is already being
// cancelled by something that got there first — the gesture's outcome is the one
// the caller asked for. When the claim is won and the RPC then fails, the claim is
// handed back, so a later Cancel is not silently refused.
func (h *Hub) CancelRun(ctx context.Context, workflowID string) error {
	if workflowID == "" {
		return errors.New("missing workflow id")
	}
	if !h.claimRunTermination(workflowID) {
		return nil
	}
	return h.finishRunTermination(ctx, workflowID, "")
}

// cancelRunRPC issues the cancel VERB and nothing else — no claim, no record.
//
// On the run's own bridge when one is live (the manual-launch case); through
// the utility session otherwise — an agent-launched run, or one from a
// previous boot, has no run bridge and cancel is an ordinary client RPC that
// any connection may issue. The bridge is NOT closed here: the terminal
// `run_complete` the cancel eventually produces closes it (see runDispatch),
// because the owning process must live to the node boundary to certify the
// cancelled state.
func (h *Hub) cancelRunRPC(ctx context.Context, workflowID string) error {
	return h.runControl(ctx, workflowID, methodKiroWorkflowCancel, "workflow cancel call")
}

// DeleteRun removes a run and everything either side keeps about it, on the
// USER's behalf. It is the History row's delete, and the only run verb that is
// not recoverable.
//
// Order matters and is the opposite of cancel's. KAS's delete cancels a
// non-terminal run itself before removing the run directory, so vibekit must NOT
// pre-cancel: taking the termination claim first would record an end reason for a
// row that is about to stop existing, and `CancelRun`'s own comment explains why a
// recorded reason outlives the run. So the verb goes first, and vibekit's
// bookkeeping is dropped only once KAS reports the run gone.
//
// The bridge IS closed here, unlike cancel's path. Cancel leaves it open on
// purpose — the owning process must live to the node boundary to certify the
// cancelled state — but a deleted run has no state left to certify and no
// directory to write it to, so waiting for a terminal frame that may never come
// would leak a kiro-cli subprocess for every deleted run.
//
// A failed verb leaves everything in place: the run still exists in KAS, so
// forgetting the lease would strand a row vibekit could no longer bound or cancel.
func (h *Hub) DeleteRun(ctx context.Context, workflowID string) error {
	if workflowID == "" {
		return errors.New("missing workflow id")
	}
	if err := h.runControl(ctx, workflowID, methodKiroWorkflowDelete, "workflow delete call"); err != nil {
		return err
	}
	h.coord.CloseBridge(runChatID(workflowID))
	h.forgetRunBounds(ctx, workflowID)
	h.clearRunEnd(workflowID)
	slog.Info("workflow run deleted", "workflow_id", workflowID)
	return nil
}

// errRunNotHosted is returned by a control verb that needs the run's OWN bridge
// when there is none. Distinct from a KAS refusal so the REST layer can answer
// 409 with an explanation rather than 500.
//
// The wording says what is missing (a bridge these verbs can address) rather than
// claiming the run is not running here: an agent-launched run IS executing on
// this server, on the calling chat's bridge, and is simply out of reach. It also
// names cancel as the surviving verb rather than claiming it is the only possible
// one -- KAS would rehydrate for resume too (`_kiro/workflow/resume` loads from
// disk), so a restart-orphaned paused run is dead-ended by vibekit's missing
// carrier, not by the protocol.
var errRunNotHosted = errors.New(
	"this run has no live bridge on this server, so it cannot be paused or resumed from here; " +
		"cancel still works. An agent-launched run is always in this state, " +
		"and so is any run from before the last restart")

// PauseRun asks a running run to stop at its next node boundary, keeping its
// state resumable. KAS sets `control.pauseRequested` and the in-flight node runs
// to completion, so the reply confirms the ASK rather than a paused state.
//
// Requires the run's own bridge: KAS's pause reaches `registry.require`, which
// throws for a run that is not in the live in-memory registry and does NOT
// rehydrate from disk the way cancel, resume and retry do.
func (h *Hub) PauseRun(ctx context.Context, workflowID string) error {
	return h.hostedRunControl(ctx, workflowID, methodKiroWorkflowPause)
}

// ResumeRun re-drives a paused run.
//
// Re-arms the wall clock, because the pause parked it (run_bounds.go): a resumed
// run is executing again and gets a FRESH budget rather than the remainder of the
// one it was holding when it parked. `run_start` re-fires on resume and arms too;
// whichever lands first wins.
func (h *Hub) ResumeRun(ctx context.Context, workflowID string) error {
	err := h.hostedRunControl(ctx, workflowID, methodKiroWorkflowResume)
	if err == nil {
		h.armRunDeadline(ctx, workflowID)
	}
	return err
}

// RetryRun re-hosts a finished run and resets its failed work.
//
// This is the re-hosting case, and it exists because retry's legality window and
// vibekit's bridge lifetime are disjoint: retry is legal only from `failed` or
// `aborted`, and `closeFinishedRunBridge` tears a run's bridge down on exactly
// those statuses. So unlike every other control verb, retry cannot use the run's
// own bridge -- there never is one when it is legal -- and it must create one.
//
// KAS's side needs no help: `_kiro/workflow/retry` calls `rehydrateWorkflowFromDisk`
// then `runner.loadFromDisk`, so it registers the run in the fresh process itself.
// vibekit only has to supply the carrier: a bridge, registered under the run's
// synthetic chat id so its lifecycle frames route, and no `new`/`invoke` because
// the run already exists on disk.
//
// OWNERSHIP, which is what kept this out until the UX was settled: the user opens
// the run's page and clicks Retry, so the user is the launcher and the run tab
// owns the bridge exactly as it owns a freshly launched one. There is no
// ambiguity about who owns a bridge nobody launched, because somebody did.
//
// Only reachable for a PARENTLESS run (user decision). An agent-parented run's
// recovery is the agent's own: it reaches these verbs through KAS directly, on a
// bridge it already has.
func (h *Hub) RetryRun(ctx context.Context, workflowID string) error {
	if workflowID == "" {
		return errors.New("missing workflow id")
	}
	chatID := runChatID(workflowID)

	// An already-hosted run needs no re-hosting: send on the bridge it has. This
	// is not the expected path (retry's window implies a closed bridge) but a run
	// aborted without a terminal frame can still be registered.
	if sb := h.bridge.mgr.get(chatID); sb != nil {
		recipe := h.recipeOfRun(ctx, workflowID)
		_, err := sb.Call(ctx, methodKiroWorkflowRetry, map[string]any{keyWorkflowID: workflowID})
		if err == nil {
			// Only on success, and in this order: a retry KAS refused re-drove
			// nothing, so the run's previous terminal reason is still the truth
			// about it and its row must keep saying so.
			h.rearmRetriedRun(ctx, workflowID, recipe)
		}
		return err
	}

	cctx, cancel := context.WithTimeout(ctx, launchTimeout)
	defer cancel()

	// The recipe name, read off KAS's run list BEFORE anything is re-driven. The
	// list is the only place a re-hosted run's recipe is available to this process,
	// and it has to be read while the run is still listed under its pre-retry
	// state rather than after the lease is needed.
	recipe := h.recipeOfRun(cctx, workflowID)

	bridge := h.bridge.mgr.factory()
	if err := bridge.Start(cctx, &api.StartOpts{Lifetime: h.lifecycle.shutdownCtx}); err != nil {
		return fmt.Errorf("retry bridge start: %w", err)
	}
	// Register BEFORE the call, for the same reason LaunchRun does: retry's first
	// lifecycle frame follows it immediately, and a frame arriving before the map
	// entry would find no bridge to answer through.
	h.bridge.mgr.insert(chatID, &sharedBridge{bridge: bridge, state: bridgeIdle})
	go h.coord.Forward(chatID, bridge)

	// The lease before the verb, exactly as a fresh launch grants between `new`
	// and `invoke`: retry's own `run_start` can arrive before the call returns, and
	// a lease minted from that frame would carry the frame's name rather than the
	// list's and would depend on the frame having one at all. Granting first makes
	// the retried run's envelope independent of that race.
	minted := false
	if _, held := h.lease(workflowID); !held {
		h.grantLease(cctx, workflowID, recipe, manualLaunch())
		minted = true
	}

	if _, err := bridge.Call(cctx, methodKiroWorkflowRetry, map[string]any{keyWorkflowID: workflowID}); err != nil {
		// Nothing is executing: tear the bridge down rather than leaving a
		// process hosting a run it failed to restart, and give back a lease this
		// call minted for a run that never re-drove.
		if minted {
			h.releaseLease(cctx, workflowID)
		}
		h.coord.CloseBridge(chatID)
		return fmt.Errorf("workflow retry: %w", err)
	}
	// A fresh clock and a clean row, both only now that the retry has landed: the
	// run is executing again, so its recorded termination is no longer a fact
	// about it — and the client deliberately lets a recognised end_reason outrank
	// live status, so leaving it would render the running retry as aborted.
	h.rearmRetriedRun(cctx, workflowID, recipe)
	slog.Info("workflow run retried", "workflow_id", workflowID, "recipe", recipe)
	return nil
}

// recipeOfRun reads a run's recipe NAME off KAS's own run list.
//
// The list is the only place this process can learn the recipe of a run it did not
// launch: a retry rehydrates a run KAS holds on disk, so vibekit never saw its
// `new`. The name it reports is the same string the single-run rule compares
// against, which is what makes a lease keyed with it useful rather than decorative.
//
// Best-effort — "" when the list cannot be read or does not carry the run — because
// a retry must not fail over a name. A nameless lease still bounds the run; it is
// only invisible to the single-run rule's comparison.
func (h *Hub) recipeOfRun(ctx context.Context, workflowID string) string {
	runs, err := h.workflowRunsRaw(ctx)
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

// SetRunStepStatus marks an in-flight step completed or failed so a wedged run
// can advance.
//
// Requires the run's own bridge and does NOT re-host, which is the honest
// difference from RetryRun: this verb acts on a step that is IN FLIGHT, so a run
// with no live bridge has no in-flight step to mark and the request is a
// mistake rather than something to rehydrate for.
func (h *Hub) SetRunStepStatus(ctx context.Context, workflowID, nodeID, status string) error {
	if nodeID == "" {
		return errors.New("missing node id")
	}
	if status != runStepCompleted && status != runStepFailed {
		return fmt.Errorf("step status must be %q or %q", runStepCompleted, runStepFailed)
	}
	sb := h.bridge.mgr.get(runChatID(workflowID))
	if sb == nil {
		return errRunNotHosted
	}
	_, err := sb.Call(ctx, methodKiroWorkflowUpdate, map[string]any{
		keyWorkflowID: workflowID,
		"update": map[string]any{
			"type":   "set_step_status",
			"nodeId": nodeID,
			"status": status,
		},
	})
	return err
}

// The two step statuses a human may set. Narrowed deliberately: KAS's `update`
// verb also carries `replace_remaining`, which is a plan editor and not proposed.
const (
	runStepCompleted = "completed"
	runStepFailed    = "failed"
)

// hostedRunControl issues a verb that must run on the run's OWN bridge, and
// refuses rather than falling back to the utility bridge.
//
// The fallback would be actively harmful for two of the three. Resume and retry
// make the run EXECUTE, and the utility bridge is a text-only session: it denies
// every `session/request_permission`, errors every `fs/*` and `terminal/*`
// request, and discards every `_kiro/workflow/*` notification. So a run resumed
// there would grind through its steps with no tools and no UI updates, which is
// worse than not offering the verb. Pause cannot use it either, for the
// unrelated reason above.
//
// The cost is stated rather than hidden, and it is bigger than it first looks.
// These verbs reach a run only while it has a live bridge under its own synthetic
// chat id, which means a run launched through POST /api/runs, in this process,
// still running or paused. Two cases fall outside that and can only be cancelled:
// a run orphaned by a container restart, and an AGENT-launched run, which KAS
// parents on the calling chat's session and which therefore never has a
// `run:<id>` bridge at all. The second is not an edge case, and closing it means
// routing by the run's parent chat, which the wire already carries.
//
// That is why the 409 says "no live bridge" rather than naming a server: the run
// may well be executing, on a connection these verbs cannot address.
func (h *Hub) hostedRunControl(ctx context.Context, workflowID, method string) error {
	sb := h.bridge.mgr.get(runChatID(workflowID))
	if sb == nil {
		return errRunNotHosted
	}
	resp, err := sb.bridge.Call(ctx, method, map[string]any{keyWorkflowID: workflowID})
	return runCallErr(resp, err)
}

// runControl issues a verb that is safe on either connection, preferring the
// run's OWN bridge and falling back to the utility bridge.
//
// Only cancel qualifies. A run launched by this process has a live bridge that
// owns the run's registry entry, and issuing the verb there lets KAS act on the
// in-memory record. The utility fallback is what makes cancel work on a run this
// process did not launch — a TUI-launched run, or one from before a restart —
// because cancel rehydrates from disk and then only WRITES state; it never
// re-drives execution, so a text-only session is a sufficient carrier for it.
// The verbs that do execute use hostedRunControl instead.
func (h *Hub) runControl(ctx context.Context, workflowID, method, logLabel string) error {
	params := map[string]any{keyWorkflowID: workflowID}
	if sb := h.bridge.mgr.get(runChatID(workflowID)); sb != nil {
		resp, err := sb.bridge.Call(ctx, method, params)
		return runCallErr(resp, err)
	}
	_, err := h.ensureUtility().session.rawCall(ctx, logLabel, method, callerParams(params))
	return err
}

// runDispatch is translateACPEvent's branch for frames arriving on a RUN
// bridge. The population differs from a chat's in both directions — step
// content must NOT flow into any transcript, and workflow lifecycle frames
// arrive here as this connection's own — so the dispatch is explicit rather
// than a filtered copy of the chat table:
//
//   - host REQUESTS (fs, terminals, auth, secrets) go through the SAME
//     handlers a chat's do, keyed by the synthetic chat id. Steps edit files
//     and run commands; refusing these (the utility bridge's posture) would
//     break every step. respondBridge resolves the reply bridge from the
//     manager, which is why the registration under runChatID makes this work
//     unchanged.
//   - the three ASK kinds (permission, elicitation, user input) go through
//     their chat handlers too: the broadcast keyed `run:<id>` is exactly what
//     the client dock renders in the run tab, and the reply routes back by
//     that same id.
//   - workflow LIFECYCLE frames go to the run translate handlers with an
//     EMPTY chat id (workspace-global): a parentless run is not owned by any
//     chat, and the client routes these by workflow id, not topic.
//   - session/update is DROPPED. A step's content on a CHAT's bridge feeds
//     that chat's transcript (attributed, see translate); here there is no
//     transcript — the run tab renders from `inspect` refetches — and buffering
//     into the synthetic id would open a phantom assistant message on a chat
//     that must never exist.
func (h *Hub) runDispatch(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	if msg.ID != nil {
		h.runDispatchRequest(ctx, chatID, msg)
		return
	}
	if strings.HasPrefix(msg.Method, "_kiro/workflow/") {
		if fn, ok := h.chatHandlers[msg.Method]; ok {
			fn(ctx, "", msg)
		}
		if msg.Method == methodWFRunComplete {
			h.closeFinishedRunBridge(chatID, msg)
		}
		return
	}
	if msg.Method == api.MethodSessionUpdate {
		return
	}
	slog.Debug("run bridge: unhandled notification", "method", msg.Method, "chat_id", chatID)
}

// runDispatchRequest answers an A→C request on a run bridge. The ladder
// mirrors translateACPEvent's request half minus the chat-only concerns; an
// unmatched request is REFUSED rather than dropped, because an unanswered
// request wedges the step's turn.
func (h *Hub) runDispatchRequest(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	switch {
	case h.handleFSRequest(ctx, chatID, msg),
		h.handleKiroFSRequest(ctx, chatID, msg),
		h.handleKiroClientRequest(ctx, chatID, msg),
		h.handleKiroSecretRequest(ctx, chatID, msg):
		return
	case strings.HasPrefix(msg.Method, methodTermPrefix):
		h.handleTerminalRequest(ctx, chatID, msg.Method, msg)
		return
	}
	if fn, ok := h.chatHandlers[msg.Method]; ok &&
		(msg.Method == api.MethodRequestPermission ||
			msg.Method == api.MethodElicitationCreate ||
			msg.Method == api.MethodKiroUserInput) {
		fn(ctx, chatID, msg)
		return
	}
	slog.Warn("run bridge: refusing unexpected request", "method", msg.Method, "chat_id", chatID)
	_ = h.BridgeRespond(ctx, chatID, *msg.ID, nil, &api.RPCError{
		Code:    api.RPCCodeMethodNotFound,
		Message: "unsupported on a run bridge: " + msg.Method,
	})
}

// closeFinishedRunBridge closes a run bridge once its run reached a TERMINAL
// state. One rule covers completion, failure and cancel — they all end in
// run_complete — and a non-terminal run_complete (a policy pause reports
// through the same frame) keeps the bridge, because the run is still this
// process's to resume.
//
// The close runs in a goroutine because this is called FROM the bridge's own
// forward loop, and CloseBridge → Stop closes the channel that loop ranges
// over.
func (h *Hub) closeFinishedRunBridge(chatID api.ChatID, msg *api.RPCResponse) {
	var p struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(msg.Params, &p) != nil || !terminalRunStatus(p.Status) {
		return
	}
	slog.Info("run finished, closing its bridge", "chat_id", chatID, "status", p.Status)
	// The run's LEASE is not released here. forgetRunBounds owns that, on the same
	// terminal frame, because it is the one site every origin reaches — an
	// agent-parented run has no bridge of its own to close.
	go h.coord.CloseBridge(chatID)
}

// terminalRunStatus mirrors KAS's isTerminalWorkflowStatus: paused is the one
// non-terminal run_complete status (an onMaxIterations policy stop).
func terminalRunStatus(s string) bool {
	return s == "completed" || s == "failed" || s == "aborted" || s == "cancelled"
}

// recipeBySource resolves a launch source against the CURRENT recipe list.
func (h *Hub) recipeBySource(ctx context.Context, source string) (api.Recipe, error) {
	if source == "" {
		return api.Recipe{}, errors.New("missing recipe source")
	}
	recipes, err := h.listRecipes(ctx)
	if err != nil {
		return api.Recipe{}, err
	}
	for _, r := range recipes {
		if r.Source == source {
			return r, nil
		}
	}
	return api.Recipe{}, fmt.Errorf("unknown recipe source %q", source)
}

// recipeIdle enforces the single-run rule against the current run list.
//
// KAS's list stays the source of truth here, and deliberately so: it is the only
// thing that sees the runs vibekit did not launch — an agent's, which KAS parents
// on the calling chat's session, and the TUI's — so keying the rule on vibekit's
// own leases instead would let a second live run of one recipe start.
//
// What the leases add is the ability to EXPLAIN a blocking row rather than refuse
// it blindly: before returning busy, ask whether that row is an orphan vibekit
// itself owns and left behind (run_orphan.go). This is the backstop half of the
// orphan clearing; the boot sweep is the half that makes a restart leave the system
// idle without waiting for someone to try a launch.
func (h *Hub) recipeIdle(ctx context.Context, name string) error {
	runs, err := h.workflowRuns(ctx, nil)
	if err != nil {
		// The guard needs the list; launching blind would let a second live
		// run of the recipe exist, which the Run ⇄ Cancel row cannot represent.
		return fmt.Errorf("run list unavailable: %w", err)
	}
	for i := range runs {
		if runs[i].Name != name || terminalRunStatus(runs[i].Status) {
			continue
		}
		if h.clearBlockingOrphan(ctx, runs[i].WorkflowID, runs[i].Status) {
			slog.Info("cleared a restart-orphaned run that was holding its recipe",
				"workflow_id", runs[i].WorkflowID, "recipe", name)
			continue
		}
		return errRecipeBusy
	}
	return nil
}

// kasRecipe is one listRecipes entry as KAS reports it (probe 26). `plan` rides
// through as raw JSON — see api.Recipe.
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
func (h *Hub) listRecipes(ctx context.Context) ([]api.Recipe, error) {
	u := h.ensureUtility()
	cctx, cancel := context.WithTimeout(ctx, sessionListTimeout)
	defer cancel()
	raw, err := u.session.rawCall(cctx, "workflow listRecipes call", methodKiroWorkflowListRecipes,
		callerParams(map[string]any{keyWorkspacePaths: []string{h.lifecycle.workDir}}))
	if err != nil {
		return nil, err
	}
	var list struct {
		Recipes []kasRecipe `json:"recipes"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	out := make([]api.Recipe, 0, len(list.Recipes))
	for _, r := range list.Recipes {
		if r.Name == "" || r.Source == "" {
			continue
		}
		out = append(out, api.Recipe{
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
func (h *Hub) workflowNew(ctx context.Context, bridge acpCaller, source string, inputs map[string]string) (string, error) {
	// inputs is always a map, never nil: KAS requires the key ("inputs is not
	// iterable" without it), and an input-less recipe takes {}.
	in := map[string]any{}
	for k, v := range inputs {
		in[k] = v
	}
	resp, err := bridge.Call(ctx, methodKiroWorkflowNew, map[string]any{
		"workflowPath":    source,
		keyWorkspacePaths: []string{h.lifecycle.workDir},
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
func runCallErr(resp *api.RPCResponse, err error) error {
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
// LITERAL, deliberately: at least five sites set pauseReason and only this one
// means "the process died under it"; every other reason (a deliberate pause, a
// policy stop, a step waiting for input, a torn plan) must be left alone.
const stalePauseReason = "Interrupted by agent restart; the previously running step was paused for resume."

// resumeRestartPausedRuns resumes the runs a chat's rehydrated bridge should
// pick back up: the ones ITS sessions launched, that a restart paused.
//
// This is the recovery model for agent-launched runs, and it is why there is
// no Resume button anywhere: a chat's bridge dying pauses its runs (KAS
// reconciles on the next read), and the user's next message respawns the
// bridge — so resuming here makes the run heal WITH the chat, the same way the
// chat itself heals. KAS's own startupRecovery auto-resumes only WATCH-parked
// runs; a step-parked one waits for a verb, and this is the verb.
//
// Scoped twice, both load-bearing: to THIS chat's session chain (never
// resumeAll, which would sweep runs other chats or the TUI paused on purpose),
// and to the restart pauseReason literal (a deliberately-paused run stays
// paused).
func (h *Hub) resumeRestartPausedRuns(ctx context.Context, chatID api.ChatID) {
	chat, ok := h.chatStore.Get(ctx, chatID)
	if !ok {
		return
	}
	chain := make(map[string]bool, len(chat.PriorACPSessionIDs)+1)
	chain[chat.ACPSessionID] = true
	for _, id := range chat.PriorACPSessionIDs {
		chain[id] = true
	}

	runs, err := h.workflowRunsRaw(ctx)
	if err != nil {
		slog.Warn("rehydrate: run list unavailable, skipping resume sweep", "chat_id", chatID, "error", err)
		return
	}
	for i := range runs {
		r := &runs[i]
		if r.Status != "paused" || !chain[r.ParentSessionID] {
			continue
		}
		h.resumeIfRestartPaused(ctx, chatID, r.WorkflowID)
	}
}

// resumeIfRestartPaused inspects one paused run and resumes it when its pause
// reason is the restart literal. Resumed on the CHAT's bridge, so the chat's
// process becomes the run's owner again — which is where an agent-launched
// run's frames belong.
func (h *Hub) resumeIfRestartPaused(ctx context.Context, chatID api.ChatID, workflowID string) {
	// The same predicate the orphan sweep reads, inverted in action: that one
	// CANCELS what this one RESUMES. One function rather than two copies of a
	// literal comparison, so the two cannot drift into disagreeing about which
	// runs a dead process left behind.
	if !h.restartPaused(ctx, workflowID) {
		return
	}
	sb := h.bridge.mgr.get(chatID)
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

// workflowRunsRaw lists runs with their raw parent session ids, for callers
// that scope by session chain rather than by resolved chat.
func (h *Hub) workflowRunsRaw(ctx context.Context) ([]kasWorkflowRun, error) {
	u := h.ensureUtility()
	cctx, cancel := context.WithTimeout(ctx, sessionListTimeout)
	defer cancel()
	raw, err := u.session.rawCall(cctx, "workflow list call", methodKiroWorkflowList,
		callerParams(map[string]any{keyWorkspacePaths: []string{h.lifecycle.workDir}}))
	if err != nil {
		return nil, err
	}
	var list kasWorkflowRuns
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	return list.Runs, nil
}

// CancelChatRuns cancels every non-terminal run this chat's sessions launched.
// The tab-close half of the run lifecycle (command.CmdCloseChat): a run is
// durable state, so killing the chat's process only PAUSES it — it must be told
// to cancel, per run, while the owning bridge is still alive to say it.
//
// Best-effort throughout: a run list failure or a per-run cancel failure is
// logged and the close proceeds. Blocking a tab close on a workflow RPC would
// invert the gesture's meaning — the user said stop, not wait.
func (h *Hub) CancelChatRuns(ctx context.Context, chatID api.ChatID) {
	chat, ok := h.chatStore.Get(ctx, chatID)
	if !ok {
		return
	}
	chain := make(map[string]bool, len(chat.PriorACPSessionIDs)+1)
	chain[chat.ACPSessionID] = true
	for _, id := range chat.PriorACPSessionIDs {
		chain[id] = true
	}
	runs, err := h.workflowRunsRaw(ctx)
	if err != nil {
		slog.Warn("close: run list unavailable, skipping run cancel", "chat_id", chatID, "error", err)
		return
	}
	for i := range runs {
		r := &runs[i]
		if terminalRunStatus(r.Status) || !chain[r.ParentSessionID] {
			continue
		}
		if cErr := h.CancelRun(ctx, r.WorkflowID); cErr != nil {
			slog.Warn("close: run cancel failed", "workflow_id", r.WorkflowID, "chat_id", chatID, "error", cErr)
			continue
		}
		slog.Info("close: cancelled chat's run", "workflow_id", r.WorkflowID, "chat_id", chatID)
	}
}
