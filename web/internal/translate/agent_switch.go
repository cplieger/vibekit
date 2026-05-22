package translate

// _kiro.dev/agent/switched handler.

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"vibekit/internal/api"
)

// HandleAgentSwitched handles the agent/switched notification.
func (t *Translator) HandleAgentSwitched(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	var p struct {
		AgentName         string `json:"agentName"`
		PreviousAgentName string `json:"previousAgentName"`
		WelcomeMessage    string `json:"welcomeMessage"`
		Model             string `json:"model"`
	}
	if json.Unmarshal(msg.Params, &p) != nil || p.AgentName == "" {
		return
	}
	if chatID != "" {
		if err := t.deps.ChatStore().Mutate(ctx, chatID, func(c *api.Chat, ex bool) bool {
			if !ex {
				return false
			}
			changed := false
			if c.Agent != p.AgentName {
				c.Agent = p.AgentName
				changed = true
			}
			if p.Model != "" && c.Model != p.Model {
				c.Model = p.Model
				changed = true
			}
			return changed
		}); err != nil {
			slog.Error("agent_switched: persist", "error", err)
		}
	}
	evt := api.Message{
		ID:        NewMessageID(),
		Role:      api.RoleEvent,
		Ts:        time.Now().UnixMilli(),
		EventKind: api.EventAgentSwitched,
		Content:   p.WelcomeMessage,
	}
	if err := t.deps.ChatStore().AppendMessage(ctx, chatID, &evt); err != nil {
		slog.Error("agent_switched: append event", "chat_id", chatID, "error", err)
	}
}
