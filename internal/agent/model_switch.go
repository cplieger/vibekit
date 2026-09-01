package agent

// KIRO-CLI 2.0.1 tui.js:886710603bed3fb6 — payload shapes pinned.
//
// cmdSwitchModel: switch the model for an active chat. Two strategies:
//
//  1. Fast path (session/set_config_option, configId "model"): swap the model
//     in-session on the running bridge. No priming, no token cost, instant.
//
//  2. Fallback (bridge restart): if the in-session switch fails, close the
//     bridge, start a new one, and let getOrCreateBridge try session/load
//     then session/new.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/cplieger/vibekit/internal/command"
	"github.com/cplieger/vibekit/internal/ids"
	"github.com/cplieger/vibekit/internal/rpcerr"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// resolveSwitchModel returns the effective model after applying the
// optional payload override.
func resolveSwitchModel(chat *vibekit.Chat, p vibekit.SwitchModelCommand) (model string, isSwitch bool) {
	model = chat.Model
	if p.Model == "" || p.Model == modelAuto || p.Model == model {
		return model, false
	}
	return p.Model, true
}

// responseOK2 is the canonical success response shape for agent
// commands that go through the dedup cache via h.respond.
var responseOK2 = map[string]bool{"ok": true}

// errModelNotServed is the 409 body for a pick this account cannot run. A
// conflict rather than a bad request: the id is well-formed and was legal for
// some account, so the refusal is about this session's entitlement state.
var errModelNotServed = errors.New("that model is not available on this account")

func (rt *Runtime) cmdSwitchModel(ctx context.Context, cmd *vibekit.ClientCommand) (any, error) {
	if cmd.ChatID == "" {
		return nil, command.StatusError(http.StatusBadRequest, command.ErrMissingChatID)
	}
	var p vibekit.SwitchModelCommand
	if len(cmd.Payload) > 0 {
		if err := json.Unmarshal(cmd.Payload, &p); err != nil {
			return nil, command.StatusError(http.StatusBadRequest, command.ErrInvalidPayload)
		}
	}

	if !ids.ValidIdent(p.Model) {
		return nil, command.StatusError(http.StatusBadRequest, command.ErrInvalidPayload)
	}

	chat, ok := rt.chatStore.Get(ctx, cmd.ChatID)
	if !ok {
		return nil, command.StatusError(http.StatusNotFound, command.ErrChatNotFound)
	}

	model, isSwitch := resolveSwitchModel(chat, p)

	if isSwitch {
		if err := rt.refuseUnservedModel(ctx, cmd.ChatID, chat, model); err != nil {
			return nil, err
		}
		// A pick on a chat that has never run is a PREFERENCE, not a switch:
		// persist it and let the first prompt's session/new carry it through.
		// Falling through would spawn a ~300MB bridge tree for a pill click.
		if chat.ACPSessionID == "" && len(chat.Messages) == 0 && !rt.HasLiveBridge(cmd.ChatID) {
			rt.persistModelPick(ctx, cmd.ChatID, model)
			return responseOK2, nil
		}
		if rt.coord.TryFastModelSwitch(ctx, cmd.ChatID, model, rt.coord.EffortForSwitch(ctx, chat, model)) {
			rt.coord.PersistModelSwitch(ctx, cmd.ChatID, model, chat.Usage.ContextSize)
			return responseOK2, nil
		}
	}

	return rt.switchByRestart(ctx, cmd, chat, model, isSwitch)
}

// persistModelPick records a pre-session model choice on the chat record: no
// event row (nothing was switched — the chat never ran), no usage reset, no
// bridge. The chosen-effort clear matches PersistModelSwitch: a tier picked
// under the previous model does not survive onto this one.
func (rt *Runtime) persistModelPick(ctx context.Context, chatID vibekit.ChatID, model string) {
	if err := rt.chatStore.Mutate(ctx, chatID, func(c *vibekit.Chat, ex bool) bool {
		if !ex {
			return false
		}
		c.Model = model
		c.Effort = ""
		return true
	}); err != nil {
		slog.Error("switch_model: persist pre-session pick", "chat_id", chatID, "error", err)
	}
}

// switchByRestart is the fallback when the in-session swap did not take: tear
// the bridge down and let OpenBridge try session/load, then session/new.
func (rt *Runtime) switchByRestart(
	ctx context.Context, cmd *vibekit.ClientCommand,
	chat *vibekit.Chat, model string, isSwitch bool,
) (any, error) {
	rt.coord.FlushInFlightTurnOnSwitch(ctx, cmd.ChatID)
	rt.coord.CloseBridge(cmd.ChatID)

	sb, err := rt.coord.OpenBridge(ctx, cmd.ChatID, model)
	if err != nil {
		rt.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventError, cmd.ChatID, vibekit.ErrorPayload{Code: vibekit.ErrCodeSwitchFailed, Message: rpcerr.Text(err)}))
		return nil, command.StatusError(http.StatusInternalServerError, err)
	}
	if isSwitch {
		rt.coord.PersistModelSwitch(ctx, cmd.ChatID, model, chat.Usage.ContextSize)
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
		// A RESUMED session has never seen this pick: session/load restores KAS's
		// own persisted model and session/new's priming door does not run, so
		// without this the switch would silently not happen — the load's
		// config_option_update would race PersistModelSwitch's write back to the
		// old id.
		//
		// Only on this branch: a fresh session already carried the model and
		// level on session/new, so re-sending would be two round trips that
		// change nothing.
		//
		// Through the bridge this function HOLDS, never a fresh lookup by chat
		// id: the old bridge's exit cleanup can evict the manager entry after the
		// new one registered.
		if isSwitch && !rt.coord.applyModelSwitch(ctx, cmd.ChatID, sb, model, rt.coord.EffortForSwitch(ctx, chat, model)) {
			slog.Warn("model switch: the resumed session kept its own model",
				"chat_id", cmd.ChatID, "model", model)
		}
	}

	return responseOK2, nil
}

// refuseUnservedModel is the LOUD half of the entitlement check: spawnBridge
// withholds an inherited value silently, because nobody chose it in that
// moment, while a pick the user just made must be refused rather than
// downgraded behind their back.
//
// Without it the id reaches the wire, KAS accepts it, and the rejection
// arrives mid-prompt on this and every later turn.
//
// The live session's own advertised set is the evidence when a bridge exists,
// and it is the UNFILTERED one: validating against the picker's list would
// refuse a deprecated model the account can still use. An empty set means
// entitlement is unknowable and vibekit.ModelServed allows it.
func (rt *Runtime) refuseUnservedModel(
	ctx context.Context, chatID vibekit.ChatID, chat *vibekit.Chat, model string,
) error {
	served := chat.ServedModelIDs
	if sb := rt.coord.Bridge(chatID); sb != nil {
		if live := sb.bridge.ServedModels(); len(live) > 0 {
			served = live
		}
	}
	if vibekit.ModelServed(model, served) {
		return nil
	}
	slog.Warn("refusing a model switch this account does not serve",
		"chat_id", chatID, "model", model)
	rt.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventError, chatID, vibekit.ErrorPayload{
		Code:    vibekit.ErrCodeModelNotServed,
		Message: "\"" + model + "\" is not available on this account. Pick another model.",
	}))
	return command.StatusError(http.StatusConflict, errModelNotServed)
}
