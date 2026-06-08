package translate

// kiro/metadata + _kiro.dev/metadata handler.

import (
	"context"
	"log/slog"

	"github.com/cplieger/vibekit/internal/api"
)

// HandleMetadata processes metadata notifications (usage stats).
func (t *Translator) HandleMetadata(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	type metadataParams struct {
		ContextUsagePercentage *float64 `json:"contextUsagePercentage"`
		TurnDurationMs         *float64 `json:"turnDurationMs"`
		SessionID              string   `json:"sessionId"`
		MeteringUsage          []struct {
			UnitSingular string  `json:"unitSingular"`
			UnitPlural   string  `json:"unitPlural"`
			Value        float64 `json:"value"`
		} `json:"meteringUsage"`
	}
	meta, ok := unmarshalParams[metadataParams](msg, "metadata")
	if !ok {
		return
	}

	// Parent-session gate: drop metadata for a subagent session.
	if meta.SessionID != "" && meta.SessionID != t.deps.ParentACPSession(chatID) {
		return
	}

	if err := t.deps.ChatStore().Mutate(ctx, chatID, func(c *api.Chat, exists bool) bool {
		if !exists {
			return false
		}
		changed := false
		if meta.ContextUsagePercentage != nil {
			c.Usage.ContextPct = *meta.ContextUsagePercentage
			c.Usage.HasRealData = true
			changed = true
		}
		if len(meta.MeteringUsage) > 0 {
			items := make([]api.MeteringItem, len(meta.MeteringUsage))
			for i, m := range meta.MeteringUsage {
				items[i] = api.MeteringItem{
					Value:        m.Value,
					UnitSingular: m.UnitSingular,
					UnitPlural:   m.UnitPlural,
				}
			}
			c.Usage.MeteringItems = items
			c.Usage.Credits = items[0].Value
			for _, it := range items {
				if it.UnitPlural == "credits" {
					c.Usage.Credits = it.Value
					break
				}
			}
			c.Usage.HasRealData = true
			changed = true
		}
		if meta.TurnDurationMs != nil && *meta.TurnDurationMs > 0 {
			c.Usage.LastTurnMs = *meta.TurnDurationMs
			c.Usage.TurnCount++
			changed = true
		}
		return changed
	}); err != nil {
		slog.Error("persist usage", "chat_id", chatID, "error", err)
	}
}
