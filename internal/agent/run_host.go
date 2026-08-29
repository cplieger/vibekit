package agent

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
// workflow — subagents or a `parallel` node — never by launching the same
// recipe twice; the rule is also what lets the Workflows row carry a Run ⇄
// Cancel button that unambiguously names one run.
var errRecipeBusy = errors.New("this recipe already has a live run")

// Launch starts one parentless run of the recipe with the given source key
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
func (rs *Runs) Launch(ctx context.Context, source string, inputs map[string]string) (id, name string, err error) {
	return rs.launch(ctx, source, inputs, manualLaunch())
}

// LaunchScheduled launches a run on behalf of a schedule, marking it
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
		// recipe's next slot, and the slot is only knowable once the recipe is
		// resolved. Without this the run took the ceiling as its whole bound and
		// refused every slot underneath it (run_lease.go manualSlot).
		o.slotAt = rs.manualSlot(recipe.Source)
	}
	if bErr := rs.recipeIdle(cctx, recipe.Name); bErr != nil {
		return "", "", bErr
	}

	// The run's own process. Started OUTSIDE the manager: the map key is the
	// workflow id, which only `new`'s reply knows. Call replies ride the
	// bridge's readLoop (matched by request id), not NotifCh, so the forward
	// goroutine can attach after `new` — NotifCh is buffered, and whatever
	// session/new-time notifications land there are drained once Forward
	// starts.
	bridge := rs.bridges.factory()
	// Presets carry the active security profile, same as a chat bridge. A run
	// executes agent work with the same tools, so a run at a different posture than
	// the chats beside it would be a gap nobody selected — and the unattended path
	// is where that matters most, since nobody is watching to answer a prompt the
	// profile was meant to remove. ToolSearch and Knowledge ride along for exactly
	// the same reason: a workflow step reaching for a knowledge base or an MCP tool
	// is the same agent doing the same work.
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
	// The wall clock, for every run this path launches — manual and scheduled
	// alike (run_bounds.go). After invoke, so a run that never started leaves no
	// timer, and idempotent with the `run_start` frame's own arm.
	rs.armDeadline(cctx, wfID)
	slog.Info("workflow run launched", "workflow_id", wfID, "recipe", recipe.Name)
	return wfID, recipe.Name, nil
}

// Cancel asks a run to stop, on the USER's behalf.
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
// `run_complete` the cancel eventually produces closes it (see dispatch),
// because the owning process must live to the node boundary to certify the
// cancelled state.
func (rs *Runs) cancelRPC(ctx context.Context, workflowID string) error {
	return rs.control(ctx, workflowID, methodKiroWorkflowCancel, "workflow cancel call")
}

// Delete removes a run and everything either side keeps about it, on the
// USER's behalf. It is the History row's delete, and the only run verb that is
// not recoverable.
//
// Order matters and is the opposite of cancel's. KAS's delete cancels a
// non-terminal run itself before removing the run directory, so vibekit must NOT
// pre-cancel: taking the termination claim first would record an end reason for a
// row that is about to stop existing, and `Cancel`'s own comment explains why a
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
//
// The wording says what is missing (a bridge these verbs can address) rather than
// claiming the run is not running here. It also names cancel as the surviving verb
// rather than claiming it is the only possible one -- KAS would rehydrate for
// resume too (`_kiro/workflow/resume` loads from disk), so a restart-orphaned
// paused run is dead-ended by vibekit's missing carrier, not by the protocol.
//
// It no longer says an agent-launched run is ALWAYS in this state: hostBridge
// resolves the launching chat's bridge, so such a run is reachable whenever that
// chat has one. What is left is the genuinely carrier-less case, and the message
// names the one remedy a user has for it.
var errRunNotHosted = errors.New(
	"this run has no live bridge on this server, so it cannot be paused or resumed from here; " +
		"cancel still works. A run from before the last restart is in this state, " +
		"and so is an agent-launched run whose chat is closed -- open that chat to bring it back",
)

// Pause asks a running run to stop at its next node boundary, keeping its
// state resumable. KAS sets `control.pauseRequested` and the in-flight node runs
// to completion, so the reply confirms the ASK rather than a paused state.
//
// Requires the run's own bridge: KAS's pause reaches `registry.require`, which
// throws for a run that is not in the live in-memory registry and does NOT
// rehydrate from disk the way cancel, resume and retry do.
func (rs *Runs) Pause(ctx context.Context, workflowID string) error {
	return rs.hostedControl(ctx, workflowID, methodKiroWorkflowPause)
}

// Resume re-drives a paused run.
//
// Re-arms the wall clock, because the pause parked it (run_bounds.go): a resumed
// run is executing again and gets a FRESH budget rather than the remainder of the
// one it was holding when it parked. `run_start` re-fires on resume and arms too;
// whichever lands first wins.
func (rs *Runs) Resume(ctx context.Context, workflowID string) error {
	err := rs.hostedControl(ctx, workflowID, methodKiroWorkflowResume)
	if err == nil {
		rs.armDeadline(ctx, workflowID)
	}
	return err
}

// Retry re-hosts a finished run and resets its failed work.
//
// This is the re-hosting case, and it exists because retry's legality window and
// vibekit's bridge lifetime are disjoint: retry is legal only from `failed` or
// `aborted`, and `closeFinishedBridge` tears a run's bridge down on exactly
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
func (rs *Runs) Retry(ctx context.Context, workflowID string) error {
	if workflowID == "" {
		return errors.New("missing workflow id")
	}
	chatID := runChatID(workflowID)

	// An already-hosted run needs no re-hosting: send on the bridge it has. This
	// is not the expected path (retry's window implies a closed bridge) but a run
	// aborted without a terminal frame can still be registered.
	if sb := rs.bridges.get(chatID); sb != nil {
		recipe := rs.recipeOf(ctx, workflowID)
		_, err := sb.Call(ctx, methodKiroWorkflowRetry, map[string]any{keyWorkflowID: workflowID})
		if err == nil {
			// Only on success, and in this order: a retry KAS refused re-drove
			// nothing, so the run's previous terminal reason is still the truth
			// about it and its row must keep saying so.
			rs.rearmRetried(ctx, workflowID, recipe)
		}
		return err
	}

	cctx, cancel := context.WithTimeout(ctx, launchTimeout)
	defer cancel()

	// The recipe name, read off KAS's run list BEFORE anything is re-driven. The
	// list is the only place a re-hosted run's recipe is available to this process,
	// and it has to be read while the run is still listed under its pre-retry
	// state rather than after the lease is needed.
	recipe := rs.recipeOf(cctx, workflowID)

	bridge := rs.bridges.factory()
	if err := bridge.Start(cctx, &vibekit.StartOpts{
		Lifetime:    rs.lifecycle.shutdownCtx,
		AgentEngine: resolveAgentEngine(),
		Presets:     securityPresets(cctx, rs.lifecycle.configDir),
		ToolSearch:  toolSearchEnabled(cctx, rs.lifecycle.configDir),
		Knowledge:   knowledgeEnabled(cctx, rs.lifecycle.configDir),
		Memory:      memoryEnabled(cctx, rs.lifecycle.configDir),
	}); err != nil {
		return fmt.Errorf("retry bridge start: %w", err)
	}
	// Register BEFORE the call, for the same reason Launch does: retry's first
	// lifecycle frame follows it immediately, and a frame arriving before the map
	// entry would find no bridge to answer through.
	rs.bridges.insert(chatID, &sharedBridge{bridge: bridge, state: bridgeIdle})
	go rs.coord.Forward(chatID, bridge)

	// The lease before the verb, exactly as a fresh launch grants between `new`
	// and `invoke`: retry's own `run_start` can arrive before the call returns, and
	// a lease minted from that frame would carry the frame's name rather than the
	// list's and would depend on the frame having one at all. Granting first makes
	// the retried run's envelope independent of that race.
	minted := false
	if _, held := rs.lease(workflowID); !held {
		rs.grantLease(cctx, workflowID, recipe, manualLaunch())
		minted = true
	}

	if _, err := bridge.Call(cctx, methodKiroWorkflowRetry, map[string]any{keyWorkflowID: workflowID}); err != nil {
		// Nothing is executing: tear the bridge down rather than leaving a
		// process hosting a run it failed to restart, and give back a lease this
		// call minted for a run that never re-drove.
		if minted {
			rs.releaseLease(cctx, workflowID)
		}
		rs.coord.CloseBridge(chatID)
		return fmt.Errorf("workflow retry: %w", err)
	}
	// A fresh clock and a clean row, both only now that the retry has landed: the
	// run is executing again, so its recorded termination is no longer a fact
	// about it — and the client deliberately lets a recognised end_reason outrank
	// live status, so leaving it would render the running retry as aborted.
	rs.rearmRetried(cctx, workflowID, recipe)
	slog.Info("workflow run retried", "workflow_id", workflowID, "recipe", recipe)
	return nil
}

// recipeOf reads a run's recipe NAME off KAS's own run list.
//
// The list is the only place this process can learn the recipe of a run it did not
// launch: a retry rehydrates a run KAS holds on disk, so vibekit never saw its
// `new`. The name it reports is the same string the single-run rule compares
// against, which is what makes a lease keyed with it useful rather than decorative.
//
// Best-effort — "" when the list cannot be read or does not carry the run — because
// a retry must not fail over a name. A nameless lease still bounds the run; it is
// only invisible to the single-run rule's comparison.
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

// SetStepStatus marks an in-flight step completed or failed so a wedged run
// can advance.
//
// Requires the run's own bridge and does NOT re-host, which is the honest
// difference from Retry: this verb acts on a step that is IN FLIGHT, so a run
// with no live bridge has no in-flight step to mark and the request is a
// mistake rather than something to rehydrate for.
func (rs *Runs) SetStepStatus(ctx context.Context, workflowID, nodeID, status string) error {
	if nodeID == "" {
		return errors.New("missing node id")
	}
	if status != runStepCompleted && status != runStepFailed {
		return fmt.Errorf("step status must be %q or %q", runStepCompleted, runStepFailed)
	}
	sb := rs.bridges.get(runChatID(workflowID))
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

// hostedControl issues a verb that must run on the run's OWN bridge, and
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
// The remaining cost is stated rather than hidden. These verbs reach a run only
// while SOME live bridge in this process holds its registry entry, so a run
// orphaned by a container restart can still only be cancelled: every bridge died
// with the process, and nothing here re-hosts (retry does, and its legality
// window is the two statuses where that is the only option).
//
// That is why the 409 says "no live bridge" rather than naming a server: the run
// may well be executing, on a connection these verbs cannot address.
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
// Two ways a run is hosted, because there are two ways one starts, and until
// 2026-08-26 this only knew the first. A run vibekit LAUNCHED has a bridge under
// its synthetic `run:<id>` chat id. An AGENT-launched run has none and never will:
// KAS parents it on the calling chat's session, so the LAUNCHING CHAT's bridge is
// the process that registered it. That bridge is therefore not a fallback carrier,
// it is where the verb belongs — which is the whole difference from the utility
// bridge this function refuses to fall back to.
//
// Before this, every pause and resume against an agent-launched run answered 409
// no matter what state the run was in, so the population with no recovery door was
// exactly the population an agent creates.
//
// Costs one `workflow/list` round trip on the agent-launched path, and only there.
// That is the same trade `runRoutes.status` already takes for its per-click
// `inspect`: these are deliberate user actions, not a hot path.
func (rs *Runs) hostBridge(ctx context.Context, workflowID string) *sharedBridge {
	if sb := rs.bridges.get(runChatID(workflowID)); sb != nil {
		return sb
	}
	parent := rs.parentSession(ctx, workflowID)
	if parent == "" {
		return nil
	}
	// Only LIVE bridges are candidates, which is why this walks the bridge map
	// rather than the chat store: a chat with no bridge is no carrier, so resolving
	// its id would answer a question nobody can act on.
	for chatID, sb := range rs.bridges.all() {
		chat, ok := rs.chats.Get(ctx, chatID)
		if !ok {
			continue
		}
		// The whole CHAIN, not the current session id. A chat changes session on a
		// failed session/load, a model-switch fallback and empty-turn recovery, so
		// a run launched before such a change is parented on a retired id — and
		// matching only the live one would strand exactly the runs a rough session
		// produced.
		if slices.Contains(chat.SessionChain(), parent) {
			return sb
		}
	}
	return nil
}

// parentSession reports the KAS session that launched a run, or "" when the run is
// parentless, unknown, or the inventory cannot be read.
//
// `workflow/list` is the only source: `inspect` does not carry the field, and the
// notification that does arrives once at run_start and is not retained.
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
// Only cancel qualifies. A run launched by this process has a live bridge that
// owns the run's registry entry, and issuing the verb there lets KAS act on the
// in-memory record. The utility fallback is what makes cancel work on a run this
// process did not launch — a TUI-launched run, or one from before a restart —
// because cancel rehydrates from disk and then only WRITES state; it never
// re-drives execution, so a text-only session is a sufficient carrier for it.
// The verbs that do execute use hostedControl instead.
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
//   - session/update is PROJECTED, not dropped, and not into a transcript.
//     A step's content on a CHAT's bridge feeds that chat's transcript
//     (attributed, see translate); here there is no transcript, and opening a
//     buffer under the synthetic id would create the phantom assistant message
//     on a chat that must never exist. So the frames go out as run-scoped
//     `run_step` events instead, straight to the clients watching this run, and
//     the run card renders them into the step rows it already has. See
//     translate/workflow_step_content.go — including which kinds it declines.
//     They were DROPPED until 2026-08, which left exactly the runs whose only
//     surface is the run tab as the ones whose steps could not be watched: the
//     tab had a step's captured output once it finished and nothing at all while
//     it worked.
func (rt *Runtime) dispatch(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	if msg.ID != nil {
		rt.dispatchRequest(ctx, chatID, msg)
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
// LITERAL, deliberately: at least five sites set pauseReason and only this one
// means "the process died under it"; every other reason (a deliberate pause, a
// policy stop, a step waiting for input, a torn plan) must be left alone.
const stalePauseReason = "Interrupted by agent restart; the previously running step was paused for resume."

// The pause reasons KAS records when a run stopped for a cause NOBODY CHOSE,
// beside the reconcile literal above. Read off @kiro/agent's own step-error
// classification (`acp-server.js`: `isTransientClass` splits an `interruption`
// from a `transient-network` code from a transient model 5xx), and each one says
// in its own text that the run is meant to continue.
//
// THESE ARE THE RESUME SIDE ONLY, and the asymmetry with stalePauseReason is the
// point rather than an oversight. That literal answers "did the owning process
// die", which is what licenses the orphan sweep to CANCEL a run; this set answers
// the wider "was this pause involuntary", which only ever licenses a RESUME. The
// consequences are not symmetric: resuming a run that did not need it costs
// nothing, cancelling one that did not need it costs the work. So the wider set
// goes to the safe verb and the narrow one keeps the destructive verb, and
// `clearOrphaned`'s standing instruction not to widen its predicate is obeyed.
//
// A network pause is matched by PREFIX because KAS interpolates the error code
// into it (`Transient connection error (EAI_AGAIN); …`). That is a mechanical
// parse over a closed set of literals KAS constructs, not a keyword sweep over
// free-form text: the prefix carries the whole distinguishing phrase, so it
// cannot reach any of the reasons that must be left alone — a step waiting on a
// human (`Step requested user input via send_message.`, `Step '<id>' is waiting
// for the next user message.`), a policy stop (`Repeat '<id>' reached
// maxIterations.`), or a recorded failure (`Run failed: …`).
const (
	interruptedPauseReason  = "Step interrupted (agent shutdown or connection reset); will resume."
	modelServicePauseReason = "Transient model service error (service 5xx/throttling); will resume."
	networkPausePrefix      = "Transient connection error ("
)

// resumablePause reports whether a pause reason means the run stopped for a cause
// nobody chose, and is therefore vibekit's to resume unasked.
//
// Pure and reason-only, so the table test is the reason list rather than a set of
// RPC fixtures; the status and identity conditions live with the caller that
// reads them off one reply.
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
// This is the recovery model for agent-launched runs, and it is why there is
// no Resume button anywhere: a chat's bridge dying pauses its runs (KAS
// reconciles on the next read), and the user's next message respawns the
// bridge — so resuming here makes the run heal WITH the chat, the same way the
// chat itself heals. KAS's own startupRecovery auto-resumes only WATCH-parked
// runs; a step-parked one waits for a verb, and this is the verb.
//
// Scoped twice, both load-bearing: to THIS chat's session chain (never
// resumeAll, which would sweep runs other chats or the TUI paused on purpose),
// and to the involuntary pause reasons (a deliberately-paused run, one waiting on
// a human, and one stopped by policy all stay paused).
//
// It used to gate on the restart literal ALONE, which left every other
// involuntary pause with no door at all: the sweep skipped it, the UI offers an
// agent-launched run no controls, retry is illegal from `paused`, and no agent
// tool reaches the verb. Measured 2026-08-26 on the live workspace: 6 of 9 paused
// runs carried `Transient connection error (EAI_AGAIN)`, which KAS's own message
// says can be resumed, and nothing in this app would ever have resumed them.
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
		if r.Status != "paused" || !chain[r.ParentSessionID] {
			continue
		}
		rs.resumeIfInterrupted(ctx, chatID, r.WorkflowID)
	}
}

// maxAutoHeals bounds the automatic resumes one run may spend between two pieces
// of progress. Three, because the failure it guards against is a fault that is not
// clearing: a fourth attempt against a dead network tells nobody anything the
// third did not, and leaving the run paused hands it to the chat-rehydration path,
// where a human is present.
const maxAutoHeals = 3

// healBaseDelay is the wait before the FIRST automatic resume, doubling per
// attempt (5s, 10s, 20s).
//
// Not zero, and the delay is the whole point: an immediate retry against the fault
// that just paused the run spends an attempt to learn nothing. It also lets a
// deliberate cancel land first, which is why the callback re-reads the run's state
// rather than trusting the frame that scheduled it.
//
// A `var` so a test can drive the whole path in milliseconds instead of waiting
// out a real backoff — the delay-constant seam this fleet already uses for the
// same reason. Never reassigned in production.
var healBaseDelay = 5 * time.Second

// healPaused resumes a run KAS has just parked for a reason nobody chose.
//
// This is the trigger the recovery model was missing. `resumeInterruptedRuns` runs
// off `onSessionRehydrated`, so it only ever fires when a chat's bridge comes BACK
// — which covers a restart and covers nothing else. A run that pauses on a
// transient network error while its chat's bridge is still alive had no trigger at
// all and waited for the next bridge respawn: a restart, a recreate, or an idle
// cull followed by a message. Measured on the live workspace, that is the state
// most stranded runs were in.
//
// The frame is the ideal signal and it was already arriving: KAS emits
// `_kiro/workflow/paused` with `{workflowId, pauseReason}` on the LAUNCHING CHAT's
// bridge the moment it parks a run, so the reason needs no round trip and the
// timing is exact. No polling, no timer per chat, no per-message RPC.
//
// It runs AFTER `next`, so the client renders the pause before anything undoes it.
// A reader watching the run sees what happened rather than a state that silently
// never appeared.
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
		// The timer handle is deliberately NOT tracked, unlike the deadline timers
		// this type keeps in `bounds.timers`. Those must be stoppable because a
		// fired one CANCELS a run; this one re-reads the run's state first and does
		// nothing unless it is still involuntarily paused, so a run cancelled,
		// resumed or finished in the meantime costs one wasted inspect rather than a
		// wrong action. That guard is what makes an untracked timer safe here.
		time.AfterFunc(delay, func() {
			hctx, cancel := rs.lifecycle.derivedContext()
			defer cancel()
			rs.resumeIfInterrupted(hctx, chatID, f.WorkflowID)
		})
	}
}

// pauseFrame is the two fields the heal reads off `_kiro/workflow/paused`.
//
// Its own minimal decode rather than a share of `lifecycleFrame`, which carries
// the status and name the BOUNDS read and no pause reason at all. Two readers of
// one frame asking different questions is exactly the case that comment already
// makes for keeping the bounds' decode separate from the translator's.
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
// Progress is the only honest evidence that whatever paused the run has cleared,
// and without this the budget is per-process: a job running for hours would spend
// its three attempts on one morning blip and then have no automatic recovery left
// for an unrelated one that afternoon.
func (rs *Runs) healProgress(
	next func(context.Context, vibekit.ChatID, *vibekit.RPCResponse),
) func(context.Context, vibekit.ChatID, *vibekit.RPCResponse) {
	return func(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
		if id := workflowIDOfFrame(msg); id != "" {
			rs.clearHeals(id)
		}
		next(ctx, chatID, msg)
	}
}

// resumeIfInterrupted inspects one paused run and resumes it when its pause
// reason means the stop was involuntary. Resumed on the CHAT's bridge, so the
// chat's process becomes the run's owner again — which is where an
// agent-launched run's frames belong.
func (rs *Runs) resumeIfInterrupted(ctx context.Context, chatID vibekit.ChatID, workflowID string) {
	// The orphan sweep's predicate is the NARROWER `restartPaused`, deliberately:
	// it cancels, and only "the owning process died" licenses that. This one
	// resumes, so it reads the wider involuntary set. See resumablePause for why
	// the two must not be one function.
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
// The tab-close half of the run lifecycle (the teardown close_tab runs for a
// chat tab, command.closeChatTeardown): a run is
// durable state, so killing the chat's process only PAUSES it — it must be told
// to cancel, per run, while the owning bridge is still alive to say it.
//
// It BEGINS with a record read, so it no-ops on a deleted chat. The delete
// grades — delete_chat's teardown, which runs before the record goes, and the
// close escalation's, which runs after — resolve the chain themselves and call
// CancelForSessions, which is the half that does the work.
func (rs *Runs) CancelForChat(ctx context.Context, chatID vibekit.ChatID) {
	chat, ok := rs.chats.Get(ctx, chatID)
	if !ok {
		return
	}
	rs.CancelForSessions(ctx, chatID, chat.SessionChain())
}

// CancelForSessions cancels every non-terminal run launched by one of these
// sessions. The chain-shaped half of CancelForChat: it reads no chat record, so
// it works from a CAPTURED chain after the record is gone — the retention-off
// close escalation's case, where the cancel runs post-commit. chatID is carried
// for log attribution only.
//
// Best-effort throughout: a run list failure or a per-run cancel failure is
// logged and the close proceeds. Blocking a tab close on a workflow RPC would
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
