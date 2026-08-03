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
	notifCh      chan *api.RPCResponse
	callResults  map[string]json.RawMessage
	callErrs     map[string]error
	lastParams   map[string]map[string]any
	callDeadline map[string]bool
	chunksOnCall map[string][]string
	// blockOn optionally makes Call block (after recording the call) until
	// the method's channel is closed — for tests proving concurrency
	// properties (e.g. RPC reads completing while a text turn is in flight).
	blockOn      map[string]chan struct{}
	sessionID    string
	modelID      string
	sessionTitle string
	calls        []string
	// startOpts records the StartOpts of the most recent Start, so a test can
	// assert what a spawn was actually handed (e.g. that the utility bridge
	// gets no operator launch flags).
	startOpts *api.StartOpts
	mu        sync.Mutex
	responds  int
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
	b.startOpts = opts
	if opts.SessionID != "" {
		b.sessionID = opts.SessionID
	}
	b.mu.Unlock()
	return nil
}

// lastStartOpts returns the StartOpts of the most recent Start, or nil.
func (b *fakeBridge) lastStartOpts() *api.StartOpts {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.startOpts
}

func (b *fakeBridge) Stop() {
	b.mu.Lock()
	if !b.stopped {
		b.stopped = true
		close(b.notifCh)
	}
	b.mu.Unlock()
}

func (b *fakeBridge) Call(ctx context.Context, method string, params any) (*api.RPCResponse, error) {
	b.mu.Lock()
	b.calls = append(b.calls, method)
	if p, ok := params.(map[string]any); ok {
		if b.lastParams == nil {
			b.lastParams = map[string]map[string]any{}
		}
		b.lastParams[method] = p
	}
	if b.callDeadline == nil {
		b.callDeadline = map[string]bool{}
	}
	_, hasDeadline := ctx.Deadline()
	b.callDeadline[method] = hasDeadline
	if err, ok := b.callErrs[method]; ok {
		b.mu.Unlock()
		return nil, err
	}
	res := json.RawMessage(`{"stopReason":"end_turn"}`)
	if r, ok := b.callResults[method]; ok {
		res = r
	}
	chunks := b.chunksOnCall[method]
	blocker := b.blockOn[method]
	b.mu.Unlock()
	// Block OUTSIDE the fake's mutex so concurrent Calls on other methods
	// proceed (mirrors the real bridge, whose Call blocks per-request).
	if blocker != nil {
		select {
		case <-blocker:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	// Deliver configured response chunks on notifCh AFTER unlocking so they
	// arrive after the caller's Call begins (the forward goroutine moves them
	// to responseCh). notifCh is buffered, so this doesn't block.
	for _, text := range chunks {
		p, _ := json.Marshal(map[string]any{
			"sessionUpdate": "agent_message_chunk",
			"content":       map[string]any{"type": "text", "text": text},
		})
		b.notifCh <- &api.RPCResponse{Method: "session/update", Params: p}
	}
	return &api.RPCResponse{Result: res}, nil
}

// paramsFor returns the params captured for the most recent Call to method.
func (b *fakeBridge) paramsFor(method string) map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastParams[method]
}

// callHadDeadline reports whether the most recent Call to method ran with a
// context deadline (proving the caller wrapped it in a timeout).
func (b *fakeBridge) callHadDeadline(method string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.callDeadline[method]
}

func (b *fakeBridge) Notify(_ context.Context, _ string, _ any) error { return nil }

func (b *fakeBridge) Respond(_ context.Context, _ int64, _ any, _ error) error {
	b.mu.Lock()
	b.responds++
	b.mu.Unlock()
	return nil
}

// respondCount reports how many A→C requests were answered on this bridge.
func (b *fakeBridge) respondCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.responds
}

// callLog snapshots the ordered method names Call received.
func (b *fakeBridge) callLog() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.calls))
	copy(out, b.calls)
	return out
}

// lastCall is the most recent Call's method, or "".
func (b *fakeBridge) lastCall() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.calls) == 0 {
		return ""
	}
	return b.calls[len(b.calls)-1]
}

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

func (b *fakeBridge) CurrentMode() string { return "" }

// SessionTitle reports KAS's own session title. The fake returns the value
// tests set on it so the bridge_coord adoption guard can be exercised.
func (b *fakeBridge) SessionTitle() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sessionTitle
}

func (b *fakeBridge) Modes() []api.SessionMode   { return nil }
func (b *fakeBridge) Models() []api.SessionModel { return nil }

func (b *fakeBridge) SetModel(_ context.Context, modelID string) error {
	b.mu.Lock()
	b.modelID = modelID
	b.calls = append(b.calls, "session/set_config_option")
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
