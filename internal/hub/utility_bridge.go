// Utility bridge for ambient AI tasks.
//
// A dedicated long-lived kiro-cli bridge for cheap, stateless tasks
// (commit messages, chat rename, summaries, error explanations). Has
// its own ACP session so it never pollutes any chat's context. Uses
// CheapestModel(). Lazily started on first request, culled after 30
// minutes of inactivity (same as chat bridges).
//
// Callers serialize through a mutex; if the bridge is busy, the caller
// waits. This is acceptable because ambient tasks are not latency-
// critical enough to warrant parallelism.

package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cplieger/vibekit/internal/api"
)

// utilityBridge wraps a dedicated kiro-cli bridge for ambient tasks.
// The bridge is recycled after maxUtilityPrompts to prevent context
// accumulation from bleeding between unrelated tasks.
type utilityBridge struct {
	lastActiveAt  time.Time
	bridge        api.ACPBridge
	shutdownCtx   context.Context
	bridgeFactory api.ACPBridgeFactory
	hubModels     func() []api.SessionModel
	responseCh    chan utilityChunkPayload
	forwardDone   chan struct{}
	// Hooks-management plumbing (set by ensureUtility). runHookCommand runs
	// a runCommand hook's shell command for the executeHook A→C callback;
	// onHooksChanged broadcasts an hooks_changed SSE on _kiro/hooks/didChange;
	// lastHookRun captures the most recent run's output for the trigger
	// endpoint; expectingHookExec gates executeHook so a hook command runs
	// ONLY during a user-initiated "Run now" trigger. See hooks.go.
	runHookCommand func(context.Context, string, int) hookRunResult
	onHooksChanged func()
	// onGovernanceState captures the _kiro/governance/state notification the
	// utility bridge receives on session/new (its notifications bypass the main
	// dispatcher). Wired to hub.cacheGovernanceFromUtility so GET /api/governance
	// is warm with no chat open. nil = not wired (older tests).
	onGovernanceState func(json.RawMessage)
	lastHookRun       atomic.Pointer[hookRunResult]
	promptCount       int
	mu                sync.Mutex
	expectingHookExec atomic.Bool
	// enableHooks opts this bridge into KAS's v2 hook engine (StartOpts.
	// EnableHooks → _meta.kiro.hooks); always true for the utility bridge so
	// the hooks dashboard's list/setEnabled/triggerHook RPCs are available.
	enableHooks bool
	started     bool
}

const maxUtilityPrompts = 20

// utilityUpdateBase extracts the sessionUpdate kind discriminator.
// Local to utility_bridge to avoid coupling to translate's wire types.
type utilityUpdateBase struct {
	Kind api.ACPUpdateKind `json:"sessionUpdate"`
}

// utilityChunkPayload is the minimal shape the utility bridge needs
// from an agent_message_chunk notification: just the text content.
type utilityChunkPayload struct {
	Content struct {
		Text string `json:"text"`
	} `json:"content"`
}

// reset stops the bridge and clears the per-bridge prompt state so the
// next UtilityPrompt call starts a fresh bridge. Called from both
// mu-locked sites (UtilityPrompt recycle + error paths) and drainResponse
// (which always runs under ub.mu, held by UtilityPrompt). Callers must
// hold ub.mu themselves — reset does not lock.
func (ub *utilityBridge) reset() {
	ub.bridge.Stop()
	// Wait for forwardUtility to exit (it returns when NotifCh closes
	// after Stop). This ensures no goroutine leak on recycle. Safe against
	// a wedged forward because forwardChunk's send is non-blocking, so a
	// full responseCh can't park forward and stall this <-forwardDone.
	if ub.forwardDone != nil {
		<-ub.forwardDone
	}
	ub.started = false
	ub.promptCount = 0
	ub.responseCh = nil
	ub.forwardDone = nil
}

// drainResponseCh non-blockingly empties responseCh of any chunks a prior
// turn left behind. drainResponse returns on a short idle debounce, so a
// late chunk can land after it returns and sit in the buffer; the success
// path never clears the channel. Called at the top of UtilityPrompt (under
// ub.mu, before the turn's Call) so stale chunks can't prepend to this
// task's output. No-op when the bridge hasn't started (responseCh nil).
func (ub *utilityBridge) drainResponseCh() {
	if ub.responseCh == nil {
		return
	}
	for {
		select {
		case <-ub.responseCh:
		default:
			return
		}
	}
}

// newUtilityBridge constructs a utilityBridge with the initialization
// invariants explicit: started=false, promptCount=0, lastActiveAt=zero.
func newUtilityBridge(shutdownCtx context.Context, factory api.ACPBridgeFactory, hubModels func() []api.SessionModel) *utilityBridge {
	return &utilityBridge{
		bridgeFactory: factory,
		hubModels:     hubModels,
		shutdownCtx:   shutdownCtx,
	}
}

// UtilityPrompt sends a prompt to the utility bridge and returns the
// text response. Lazily starts the bridge on first call. Thread-safe;
// concurrent callers serialize. The bridge is recycled after
// maxUtilityPrompts to prevent context accumulation.
func (ub *utilityBridge) UtilityPrompt(ctx context.Context, prompt string) (string, error) {
	ub.mu.Lock()
	defer ub.mu.Unlock()

	// Recycle the bridge if it has served too many prompts. This
	// prevents context from prior tasks bleeding into new ones.
	if ub.started && ub.promptCount >= maxUtilityPrompts {
		slog.Info("utility bridge recycled", "prompts", ub.promptCount)
		ub.reset()
	}

	if !ub.started {
		if err := ub.start(ctx); err != nil {
			return "", fmt.Errorf("utility bridge start: %w", err)
		}
	}
	ub.lastActiveAt = time.Now()
	ub.promptCount++

	// Clear any residual chunks a prior turn left in responseCh so they
	// can't prepend to this task's output (drainResponse below would
	// otherwise read the leftover first).
	ub.drainResponseCh()

	resp, err := ub.bridge.Call(ctx, api.MethodPrompt, ub.sessionParams(map[string]any{
		"prompt": []map[string]any{api.TextBlock(utilitySystemPrompt + prompt)},
	}))
	if err != nil {
		// Bridge may be dead; reset so next call restarts.
		ub.reset()
		return "", fmt.Errorf("utility prompt: %w", err)
	}

	// Drain the forwarded response channel to consume the response chunks.
	return ub.drainResponse(ctx, resp)
}

// accountUsageRaw issues the account-level _kiro/account/getUsage request
// on the utility bridge and returns the raw JSON-RPC result. Lazily starts
// the bridge (same as UtilityPrompt) since getUsage is a bare C→A request
// that needs no model or tools — just a live acp session with valid auth
// (the getAccessToken callback supplies the profileArn getUsage requires).
// Thread-safe; serialises with UtilityPrompt through ub.mu.
func (ub *utilityBridge) accountUsageRaw(ctx context.Context) (json.RawMessage, error) {
	ub.mu.Lock()
	defer ub.mu.Unlock()

	if !ub.started {
		if err := ub.start(ctx); err != nil {
			return nil, fmt.Errorf("utility bridge start: %w", err)
		}
	}
	ub.lastActiveAt = time.Now()

	resp, err := ub.bridge.Call(ctx, methodKiroGetUsage, ub.sessionParams(nil))
	if err != nil {
		// Bridge may be dead; reset so the next call restarts it.
		ub.reset()
		return nil, fmt.Errorf("account usage call: %w", err)
	}
	if resp == nil {
		return nil, errors.New("account usage: nil response")
	}
	return resp.Result, nil
}

// knowledgeRaw issues a _kiro/knowledge subcommand request on the utility
// bridge and returns the raw JSON-RPC result. Unlike accountUsageRaw it does
// NOT inject the session id: omitting sessionId targets the workspace-global
// default store (a builtin-agent session would resolve to the same store, but
// omitting it is unconditional and matches the "knowledge is workspace-global"
// model). Lazily starts the bridge; serialises with UtilityPrompt through
// ub.mu. The knowledge subsystem embeds its own ONNX/MiniLM engine, so this
// needs only a live acp session with valid auth — no model or tools.
func (ub *utilityBridge) knowledgeRaw(ctx context.Context, params map[string]any) (json.RawMessage, error) {
	ub.mu.Lock()
	defer ub.mu.Unlock()

	if !ub.started {
		if err := ub.start(ctx); err != nil {
			return nil, fmt.Errorf("utility bridge start: %w", err)
		}
	}
	ub.lastActiveAt = time.Now()

	resp, err := ub.bridge.Call(ctx, methodKiroKnowledge, params)
	if err != nil {
		// Bridge may be dead; reset so the next call restarts it.
		ub.reset()
		return nil, fmt.Errorf("knowledge call: %w", err)
	}
	if resp == nil {
		return nil, errors.New("knowledge: nil response")
	}
	return resp.Result, nil
}

// specTaskStatusesRaw issues a _kiro/spec/getTaskStatuses request on the
// utility bridge and returns the raw JSON-RPC result. Unlike accountUsageRaw
// it does NOT inject a sessionId: getTaskStatuses is a stateless read that
// takes workspacePaths + tasksFilePath in params and needs no session context
// (verified live), so the caller supplies the full param set. Lazily starts
// the bridge; serialises with UtilityPrompt through ub.mu. The board works
// with no chat open because this runs on the always-available utility bridge.
func (ub *utilityBridge) specTaskStatusesRaw(ctx context.Context, params map[string]any) (json.RawMessage, error) {
	ub.mu.Lock()
	defer ub.mu.Unlock()

	if !ub.started {
		if err := ub.start(ctx); err != nil {
			return nil, fmt.Errorf("utility bridge start: %w", err)
		}
	}
	ub.lastActiveAt = time.Now()

	resp, err := ub.bridge.Call(ctx, methodV3SpecGetTaskStatuses, params)
	if err != nil {
		// Bridge may be dead; reset so the next call restarts it.
		ub.reset()
		return nil, fmt.Errorf("spec getTaskStatuses call: %w", err)
	}
	if resp == nil {
		return nil, errors.New("spec getTaskStatuses: nil response")
	}
	return resp.Result, nil
}

// policyRaw issues a _kiro/permissions/* request on the utility bridge and
// returns the raw JSON-RPC result. Injects the utility session id (these
// requests are session-scoped: list reads the session's resolved policy;
// explain simulates against it). The kiro/user/workspace scopes are
// workspace-global so the utility session's view is representative; the
// agent scope reflects the utility session's (default) agent. Lazily starts
// the bridge; serialises with UtilityPrompt through ub.mu. Read-only — pure
// policy inspection, no consent prompt (explain is a pure simulation;
// list is synchronous).
func (ub *utilityBridge) policyRaw(ctx context.Context, method string, extra map[string]any) (json.RawMessage, error) {
	ub.mu.Lock()
	defer ub.mu.Unlock()

	if !ub.started {
		if err := ub.start(ctx); err != nil {
			return nil, fmt.Errorf("utility bridge start: %w", err)
		}
	}
	ub.lastActiveAt = time.Now()

	resp, err := ub.bridge.Call(ctx, method, ub.sessionParams(extra))
	if err != nil {
		// Bridge may be dead; reset so the next call restarts it.
		ub.reset()
		return nil, fmt.Errorf("policy call %s: %w", method, err)
	}
	if resp == nil {
		return nil, errors.New("policy: nil response")
	}
	return resp.Result, nil
}

// sessionParams builds the ACP parameter map with the session ID
// injected. Mirrors command.SessionParams but works with the raw
// ACPBridge interface (which doesn't satisfy command.Bridge).
func (ub *utilityBridge) sessionParams(extra map[string]any) map[string]any {
	m := map[string]any{api.KeySessionID: ub.bridge.SessionID()}
	maps.Copy(m, extra)
	return m
}

// ensureStarted lazily starts the bridge without issuing a prompt, so a caller
// that only needs a live acp session (e.g. warming GET /api/governance, whose
// state arrives unsolicited on session/new) can trigger the session without
// spending a turn. No-op if already started. Serialises through ub.mu.
func (ub *utilityBridge) ensureStarted(ctx context.Context) error {
	ub.mu.Lock()
	defer ub.mu.Unlock()
	if ub.started {
		return nil
	}
	return ub.start(ctx)
}

func (ub *utilityBridge) start(ctx context.Context) error {
	bridge := ub.bridgeFactory()
	model := cheapestModel(ctx, ub.hubModels())

	// Start with the hub's shutdown context as the subprocess lifecycle
	// context. The per-request ctx is only used for CheapestModel above;
	// the bridge subprocess must outlive individual requests. Runs v3
	// (KAS) like every chat bridge (resolveAgentEngine, bridge_coord.go)
	// — without the engine it would default to v2, which vibekit can no
	// longer talk to.
	// The forward goroutine must be draining NotifCh BEFORE Start: on v3
	// (KAS) session/new blocks until the host answers the
	// _kiro/auth/getAccessToken and _kiro/terminal/shell_type requests,
	// which arrive on NotifCh and are answered by forward's
	// answerHostRequest. Channels are locals so a failed Start (bridge
	// stopped, NotifCh closed, goroutine exits) leaves no ub state behind.
	responseCh := make(chan utilityChunkPayload, 64)
	forwardDone := make(chan struct{})
	go ub.forward(bridge, bridge.NotifCh(), responseCh, forwardDone)

	if err := bridge.Start(ub.shutdownCtx, &api.StartOpts{Model: model, AgentEngine: resolveAgentEngine(), EnableHooks: ub.enableHooks}); err != nil {
		return err
	}
	ub.bridge = bridge
	ub.started = true
	// Zero the prompt counter on every (re)start so the recycle window is
	// measured from this fresh session. reset() zeroes it too, but the
	// cull path stops the bridge and sets started=false WITHOUT calling
	// reset(), so without this a culled-then-restarted bridge would carry
	// a stale count and recycle after fewer than maxUtilityPrompts prompts.
	ub.promptCount = 0
	ub.lastActiveAt = time.Now()

	ub.responseCh = responseCh
	ub.forwardDone = forwardDone

	slog.Info("utility bridge started", "model", model)
	return nil
}

// Stop stops the utility bridge if it is running. Thread-safe.
func (ub *utilityBridge) Stop() {
	ub.mu.Lock()
	started := ub.started
	forwardDone := ub.forwardDone
	ub.started = false
	ub.mu.Unlock()
	if started {
		ub.bridge.Stop()
		if forwardDone != nil {
			<-forwardDone
		}
		slog.Info("utility bridge stopped")
	}
}

// drainAndResetTimer stops t, drains its channel if it had already fired
// (so a stale tick can't fire spuriously), then rearms it to d.
func drainAndResetTimer(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}

// drainResponse reads the prompt response and collects assistant
// text from the forwarded response channel. Returns the concatenated text.
//
// kiro-cli does NOT emit a `session/update` sessionUpdate=="end_turn"
// notification: per ACP, the turn-end signal is the JSON-RPC RESPONSE
// to session/prompt (which ub.bridge.Call already awaited before this
// function runs). By the time we get here, the turn is already over;
// all we need to do is drain whatever chunks are still buffered in
// responseCh from before the response landed.
//
// Strategy: keep reading chunks until a short idle period elapses or
// ctx / the 60 s hard ceiling fire. The idle debounce handles the race
// between Call returning and the last chunks landing in the channel
// buffer. Caller-supplied ctx lets HTTP handlers cancel the operation
// without waiting for the ceiling.
func (ub *utilityBridge) drainResponse(ctx context.Context, resp *api.RPCResponse) (string, error) {
	if resp == nil {
		return "", errors.New("nil response")
	}

	const (
		idleDebounce  = 50 * time.Millisecond
		totalDeadline = 60 * time.Second
	)

	var text strings.Builder
	idle := time.NewTimer(idleDebounce)
	defer idle.Stop()
	timeout := time.NewTimer(totalDeadline)
	defer timeout.Stop()

	for {
		select {
		case <-ctx.Done():
			// Caller cancelled. Only reset the bridge if the
			// cancellation came from the request context (user
			// navigated away), not from shutdownCtx (graceful
			// shutdown). During shutdown, stopUtilityBridge handles
			// cleanup; an extra reset here would race with Stop().
			if ub.shutdownCtx.Err() == nil {
				ub.reset()
			}
			return text.String(), ctx.Err()
		case chunk, ok := <-ub.responseCh:
			if !ok {
				return text.String(), nil
			}
			text.WriteString(chunk.Content.Text)
			// Reset the idle timer on every chunk we accepted.
			drainAndResetTimer(idle, idleDebounce)
		case <-idle.C:
			// No chunks for idleDebounce → all chunks drained.
			return text.String(), nil
		case <-timeout.C:
			// Hard ceiling hit. Stop the bridge so a wedged turn
			// doesn't pollute the next UtilityPrompt caller with
			// leftover chunks interleaving with their own stream.
			ub.reset()
			return text.String(), errors.New("utility prompt timeout")
		}
	}
}

// forward is a dedicated goroutine that continuously drains the bridge's
// NotifCh, forwarding agent_chunk text to responseCh. Peer requests
// (msg.ID != nil) are answered via answerHostRequest — the utility bridge
// runs v3 (KAS) like every chat bridge, so it must vend the host-mediated
// auth token + shell type or session/new stalls, and (with EnableHooks) it
// answers the executeHook A→C callback. A _kiro/hooks/didChange notification
// fans out an hooks_changed SSE. All other notifications (usage stats, stale
// chunks) are discarded. This prevents NotifCh from filling up between calls,
// which would block readLoop and deadlock all pending Call waiters.
//
// bridge is passed explicitly (not read from ub.bridge) so a recycle that
// reassigns ub.bridge can't make this goroutine answer on the wrong pipe.
func (ub *utilityBridge) forward(bridge api.ACPBridge, notifCh <-chan *api.RPCResponse, responseCh chan<- utilityChunkPayload, done chan<- struct{}) {
	defer close(done)
	defer close(responseCh)
	for msg := range notifCh {
		switch {
		case msg.ID != nil:
			ub.answerHostRequest(bridge, msg)
		case msg.Method == methodKiroHooksDidChange:
			if ub.onHooksChanged != nil {
				ub.onHooksChanged()
			}
		case msg.Method == methodV3Governance:
			if ub.onGovernanceState != nil {
				ub.onGovernanceState(msg.Params)
			}
		default:
			forwardChunk(msg, responseCh)
		}
	}
}

// forwardChunk forwards an agent_message_chunk's text to responseCh, ignoring
// every other notification. Split out of forward to keep it under the
// cognitive-complexity gate.
func forwardChunk(msg *api.RPCResponse, responseCh chan<- utilityChunkPayload) {
	if msg.Method != api.MethodSessionUpdate || msg.Params == nil {
		return
	}
	var base utilityUpdateBase
	if json.Unmarshal(msg.Params, &base) != nil || base.Kind != api.ACPUpdateAgentChunk {
		return
	}
	var chunk utilityChunkPayload
	if json.Unmarshal(msg.Params, &chunk) == nil {
		// Non-blocking send: if responseCh (buffer 64) is full — a wedged
		// or already-drained turn whose leftover chunks nobody reads — drop
		// the chunk instead of blocking here forever. A blocked forward
		// never loops back to observe notifCh closing, so reset()'s
		// <-forwardDone (taken under ub.mu) would never return and the whole
		// utility subsystem would deadlock holding ub.mu. Post-turn leftover
		// chunks are noise; dropping them is correct.
		select {
		case responseCh <- chunk:
		default:
		}
	}
}

// answerHostRequest answers the v3 (KAS) host-mediated requests the utility
// bridge receives. getAccessToken + shell_type are on the session-creation
// critical path (session/new stalls without them). executeHook (only when
// EnableHooks) runs a runCommand hook's command for a user-initiated trigger.
// Unknown peer requests are ignored (logged).
func (ub *utilityBridge) answerHostRequest(bridge api.ACPBridge, msg *api.RPCResponse) {
	ctx := context.Background()
	switch msg.Method {
	case methodKiroGetAccessToken:
		result, err := kiroAccessTokenResult(ctx)
		if err != nil {
			slog.Error("utility bridge v3 auth: token unavailable", "error", err)
			_ = bridge.Respond(ctx, *msg.ID, nil, err)
			return
		}
		_ = bridge.Respond(ctx, *msg.ID, result, nil)
	case methodKiroShellType:
		_ = bridge.Respond(ctx, *msg.ID, kiroShellTypeResult(), nil)
	case methodKiroHooksExecuteHook:
		ub.answerExecuteHook(bridge, msg)
	default:
		slog.Warn("utility bridge: unexpected peer request, ignoring", "method", msg.Method, "id", *msg.ID)
	}
}

// answerExecuteHook handles the security-sensitive _kiro/hooks/executeHook A→C
// request: KAS asks the client to run a runCommand hook's shell command. It is
// answered ONLY while a user-initiated "Run now" trigger is in flight
// (expectingHookExec) — the utility bridge issues no agent tool calls, so no
// hook ever auto-fires here; a stray callback is refused (cancelled). The
// command is run via the hub's runHookCommand (workDir cwd, bounded timeout,
// capped + sanitized output); its result is captured for the trigger endpoint.
func (ub *utilityBridge) answerExecuteHook(bridge api.ACPBridge, msg *api.RPCResponse) {
	ctx := context.Background()
	var p struct {
		Command  string `json:"command"`
		HookName string `json:"hookName"`
		Timeout  int    `json:"timeout"`
	}
	if msg.Params != nil {
		_ = json.Unmarshal(msg.Params, &p)
	}
	if !ub.expectingHookExec.Load() || ub.runHookCommand == nil {
		slog.Warn("utility bridge: unexpected hook executeHook, refusing", "hook", p.HookName)
		_ = bridge.Respond(ctx, *msg.ID, map[string]any{"cancelled": true}, nil)
		return
	}
	res := ub.runHookCommand(ctx, p.Command, p.Timeout)
	ub.lastHookRun.Store(&res)
	_ = bridge.Respond(ctx, *msg.ID, map[string]any{"output": res.Output, keyExitCode: res.ExitCode}, nil)
}

// hooksRaw issues a non-session _kiro/hooks request (list / setEnabled) on the
// utility bridge and returns the raw JSON-RPC result. Lazily starts the bridge;
// serialises with UtilityPrompt through ub.mu. Mirrors knowledgeRaw.
func (ub *utilityBridge) hooksRaw(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	ub.mu.Lock()
	defer ub.mu.Unlock()

	if !ub.started {
		if err := ub.start(ctx); err != nil {
			return nil, fmt.Errorf("utility bridge start: %w", err)
		}
	}
	ub.lastActiveAt = time.Now()

	resp, err := ub.bridge.Call(ctx, method, params)
	if err != nil {
		ub.reset()
		return nil, fmt.Errorf("hooks call %s: %w", method, err)
	}
	if resp == nil {
		return nil, errors.New("hooks: nil response")
	}
	return resp.Result, nil
}

// triggerRunCommandHook triggers a runCommand hook and returns the captured
// command output. It sets expectingHookExec around the triggerHook Call so the
// executeHook callback (which fires DURING the Call, handled by the forward
// goroutine) is allowed to run the command; the result lands in lastHookRun.
// approved:true — the user's "Run now" click is the consent, so KAS skips its
// own per-command approval round-trip. Serialises through ub.mu.
func (ub *utilityBridge) triggerRunCommandHook(ctx context.Context, hookID, hookName, command string) (hookRunResult, error) {
	ub.mu.Lock()
	defer ub.mu.Unlock()

	if !ub.started {
		if err := ub.start(ctx); err != nil {
			return hookRunResult{}, fmt.Errorf("utility bridge start: %w", err)
		}
	}
	ub.lastActiveAt = time.Now()
	ub.lastHookRun.Store(nil)
	ub.expectingHookExec.Store(true)
	defer ub.expectingHookExec.Store(false)

	resp, err := ub.bridge.Call(ctx, methodKiroHooksTriggerHook, ub.sessionParams(map[string]any{
		"hookId":         hookID,
		"hookName":       hookName,
		"hookActionType": actionRunCommand,
		"command":        command,
		"approved":       true,
	}))
	if err != nil {
		ub.reset()
		return hookRunResult{}, fmt.Errorf("hooks trigger: %w", err)
	}
	// A success:false reply (e.g. session gone) is a real failure; a
	// success:true with a non-zero command exit is a valid "ran, failed"
	// outcome carried in lastHookRun.
	if res := parseHookResult(resp.Result); !res.Success {
		return hookRunResult{}, hookTriggerError(res)
	}
	if run := ub.lastHookRun.Load(); run != nil {
		return *run, nil
	}
	return hookRunResult{}, nil
}

// stopUtilityBridge stops the utility bridge if it exists.
//
// The h.bridge.utility field read + nil is guarded by h.lifecycle.mu so it
// serialises with cullIdleBridgesOnce, which reads the same field under
// that lock (snapshot-and-release). Without the lock this write raced the
// cull's read. Only called from Shutdown, where inflight.Wait() has already
// returned, so no concurrent UtilityPrompt holds ub.mu and the subsequent
// ub.Stop() can't contend with an in-flight prompt.
func (h *Hub) stopUtilityBridge() {
	h.lifecycle.mu.Lock()
	ub := h.bridge.utility
	h.bridge.utility = nil
	h.lifecycle.mu.Unlock()
	if ub == nil {
		return
	}
	ub.Stop()
}

// --- Model selection (inlined from internal/models) ---
