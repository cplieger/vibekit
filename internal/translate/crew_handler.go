package translate

// Subagent orchestration: _kiro.dev/subagent/list_update notifications.

import (
	"bytes"
	"context"
	"log/slog"
	"slices"

	"github.com/cplieger/vibekit/internal/api"
)

// HandleCrewUpdate processes subagent list update notifications.
func (t *Translator) HandleCrewUpdate(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	p, ok := unmarshalParams[CrewNotifPayload](msg, "subagent/list_update")
	if !ok {
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
	crew := crewFromWire(&p)
	t.PersistCrew(ctx, chatID, crew)
}

// PersistCrew finds-or-creates the per-group crew message and mutates
// it in place. Identical snapshots are short-circuited by JSON-digest
// comparison. Concurrent updates for the same chat+group are coalesced
// via singleflight to prevent race conditions on the read-modify-write
// sequence.
func (t *Translator) PersistCrew(ctx context.Context, chatID api.ChatID, crew *api.Crew) {
	sfKey := string(chatID) + ":" + crew.Group
	t.crewSF.Do(sfKey, func() (any, error) { //nolint:errcheck // result unused
		t.doPersistCrew(ctx, chatID, crew)
		return nil, nil
	})
}

func (t *Translator) doPersistCrew(ctx context.Context, chatID api.ChatID, crew *api.Crew) {
	existingID, existingDigest := t.lookupCrewMessage(ctx, chatID, crew.Group)
	newDigest := MarshalCrew(crew)
	if existingID != "" && bytes.Equal(existingDigest, newDigest) {
		return
	}
	if existingID == "" {
		evt := t.newEventMessage(api.EventCrew, "")
		evt.Crew = crew
		if err := t.deps.ChatStore().AppendMessage(ctx, chatID, &evt); err != nil {
			slog.Error("crew: append event", "chat_id", chatID, "error", err)
			return
		}
		t.crewCache.remember(chatID, crew.Group, evt.ID)
		t.persistCrewIndex(ctx, chatID, crew.Group, evt.ID)
		return
	}
	if err := t.deps.ChatStore().UpdateMessage(ctx, chatID, existingID, func(m *api.Message) {
		m.Crew = crew
	}); err != nil {
		slog.Error("crew: update event", "chat_id", chatID, "error", err)
	}
}

// persistCrewIndex updates the chat's CrewMessageIDs map so the index
// survives restarts without requiring a history walk.
func (t *Translator) persistCrewIndex(ctx context.Context, chatID api.ChatID, groupKey, messageID string) {
	_ = t.deps.ChatStore().Mutate(ctx, chatID, func(c *api.Chat, ex bool) bool {
		if !ex {
			return false
		}
		if c.CrewMessageIDs == nil {
			c.CrewMessageIDs = make(map[string]string)
		}
		c.CrewMessageIDs[groupKey] = messageID
		return true
	})
}

// lookupCrewMessage tries the in-memory cache first, then the
// persisted CrewMessageIDs index, and falls back to a bounded
// history walk on miss. Fetches the chat once to avoid redundant
// ChatStore.Get calls across the three lookup paths.
func (t *Translator) lookupCrewMessage(ctx context.Context, chatID api.ChatID, groupKey string) (id string, digest []byte) {
	chat, chatOk := t.deps.ChatStore().Get(ctx, chatID)
	if !chatOk {
		return "", nil
	}

	// In-memory cache hit.
	if cached, ok := t.crewCache.lookup(chatID, groupKey); ok {
		if crew := findCrewMessageByID(chat.Messages, cached); crew != nil {
			return cached, MarshalCrew(crew)
		}
	}
	// Persisted index (warm after restart without a history walk).
	if indexed, ok := indexedCrewID(chat, groupKey); ok {
		if crew := findCrewMessageByID(chat.Messages, indexed); crew != nil {
			t.crewCache.remember(chatID, groupKey, indexed)
			return indexed, MarshalCrew(crew)
		}
	}
	// Fall back to a bounded reverse history walk.
	id, digest = t.walkCrewHistoryFromChat(chat, groupKey)
	if id != "" {
		t.crewCache.remember(chatID, groupKey, id)
	}
	return id, digest
}

// findCrewMessageByID returns the Crew payload of the crew-bearing
// message with the given ID, or nil if no such message exists.
func findCrewMessageByID(messages []api.Message, id string) *api.Crew {
	for i := range messages {
		m := &messages[i]
		if m.ID == id && m.Crew != nil {
			return m.Crew
		}
	}
	return nil
}

// indexedCrewID returns the message ID recorded for groupKey in the
// chat's persisted crew index, reporting false when absent.
func indexedCrewID(chat *api.Chat, groupKey string) (string, bool) {
	if chat.CrewMessageIDs == nil {
		return "", false
	}
	id, ok := chat.CrewMessageIDs[groupKey]
	return id, ok
}

// maxCrewScanDepth caps the reverse history scan to avoid O(n) worst
// case on long chats. A crew message older than this is stale.
const maxCrewScanDepth = 200

// walkCrewHistoryFromChat scans messages in reverse for the latest crew with
// a matching group, bounded by maxCrewScanDepth.
func (t *Translator) walkCrewHistoryFromChat(chat *api.Chat, groupKey string) (id string, digest []byte) {
	scanned := 0
	for i := range slices.Backward(chat.Messages) {
		if scanned >= maxCrewScanDepth {
			break
		}
		scanned++
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
