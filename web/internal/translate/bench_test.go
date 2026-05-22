package translate

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"vibekit/internal/api"
	"vibekit/internal/buffer"
	"vibekit/internal/permissions"
)

// stubDeps is a minimal Deps implementation for benchmarking.
type stubDeps struct {
	store       *stubChatStore
	bufStore    *buffer.Store
	lineTracker *buffer.LineTracker
}

func newStubDeps() *stubDeps {
	return &stubDeps{
		store:       &stubChatStore{},
		bufStore:    buffer.NewStore(),
		lineTracker: buffer.NewLineTracker(),
	}
}

func (d *stubDeps) Broadcast(context.Context, api.ServerEvent) {}
func (d *stubDeps) ChatStore() api.ChatStore                   { return d.store }
func (d *stubDeps) ParentACPSession(api.ChatID) string         { return "" }
func (d *stubDeps) WorkDir() string                            { return "/tmp" }
func (d *stubDeps) BridgeNotify(context.Context, api.ChatID, string, map[string]any) error {
	return nil
}
func (d *stubDeps) BridgeRespond(context.Context, api.ChatID, int64, any, error) error { return nil }
func (d *stubDeps) MCPRecordConnected(context.Context, string)                         {}
func (d *stubDeps) MCPRecordOAuth(context.Context, string, string)                     {}
func (d *stubDeps) MCPRecordInitFailure(context.Context, string, string)               {}
func (d *stubDeps) MCPSignalReady()                                                    {}
func (d *stubDeps) MCPSetKnownTools(_ string, _ []string)                              {}
func (d *stubDeps) PendingPermsAdd(int64, api.ServerEvent)                             {}
func (d *stubDeps) PendingPermsRemove(int64)                                           {}
func (d *stubDeps) NotifyPush(context.Context, string, api.PushKind)                   {}
func (d *stubDeps) ConfigDir() string                                                  { return "/tmp" }
func (d *stubDeps) PermissionRules() *permissions.CommandRules                         { return nil }
func (d *stubDeps) BufferStore() *buffer.Store                                         { return d.bufStore }
func (d *stubDeps) LineTracker() *buffer.LineTracker                                   { return d.lineTracker }
func (d *stubDeps) OpenPartialFile(api.ChatID, *buffer.Buffer)                         {}
func (d *stubDeps) IsHookStatusEnabled() bool                                          { return false }
func (d *stubDeps) NewMessageID() string                                               { return "m-bench-1" }

// stubChatStore satisfies api.ChatStore for benchmarks.
type stubChatStore struct{}

func (s *stubChatStore) RegisterRoutes(*http.ServeMux)                         {}
func (s *stubChatStore) SetBroadcaster(api.Broadcaster)                        {}
func (s *stubChatStore) Get(_ context.Context, _ api.ChatID) (*api.Chat, bool) { return nil, false }
func (s *stubChatStore) List(_ context.Context) []api.ChatHeader               { return nil }
func (s *stubChatStore) BuildHistory(_ context.Context, _ api.ChatID) string   { return "" }
func (s *stubChatStore) Mutate(_ context.Context, _ api.ChatID, _ func(*api.Chat, bool) bool) error {
	return nil
}
func (s *stubChatStore) Delete(_ context.Context, _ api.ChatID) error          { return nil }
func (s *stubChatStore) Archive(_ context.Context, _ api.ChatID) error         { return nil }
func (s *stubChatStore) ListArchived(_ context.Context) []api.ChatHeader       { return nil }
func (s *stubChatStore) RestoreArchived(_ context.Context, _ api.ChatID) error { return nil }
func (s *stubChatStore) UpdateArchivedSummary(_ context.Context, _ api.ChatID, _ string) error {
	return nil
}
func (s *stubChatStore) LoadArchived(_ context.Context, _ api.ChatID) (*api.Chat, error) {
	return nil, nil
}
func (s *stubChatStore) DeleteArchived(_ context.Context, _ api.ChatID) error { return nil }
func (s *stubChatStore) AppendMessage(_ context.Context, _ api.ChatID, _ *api.Message) error {
	return nil
}
func (s *stubChatStore) UpdateMessage(_ context.Context, _ api.ChatID, _ string, _ func(*api.Message)) error {
	return nil
}

var crewPayload = json.RawMessage(`{"subagents":[{"sessionId":"s1","sessionName":"worker","agentName":"coder","initialQuery":"fix bug","group":"g1","role":"worker","status":{"type":"running","message":"coding"}}]}`)

var commandsPayload = json.RawMessage(`{"commands":[{"name":"/help","description":"Show help"}],"prompts":[],"tools":["t1","t2"],"mcpServers":[{"name":"srv","status":"running","tools":["a"]}]}`)

var toolCallPayload = json.RawMessage(`{"toolCallId":"tc-1","title":"ReadFile","kind":"read","status":"pending","rawInput":{},"locations":[],"content":[{"type":"text","content":{"text":"reading file"}}]}`)

func BenchmarkTranslator_HandleCrewUpdate(b *testing.B) {
	tr := New(newStubDeps())
	msg := &api.RPCResponse{Params: crewPayload}
	ctx := context.Background()
	chatID := api.ChatID("bench-chat")

	b.ResetTimer()
	for b.Loop() {
		tr.HandleCrewUpdate(ctx, chatID, msg)
	}
}

func BenchmarkTranslator_HandleCommandsAvailable(b *testing.B) {
	tr := New(newStubDeps())
	msg := &api.RPCResponse{Params: commandsPayload}
	ctx := context.Background()
	chatID := api.ChatID("bench-chat")

	b.ResetTimer()
	for b.Loop() {
		tr.HandleCommandsAvailable(ctx, chatID, msg)
	}
}

func BenchmarkTranslator_HandleToolCall(b *testing.B) {
	tr := New(newStubDeps())
	ctx := context.Background()
	chatID := api.ChatID("bench-chat")

	b.ResetTimer()
	for b.Loop() {
		tr.HandleToolCall(ctx, chatID, toolCallPayload, "")
	}
}

// BenchmarkTranslator_FullTurn simulates a complete streaming turn:
// 50 text chunks → 1 tool call → 1 tool call update → 50 more chunks.
// Measures end-to-end throughput including buffer management.
func BenchmarkTranslator_FullTurn(b *testing.B) {
	deps := newStubDeps()
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
