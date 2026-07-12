package translate

// v3 (KAS) init-error, rate-limit, and system-notify handlers.

import (
	"context"
	"log/slog"

	"github.com/cplieger/vibekit/internal/api"
)

// HandleAgentNotFound handles the _kiro/customAgent/not_found notification:
// the requested agent (a mode id on v3) doesn't exist. Persists the
// fallback mode (always "vibe" on v3) as the chat's CurrentModeID and
// broadcasts a typed error — on v3 the fallback agent IS a mode id. v3
// carries no model fields here — a bad model is an InvalidModelError RPC
// error on the set_config_option/prompt call, not a notification, so there
// is no model-not-found handler.
func (t *Translator) HandleAgentNotFound(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	p, ok := unmarshalParams[struct {
		Requested string `json:"requestedAgent"`
		Fallback  string `json:"fallbackAgent"`
	}](msg, string(api.ErrCodeAgentNotFound))
	if !ok {
		return
	}
	if p.Fallback != "" && chatID != "" {
		if err := t.deps.ChatStore().Mutate(ctx, chatID, func(c *api.Chat, ex bool) bool {
			if !ex {
				return false
			}
			c.CurrentModeID = p.Fallback
			return true
		}); err != nil {
			slog.Error("agent_not_found: persist fallback", "error", err)
		}
	}
	t.deps.Broadcast(ctx, api.NewEvent(api.EventError, chatID, api.ErrorPayload{
		Code:    api.ErrCodeAgentNotFound,
		Message: "\"" + p.Requested + "\" not found — using \"" + p.Fallback + "\"",
	}))
}

// HandleAgentConfigError handles the _kiro/customAgent/config_error
// notification ({path, error}); the extra v3 sessionId is ignored.
func (t *Translator) HandleAgentConfigError(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	p, ok := unmarshalParams[struct {
		Path  string `json:"path"`
		Error string `json:"error"`
	}](msg, "agent_config_error")
	if !ok {
		return
	}
	t.deps.Broadcast(ctx, api.NewEvent(api.EventError, chatID, api.ErrorPayload{
		Code:    api.ErrCodeAgentConfigError,
		Message: p.Path + ": " + p.Error,
	}))
}

// HandleRateLimit handles the _kiro/error/rate_limit notification
// ({message}); the extra v3 sessionId is ignored. Rendered as an
// auto-clearing amber banner.
func (t *Translator) HandleRateLimit(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	p, ok := unmarshalParams[struct {
		Message string `json:"message"`
	}](msg, "rate_limit")
	if !ok {
		return
	}
	t.deps.Broadcast(ctx, api.NewEvent(api.EventError, chatID, api.ErrorPayload{
		Code:    api.ErrCodeRateLimit,
		Message: p.Message,
	}))
}

// HandleSystemNotify handles the v3 _kiro/system/notify notification
// ({level, message}) — the replacement for v2's session/retry banner. KAS
// emits it as a connection-level "model under high load" notice: no attempt
// counter and no sessionId, so it is a bridge-scope broadcast (chatID may
// be empty). The message is surfaced verbatim as an auto-clearing banner;
// level (info/warning/error) is decoded for forward-compatibility but not
// separately surfaced — banner styling keys off the error code.
func (t *Translator) HandleSystemNotify(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	p, ok := unmarshalParams[struct {
		Level   string `json:"level"`
		Message string `json:"message"`
	}](msg, "system/notify")
	if !ok || p.Message == "" {
		return
	}
	t.deps.Broadcast(ctx, api.NewEvent(api.EventError, chatID, api.ErrorPayload{
		Code:    api.ErrCodeRateLimit,
		Message: p.Message,
	}))
}
