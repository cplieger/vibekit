package hub

// Test fakes: in-memory implementations of ACPBridge and ChatStore
// interfaces for use across hub package tests.

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/testsupport"
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

func (b *fakeBridge) SupportsDocuments() bool { return false }

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

// --- Fake ChatStore (delegates to testsupport.RecordingChatStore) ---

type fakeChatStore = testsupport.RecordingChatStore

func newFakeChatStore() *fakeChatStore {
	return testsupport.NewRecordingChatStore()
}
