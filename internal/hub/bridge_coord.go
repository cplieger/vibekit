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
	permArgsFn     func() []string
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
		permArgsFn:     h.permArgsFn,
	}
}

// GetOrCreateBridge returns an existing bridge for chatID, or creates one.
// Concurrent callers for the same chatID coalesce via singleflight.
//
//nolint:revive // unexported-return: sharedBridge is package-internal; callers within hub use the methods on it. Exporting would leak ACP wiring outside the hub package.
func (bc *BridgeCoordinator) GetOrCreateBridge(ctx context.Context, chatID api.ChatID, agentOverride, modelOverride string) (*sharedBridge, error) {
	// Fast path: bridge already exists.
	if sb := bc.bridge.mgr.get(chatID); sb != nil {
		return sb, nil
	}

	// Coalesce concurrent spawn attempts for the same chatID+overrides.
	// Including overrides in the key ensures callers with different
	// agent/model parameters don't coalesce onto the wrong bridge.
	sfKey := string(chatID) + "\x00" + agentOverride + "\x00" + modelOverride
	v, err, _ := bc.bridge.mgr.spawnSF.Do(sfKey, func() (any, error) {
		return bc.spawnBridge(ctx, chatID, agentOverride, modelOverride)
	})
	if err != nil {
		return nil, err
	}
	b, _ := v.(*sharedBridge)
	return b, nil
}

// spawnBridge creates (or returns the just-created) bridge for chatID.
// It runs inside the singleflight so concurrent callers coalesce. It
// resolves the agent/model (overrides beat the chat's stored values),
// tries session/load when an ACP session id is stored, and otherwise
// starts a fresh session/new. On any start failure it rolls the
// half-registered bridge back out of the map and returns the error.
func (bc *BridgeCoordinator) spawnBridge(ctx context.Context, chatID api.ChatID, agentOverride, modelOverride string) (*sharedBridge, error) {
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

	agent := chat.Agent
	if agentOverride != "" {
		agent = agentOverride
	}
	model := chat.Model
	if modelOverride != "" && modelOverride != modelAuto {
		model = modelOverride
	}

	permArgs := bc.permArgsFn()

	var mcpServers []map[string]any
	if bc.mcpConfig != nil {
		mcpServers = bc.mcpConfig.ACPServers(ctx)
	}

	if bc.preBridgeSpawn != nil {
		bc.preBridgeSpawn(ctx)
	}

	if chat.ACPSessionID != "" {
		if bc.tryLoadSession(ctx, chatID, sb, chat.ACPSessionID, agent, model, permArgs, mcpServers) {
			go bc.Forward(chatID, sb.bridge)
			return sb, nil
		}
	}

	if err := sb.bridge.Start(ctx, &api.StartOpts{Agent: agent, Model: model, Effort: bc.effortForModel(ctx, model), ExtraArgs: permArgs, MCPServers: mcpServers}); err != nil {
		return nil, setupErr(err)
	}
	bc.persistNewSessionMetadata(ctx, chatID, sb.bridge)

	sb.primed = false
	sb.state = bridgeIdle

	go bc.Forward(chatID, sb.bridge)

	return sb, nil
}

// tryLoadSession attempts session/load against the stored ACP session id.
func (bc *BridgeCoordinator) tryLoadSession(
	ctx context.Context, chatID api.ChatID, sb *sharedBridge, acpSessionID, agent, model string,
	permArgs []string, mcpServers []map[string]any,
) bool {
	if err := sb.bridge.Start(ctx, &api.StartOpts{SessionID: acpSessionID, Agent: agent, Model: model, ExtraArgs: permArgs, MCPServers: mcpServers}); err != nil {
		slog.Warn("session/load failed, starting new",
			"chat_id", chatID, "acp_session", acpSessionID, "error", err)
		sb.bridge.Stop()
		if mErr := bc.chatStore.Mutate(ctx, chatID, func(c *api.Chat, ex bool) bool {
			if !ex {
				return false
			}
			c.ACPSessionID = ""
			return true
		}); mErr != nil {
			slog.Error("clear stale acp_session_id", "chat_id", chatID, "error", mErr)
		}
		sb.bridge = bc.bridge.mgr.factory()
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
		closeAndRemovePartial(ctx, chatID, buf)
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
		}
		if err := bc.chatStore.AppendMessage(ctx, chatID, &msg); err != nil {
			slog.Error("persist assistant turn", "chat_id", chatID, "error", err)
		}
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

// TryFastModelSwitch attempts session/set_model on the running bridge.
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
	slog.Info("model switch: fast path succeeded (session/set_model)",
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
