package testsupport

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"vibekit/internal/api"
)

// InMemoryChatStore is a functional in-memory api.ChatStore with broadcast
// support. Suitable for integration-style tests that need real Mutate/Get
// semantics without filesystem I/O.
type InMemoryChatStore struct {
	bus   api.Broadcaster
	chats map[api.ChatID]*api.Chat
	mu    sync.Mutex
}

// NewInMemoryChatStore returns a ready-to-use InMemoryChatStore.
func NewInMemoryChatStore() *InMemoryChatStore {
	return &InMemoryChatStore{chats: make(map[api.ChatID]*api.Chat)}
}

func (s *InMemoryChatStore) SetBroadcaster(b api.Broadcaster) { s.bus = b }
func (s *InMemoryChatStore) RegisterRoutes(_ *http.ServeMux)   {}

func (s *InMemoryChatStore) Get(_ context.Context, id api.ChatID) (*api.Chat, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.chats[id]
	if !ok {
		return nil, false
	}
	clone := *c
	return &clone, true
}

func (s *InMemoryChatStore) List(_ context.Context) []api.ChatHeader {
	s.mu.Lock()
	defer s.mu.Unlock()
	hs := make([]api.ChatHeader, 0, len(s.chats))
	for _, c := range s.chats {
		hs = append(hs, c.Header())
	}
	return hs
}

func (s *InMemoryChatStore) ChildrenOf(_ context.Context, parentID api.ChatID) []api.ChatID {
	s.mu.Lock()
	defer s.mu.Unlock()
	var children []api.ChatID
	for _, c := range s.chats {
		if c.ParentChatID == parentID {
			children = append(children, api.ChatID(c.ID))
		}
	}
	return children
}

func (s *InMemoryChatStore) BuildHistory(_ context.Context, id api.ChatID) string {
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

func (s *InMemoryChatStore) Mutate(_ context.Context, id api.ChatID, mutate func(*api.Chat, bool) bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	orig, exists := s.chats[id]
	var c api.Chat
	if exists {
		c = *orig
	} else {
		c = api.Chat{ID: string(id), CreatedAt: time.Now().UnixMilli()}
	}
	if !mutate(&c, exists) {
		return nil
	}
	c.UpdatedAt = time.Now().UnixMilli()
	s.chats[id] = &c
	if s.bus != nil {
		if !exists {
			s.bus.Broadcast(context.Background(), api.ServerEvent{Type: "chat_created", ChatID: id, Payload: c.Header()})
		} else {
			s.bus.Broadcast(context.Background(), api.ServerEvent{Type: "chat_updated", ChatID: id, Payload: c.Header()})
		}
	}
	return nil
}

func (s *InMemoryChatStore) Delete(_ context.Context, id api.ChatID) error {
	s.mu.Lock()
	delete(s.chats, id)
	s.mu.Unlock()
	if s.bus != nil {
		s.bus.Broadcast(context.Background(), api.ServerEvent{Type: "chat_deleted", ChatID: id, Payload: map[string]string{"id": string(id)}})
	}
	return nil
}

func (s *InMemoryChatStore) Archive(_ context.Context, id api.ChatID) error {
	return s.Delete(context.Background(), id)
}

func (s *InMemoryChatStore) ListArchived(_ context.Context) []api.ChatHeader       { return nil }
func (s *InMemoryChatStore) RestoreArchived(_ context.Context, _ api.ChatID) error { return nil }
func (s *InMemoryChatStore) UpdateArchivedSummary(_ context.Context, _ api.ChatID, _ string) error {
	return nil
}
func (s *InMemoryChatStore) LoadArchived(_ context.Context, _ api.ChatID) (*api.Chat, error) {
	return nil, nil
}
func (s *InMemoryChatStore) DeleteArchived(_ context.Context, _ api.ChatID) error { return nil }

func (s *InMemoryChatStore) AppendMessage(_ context.Context, chatID api.ChatID, msg *api.Message) error {
	return s.Mutate(context.Background(), chatID, func(c *api.Chat, exists bool) bool {
		if !exists {
			return false
		}
		c.Messages = append(c.Messages, *msg)
		if s.bus != nil {
			s.bus.Broadcast(context.Background(), api.ServerEvent{Type: "message_appended", ChatID: chatID, Payload: msg})
		}
		return true
	})
}

func (s *InMemoryChatStore) UpdateMessage(_ context.Context, chatID api.ChatID, msgID string, mutate func(*api.Message)) error {
	return s.Mutate(context.Background(), chatID, func(c *api.Chat, exists bool) bool {
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
var _ api.ChatStore = (*InMemoryChatStore)(nil)
