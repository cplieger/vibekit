package pending

import "vibekit/internal/api"

// ListForChat returns snapshots of every pending op for the chat in
// insertion order. Used by SSE reconnect replay. Safe to call from any
// goroutine.
func (s *Store) ListForChat(chatID api.ChatID) []api.PendingChange {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := s.byChat[chatID]
	out := make([]api.PendingChange, 0, len(ids))
	for _, id := range ids {
		if op, ok := s.ops[id]; ok {
			out = append(out, op.Snapshot())
		}
	}
	return out
}

// Get returns the snapshot of one op, or ok=false if it's not pending.
// Used by the editor's pending-diff virtual path endpoint.
func (s *Store) Get(toolCallID string) (api.PendingChange, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	op, ok := s.ops[toolCallID]
	if !ok {
		return api.PendingChange{}, false
	}
	return op.Snapshot(), true
}

// CountForChat returns the number of pending ops for the chat.
// Cheap O(1) read used by tests; not part of the hot path.
func (s *Store) CountForChat(chatID api.ChatID) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.byChat[chatID])
}

// ChatIDs returns the set of chat IDs that currently have at least one
// pending op. O(1) bounded by pending ops (not total chats). Used by
// the hub's replay path to avoid scanning the entire chat directory.
func (s *Store) ChatIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.byChat))
	for id := range s.byChat {
		ids = append(ids, string(id))
	}
	return ids
}
