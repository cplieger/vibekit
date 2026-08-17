// Package testsupport provides shared test fakes for interfaces defined in
// the api package. These are intended for use across multiple test packages
// to avoid duplicating interface implementations.
package testsupport

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/vibekit/internal/api"
)

// NopChatStore is a no-op api.ChatStore implementation for benchmarks.
// Every method returns zero/nil.
type NopChatStore struct{}

// RegisterRoutes is a no-op; implements api.ChatStore.
func (NopChatStore) RegisterRoutes(*http.ServeMux) {}

// Get returns (nil, false); implements api.ChatStore.
func (NopChatStore) Get(context.Context, api.ChatID) (*api.Chat, bool) { return nil, false }

// List returns nil; implements api.ChatStore.
func (NopChatStore) List(context.Context) []api.ChatHeader { return nil }

// BuildHistory returns an empty string; implements api.ChatStore.
func (NopChatStore) BuildHistory(context.Context, api.ChatID) string { return "" }

// Mutate is a no-op; implements api.ChatStore.
func (NopChatStore) Mutate(context.Context, api.ChatID, func(*api.Chat, bool) bool) error {
	return nil
}

// SetDraft is a no-op; implements api.ChatStore.
func (NopChatStore) SetDraft(context.Context, api.ChatID, string) error { return nil }

// Delete is a no-op; implements api.ChatStore.
func (NopChatStore) Delete(context.Context, api.ChatID) error { return nil }

// AppendMessage is a no-op; implements api.ChatStore.
func (NopChatStore) AppendMessage(context.Context, api.ChatID, *api.Message) error { return nil }

// UpdateMessage is a no-op; implements api.ChatStore.
func (NopChatStore) UpdateMessage(context.Context, api.ChatID, string, func(*api.Message)) error {
	return nil
}

// Compile-time assertion.
var _ api.ChatStore = NopChatStore{}

// RecordingChatStore is an in-memory api.ChatStore that stores chats in a
// map and fires broadcasts via an attached Broadcaster. Suitable for
// integration-style tests that need a ChatStore that actually stores things.
type RecordingChatStore struct {
	Bus   api.Broadcaster
	Chats map[api.ChatID]*api.Chat
	mu    sync.Mutex
}

// NewRecordingChatStore returns a ready-to-use RecordingChatStore.
func NewRecordingChatStore() *RecordingChatStore {
	return &RecordingChatStore{Chats: make(map[api.ChatID]*api.Chat)}
}

// RegisterRoutes is a no-op; implements api.ChatStore.
func (s *RecordingChatStore) RegisterRoutes(_ *http.ServeMux) {}

// Get returns a copy of the stored chat for id, or (nil, false) if not found.
func (s *RecordingChatStore) Get(_ context.Context, id api.ChatID) (*api.Chat, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.Chats[id]
	if !ok {
		return nil, false
	}
	clone := *c
	return &clone, true
}

// List returns headers for all stored chats.
func (s *RecordingChatStore) List(_ context.Context) []api.ChatHeader {
	s.mu.Lock()
	defer s.mu.Unlock()
	hs := make([]api.ChatHeader, 0, len(s.Chats))
	for _, c := range s.Chats {
		hs = append(hs, c.Header())
	}
	return hs
}

// BuildHistory returns the plain-text transcript for the chat with the given id.
func (s *RecordingChatStore) BuildHistory(_ context.Context, id api.ChatID) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.Chats[id]
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
func (s *RecordingChatStore) Mutate(_ context.Context, id api.ChatID, mutate func(*api.Chat, bool) bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	orig, exists := s.Chats[id]
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
	s.Chats[id] = &c
	if s.Bus != nil {
		if !exists {
			s.Bus.Broadcast(context.Background(), api.ServerEvent{Type: "chat_created", ChatID: id, Payload: c.Header()})
		} else {
			s.Bus.Broadcast(context.Background(), api.ServerEvent{Type: "chat_updated", ChatID: id, Payload: c.Header()})
		}
	}
	return nil
}

// SetDraft stores the chat's draft without touching UpdatedAt and without
// broadcasting, which is the contract api.ChatStore states for it. A fake that
// went through Mutate would stamp activity and make a test unable to observe the
// one property the real method exists to hold. Absent chat: no-op, like the real
// store's load-then-write.
func (s *RecordingChatStore) SetDraft(_ context.Context, id api.ChatID, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.Chats[id]; ok {
		c.Draft = text
	}
	return nil
}

// Delete removes the chat with the given id and broadcasts a chat_deleted event.
func (s *RecordingChatStore) Delete(_ context.Context, id api.ChatID) error {
	s.mu.Lock()
	delete(s.Chats, id)
	s.mu.Unlock()
	if s.Bus != nil {
		s.Bus.Broadcast(context.Background(), api.ServerEvent{Type: "chat_deleted", ChatID: id, Payload: map[string]string{"id": string(id)}})
	}
	return nil
}

// AppendMessage appends a message to the stored chat and broadcasts message_appended.
func (s *RecordingChatStore) AppendMessage(_ context.Context, chatID api.ChatID, msg *api.Message) error {
	return s.Mutate(context.Background(), chatID, func(c *api.Chat, exists bool) bool {
		if !exists {
			return false
		}
		c.Messages = append(c.Messages, *msg)
		if s.Bus != nil {
			s.Bus.Broadcast(context.Background(), api.ServerEvent{Type: "message_appended", ChatID: chatID, Payload: msg})
		}
		return true
	})
}

// UpdateMessage applies mutate to the message identified by msgID within the stored chat.
func (s *RecordingChatStore) UpdateMessage(_ context.Context, chatID api.ChatID, msgID string, mutate func(*api.Message)) error {
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
var _ api.ChatStore = (*RecordingChatStore)(nil)
