package translate

// v3 (KAS) init-error, rate-limit, and system-notify handlers.

import (
	"context"
	"log/slog"

	"github.com/cplieger/runesafe"
	"github.com/cplieger/vibekit/internal/api"
)

// maxNotifyTextBytes bounds one backend-supplied string on its way into banner
// copy. Every value these four handlers interpolate is chosen upstream: an agent
// name and a config path from a local file KAS read, and two free-form provider
// messages. None has a length bound on the wire, and all four land in a banner
// and a log line, so they get the same treatment as an RPC error's text.
const maxNotifyTextBytes = 512

// notifyText prepares one upstream string for banner copy: runesafe's
// single-line preset (C0/C1 controls, DEL, Bidi overrides and the paragraph
// separators become spaces, so a mangled message cannot forge a log record or
// reorder a sentence in a viewer) capped on a rune boundary.
func notifyText(s string) string {
	return runesafe.SanitizeSingleLineBounded(s, maxNotifyTextBytes)
}

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
		Message: "\"" + notifyText(p.Requested) + "\" not found, using \"" + notifyText(p.Fallback) + "\"",
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
		Message: notifyText(t.relPath(p.Path)) + ": " + notifyText(p.Error),
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
		Message: notifyText(p.Message),
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
		Message: notifyText(p.Message),
	}))
}
