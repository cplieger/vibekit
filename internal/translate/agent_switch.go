package translate

// _kiro.dev/agent/switched handler.

import (
	"context"
	"log/slog"

	"github.com/cplieger/vibekit/internal/api"
)

// HandleAgentSwitched handles the agent/switched notification.
func (t *Translator) HandleAgentSwitched(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	p, ok := unmarshalParams[struct {
		AgentName         string `json:"agentName"`
		PreviousAgentName string `json:"previousAgentName"`
		WelcomeMessage    string `json:"welcomeMessage"`
		Model             string `json:"model"`
	}](msg, "agent/switched")
	if !ok || p.AgentName == "" {
		return
	}
	if chatID != "" {
		if err := t.deps.ChatStore().Mutate(ctx, chatID, func(c *api.Chat, ex bool) bool {
			if !ex {
				return false
			}
			return applyAgentSwitch(c, p.AgentName, p.Model)
		}); err != nil {
			slog.Error("agent_switched: persist", "error", err)
		}
	}
	evt := t.newEventMessage(api.EventAgentSwitched, p.WelcomeMessage)
	if err := t.deps.ChatStore().AppendMessage(ctx, chatID, &evt); err != nil {
		slog.Error("agent_switched: append event", "chat_id", chatID, "error", err)
	}
}

// applyAgentSwitch updates the chat's agent (and model when the
// notification carries one), returning whether anything changed.
func applyAgentSwitch(c *api.Chat, agentName, model string) bool {
	changed := false
	if c.Agent != agentName {
		c.Agent = agentName
		changed = true
	}
	if model != "" && c.Model != model {
		c.Model = model
		changed = true
	}
	return changed
}
