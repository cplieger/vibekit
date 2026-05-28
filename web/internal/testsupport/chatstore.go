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

	"vibekit/internal/api"
)

// NopChatStore is a no-op api.ChatStore implementation for benchmarks.
// Every method returns zero/nil.
type NopChatStore struct{}

func (NopChatStore) RegisterRoutes(*http.ServeMux)                                            {}
func (NopChatStore) SetBroadcaster(api.Broadcaster)                                           {}
func (NopChatStore) Get(context.Context, api.ChatID) (*api.Chat, bool)                        { return nil, false }
func (NopChatStore) List(context.Context) []api.ChatHeader                                    { return nil }
func (NopChatStore) ChildrenOf(context.Context, api.ChatID) []api.ChatID                      { return nil }
func (NopChatStore) BuildHistory(context.Context, api.ChatID) string                          { return "" }
func (NopChatStore) Mutate(context.Context, api.ChatID, func(*api.Chat, bool) bool) error     { return nil }
func (NopChatStore) Delete(context.Context, api.ChatID) error                                 { return nil }
func (NopChatStore) Archive(context.Context, api.ChatID) error                                { return nil }
func (NopChatStore) ListArchived(context.Context) []api.ChatHeader                            { return nil }
func (NopChatStore) RestoreArchived(context.Context, api.ChatID) error                        { return nil }
func (NopChatStore) UpdateArchivedSummary(context.Context, api.ChatID, string) error          { return nil }
func (NopChatStore) LoadArchived(context.Context, api.ChatID) (*api.Chat, error)              { return nil, nil }
func (NopChatStore) DeleteArchived(context.Context, api.ChatID) error                         { return nil }
func (NopChatStore) AppendMessage(context.Context, api.ChatID, *api.Message) error            { return nil }
func (NopChatStore) UpdateMessage(context.Context, api.ChatID, string, func(*api.Message)) error { return nil }

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

func (s *RecordingChatStore) SetBroadcaster(b api.Broadcaster) { s.Bus = b }
func (s *RecordingChatStore) RegisterRoutes(_ *http.ServeMux)  {}

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

func (s *RecordingChatStore) List(_ context.Context) []api.ChatHeader {
	s.mu.Lock()
	defer s.mu.Unlock()
	hs := make([]api.ChatHeader, 0, len(s.Chats))
	for _, c := range s.Chats {
		hs = append(hs, c.Header())
	}
	return hs
}

func (s *RecordingChatStore) ChildrenOf(_ context.Context, parentID api.ChatID) []api.ChatID {
	s.mu.Lock()
	defer s.mu.Unlock()
	var children []api.ChatID
	for _, c := range s.Chats {
		if c.ParentChatID == parentID {
			children = append(children, api.ChatID(c.ID))
		}
	}
	return children
}

func (s *RecordingChatStore) BuildHistory(_ context.Context, id api.ChatID) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.Chats[id]
	if !ok {
		return ""
	}
	var b strings.Builder
	for _, m := range c.Messages {
		b.WriteString(string(m.Role))
		b.WriteString(": ")
		b.WriteString(m.Content)
		b.WriteByte('\n')
	}
	return b.String()
}

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

func (s *RecordingChatStore) Delete(_ context.Context, id api.ChatID) error {
	s.mu.Lock()
	delete(s.Chats, id)
	s.mu.Unlock()
	if s.Bus != nil {
		s.Bus.Broadcast(context.Background(), api.ServerEvent{Type: "chat_deleted", ChatID: id, Payload: map[string]string{"id": string(id)}})
	}
	return nil
}

func (s *RecordingChatStore) Archive(_ context.Context, id api.ChatID) error {
	return s.Delete(context.Background(), id)
}
func (s *RecordingChatStore) ListArchived(_ context.Context) []api.ChatHeader       { return nil }
func (s *RecordingChatStore) RestoreArchived(_ context.Context, _ api.ChatID) error { return nil }
func (s *RecordingChatStore) UpdateArchivedSummary(_ context.Context, _ api.ChatID, _ string) error {
	return nil
}
func (s *RecordingChatStore) LoadArchived(_ context.Context, _ api.ChatID) (*api.Chat, error) {
	return nil, nil
}
func (s *RecordingChatStore) DeleteArchived(_ context.Context, _ api.ChatID) error { return nil }

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
