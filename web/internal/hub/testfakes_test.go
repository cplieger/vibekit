package hub

// Test fakes: in-memory implementations of ACPBridge and ChatStore
// interfaces for use across hub package tests.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"vibekit/internal/api"
)

// --- Fake ACP bridge ---

type fakeBridge struct {
	notifCh   chan *api.RPCResponse
	sessionID string
	modelID   string
	calls     []string
	mu        sync.Mutex
	stopped   bool
	started   bool
}

func newFakeBridge() *fakeBridge {
	return &fakeBridge{
		sessionID: "fake-sess-" + time.Now().Format("150405.000"),
		modelID:   "fake-model",
		notifCh:   make(chan *api.RPCResponse, 16),
	}
}

func (b *fakeBridge) Start(_ context.Context, opts *api.StartOpts) error {
	b.mu.Lock()
	b.started = true
	if opts.SessionID != "" {
		b.sessionID = opts.SessionID
	}
	b.mu.Unlock()
	return nil
}

func (b *fakeBridge) Stop() {
	b.mu.Lock()
	if !b.stopped {
		b.stopped = true
		close(b.notifCh)
	}
	b.mu.Unlock()
}

func (b *fakeBridge) Call(_ context.Context, method string, _ any) (*api.RPCResponse, error) {
	b.mu.Lock()
	b.calls = append(b.calls, method)
	b.mu.Unlock()
	return &api.RPCResponse{Result: json.RawMessage(`{"stopReason":"end_turn"}`)}, nil
}

func (b *fakeBridge) Notify(_ context.Context, _ string, _ any) error { return nil }

func (b *fakeBridge) Respond(_ context.Context, _ int64, _ any, _ error) error { return nil }

func (b *fakeBridge) SessionID() api.SessionID {
	b.mu.Lock()
	defer b.mu.Unlock()
	return api.SessionID(b.sessionID)
}

func (b *fakeBridge) ModelID() api.ModelID {
	b.mu.Lock()
	defer b.mu.Unlock()
	return api.ModelID(b.modelID)
}

func (b *fakeBridge) CurrentMode() string        { return "" }
func (b *fakeBridge) Modes() []api.SessionMode   { return nil }
func (b *fakeBridge) Models() []api.SessionModel { return nil }

func (b *fakeBridge) SetModel(_ context.Context, modelID string) error {
	b.mu.Lock()
	b.modelID = modelID
	b.calls = append(b.calls, "session/set_model")
	b.mu.Unlock()
	return nil
}

func (b *fakeBridge) NotifCh() <-chan *api.RPCResponse { return b.notifCh }


// newNoopBridge returns a zero-value fakeBridge suitable for benchmarks
// where the bridge is never actually called. Replaces the former stubBridge type.
func newNoopBridge() api.ACPBridge { return &fakeBridge{notifCh: make(chan *api.RPCResponse)} }
// --- Fake ChatStore (in-memory, broadcasts via attached Hub) ---

type fakeChatStore struct {
	bus   api.Broadcaster
	chats map[api.ChatID]*api.Chat
	mu    sync.Mutex
}

func newFakeChatStore() *fakeChatStore {
	return &fakeChatStore{chats: make(map[api.ChatID]*api.Chat)}
}

func (s *fakeChatStore) SetBroadcaster(b api.Broadcaster) { s.bus = b }
func (s *fakeChatStore) RegisterRoutes(_ *http.ServeMux)  {}

func (s *fakeChatStore) Get(_ context.Context, id api.ChatID) (*api.Chat, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.chats[id]
	if !ok {
		return nil, false
	}
	clone := *c
	return &clone, true
}

func (s *fakeChatStore) List(_ context.Context) []api.ChatHeader {
	s.mu.Lock()
	defer s.mu.Unlock()
	hs := make([]api.ChatHeader, 0, len(s.chats))
	for _, c := range s.chats {
		hs = append(hs, c.Header())
	}
	return hs
}

func (s *fakeChatStore) BuildHistory(_ context.Context, id api.ChatID) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.chats[id]
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

func (s *fakeChatStore) Mutate(_ context.Context, id api.ChatID, mutate func(*api.Chat, bool) bool) error {
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

func (s *fakeChatStore) Delete(_ context.Context, id api.ChatID) error {
	s.mu.Lock()
	delete(s.chats, id)
	s.mu.Unlock()
	if s.bus != nil {
		s.bus.Broadcast(context.Background(), api.ServerEvent{Type: "chat_deleted", ChatID: id, Payload: map[string]string{"id": string(id)}})
	}
	return nil
}

// The fake has no archive directory, so an archived chat is
// equivalent to a deleted one. Tests that need real archive behaviour
// (summarisation, listing, restore) use the real chat.Store under a
// t.TempDir. These stubs are intentionally minimal; do not add logic
// here without a test that needs it.
func (s *fakeChatStore) Archive(_ context.Context, id api.ChatID) error {
	return s.Delete(context.Background(), id)
}
func (s *fakeChatStore) ListArchived(_ context.Context) []api.ChatHeader       { return nil }
func (s *fakeChatStore) RestoreArchived(_ context.Context, _ api.ChatID) error { return nil }
func (s *fakeChatStore) UpdateArchivedSummary(_ context.Context, _ api.ChatID, _ string) error {
	return nil
}
func (s *fakeChatStore) LoadArchived(_ context.Context, _ api.ChatID) (*api.Chat, error) {
	return nil, nil
}
func (s *fakeChatStore) DeleteArchived(_ context.Context, _ api.ChatID) error { return nil }

func (s *fakeChatStore) AppendMessage(_ context.Context, chatID api.ChatID, msg *api.Message) error {
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

func (s *fakeChatStore) UpdateMessage(_ context.Context, chatID api.ChatID, msgID string, mutate func(*api.Message)) error {
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
