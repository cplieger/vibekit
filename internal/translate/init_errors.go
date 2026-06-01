package translate

// Init-error and rate-limit handlers.

import (
	"context"
	"fmt"
	"log/slog"

	"vibekit/internal/api"
)

// HandleFallback is the shared logic for agent_not_found and
// model_not_found: decode a requested/fallback pair, persist the
// fallback into the chat, and broadcast a typed error.
func (t *Translator) HandleFallback(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse, code api.ErrorCode, mutate func(c *api.Chat, requested, fallback string)) {
	p, ok := unmarshalParams[struct {
		Requested      string `json:"requestedAgent"`
		Fallback       string `json:"fallbackAgent"`
		RequestedModel string `json:"requestedModel"`
		FallbackModel  string `json:"fallbackModel"`
	}](msg, string(code))
	if !ok {
		return
	}
	requested := p.Requested
	if requested == "" {
		requested = p.RequestedModel
	}
	fallback := p.Fallback
	if fallback == "" {
		fallback = p.FallbackModel
	}
	if fallback != "" && chatID != "" {
		if err := t.deps.ChatStore().Mutate(ctx, chatID, func(c *api.Chat, ex bool) bool {
			if !ex {
				return false
			}
			mutate(c, requested, fallback)
			return true
		}); err != nil {
			slog.Error(string(code)+": persist fallback", "error", err)
		}
	}
	t.deps.Broadcast(ctx, api.NewEvent(api.EventError, chatID, api.ErrorPayload{
		Code:    code,
		Message: "\"" + requested + "\" not found — using \"" + fallback + "\"",
	}))
}

// HandleAgentNotFound handles the agent_not_found notification.
func (t *Translator) HandleAgentNotFound(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	t.HandleFallback(ctx, chatID, msg, api.ErrCodeAgentNotFound, func(c *api.Chat, _, fallback string) {
		c.Agent = fallback
	})
}

// HandleModelNotFound handles the model_not_found notification.
func (t *Translator) HandleModelNotFound(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	t.HandleFallback(ctx, chatID, msg, api.ErrCodeModelNotFound, func(c *api.Chat, _, fallback string) {
		c.Model = fallback
	})
}

// HandleAgentConfigError handles the agent_config_error notification.
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

// HandleRateLimit handles the rate_limit notification.
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

// HandleSessionRetry handles the session_retry notification.
func (t *Translator) HandleSessionRetry(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	p, ok := unmarshalParams[struct {
		Reason  string `json:"reason"`
		Attempt int    `json:"attempt"`
	}](msg, "session_retry")
	if !ok {
		return
	}
	message := "Retrying"
	if p.Reason != "" {
		message = p.Reason
	}
	if p.Attempt > 0 {
		message += fmt.Sprintf(" (attempt %d)", p.Attempt)
	}
	t.deps.Broadcast(ctx, api.NewEvent(api.EventError, chatID, api.ErrorPayload{
		Code:    api.ErrCodeStreamTimeout,
		Message: message,
	}))
}
