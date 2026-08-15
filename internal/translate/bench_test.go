package translate

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/buffer"
	"github.com/cplieger/vibekit/internal/testsupport"
)

// baseDeps is a composable Deps implementation for tests and benchmarks.
// By default all methods are no-ops; set the hook fields to override
// specific behaviors (e.g. onBroadcast to capture events).
type baseDeps struct {
	store       api.ChatStore
	bufStore    *buffer.Store
	lineTracker *buffer.LineTracker
	onBroadcast func(context.Context, api.ServerEvent)
	// onSetGovernance, when set, is invoked by SetGovernance so a test can
	// assert the hub-side cache write (mirrors onBroadcast).
	onSetGovernance func(api.GovernanceStatePayload)
	// parent is returned by ParentACPSession; zero value "" preserves the
	// historical "parent unknown" behavior for existing callers.
	parent string
}

func newBaseDeps() *baseDeps {
	return &baseDeps{
		store:       testsupport.NopChatStore{},
		bufStore:    buffer.NewStore(),
		lineTracker: buffer.NewLineTracker(),
	}
}

func (d *baseDeps) Broadcast(ctx context.Context, evt api.ServerEvent) {
	if d.onBroadcast != nil {
		d.onBroadcast(ctx, evt)
	}
}
func (d *baseDeps) ChatStore() api.ChatStore           { return d.store }
func (d *baseDeps) ParentACPSession(api.ChatID) string { return d.parent }
func (d *baseDeps) WorkDir() string                    { return "/tmp" }
func (d *baseDeps) BridgeNotify(context.Context, api.ChatID, string, map[string]any) error {
	return nil
}
func (d *baseDeps) BridgeRespond(context.Context, api.ChatID, int64, any, error) error { return nil }
func (d *baseDeps) MCPRecorder() MCPRecorder                                           { return &testsupport.NopMCPRecorder{} }
func (d *baseDeps) SetGovernance(g api.GovernanceStatePayload) {
	if d.onSetGovernance != nil {
		d.onSetGovernance(g)
	}
}
func (d *baseDeps) PendingPermsAdd(int64, api.ServerEvent)                       {}
func (d *baseDeps) PendingPermsRemove(int64)                                     {}
func (d *baseDeps) NotifyPush(context.Context, string, api.PushKind, api.ChatID) {}
func (d *baseDeps) BufferStore() BufferAccess                                    { return d.bufStore }
func (d *baseDeps) LineTracker() LineRecorder                                    { return d.lineTracker }
func (d *baseDeps) IsHookStatusEnabled() bool                                    { return false }

var toolCallPayload = json.RawMessage(`{"toolCallId":"tc-1","title":"ReadFile","kind":"read","status":"pending","rawInput":{},"locations":[],"content":[{"type":"text","content":{"text":"reading file"}}]}`)

func BenchmarkTranslator_HandleToolCall(b *testing.B) {
	tr := New(newBaseDeps(), withIDGenerator(func() string { return "stub-msg-id" }))
	ctx := b.Context()
	chatID := api.ChatID("bench-chat")

	b.ResetTimer()
	for b.Loop() {
		tr.HandleToolCall(ctx, chatID, toolCallPayload, "")
	}
}

// BenchmarkTranslator_HandleAssistantChunk measures per-token allocation
// overhead on the steady-state path (buffer already started).
func BenchmarkTranslator_HandleAssistantChunk(b *testing.B) {
	deps := newBaseDeps()
	tr := New(deps, withIDGenerator(func() string { return "stub-msg-id" }))
	ctx := b.Context()
	chatID := api.ChatID("bench-chunk")

	chunkPayload := json.RawMessage(`{"content":{"type":"text","text":"Hello world, this is a streaming token. "}}`)

	// Prime the buffer with a first chunk so subsequent iterations hit the
	// steady-state path (no message creation overhead).
	tr.HandleAssistantChunk(ctx, chatID, chunkPayload, false)

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		tr.HandleAssistantChunk(ctx, chatID, chunkPayload, false)
	}
}

// BenchmarkTranslator_FullTurn simulates a complete streaming turn:
// 50 text chunks → 1 tool call → 1 tool call update → 50 more chunks.
// Measures end-to-end throughput including buffer management.
func BenchmarkTranslator_FullTurn(b *testing.B) {
	deps := newBaseDeps()
	tr := New(deps, withIDGenerator(func() string { return "stub-msg-id" }))
	ctx := b.Context()

	chunkPayload := json.RawMessage(`{"content":{"type":"text","text":"Hello world, this is a streaming token. "}}`)
	toolCallPL := toolCallPayload
	toolUpdatePL := json.RawMessage(`{"toolCallId":"tc-1","status":"completed","content":[{"type":"text","content":{"text":"done"}}]}`)

	b.ResetTimer()
	for b.Loop() {
		chatID := api.ChatID("bench-turn")
		// Phase 1: initial streaming chunks
		for range 50 {
			tr.HandleAssistantChunk(ctx, chatID, chunkPayload, false)
		}
		// Phase 2: tool call
		tr.HandleToolCall(ctx, chatID, toolCallPL, "")
		tr.HandleToolCallUpdate(ctx, chatID, toolUpdatePL, "")
		// Phase 3: more streaming
		for range 50 {
			tr.HandleAssistantChunk(ctx, chatID, chunkPayload, false)
		}
		// Cleanup: reset buffer for next iteration
		deps.bufStore.Delete(chatID)
	}
}

func BenchmarkTranslator_HandleUsageUpdate(b *testing.B) {
	deps := newBaseDeps()
	tr := New(deps, withIDGenerator(func() string { return "stub-msg-id" }))
	ctx := b.Context()
	chatID := api.ChatID("bench-usage")
	raw := json.RawMessage(`{"size":100000,"used":42500}`)

	// Pre-create a chat so Mutate finds it.
	_ = deps.store.Mutate(ctx, chatID, func(_ *api.Chat, _ bool) bool { return true })

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		tr.HandleUsageUpdate(ctx, chatID, raw)
	}
}
