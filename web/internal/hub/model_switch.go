package hub

// KIRO-CLI 2.0.1 tui.js:886710603bed3fb6 — payload shapes pinned.
//
// cmdSwitchModel: switch the model for an active chat. Two strategies:
//
//  1. Fast path (session/set_model): call the ACP-core method on the
//     running bridge. kiro-cli swaps the model in-session without
//     tearing down the subprocess. No priming, no token cost, instant.
//
//  2. Fallback (bridge restart): if session/set_model fails, close the
//     bridge, start a new one, and let getOrCreateBridge try session/load
//     then session/new.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"vibekit/internal/api"
	"vibekit/internal/command"
)

// resolveSwitchModel returns the effective model after applying the
// optional payload override.
func resolveSwitchModel(chat *api.Chat, p api.SwitchModelCommand) (model string, isSwitch bool) {
	model = chat.Model
	if p.Model == "" || p.Model == modelAuto || p.Model == model {
		return model, false
	}
	return p.Model, true
}

// hubResponseOK is the canonical success response shape for hub
// commands that go through the dedup cache via h.respond.
var hubResponseOK = map[string]bool{"ok": true}

func (h *Hub) cmdSwitchModel(ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand) {
	if !h.requireChatID(w, cmd) {
		return
	}
	var p api.SwitchModelCommand
	if len(cmd.Payload) > 0 {
		if err := json.Unmarshal(cmd.Payload, &p); err != nil {
			h.respondErr(w, http.StatusBadRequest, command.ErrInvalidPayload)
			return
		}
	}

	if !api.ValidIdent(p.Model) {
		h.respondErr(w, http.StatusBadRequest, command.ErrInvalidPayload)
		return
	}

	chat, ok := h.chatStore.Get(ctx, cmd.ChatID)
	if !ok {
		h.respondErr(w, http.StatusNotFound, command.ErrChatNotFound)
		return
	}

	model, isSwitch := resolveSwitchModel(chat, p)

	// Try the fast path: in-session model swap on the running bridge.
	if isSwitch {
		if h.coord.TryFastModelSwitch(ctx, cmd.ChatID, model) {
			h.coord.PersistModelSwitch(ctx, cmd.ChatID, model, chat.Usage.ContextSize)
			h.respond(w, cmd.RequestID, hubResponseOK)
			return
		}
	}

	// Fallback: full bridge restart.
	h.coord.FlushInFlightTurnOnSwitch(ctx, cmd.ChatID, h.closeAndRemovePartial)
	h.coord.CloseBridge(cmd.ChatID)

	sb, err := h.coord.GetOrCreateBridge(ctx, cmd.ChatID, chat.Agent, model)
	if err != nil {
		h.Broadcast(ctx, api.NewEvent(api.EventError, cmd.ChatID, api.ErrorPayload{Code: api.ErrCodeSwitchFailed, Message: err.Error()}))
		h.respondErr(w, http.StatusInternalServerError, err)
		return
	}
	if isSwitch {
		h.coord.PersistModelSwitch(ctx, cmd.ChatID, model, chat.Usage.ContextSize)
	}

	sb.mu.Lock()
	needsPrime := !sb.primed
	if needsPrime {
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

	h.respond(w, cmd.RequestID, hubResponseOK)
}
