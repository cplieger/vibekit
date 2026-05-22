package translate

import (
	"context"
	"encoding/json"
	"testing"

	"vibekit/internal/api"
	"vibekit/internal/buffer"
	"vibekit/internal/permissions"
)

// recordingDeps captures broadcast events. Delegates everything else
// to an inner stubDeps but does NOT embed it (embedding would promote
// stubDeps.Broadcast and shadow our override via interface dispatch).
type recordingDeps struct {
	inner  *stubDeps
	events []api.ServerEvent
}

func newRecordingDeps() *recordingDeps {
	return &recordingDeps{inner: newStubDeps()}
}

func (d *recordingDeps) Broadcast(_ context.Context, evt api.ServerEvent) {
	d.events = append(d.events, evt)
}
func (d *recordingDeps) ChatStore() api.ChatStore             { return d.inner.store }
func (d *recordingDeps) ParentACPSession(_ api.ChatID) string { return "" }
func (d *recordingDeps) WorkDir() string                      { return "/tmp" }
func (d *recordingDeps) BridgeNotify(context.Context, api.ChatID, string, map[string]any) error {
	return nil
}
func (d *recordingDeps) BridgeRespond(context.Context, api.ChatID, int64, any, error) error {
	return nil
}
func (d *recordingDeps) MCPRecordConnected(context.Context, string)           {}
func (d *recordingDeps) MCPRecordOAuth(context.Context, string, string)       {}
func (d *recordingDeps) MCPRecordInitFailure(context.Context, string, string) {}
func (d *recordingDeps) MCPSignalReady()                                      {}
func (d *recordingDeps) MCPSetKnownTools(_ string, _ []string)                {}
func (d *recordingDeps) PendingPermsAdd(int64, api.ServerEvent)               {}
func (d *recordingDeps) PendingPermsRemove(int64)                             {}
func (d *recordingDeps) NotifyPush(context.Context, string, api.PushKind)     {}
func (d *recordingDeps) ConfigDir() string                                    { return "/tmp" }
func (d *recordingDeps) PermissionRules() *permissions.CommandRules           { return nil }
func (d *recordingDeps) BufferStore() *buffer.Store                           { return d.inner.bufStore }
func (d *recordingDeps) LineTracker() *buffer.LineTracker                     { return d.inner.lineTracker }
func (d *recordingDeps) OpenPartialFile(api.ChatID, *buffer.Buffer)           {}
func (d *recordingDeps) IsHookStatusEnabled() bool                            { return false }
func (d *recordingDeps) NewMessageID() string                                 { return "m-test-1" }

// --- Event sequence tests ---

func TestSequence_AssistantChunk_CreatesMessageThenChunks(t *testing.T) {
	deps := newRecordingDeps()
	tr := New(deps)
	chatID := api.ChatID("c1")

	// First chunk: should create message + emit chunk
	tr.HandleAssistantChunk(context.Background(), chatID, mustJSON(t, map[string]any{
		"content": map[string]any{"type": "text", "text": "Hello"},
	}), false)

	if len(deps.events) < 2 {
		t.Fatalf("expected at least 2 events (message_created + message_chunk), got %d: %v",
			len(deps.events), eventTypes(deps.events))
	}
	if deps.events[0].Type != api.EventMessageCreated {
		t.Errorf("event[0].Type = %q, want message_created", deps.events[0].Type)
	}
	if deps.events[1].Type != api.EventMessageChunk {
		t.Errorf("event[1].Type = %q, want message_chunk", deps.events[1].Type)
	}

	// Second chunk: should only emit chunk (no duplicate message_created)
	deps.events = nil
	tr.HandleAssistantChunk(context.Background(), chatID, mustJSON(t, map[string]any{
		"content": map[string]any{"type": "text", "text": " world"},
	}), false)

	if len(deps.events) != 1 {
		t.Fatalf("expected 1 event (message_chunk only), got %d: %v",
			len(deps.events), eventTypes(deps.events))
	}
	if deps.events[0].Type != api.EventMessageChunk {
		t.Errorf("event[0].Type = %q, want message_chunk", deps.events[0].Type)
	}
}

func TestSequence_ToolCall_EmitsToolCallEvent(t *testing.T) {
	deps := newRecordingDeps()
	tr := New(deps)
	chatID := api.ChatID("c1")

	// Start a streaming turn first (tool calls require an active buffer)
	tr.HandleAssistantChunk(context.Background(), chatID, mustJSON(t, map[string]any{
		"content": map[string]any{"type": "text", "text": "Let me check..."},
	}), false)
	deps.events = nil

	// Tool call
	tr.HandleToolCall(context.Background(), chatID, mustJSON(t, map[string]any{
		"tool_call_id": "tc1",
		"title":        "readFile",
		"kind":         "read",
		"status":       "in_progress",
	}), "")

	found := false
	for _, evt := range deps.events {
		if evt.Type == api.EventToolCall {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no tool_call event emitted; got events: %v", eventTypes(deps.events))
	}
}

func TestSequence_ToolCallUpdate_EmitsUpdateEvent(t *testing.T) {
	deps := newRecordingDeps()
	tr := New(deps)
	chatID := api.ChatID("c1")

	// Start turn + add tool call
	tr.HandleAssistantChunk(context.Background(), chatID, mustJSON(t, map[string]any{
		"content": map[string]any{"type": "text", "text": "x"},
	}), false)
	tr.HandleToolCall(context.Background(), chatID, mustJSON(t, map[string]any{
		"tool_call_id": "tc1",
		"title":        "readFile",
		"kind":         "read",
		"status":       "in_progress",
	}), "")
	deps.events = nil

	// Update tool call status
	tr.HandleToolCallUpdate(context.Background(), chatID, mustJSON(t, map[string]any{
		"tool_call_id": "tc1",
		"status":       "completed",
	}), "")

	found := false
	for _, evt := range deps.events {
		if evt.Type == api.EventToolCallUpdate {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no tool_call_update event emitted; got events: %v", eventTypes(deps.events))
	}
}

func TestSequence_MCPInitialized_RecordsConnection(t *testing.T) {
	deps := newRecordingDeps()
	var recorded string
	wrapper := &mcpCaptureDeps{recordingDeps: deps, connected: &recorded}
	tr := &Translator{deps: wrapper, crewCache: newCrewCache()}

	tr.HandleMCPInitialized(context.Background(), "", &api.RPCResponse{
		Params: mustJSON(t, map[string]any{"serverName": "github"}),
	})

	if recorded != "github" {
		t.Errorf("MCPRecordConnected called with %q, want %q", recorded, "github")
	}
}

type mcpCaptureDeps struct {
	*recordingDeps

	connected *string
}

func (d *mcpCaptureDeps) MCPRecordConnected(_ context.Context, name string) {
	*d.connected = name
}

func TestSequence_CommandsAvailable_BroadcastsAndPersistsTools(t *testing.T) {
	deps := newRecordingDeps()
	tr := New(deps)
	chatID := api.ChatID("c1")

	tr.HandleCommandsAvailable(context.Background(), chatID, &api.RPCResponse{
		Params: mustJSON(t, map[string]any{
			"commands": []map[string]any{
				{"name": "/help", "description": "Show help"},
			},
			"mcpServers": []map[string]any{
				{"name": "github", "status": "running", "tools": []string{"create_issue", "list_repos"}},
			},
		}),
	})

	found := false
	for _, evt := range deps.events {
		if evt.Type == api.EventCommandsUpdated {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no commands_updated event emitted; got events: %v", eventTypes(deps.events))
	}
}

// --- Helpers ---

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("mustJSON: %v", err)
	}
	return b
}

func eventTypes(events []api.ServerEvent) []string {
	types := make([]string, len(events))
	for i, e := range events {
		types[i] = string(e.Type)
	}
	return types
}

var _ = buffer.NewStore // keep import
