package testsupport

import (
	"context"
	"slices"
	"strings"
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
	chats map[vibekit.ChatID]*vibekit.Chat
	mu    sync.Mutex
}

// NewInMemoryChatStore returns a ready-to-use InMemoryChatStore.
func NewInMemoryChatStore() *InMemoryChatStore {
	return &InMemoryChatStore{chats: make(map[vibekit.ChatID]*vibekit.Chat)}
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

// BuildHistory returns the plain-text transcript for the chat with the given id.
func (s *InMemoryChatStore) BuildHistory(_ context.Context, id vibekit.ChatID) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.chats[id]
	if !ok {
		return ""
	}
	var b strings.Builder
	for i := range c.Messages {
		m := &c.Messages[i]
		b.WriteString(string(m.Role))
		b.WriteString(": ")
		b.WriteString(m.Content)
		b.WriteByte('\n')
	}
	return b.String()
}

// Mutate applies the mutate function to the chat with the given id, creating it if needed.
func (s *InMemoryChatStore) Mutate(_ context.Context, id vibekit.ChatID, mutate func(*vibekit.Chat, bool) bool) error {
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
		return nil
	}
	c.UpdatedAt = time.Now().UnixMilli()
	s.chats[id] = &c
	if s.Bus != nil {
		if !exists {
			s.Bus.Broadcast(context.Background(), vibekit.ServerEvent{Type: "chat_created", ChatID: id, Payload: c.Header()})
		} else {
			s.Bus.Broadcast(context.Background(), vibekit.ServerEvent{Type: "chat_updated", ChatID: id, Payload: c.Header()})
		}
	}
	return nil
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
	return &state, nil
}

// Delete removes the chat with the given id and broadcasts a chat_deleted event.
func (s *InMemoryChatStore) Delete(_ context.Context, id vibekit.ChatID) error {
	s.mu.Lock()
	delete(s.chats, id)
	s.mu.Unlock()
	if s.Bus != nil {
		s.Bus.Broadcast(context.Background(), vibekit.ServerEvent{Type: "chat_deleted", ChatID: id, Payload: map[string]string{"id": string(id)}})
	}
	return nil
}

// AppendMessage appends a message to the stored chat and broadcasts message_appended.
func (s *InMemoryChatStore) AppendMessage(_ context.Context, chatID vibekit.ChatID, msg *vibekit.Message) error {
	return s.Mutate(context.Background(), chatID, func(c *vibekit.Chat, exists bool) bool {
		if !exists {
			return false
		}
		c.Messages = append(c.Messages, *msg)
		if s.Bus != nil {
			s.Bus.Broadcast(context.Background(), vibekit.ServerEvent{Type: "message_appended", ChatID: chatID, Payload: msg})
		}
		return true
	})
}

// UpdateMessage applies mutate to the message identified by msgID within the stored chat.
func (s *InMemoryChatStore) UpdateMessage(_ context.Context, chatID vibekit.ChatID, msgID string, mutate func(*vibekit.Message)) error {
	return s.Mutate(context.Background(), chatID, func(c *vibekit.Chat, exists bool) bool {
		if !exists {
			return false
		}
		for i := range c.Messages {
			if c.Messages[i].ID == msgID {
				mutate(&c.Messages[i])
				return true
			}
		}
		return false
	})
}

// Compile-time assertion.
var _ chatStoreUnion = (*InMemoryChatStore)(nil)
