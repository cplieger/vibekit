package translate

// kiro/metadata + _kiro.dev/metadata handler.

import (
	"context"
	"log/slog"

	"github.com/cplieger/vibekit/internal/api"
)

// metadataParams is the wire shape for kiro/metadata + _kiro.dev/metadata
// (usage stats) notifications.
type metadataParams struct {
	ContextUsagePercentage *float64        `json:"contextUsagePercentage"`
	TurnDurationMs         *float64        `json:"turnDurationMs"`
	SessionID              string          `json:"sessionId"`
	MeteringUsage          []meteringEntry `json:"meteringUsage"`
}

// meteringEntry is one usage metering item in a metadata notification.
type meteringEntry struct {
	UnitSingular string  `json:"unitSingular"`
	UnitPlural   string  `json:"unitPlural"`
	Value        float64 `json:"value"`
}

// HandleMetadata processes metadata notifications (usage stats).
func (t *Translator) HandleMetadata(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
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
		return applyMetadataUsage(&c.Usage, &meta)
	}); err != nil {
		slog.Error("persist usage", "chat_id", chatID, "error", err)
	}
}

// applyMetadataUsage folds the metadata fields into the chat usage and
// reports whether anything changed (so Mutate can skip a no-op write).
func applyMetadataUsage(u *api.Usage, meta *metadataParams) bool {
	changed := false
	if meta.ContextUsagePercentage != nil {
		u.ContextPct = *meta.ContextUsagePercentage
		u.HasRealData = true
		changed = true
	}
	if len(meta.MeteringUsage) > 0 {
		u.MeteringItems = meteringItems(meta.MeteringUsage)
		u.Credits = creditsFromItems(u.MeteringItems)
		u.HasRealData = true
		changed = true
	}
	if meta.TurnDurationMs != nil && *meta.TurnDurationMs > 0 {
		u.LastTurnMs = *meta.TurnDurationMs
		u.TurnCount++
		changed = true
	}
	return changed
}

// meteringItems maps wire metering entries to domain MeteringItems.
func meteringItems(entries []meteringEntry) []api.MeteringItem {
	items := make([]api.MeteringItem, len(entries))
	for i, m := range entries {
		items[i] = api.MeteringItem{
			Value:        m.Value,
			UnitSingular: m.UnitSingular,
			UnitPlural:   m.UnitPlural,
		}
	}
	return items
}

// creditsFromItems returns the value of the first item labeled "credits",
// falling back to the first item's value when none is. Callers guarantee
// items is non-empty.
func creditsFromItems(items []api.MeteringItem) float64 {
	for _, it := range items {
		if it.UnitPlural == "credits" {
			return it.Value
		}
	}
	return items[0].Value
}
