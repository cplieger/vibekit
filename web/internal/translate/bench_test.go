package translate

import (
	"context"
	"encoding/json"
	"testing"

	"vibekit/internal/api"
	"vibekit/internal/buffer"
	"vibekit/internal/permissions"
	"vibekit/internal/testsupport"
)

// baseDeps is a composable Deps implementation for tests and benchmarks.
// By default all methods are no-ops; set the hook fields to override
// specific behaviors (e.g. onBroadcast to capture events).
type baseDeps struct {
	store       api.ChatStore
	bufStore    *buffer.Store
	lineTracker *buffer.LineTracker
	onBroadcast func(context.Context, api.ServerEvent)
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
func (d *baseDeps) ChatStore() api.ChatStore             { return d.store }
func (d *baseDeps) NewMessageID() string                 { return "stub-msg-id" }
func (d *baseDeps) ParentACPSession(api.ChatID) string   { return "" }
func (d *baseDeps) WorkDir() string                      { return "/tmp" }
func (d *baseDeps) BridgeNotify(context.Context, api.ChatID, string, map[string]any) error {
	return nil
}
func (d *baseDeps) BridgeRespond(context.Context, api.ChatID, int64, any, error) error { return nil }
func (d *baseDeps) MCPRecorder() MCPRecorder                                           { return &stubMCPRecorder{} }
func (d *baseDeps) PendingPermsAdd(int64, api.ServerEvent)                             {}
func (d *baseDeps) PendingPermsRemove(int64)                                           {}
func (d *baseDeps) NotifyPush(context.Context, string, api.PushKind)                   {}
func (d *baseDeps) ConfigDir() string                                                  { return "/tmp" }
func (d *baseDeps) PermissionRules() *permissions.CommandRules                         { return nil }
func (d *baseDeps) BufferStore() BufferAccess                                          { return d.bufStore }
func (d *baseDeps) LineTracker() LineRecorder                                           { return d.lineTracker }
func (d *baseDeps) OpenPartialFile(context.Context, api.ChatID, *buffer.Buffer)        {}
func (d *baseDeps) IsHookStatusEnabled() bool                                          { return false }

var crewPayload = json.RawMessage(`{"subagents":[{"sessionId":"s1","sessionName":"worker","agentName":"coder","initialQuery":"fix bug","group":"g1","role":"worker","status":{"type":"running","message":"coding"}}]}`)

var commandsPayload = json.RawMessage(`{"commands":[{"name":"/help","description":"Show help"}],"prompts":[],"tools":["t1","t2"],"mcpServers":[{"name":"srv","status":"running","tools":["a"]}]}`)

var toolCallPayload = json.RawMessage(`{"toolCallId":"tc-1","title":"ReadFile","kind":"read","status":"pending","rawInput":{},"locations":[],"content":[{"type":"text","content":{"text":"reading file"}}]}`)

func BenchmarkTranslator_HandleCrewUpdate(b *testing.B) {
	tr := New(newBaseDeps())
	msg := &api.RPCResponse{Params: crewPayload}
	ctx := context.Background()
	chatID := api.ChatID("bench-chat")

	b.ResetTimer()
	for b.Loop() {
		tr.HandleCrewUpdate(ctx, chatID, msg)
	}
}

func BenchmarkTranslator_HandleCommandsAvailable(b *testing.B) {
	tr := New(newBaseDeps())
	msg := &api.RPCResponse{Params: commandsPayload}
	ctx := context.Background()
	chatID := api.ChatID("bench-chat")

	b.ResetTimer()
	for b.Loop() {
		tr.HandleCommandsAvailable(ctx, chatID, msg)
	}
}

func BenchmarkTranslator_HandleToolCall(b *testing.B) {
	tr := New(newBaseDeps())
	ctx := context.Background()
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
	tr := New(deps)
	ctx := context.Background()
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
	tr := New(deps)
	ctx := context.Background()

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

// stubMCPRecorder is a no-op MCPRecorder for tests.
type stubMCPRecorder struct{}

func (*stubMCPRecorder) RecordConnected(context.Context, string)           {}
func (*stubMCPRecorder) RecordOAuth(context.Context, string, string)       {}
func (*stubMCPRecorder) RecordInitFailure(context.Context, string, string) {}
func (*stubMCPRecorder) SignalReady()                                      {}
func (*stubMCPRecorder) SetKnownTools(string, []string)                    {}
