package hub

// KIRO-CLI 2.0.1 tui.js:886710603bed3fb6 — payload shapes pinned.
//
// cmdSwitchModel: switch the model for an active chat. Two strategies:
//
//  1. Fast path (session/set_model): call the ACP-core method on the
//     running bridge. kiro-cli swaps the model in-session without
//     tearing down the subprocess. No priming, no token cost, instant.
//     The ACP session id stays the same.
//
//  2. Fallback (bridge restart): if session/set_model fails (context
//     too large for the new model, method not supported by older
//     kiro-cli, etc.), close the bridge, start a new one, and let
//     getOrCreateBridge try session/load then session/new.
//
// Agent changes are deliberately not part of this command — the running
// agent itself issues `switch_mode` permission requests when it wants
// to swap modes; vibekit never forces an agent swap on the user.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"vibekit/internal/api"
)

// resolveSwitchModel returns the effective model after applying the
// optional payload override. A switch with an empty override is still
// useful (restart a wedged bridge) so we proceed even when the model
// is unchanged.
func resolveSwitchModel(chat *api.Chat, p api.SwitchModelCommand) (model string, isSwitch bool) {
	model = chat.Model
	if p.Model == "" || p.Model == modelAuto || p.Model == model {
		return model, false
	}
	return p.Model, true
}

func (h *Hub) cmdSwitchModel(ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand) {
	if !h.requireChatID(w, cmd) {
		return
	}
	var p api.SwitchModelCommand
	if len(cmd.Payload) > 0 {
		if err := json.Unmarshal(cmd.Payload, &p); err != nil {
			h.respondErr(w, http.StatusBadRequest, errInvalidPayload)
			return
		}
	}

	// Validate the requested model at the command boundary so a
	// malformed value is rejected with 400 BEFORE we mutate chat
	// state. Previously this check fired deep inside bridge.Start
	// (validIdent on the --model arg), by which time persistModelSwitch
	// had already written a stale "model_switched" event and set
	// chat.Model to the bad value, leaving the chat stuck until the
	// next successful switch. validIdent tolerates empty (bare-restart)
	// and "auto" (keep current) so those recovery paths still work.
	if !validIdent(p.Model) {
		h.respondErr(w, http.StatusBadRequest, errInvalidPayload)
		return
	}

	chat, ok := h.chatStore.Get(ctx, cmd.ChatID)
	if !ok {
		h.respondErr(w, http.StatusNotFound, errChatNotFound)
		return
	}

	model, isSwitch := resolveSwitchModel(chat, p)

	// Try the fast path: in-session model swap on the running bridge.
	if isSwitch {
		if h.tryFastModelSwitch(ctx, cmd.ChatID, model) {
			h.persistModelSwitch(ctx, cmd.ChatID, model, chat.Usage.ContextSize)
			h.respond(w, cmd.RequestID, map[string]bool{"ok": true})
			return
		}
	}

	// Fallback: full bridge restart. Flush any in-flight streaming
	// state for this chat BEFORE stopping the bridge — closeBridge
	// unblocks an in-flight session/prompt Call with errBridgeExited,
	// which means emitTurnEndedWithStats never runs and the assistant
	// buffer + .partial file would otherwise survive into the next
	// bridge's first chunk, mixing content from two turns under the
	// same message id.
	h.flushInFlightTurnOnSwitch(cmd.ChatID)
	h.closeBridge(cmd.ChatID)

	// Persist the new model AFTER getOrCreateBridge confirms the fresh
	// bridge actually starts. If Start fails, the previous model is
	// left intact so the chat is recoverable (next prompt will retry
	// with the original model). Only emit the model_switched event
	// when there was a real model change — the bare-restart / same-model
	// recovery path should not pollute the transcript with a ghost
	// divider and should not reset the credit counter.
	sb, err := h.getOrCreateBridge(ctx, cmd.ChatID, chat.Agent, model)
	if err != nil {
		h.Broadcast(ctx, api.NewEvent(api.EventError, cmd.ChatID, api.ErrorPayload{Code: api.ErrCodeSwitchFailed, Message: err.Error()}))
		h.respondErr(w, http.StatusInternalServerError, err)
		return
	}
	if isSwitch {
		h.persistModelSwitch(ctx, cmd.ChatID, model, chat.Usage.ContextSize)
	}

	sb.mu.Lock()
	needsPrime := !sb.primed
	if needsPrime {
		// Leave primed=false so the next cmdPrompt's primed check
		// triggers primeIfNeeded. Setting primed=true here would
		// silently skip the transcript replay; setting primeReason
		// tells primeIfNeeded which prime text to use.
		sb.primeReason = primeReasonSwitch
	}
	sb.mu.Unlock()

	if needsPrime {
		slog.Info("model switch: fallback, priming fresh session",
			"chat_id", cmd.ChatID, "model", model)
	} else {
		slog.Info("model switch: fallback, session/load succeeded",
			"chat_id", cmd.ChatID, "model", model)
	}

	h.respond(w, cmd.RequestID, map[string]bool{"ok": true})
}

// flushInFlightTurnOnSwitch drops the assistant buffer and its
// .partial sibling for chatID before a bridge restart, and emits an
// interrupted turn_ended so the client clears its in-flight bubble.
// Called from cmdSwitchModel's fallback path before closeBridge, so
// the running cmdPrompt that is about to have its Call unblocked by
// errBridgeExited doesn't need to flush anything itself (its path
// returns an error without touching emitTurnEndedWithStats).
//
// Idempotent when no turn is in flight (takeBuffer returns !ok).
func (h *Hub) flushInFlightTurnOnSwitch(chatID api.ChatID) {
	buf, ok := h.takeBuffer(chatID)
	if !ok || !buf.Started {
		// Still clean up any orphan .partial left by an earlier
		// crash; cheap best-effort.
		h.closeAndRemovePartial(chatID, buf)
		return
	}
	h.closeAndRemovePartial(chatID, buf)
	// Broadcast an interrupted turn_ended so the client drops the
	// streaming tail. We don't persist the partial content — the
	// user's explicit action (switch_model) supersedes the in-flight
	// reply, and keeping a half-rendered message would confuse the
	// post-switch transcript.
	h.Broadcast(context.Background(), api.NewEvent(api.EventTurnEnded, chatID, api.TurnEndedPayload{StopReason: api.StopReasonInterrupted}))
}

// tryFastModelSwitch attempts session/set_model on the running bridge.
// Returns true on success (model swapped in-session, no restart needed).
// Returns false if no bridge exists or the call fails.
func (h *Hub) tryFastModelSwitch(ctx context.Context, chatID api.ChatID, model string) bool {
	sb := h.bridge.mgr.get(chatID)
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

// persistModelSwitch records the switch event and updates the chat's
// model + resets usage counters (preserving context_size).
func (h *Hub) persistModelSwitch(ctx context.Context, chatID api.ChatID, model string, contextSize int) {
	evt := api.Message{
		ID:        newMessageID(),
		Role:      api.RoleEvent,
		Ts:        time.Now().UnixMilli(),
		EventKind: api.EventModelSwitched,
		Content:   model,
	}
	if err := h.chatStore.AppendMessage(ctx, chatID, &evt); err != nil {
		slog.Error("switch_model: append event", "chat_id", chatID, "error", err)
	}
	if err := h.chatStore.Mutate(ctx, chatID, func(c *api.Chat, ex bool) bool {
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
