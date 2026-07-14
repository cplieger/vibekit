package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/buffer"
	"github.com/cplieger/vibekit/internal/command"
	"github.com/cplieger/vibekit/internal/push"
	"github.com/cplieger/vibekit/internal/settings"
)

// BridgeCoordinator encapsulates bridge lifecycle management: creating,
// loading, priming, forwarding notifications, model switching, and
// turn finalization. Hub delegates to this coordinator, reducing the
// hub's role to HTTP/SSE dispatch.
type BridgeCoordinator struct {
	bridge         *bridgePlane
	chatStore      api.ChatStore
	broadcast      func(ctx context.Context, e api.ServerEvent)
	translateEvent func(chatID api.ChatID, msg *api.RPCResponse)
	supervised     *supervisedState
	push           api.PushService
	mcpConfig      api.MCPConfig
	mcpRegistry    *mcpRegistry
	lifecycle      *lifecyclePlane
	preBridgeSpawn func(context.Context)
	// flushPending rejects every outstanding pending op for a chat and
	// broadcasts the cleared events. Injected (from h.flushPendingForChat)
	// so Forward can flush a chat's staged writes when its bridge exits,
	// without the coordinator importing the full Hub.
	flushPending func(context.Context, api.ChatID, api.ClearReason)
	// agentEngine is the kiro-cli agent engine for every bridge this
	// coordinator spawns. Hard-pinned to v3 (KAS) by resolveAgentEngine;
	// vibekit is v3-only.
	agentEngine string
}

// newBridgeCoordinator constructs a BridgeCoordinator from the Hub's
// fields. Called once from NewHub after all options are applied.
func newBridgeCoordinator(h *Hub) *BridgeCoordinator {
	return &BridgeCoordinator{
		bridge:         h.bridge,
		chatStore:      h.chatStore,
		broadcast:      h.Broadcast,
		translateEvent: h.translateACPEvent,
		supervised:     h.perm.supervised,
		push:           h.push,
		mcpConfig:      h.mcpConfig,
		mcpRegistry:    h.mcpRegistry,
		lifecycle:      h.lifecycle,
		preBridgeSpawn: h.preBridgeSpawn,
		flushPending:   h.flushPendingForChat,
		agentEngine:    resolveAgentEngine(),
	}
}

// resolveAgentEngine returns the kiro-cli agent engine, hard-pinned to v3
// (KAS). vibekit is v3-only: the v2 (_kiro.dev/*) wire and its handlers
// were removed, so a stray KIRO_AGENT_ENGINE=v1/v2 is deliberately
// ignored — honoring it would launch a legacy engine vibekit can no
// longer talk to (session/new stalls, every turn fails). v3 requires the
// host to answer _kiro/auth/getAccessToken + _kiro/terminal/shell_type
// (internal/hub/bridge_v3_auth.go). The v2→v3 wire comparison is in
// kiro-cli-research.md.
func resolveAgentEngine() string {
	return api.AgentEngineV3
}

// GetOrCreateBridge returns an existing bridge for chatID, or creates one.
// Concurrent callers for the same chatID coalesce via singleflight.
//
//nolint:revive // unexported-return: sharedBridge is package-internal; callers within hub use the methods on it. Exporting would leak ACP wiring outside the hub package.
func (bc *BridgeCoordinator) GetOrCreateBridge(ctx context.Context, chatID api.ChatID, modelOverride string) (*sharedBridge, error) {
	// Fast path: bridge already exists.
	if sb := bc.bridge.mgr.get(chatID); sb != nil {
		return sb, nil
	}

	// Coalesce concurrent spawn attempts for the same chatID+model.
	// Including the model override in the key ensures callers with
	// different model parameters don't coalesce onto the wrong bridge.
	sfKey := string(chatID) + "\x00" + modelOverride
	v, err, _ := bc.bridge.mgr.spawnSF.Do(sfKey, func() (any, error) {
		return bc.spawnBridge(ctx, chatID, modelOverride)
	})
	if err != nil {
		return nil, err
	}
	b, _ := v.(*sharedBridge)
	return b, nil
}

// spawnBridge creates (or returns the just-created) bridge for chatID.
// It runs inside the singleflight so concurrent callers coalesce. It
// resolves the model (the override beats the chat's stored value),
// tries session/load when an ACP session id is stored, and otherwise
// starts a fresh session/new. On any start failure it rolls the
// half-registered bridge back out of the map and returns the error.
func (bc *BridgeCoordinator) spawnBridge(ctx context.Context, chatID api.ChatID, modelOverride string) (*sharedBridge, error) {
	// Double-check after winning the singleflight race.
	sb, existed := bc.bridge.mgr.getOrInsert(chatID)
	if existed {
		return sb, nil
	}

	setupErr := func(err error) error {
		bc.bridge.mgr.removeIfSame(chatID, sb)
		sb.state = bridgeIdle
		return err
	}

	chat, exists := bc.chatStore.Get(ctx, chatID)
	if !exists {
		return nil, setupErr(fmt.Errorf("chat %s not found", chatID))
	}

	model := chat.Model
	if modelOverride != "" && modelOverride != modelAuto {
		model = modelOverride
	}

	var mcpServers []map[string]any
	if bc.mcpConfig != nil {
		mcpServers = bc.mcpConfig.ACPServers(ctx)
	}

	if bc.preBridgeSpawn != nil {
		bc.preBridgeSpawn(ctx)
	}

	if chat.ACPSessionID != "" {
		if bc.tryLoadSession(ctx, chatID, sb, chat.ACPSessionID, model, mcpServers) {
			return sb, nil
		}
	}

	// EnableHooks:true opts chat bridges into KAS's v2 hook engine
	// (_meta.kiro.hooks={enabled,v2} in the initialize handshake) so the
	// workspace's user-authored .kiro/hooks/*.json hooks AUTOFIRE on their
	// triggers (SessionStart / UserPromptSubmit / PreToolUse / PostToolUse)
	// during a turn. In v2 mode KAS loads the hook files itself and RUNS
	// runCommand hooks internally (its own process runner, in the workspace),
	// exactly as the Kiro IDE and kiro-cli TUI do — it does NOT call back the
	// client's _kiro/hooks/executeHook for autofire (verified on the live v3
	// wire: chat autofire produced zero executeHook callbacks; the hook
	// commands ran internally). So no chat-bridge executeHook handler is
	// needed. Same trust model as vibekit's `!cmd` interception: the hooks are
	// the user's own automation. The utility bridge keeps its own EnableHooks
	// for the hooks dashboard (list/setEnabled/Run-now); see hub/hooks.go.
	//
	// Forward MUST be draining NotifCh before Start: on v3 (KAS) the agent
	// sends _kiro/auth/getAccessToken and _kiro/terminal/shell_type as
	// server->client REQUESTS on the session-creation critical path, and
	// session/new does not return until they are answered. Attaching the
	// forward goroutine after Start deadlocks every fresh session. If Start
	// fails it stops the bridge (NotifCh closes), so this goroutine exits.
	go bc.Forward(chatID, sb.bridge)
	if err := sb.bridge.Start(ctx, &api.StartOpts{Model: model, Mode: chat.CurrentModeID, Effort: bc.effortForModel(ctx, model), MCPServers: mcpServers, AgentEngine: bc.agentEngine, EnableHooks: true}); err != nil {
		return nil, setupErr(err)
	}
	bc.persistNewSessionMetadata(ctx, chatID, sb.bridge)

	sb.primed = false
	// Failed-fork rewind degrade: a rewind chat reaches this fresh
	// session/new path only when session/fork was unavailable (ACPSessionID
	// never set) or a stored forked session failed to load — in both cases
	// the new session has none of the prior context the truncated UI
	// transcript shows. Flag it so PrimeIfNeeded injects that history on the
	// first prompt. Deriving from chat fields (no new schema field): a
	// successfully-forked rewind resumes via session/load above and never
	// reaches here (no prime); a promoted rewind has ParentChatID cleared;
	// a normal new chat has no ParentChatID. Messages is already non-empty
	// here (the rewind's truncated history plus the just-appended prompt).
	if chat.ParentChatID != "" && len(chat.Messages) > 0 {
		sb.primeReason = primeReasonRewind
	}
	sb.state = bridgeIdle

	return sb, nil
}

// tryLoadSession attempts session/load against the stored ACP session id.
func (bc *BridgeCoordinator) tryLoadSession(
	ctx context.Context, chatID api.ChatID, sb *sharedBridge, acpSessionID, model string,
	mcpServers []map[string]any,
) bool {
	// EnableHooks:true — the session/load path must opt into the hook engine
	// too, so hooks autofire on resumed sessions after a container restart
	// (same rationale as spawnBridge's session/new path above).
	//
	// Forward attaches BEFORE Start for the same reason as the fresh
	// session/new path: v3 session/load also blocks on the host answering
	// _kiro/auth/getAccessToken. On load failure the old bridge is swapped
	// out of sb before it is stopped, so this goroutine's exit cleanup
	// (removeIfBridge, identity-compared) cannot evict the replacement.
	go bc.Forward(chatID, sb.bridge)
	if err := sb.bridge.Start(ctx, &api.StartOpts{SessionID: acpSessionID, Model: model, MCPServers: mcpServers, AgentEngine: bc.agentEngine, EnableHooks: true}); err != nil {
		slog.Warn("session/load failed, starting new",
			"chat_id", chatID, "acp_session", acpSessionID, "error", err)
		old := sb.bridge
		sb.bridge = bc.bridge.mgr.factory()
		old.Stop()
		if mErr := bc.chatStore.Mutate(ctx, chatID, func(c *api.Chat, ex bool) bool {
			if !ex {
				return false
			}
			c.ACPSessionID = ""
			return true
		}); mErr != nil {
			slog.Error("clear stale acp_session_id", "chat_id", chatID, "error", mErr)
		}
		return false
	}
	if mErr := bc.chatStore.Mutate(ctx, chatID, func(c *api.Chat, ex bool) bool {
		if !ex {
			return false
		}
		c.CurrentModeID = sb.bridge.CurrentMode()
		c.AvailableModes = sb.bridge.Modes()
		c.AvailableModels = sb.bridge.Models()
		return true
	}); mErr != nil {
		slog.Error("refresh session metadata", "chat_id", chatID, "error", mErr)
	}
	sb.primed = true
	sb.state = bridgeIdle
	return true
}

// persistNewSessionMetadata stores the ACP session id, model, and session-
// level metadata into the chat after a fresh session/new call.
func (bc *BridgeCoordinator) persistNewSessionMetadata(ctx context.Context, chatID api.ChatID, bridge api.ACPBridge) {
	newSessionID := bridge.SessionID()
	newModelID := bridge.ModelID()
	currentMode := bridge.CurrentMode()
	modes := bridge.Modes()
	models := bridge.Models()
	if err := bc.chatStore.Mutate(ctx, chatID, func(c *api.Chat, ex bool) bool {
		if !ex {
			return false
		}
		c.ACPSessionID = string(newSessionID)
		if newModelID != "" {
			c.Model = string(newModelID)
		}
		c.CurrentModeID = currentMode
		c.AvailableModes = modes
		c.AvailableModels = models
		return true
	}); err != nil {
		slog.Error("persist new session metadata",
			"chat_id", chatID,
			"acp_session", newSessionID,
			"model", newModelID,
			"error", err)
	}
}

// GetBridge returns the bridge for chatID, or nil.
//
//nolint:revive // unexported-return: see GetOrCreateBridge above.
func (bc *BridgeCoordinator) GetBridge(chatID api.ChatID) *sharedBridge {
	return bc.bridge.mgr.get(chatID)
}

// CloseBridge stops a bridge and removes it from the map.
func (bc *BridgeCoordinator) CloseBridge(chatID api.ChatID) {
	bc.bridge.mgr.close(chatID)
}

// Forward is the ACP notification → domain event translator, run as a
// goroutine per bridge.
func (bc *BridgeCoordinator) Forward(chatID api.ChatID, bridge api.ACPBridge) {
	for msg := range bridge.NotifCh() {
		bc.translateEvent(chatID, msg)
	}

	slog.Info("bridge exited", "chat_id", chatID)

	bc.bridge.mgr.removeIfBridge(chatID, bridge)

	// Flush staged writes for the chat. A bridge exit (crash, or a
	// model-switch CloseBridge) leaves the supervised fs-handler goroutine
	// parked on its resume channel and a phantom "awaiting approval"
	// pending op that would replay to reconnecting clients. Cancel, delete,
	// and mode-disable already flush; this is the bridge-exit sibling.
	// Idempotent: chat-delete flushes before CloseBridge, so the second
	// flush here finds no ops and broadcasts nothing.
	if bc.flushPending != nil {
		bc.flushPending(bc.lifecycle.shutdownCtx, chatID, api.ClearReasonBridgeExited)
	}

	lastBridge := bc.bridge.mgr.count() == 0

	if lastBridge {
		bc.mcpRegistry.clearAll(bc.lifecycle.shutdownCtx)
	}
}

// PrimeIfNeeded sends the chat history as an ephemeral priming prompt on
// the current bridge.
func (bc *BridgeCoordinator) PrimeIfNeeded(ctx context.Context, chatID api.ChatID, sb *sharedBridge) {
	history := bc.chatStore.BuildHistory(ctx, chatID)
	if history == "" {
		return
	}

	var prime string
	switch sb.primeReason {
	case primeReasonSwitch:
		prime = "The context was just switched (new agent, new model, " +
			"or both). Below is the full conversation history. Read it " +
			"silently and reply with a single short line confirming " +
			"you're caught up.\n\n" + history
	case primeReasonRewind:
		prime = "This conversation was rewound to an earlier turn and is " +
			"resuming in a fresh session. Below is the conversation history " +
			"up to the rewind point. Read it silently and reply with a " +
			"single short line confirming you're caught up.\n\n" + history
	default:
		return
	}

	slog.Info("priming bridge", "chat_id", chatID, "reason", sb.primeReason)
	_, err := sb.bridge.Call(ctx, api.MethodPrompt, command.SessionParams(sb, map[string]any{
		"prompt": []map[string]any{api.TextBlock(prime)},
	}))
	if err != nil {
		slog.Error("prime failed", "chat_id", chatID, "error", err)
	}
}

// modelEffortSetting is the typed representation of the model_effort
// config key (vibekit-managed). Read at session start to seed the
// acp --effort launch flag.
type modelEffortSetting struct {
	LastModel string `json:"last_model"`
	Effort    string `json:"effort"`
}

// effortForModel returns the persisted effort level for model, or "" if
// none is stored or the stored model differs. The result seeds the
// kiro-cli >=2.6 `acp --effort` launch flag (StartOpts.Effort), so a new
// session starts at the user's chosen effort without a post-start
// /effort dispatch. Mid-session changes still go through CmdSetEffort.
func (bc *BridgeCoordinator) effortForModel(ctx context.Context, model string) string {
	var me modelEffortSetting
	if !settings.FieldInto(ctx, bc.lifecycle.configDir, settings.KeyModelEffort, "effort_for_model", &me) {
		return ""
	}
	if me.LastModel != model {
		return ""
	}
	return me.Effort
}

// NotifyPush sends a push notification if the push service is configured.
func (bc *BridgeCoordinator) NotifyPush(ctx context.Context, body string, kind api.PushKind) {
	if bc.push == nil || !bc.push.HasSubscribers() {
		return
	}
	bc.lifecycle.inflight.Go(func() {
		bc.push.Send(ctx, push.DefaultTitle, body, kind)
	})
}

// TakeBuffer returns and removes the chat's assistant buffer.
func (bc *BridgeCoordinator) TakeBuffer(chatID api.ChatID) (*buffer.Buffer, bool) {
	return bc.bridge.assistantBufs.Take(chatID)
}

// EmitTurnEndedWithStats finalizes any in-flight assistant message
// and broadcasts turn_ended with the credit delta and elapsed time.
func (bc *BridgeCoordinator) EmitTurnEndedWithStats(ctx context.Context, chatID api.ChatID, resp *api.RPCResponse, creditsDelta, elapsedMs float64, closeAndRemovePartial func(context.Context, api.ChatID, *buffer.Buffer)) {
	stopReason := extractStopReason(resp)

	var changedFiles map[string]*api.FileChange

	if buf, ok := bc.TakeBuffer(chatID); ok && buf.Started {
		changedFiles = buf.ChangedFiles
		if stopReason == stopReasonCancelled {
			changed := buf.MarkCancelledToolsFailed()
			for i := range changed {
				bc.broadcast(ctx, api.NewEvent(api.EventToolCallUpdate, chatID, api.ToolCallUpdatePayload{MessageID: buf.MessageID, ToolCall: changed[i]}))
			}
		}

		msg := api.Message{
			ID:        buf.MessageID,
			Role:      api.RoleAssistant,
			Ts:        time.Now().UnixMilli(),
			Content:   buf.Content.String(),
			Reasoning: buf.Reasoning.String(),
			ToolCalls: buf.ToolCalls,
			// Blocks captures the chronological text/tool/thinking
			// emission order; client renderers prefer it over
			// Content+ToolCalls so a turn renders the way the agent
			// actually produced it (text, tool, more text, another
			// tool, …) rather than collapsing all text to the top.
			Blocks: buf.Blocks,
			// CodeReferences persists the turn's licensed-code
			// attributions so the chip survives reload (the streamed
			// assistant turn is never re-broadcast as message_appended).
			CodeReferences: buf.CodeReferences,
			// Turn summary (credits · elapsed · files changed) — persisted on
			// the message so the turn footer survives reload. The same values
			// ride the turn_ended SSE for the live render; omitempty drops the
			// zero/nil cases (a read-only or zero-cost turn carries no footer).
			TurnCredits:   creditsDelta,
			TurnElapsedMs: elapsedMs,
			ChangedFiles:  changedFiles,
		}
		// Persist the finalized turn BEFORE deleting the .partial. A crash
		// in this window would otherwise lose a COMPLETED turn (the
		// .partial gone, the chat file never committed). RecoverPartials is
		// idempotent — it skips the append when the MessageID is already
		// committed — so a crash AFTER this commit / BEFORE the delete
		// below can't double-append on the next boot.
		if err := bc.chatStore.AppendMessage(ctx, chatID, &msg); err != nil {
			slog.Error("persist assistant turn", "chat_id", chatID, "error", err)
		}
		closeAndRemovePartial(ctx, chatID, buf)
	}

	if stopReason == stopReasonCancelled {
		evt := api.Message{
			ID:        newMessageID(),
			Role:      api.RoleEvent,
			Ts:        time.Now().UnixMilli(),
			EventKind: api.EventCancelled,
		}
		if err := bc.chatStore.AppendMessage(ctx, chatID, &evt); err != nil {
			slog.Error("persist cancel event", "chat_id", chatID, "error", err)
		}
	}

	if _, stillExists := bc.chatStore.Get(ctx, chatID); stillExists {
		bc.broadcast(ctx, api.NewEvent(api.EventTurnEnded, chatID, api.TurnEndedPayload{
			StopReason:   stopReason,
			CreditsDelta: creditsDelta,
			ElapsedMs:    elapsedMs,
			ChangedFiles: changedFiles,
		}))
	}

	trustReason := api.ClearReasonTurnEnded
	if stopReason == stopReasonCancelled {
		trustReason = api.ClearReasonCancelled
	}
	bc.supervised.ClearTrust(chatID, trustReason)

	if stopReason != stopReasonCancelled {
		bc.NotifyPush(ctx, "Agent finished", api.PushKindAgentFinished)
	}
}

// TryFastModelSwitch attempts an in-session model swap via
// session/set_config_option (configId "model") on the running bridge.
func (bc *BridgeCoordinator) TryFastModelSwitch(ctx context.Context, chatID api.ChatID, model string) bool {
	sb := bc.bridge.mgr.get(chatID)
	if sb == nil {
		return false
	}
	if err := sb.bridge.SetModel(ctx, model); err != nil {
		slog.Info("model switch: fast path failed, falling back to restart",
			"chat_id", chatID, "model", model, "error", err)
		return false
	}
	slog.Info("model switch: fast path succeeded (session/set_config_option)",
		"chat_id", chatID, "model", model)
	return true
}

// PersistModelSwitch records the switch event and updates the chat's
// model + resets usage counters.
func (bc *BridgeCoordinator) PersistModelSwitch(ctx context.Context, chatID api.ChatID, model string, contextSize int) {
	evt := api.Message{
		ID:        newMessageID(),
		Role:      api.RoleEvent,
		Ts:        time.Now().UnixMilli(),
		EventKind: api.EventModelSwitched,
		Content:   model,
	}
	if err := bc.chatStore.AppendMessage(ctx, chatID, &evt); err != nil {
		slog.Error("switch_model: append event", "chat_id", chatID, "error", err)
	}
	if err := bc.chatStore.Mutate(ctx, chatID, func(c *api.Chat, ex bool) bool {
		if !ex {
			return false
		}
		c.Model = model
		c.Usage = api.Usage{ContextSize: contextSize}
		return true
	}); err != nil {
		slog.Error("switch_model: persist model", "chat_id", chatID, "error", err)
	}
}

// FlushInFlightTurnOnSwitch drops the assistant buffer and its .partial
// sibling for chatID before a bridge restart.
func (bc *BridgeCoordinator) FlushInFlightTurnOnSwitch(ctx context.Context, chatID api.ChatID, closeAndRemovePartial func(context.Context, api.ChatID, *buffer.Buffer)) {
	buf, ok := bc.TakeBuffer(chatID)
	if !ok || !buf.Started {
		closeAndRemovePartial(ctx, chatID, buf)
		return
	}
	closeAndRemovePartial(ctx, chatID, buf)
	bc.broadcast(ctx, api.NewEvent(api.EventTurnEnded, chatID, api.TurnEndedPayload{StopReason: api.StopReasonInterrupted}))
}

const stopReasonCancelled = api.StopReasonCancelled

func extractStopReason(resp *api.RPCResponse) api.StopReason {
	if resp == nil || resp.Result == nil {
		return ""
	}
	var result struct {
		StopReason api.StopReason `json:"stopReason"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		slog.Debug("turn_ended: parse result", "error", err)
		return ""
	}
	return result.StopReason
}
