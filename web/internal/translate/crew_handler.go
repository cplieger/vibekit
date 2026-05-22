package translate

// Subagent orchestration: _kiro.dev/subagent/list_update notifications.

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"time"

	"vibekit/internal/api"
)

// HandleCrewUpdate processes subagent list update notifications.
func (t *Translator) HandleCrewUpdate(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	var p CrewNotifPayload
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		slog.Debug("crew: bad payload", "error", err)
		return
	}
	if len(p.Subagents) == 0 {
		return
	}
	groupKey := p.Subagents[0].Group
	for i := range p.Subagents {
		if p.Subagents[i].Group != groupKey {
			slog.Warn("crew: mixed-group snapshot, skipping",
				"chat_id", chatID,
				"first_group", groupKey,
				"other_group", p.Subagents[i].Group)
			return
		}
	}
	crew := &api.Crew{
		Group:     groupKey,
		Subagents: make([]api.CrewSubagent, len(p.Subagents)),
	}
	for i := range p.Subagents {
		s := &p.Subagents[i]
		crew.Subagents[i] = api.CrewSubagent{
			SessionID:    s.SessionID,
			SessionName:  s.SessionName,
			AgentName:    s.AgentName,
			InitialQuery: s.InitialQuery,
			Status:       api.CrewStatus(s.Status.Type),
			StatusMsg:    s.Status.Message,
			Group:        s.Group,
			Role:         s.Role,
			DependsOn:    s.DependsOn,
		}
	}
	if len(p.PendingStages) > 0 {
		crew.PendingStages = make([]api.CrewPendingStage, len(p.PendingStages))
		for i := range p.PendingStages {
			ps := &p.PendingStages[i]
			crew.PendingStages[i] = api.CrewPendingStage{
				Name:      ps.Name,
				AgentName: ps.AgentName,
				Role:      ps.Role,
				DependsOn: ps.DependsOn,
			}
		}
	}
	t.PersistCrew(ctx, chatID, crew)
}

// PersistCrew finds-or-creates the per-group crew message and mutates
// it in place. Identical snapshots are short-circuited by JSON-digest
// comparison.
func (t *Translator) PersistCrew(ctx context.Context, chatID api.ChatID, crew *api.Crew) {
	existingID, existingDigest := t.lookupCrewMessage(ctx, chatID, crew.Group)
	newDigest := MarshalCrew(crew)
	if existingID != "" && bytes.Equal(existingDigest, newDigest) {
		return
	}
	if existingID == "" {
		id := NewMessageID()
		evt := api.Message{
			ID:        id,
			Role:      api.RoleEvent,
			Ts:        time.Now().UnixMilli(),
			EventKind: api.EventCrew,
			Crew:      crew,
		}
		if err := t.deps.ChatStore().AppendMessage(ctx, chatID, &evt); err != nil {
			slog.Error("crew: append event", "chat_id", chatID, "error", err)
			return
		}
		t.crewCache.remember(chatID, crew.Group, id)
		return
	}
	if err := t.deps.ChatStore().UpdateMessage(ctx, chatID, existingID, func(m *api.Message) {
		m.Crew = crew
	}); err != nil {
		slog.Error("crew: update event", "chat_id", chatID, "error", err)
	}
}

// lookupCrewMessage tries the in-memory cache first; falls back to a
// history walk on miss.
func (t *Translator) lookupCrewMessage(ctx context.Context, chatID api.ChatID, groupKey string) (id string, digest []byte) {
	if cached, ok := t.crewCache.lookup(chatID, groupKey); ok {
		chat, chatOk := t.deps.ChatStore().Get(ctx, chatID)
		if chatOk {
			for i := range chat.Messages {
				m := &chat.Messages[i]
				if m.ID == cached && m.Crew != nil {
					return cached, MarshalCrew(m.Crew)
				}
			}
		}
	}
	id, digest = t.walkCrewHistory(ctx, chatID, groupKey)
	if id != "" {
		t.crewCache.remember(chatID, groupKey, id)
	}
	return id, digest
}

// walkCrewHistory scans messages in reverse for the latest crew with
// a matching group.
func (t *Translator) walkCrewHistory(ctx context.Context, chatID api.ChatID, groupKey string) (id string, digest []byte) {
	chat, ok := t.deps.ChatStore().Get(ctx, chatID)
	if !ok {
		return "", nil
	}
	for i := range slices.Backward(chat.Messages) {
		m := &chat.Messages[i]
		if m.Role != api.RoleEvent || m.EventKind != api.EventCrew || m.Crew == nil {
			continue
		}
		if m.Crew.Group == groupKey {
			return m.ID, MarshalCrew(m.Crew)
		}
	}
	return "", nil
}

// ClearCrewCache removes all cached entries for the given chatID.
// Called when a chat is deleted or its bridge is closed.
func (t *Translator) ClearCrewCache(chatID api.ChatID) {
	t.crewCache.ClearChat(chatID)
}

// LookupCrewCache returns the cached crew message ID for a chat+group.
func (t *Translator) LookupCrewCache(chatID api.ChatID, groupKey string) (id string, ok bool) {
	return t.crewCache.lookup(chatID, groupKey)
}
