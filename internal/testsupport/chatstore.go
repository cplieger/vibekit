// Package testsupport provides the chat-store fakes that more than one sibling
// package's TEST binary needs, plus the contract suites those packages run
// against both a fake and the real implementation.
//
// Membership has one rule: a double belongs here only while at least two
// packages consume it. Five members left when that stopped being true —
// NopACPBridge, NopBroadcaster and CaptureBroadcaster had no consumer outside
// their own tests, and NopChatStore and NopMCPRecorder had exactly one, so they
// moved into internal/translate's own _test.go where they could be sized to that
// package's 3-method contract instead of the widest one going.
package testsupport

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/vibekit/internal/api"
)

// chatStoreUnion is the union of what the real consumers declare, spelled out
// here rather than named, so a consumer that grows a method fails to compile
// against these fakes instead of silently outgrowing them. It is
// ChatStoreContract's 7 plus SetDraft, which internal/command needs and the
// contract suite does not exercise.
//
// RegisterRoutes is NOT here, and each fake dropped its no-op: only
// internal/server mounts the chat routes and it does so on the concrete store,
// so both fakes carried a method no test could reach.
type chatStoreUnion interface {
	ChatStoreContract
	SetDraft(ctx context.Context, id api.ChatID, text string) error
}

// RecordingChatStore is an in-memory chat store that keeps chats in a
// map and fires broadcasts via an attached Broadcaster. Suitable for
// integration-style tests that need a ChatStore that actually stores things.
type RecordingChatStore struct {
	// Bus is the fan-out lifecycle events go to. The type is spelled out
	// rather than named because there is no shared Broadcaster interface any
	// more: internal/chat and internal/forges each declare their own 1-method
	// copy, and this is the union of the two.
	Bus interface {
		Broadcast(ctx context.Context, evt api.ServerEvent)
	}
	Chats map[api.ChatID]*api.Chat
	mu    sync.Mutex
}

// NewRecordingChatStore returns a ready-to-use RecordingChatStore.
func NewRecordingChatStore() *RecordingChatStore {
	return &RecordingChatStore{Chats: make(map[api.ChatID]*api.Chat)}
}

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
// broadcasting, which is the contract (*chat.Store).SetDraft holds and the three
// interfaces naming it — hub/deps.go, command/deps.go and chatStoreUnion above —
// depend on. A fake that went through Mutate would stamp activity and make a
// test unable to observe the one property the real method exists to hold.
// Absent chat: no-op, like the real store's load-then-write.
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
var _ chatStoreUnion = (*RecordingChatStore)(nil)
