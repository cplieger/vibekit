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
func (h *Hub) LaunchRun(ctx context.Context, source string, inputs map[string]string) (id, name string, err error) {
	cctx, cancel := context.WithTimeout(ctx, launchTimeout)
	defer cancel()

	recipe, err := h.recipeBySource(cctx, source)
	if err != nil {
		return "", "", err
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
	if sErr := bridge.Start(cctx, &api.StartOpts{}); sErr != nil {
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
	go h.coord.Forward(runChatID(wfID), bridge)

	if _, err := bridge.Call(cctx, methodKiroWorkflowInvoke, map[string]any{keyWorkflowID: wfID}); err != nil {
		// The run was created but never started; nothing is executing. Tear the
		// bridge down and surface the failure rather than leaving a zombie row.
		h.coord.CloseBridge(runChatID(wfID))
		return "", "", fmt.Errorf("workflow invoke: %w", err)
	}
	slog.Info("workflow run launched", "workflow_id", wfID, "recipe", recipe.Name)
	return wfID, recipe.Name, nil
}

// CancelRun asks a run to stop.
//
// On the run's own bridge when one is live (the manual-launch case); through
// the utility session otherwise — an agent-launched run, or one from a
// previous boot, has no run bridge and cancel is an ordinary client RPC that
// any connection may issue. The bridge is NOT closed here: the terminal
// `run_complete` the cancel eventually produces closes it (see runDispatch),
// because the owning process must live to the node boundary to certify the
// cancelled state.
func (h *Hub) CancelRun(ctx context.Context, workflowID string) error {
	return h.runControl(ctx, workflowID, methodKiroWorkflowCancel, "workflow cancel call")
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
func (h *Hub) ResumeRun(ctx context.Context, workflowID string) error {
	return h.hostedRunControl(ctx, workflowID, methodKiroWorkflowResume)
}

// There is deliberately NO RetryRun, and the missing piece is a CARRIER, not a
// capability.
//
// Retry is legal only from `failed` or `aborted` (KAS throws on the no-nodeId
// branch otherwise), and `closeFinishedRunBridge` closes a run's bridge on every
// terminal run_complete -- including those two. So the moment retry becomes legal
// is the moment the connection that could carry it is gone, and a wired retry is
// a button whose only outcome is a 409.
//
// KAS's side would cope fine, and an earlier revision of this comment was wrong
// to imply otherwise: `_kiro/workflow/retry` calls `rehydrateWorkflowFromDisk`
// and then `runner.loadFromDisk`, so it registers the run itself. What vibekit
// lacks is a process to send it on, because a registry is per-kiro-cli-process
// and each bridge is its own. Building one is roughly `LaunchRun` minus `new` and
// `invoke` -- a re-hosting feature with its own lifecycle and failure modes, and
// a decision about who owns a bridge nobody launched. That is why it stays out:
// the work is real, not because the wire refuses it.

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
func (h *Hub) recipeIdle(ctx context.Context, name string) error {
	runs, err := h.workflowRuns(ctx, nil)
	if err != nil {
		// The guard needs the list; launching blind would let a second live
		// run of the recipe exist, which the Run ⇄ Cancel row cannot represent.
		return fmt.Errorf("run list unavailable: %w", err)
	}
	for i := range runs {
		if runs[i].Name == name && !terminalRunStatus(runs[i].Status) {
			return errRecipeBusy
		}
	}
	return nil
}

// kasRecipe is one listRecipes entry as KAS reports it (probe 26). `plan` is
// deliberately not decoded — see api.Recipe.
type kasRecipe struct {
	Inputs      map[string]string `json:"inputs"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Source      string            `json:"source"`
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
			BuiltIn:     r.BuiltIn,
		})
	}
	return out, nil
}

// workflowNew creates the run on the given bridge and returns its id.
func (h *Hub) workflowNew(ctx context.Context, bridge api.ACPBridge, source string, inputs map[string]string) (string, error) {
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
	u := h.ensureUtility()
	cctx, cancel := context.WithTimeout(ctx, sessionListTimeout)
	defer cancel()
	raw, err := u.session.rawCall(cctx, "workflow inspect call", methodKiroWorkflowInspect,
		callerParams(map[string]any{keyWorkflowID: workflowID}))
	if err != nil {
		slog.Warn("rehydrate: inspect failed", "workflow_id", workflowID, "error", err)
		return
	}
	var res struct {
		State struct {
			PauseReason string `json:"pauseReason"`
		} `json:"state"`
	}
	if json.Unmarshal(raw, &res) != nil || res.State.PauseReason != stalePauseReason {
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
