package testsupport

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// InMemoryChatStore is a functional in-memory chat store with broadcast
// support. Suitable for integration-style tests that need real Mutate/Get
// semantics without filesystem I/O. Assign Bus to fan out lifecycle events
// (same shape as RecordingChatStore).
type InMemoryChatStore struct {
	// Bus is the fan-out lifecycle events go to; see RecordingChatStore.Bus
	// for why the type is spelled out rather than named.
	Bus interface {
		Broadcast(ctx context.Context, evt vibekit.ServerEvent)
	}
	chats    map[vibekit.ChatID]*vibekit.Chat
	versions chatVersions
	mu       sync.Mutex
}

// NewInMemoryChatStore returns a ready-to-use InMemoryChatStore.
func NewInMemoryChatStore() *InMemoryChatStore {
	return &InMemoryChatStore{chats: make(map[vibekit.ChatID]*vibekit.Chat)}
}

// Exists reports whether the fake holds id.
func (s *InMemoryChatStore) Exists(id vibekit.ChatID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.chats[id]
	return ok
}

// Get returns a copy of the stored chat for id, or (nil, false) if not found.
func (s *InMemoryChatStore) Get(_ context.Context, id vibekit.ChatID) (*vibekit.Chat, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.chats[id]
	if !ok {
		return nil, false
	}
	return cloneChat(c), true
}

// List returns headers for all stored chats.
func (s *InMemoryChatStore) List(_ context.Context) []vibekit.ChatHeader {
	s.mu.Lock()
	defer s.mu.Unlock()
	hs := make([]vibekit.ChatHeader, 0, len(s.chats))
	for _, c := range s.chats {
		hs = append(hs, c.Header())
	}
	return hs
}

// Mutate applies the mutate function to the chat with the given id, creating it if needed.
func (s *InMemoryChatStore) Mutate(_ context.Context, id vibekit.ChatID, mutate func(*vibekit.Chat, bool) bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	orig, exists := s.chats[id]
	var c vibekit.Chat
	if exists {
		c = *orig
	} else {
		c = vibekit.Chat{ID: string(id), CreatedAt: time.Now().UnixMilli()}
	}
	if !mutate(&c, exists) {
		return "", nil
	}
	c.UpdatedAt = time.Now().UnixMilli()
	s.chats[id] = &c
	version := s.versions.bump(id)
	if s.Bus != nil {
		evt := vibekit.EventChatUpdated
		if !exists {
			evt = vibekit.EventChatCreated
		}
		s.Bus.Broadcast(context.Background(), vibekit.ServerEvent{Type: evt, ChatID: id, Payload: c.Header()})
	}
	return version, nil
}

// SetDraft stores the chat's draft without touching UpdatedAt and without
// broadcasting; see (*chat.Store).SetDraft for why those two absences are the point.
// Absent chat: no-op, like the real store's load-then-write. Reports the state
// that landed, nil when nothing did.
func (s *InMemoryChatStore) SetDraft(_ context.Context, id vibekit.ChatID, text string) (*vibekit.ComposerState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.chats[id]
	if !ok || c.Draft == text {
		return nil, nil
	}
	c.Draft = text
	state := c.Composer()
	state.Version = s.versions.bump(id)
	return &state, nil
}

// SetAttachments stores the paths staged beside the draft under the same
// contract: no UpdatedAt, no broadcast, no-op on an absent chat.
func (s *InMemoryChatStore) SetAttachments(_ context.Context, id vibekit.ChatID, paths []string) (*vibekit.ComposerState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.chats[id]
	if !ok {
		return nil, nil
	}
	next := slices.Clone(paths)
	if len(next) == 0 {
		next = nil
	}
	if slices.Equal(c.Attachments, next) {
		return nil, nil
	}
	c.Attachments = next
	state := c.Composer()
	state.Version = s.versions.bump(id)
	return &state, nil
}

// Delete removes the chat with the given id and broadcasts a chat_deleted event.
func (s *InMemoryChatStore) Delete(_ context.Context, id vibekit.ChatID) error {
	s.mu.Lock()
	delete(s.chats, id)
	s.mu.Unlock()
	if s.Bus != nil {
		s.Bus.Broadcast(context.Background(), vibekit.ServerEvent{Type: vibekit.EventChatDeleted, ChatID: id, Payload: map[string]string{"id": string(id)}})
	}
	return nil
}

// AppendMessage appends a message to the stored chat and broadcasts message_appended.
func (s *InMemoryChatStore) AppendMessage(_ context.Context, chatID vibekit.ChatID, msg *vibekit.Message) error {
	var appended bool
	version, err := s.Mutate(context.Background(), chatID, func(c *vibekit.Chat, exists bool) bool {
		if !exists {
			return false
		}
		c.Messages = append(c.Messages, *msg)
		appended = true
		return true
	})
	if err != nil || !appended || s.Bus == nil {
		return err
	}
	s.Bus.Broadcast(context.Background(), stamped(vibekit.ServerEvent{Type: vibekit.EventMessageAppended, ChatID: chatID, Payload: msg}, chatID, version))
	return nil
}

// UpsertTurnPlan overwrites this turn's plan row, or appends msg when the turn
// carries none. Mirrors (*chat.Store).UpsertTurnPlan; the turn boundary is the
// first user message walking back from the tail.
func (s *InMemoryChatStore) UpsertTurnPlan(_ context.Context, chatID vibekit.ChatID, msg *vibekit.Message) error {
	return upsertTurnPlan(s.Mutate, s.Bus, chatID, msg)
}

// UpdateMessage applies mutate to the message identified by msgID within the stored chat.
func (s *InMemoryChatStore) UpdateMessage(_ context.Context, chatID vibekit.ChatID, msgID string, mutate func(*vibekit.Message)) error {
	var updated *vibekit.Message
	version, err := s.Mutate(context.Background(), chatID, func(c *vibekit.Chat, exists bool) bool {
		if !exists {
			return false
		}
		for i := range c.Messages {
			if c.Messages[i].ID == msgID {
				mutate(&c.Messages[i])
				updated = &c.Messages[i]
				return true
			}
		}
		return false
	})
	if err != nil || updated == nil || s.Bus == nil {
		return err
	}
	s.Bus.Broadcast(context.Background(), stamped(vibekit.ServerEvent{Type: vibekit.EventMessageUpdated, ChatID: chatID, Payload: updated}, chatID, version))
	return nil
}

// Compile-time assertion.
var _ chatStoreUnion = (*InMemoryChatStore)(nil)
