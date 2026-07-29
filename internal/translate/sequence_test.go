package translate

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// newEventCaptureDeps returns a baseDeps that captures broadcast events into
// the returned slice pointer. Tests read *events after exercising the
// translator.
func newEventCaptureDeps() (*baseDeps, *[]api.ServerEvent) {
	events := &[]api.ServerEvent{}
	deps := newBaseDeps()
	deps.onBroadcast = func(_ context.Context, evt api.ServerEvent) {
		*events = append(*events, evt)
	}
	return deps, events
}

// --- Event sequence tests ---

func TestSequence_AssistantChunk_CreatesMessageThenChunks(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(deps, withIDGenerator(func() string { return "stub-msg-id" }))
	chatID := api.ChatID("c1")

	// First chunk: should create message + emit chunk
	tr.HandleAssistantChunk(context.Background(), chatID, mustJSON(t, map[string]any{
		"content": map[string]any{"type": "text", "text": "Hello"},
	}), false)

	if len(*events) < 2 {
		t.Fatalf("expected at least 2 events (message_created + message_chunk), got %d: %v",
			len(*events), eventTypes(*events))
	}
	if (*events)[0].Type != api.EventMessageCreated {
		t.Errorf("event[0].Type = %q, want message_created", (*events)[0].Type)
	}
	if (*events)[1].Type != api.EventMessageChunk {
		t.Errorf("event[1].Type = %q, want message_chunk", (*events)[1].Type)
	}

	// Second chunk: should only emit chunk (no duplicate message_created)
	*events = nil
	tr.HandleAssistantChunk(context.Background(), chatID, mustJSON(t, map[string]any{
		"content": map[string]any{"type": "text", "text": " world"},
	}), false)

	if len(*events) != 1 {
		t.Fatalf("expected 1 event (message_chunk only), got %d: %v",
			len(*events), eventTypes(*events))
	}
	if (*events)[0].Type != api.EventMessageChunk {
		t.Errorf("event[0].Type = %q, want message_chunk", (*events)[0].Type)
	}
}

func TestSequence_ToolCall_EmitsToolCallEvent(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(deps, withIDGenerator(func() string { return "stub-msg-id" }))
	chatID := api.ChatID("c1")

	// Start a streaming turn first (tool calls require an active buffer)
	tr.HandleAssistantChunk(context.Background(), chatID, mustJSON(t, map[string]any{
		"content": map[string]any{"type": "text", "text": "Let me check..."},
	}), false)
	*events = nil

	// Tool call
	tr.HandleToolCall(context.Background(), chatID, mustJSON(t, map[string]any{
		"tool_call_id": "tc1",
		"title":        "readFile",
		"kind":         "read",
		"status":       "in_progress",
	}), "")

	found := false
	for _, evt := range *events {
		if evt.Type == api.EventToolCall {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no tool_call event emitted; got events: %v", eventTypes(*events))
	}
}

func TestSequence_ToolCallUpdate_EmitsUpdateEvent(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(deps, withIDGenerator(func() string { return "stub-msg-id" }))
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
	*events = nil

	// Update tool call status
	tr.HandleToolCallUpdate(context.Background(), chatID, mustJSON(t, map[string]any{
		"tool_call_id": "tc1",
		"status":       "completed",
	}), "")

	found := false
	for _, evt := range *events {
		if evt.Type == api.EventToolCallUpdate {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no tool_call_update event emitted; got events: %v", eventTypes(*events))
	}
}

func TestSequence_MCPStatus_RecordsConnection(t *testing.T) {
	deps, _ := newEventCaptureDeps()
	var recorded string
	wrapper := &mcpCaptureDeps{baseDeps: deps, connected: &recorded}
	tr := &Translator{deps: wrapper}

	// v3 consolidated MCP status: a "connected" server records a connection.
	tr.HandleMCPStatus(context.Background(), "", &api.RPCResponse{
		Params: mustJSON(t, map[string]any{
			"servers": []map[string]any{
				{"name": "github", "status": "connected"},
			},
		}),
	})

	if recorded != "github" {
		t.Errorf("RecordConnected called with %q, want %q", recorded, "github")
	}
}

func TestSequence_MCPStatus_CapturesPromptsAndResources(t *testing.T) {
	deps, _ := newEventCaptureDeps()
	var connected string
	var prompts []api.MCPPromptInfo
	var resources []api.MCPResourceInfo
	wrapper := &mcpCaptureDeps{baseDeps: deps, connected: &connected, prompts: &prompts, resources: &resources}
	tr := &Translator{deps: wrapper}

	tr.HandleMCPStatus(context.Background(), "", &api.RPCResponse{
		Params: mustJSON(t, map[string]any{
			"servers": []map[string]any{
				{
					"name":   "everything",
					"status": "connected",
					"prompts": []map[string]any{
						{"name": "Simple Prompt", "promptName": "simple-prompt", "description": "no args"},
						{"name": "Args Prompt", "promptName": "args-prompt", "arguments": []map[string]any{
							{"name": "city", "required": true},
						}},
						{"name": "no id", "promptName": ""}, // dropped: no machine name
					},
					"resources": []map[string]any{
						{"name": "doc", "uri": "demo://doc", "mimeType": "text/markdown"},
						{"name": "no uri", "uri": ""}, // dropped: unaddressable
					},
				},
			},
		}),
	})

	if connected != "everything" {
		t.Fatalf("connected = %q", connected)
	}
	if len(prompts) != 2 {
		t.Fatalf("prompts = %+v, want 2 (empty promptName dropped)", prompts)
	}
	if prompts[0].PromptName != "simple-prompt" || prompts[1].PromptName != "args-prompt" {
		t.Errorf("prompt names = %+v", prompts)
	}
	if len(prompts[1].Arguments) != 1 || !prompts[1].Arguments[0].Required {
		t.Errorf("args = %+v", prompts[1].Arguments)
	}
	if len(resources) != 1 || resources[0].URI != "demo://doc" {
		t.Errorf("resources = %+v, want 1 (empty uri dropped)", resources)
	}
}

type mcpCaptureDeps struct {
	*baseDeps
	connected *string
	prompts   *[]api.MCPPromptInfo
	resources *[]api.MCPResourceInfo
}

func (d *mcpCaptureDeps) MCPRecorder() MCPRecorder {
	return &captureMCPRecorder{connected: d.connected, prompts: d.prompts, resources: d.resources}
}

type captureMCPRecorder struct {
	connected *string
	prompts   *[]api.MCPPromptInfo
	resources *[]api.MCPResourceInfo
}

func (r *captureMCPRecorder) RecordConnected(_ context.Context, name string, prompts []api.MCPPromptInfo, resources []api.MCPResourceInfo) {
	if r.connected != nil {
		*r.connected = name
	}
	if r.prompts != nil {
		*r.prompts = prompts
	}
	if r.resources != nil {
		*r.resources = resources
	}
}
func (*captureMCPRecorder) RecordOAuth(context.Context, string, string)       {}
func (*captureMCPRecorder) RecordInitFailure(context.Context, string, string) {}
func (*captureMCPRecorder) SignalReady()                                      {}
func (*captureMCPRecorder) SetKnownTools(context.Context, string, []string)   {}

func TestSequence_AvailableCommands_BroadcastsUpdate(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(deps, withIDGenerator(func() string { return "stub-msg-id" }))
	chatID := api.ChatID("c1")

	// v3 available_commands_update session/update sub-kind.
	tr.HandleAvailableCommandsUpdate(context.Background(), chatID, mustJSON(t, map[string]any{
		"availableCommands": []map[string]any{
			{"name": "/help", "description": "Show help"},
		},
	}), "")

	found := false
	for _, evt := range *events {
		if evt.Type == api.EventCommandsUpdated {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no commands_updated event emitted; got events: %v", eventTypes(*events))
	}
}

func TestSequence_ReasoningChunk_RoutesToReasoningBuilder(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(deps, withIDGenerator(func() string { return "stub-msg-id" }))
	chatID := api.ChatID("c-reason")

	// Send a reasoning chunk (isReasoning=true)
	tr.HandleAssistantChunk(context.Background(), chatID, mustJSON(t, map[string]any{
		"content": map[string]any{"type": "text", "text": "thinking..."},
	}), true)

	// Verify buffer has reasoning content but no regular content
	buf := deps.bufStore.GetOrInit(chatID)
	if buf.Reasoning.String() != "thinking..." {
		t.Errorf("Reasoning = %q, want %q", buf.Reasoning.String(), "thinking...")
	}
	if buf.Content.Len() != 0 {
		t.Errorf("Content should be empty, got %q", buf.Content.String())
	}

	// Verify the chunk event has IsReasoning=true
	var found bool
	for _, evt := range *events {
		if evt.Type == "message_chunk" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("no message_chunk event emitted")
	}

	// Send a regular text chunk (isReasoning=false)
	tr.HandleAssistantChunk(context.Background(), chatID, mustJSON(t, map[string]any{
		"content": map[string]any{"type": "text", "text": "answer"},
	}), false)

	if buf.Content.String() != "answer" {
		t.Errorf("Content = %q, want %q", buf.Content.String(), "answer")
	}
	if buf.Reasoning.String() != "thinking..." {
		t.Errorf("Reasoning changed unexpectedly: %q", buf.Reasoning.String())
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
