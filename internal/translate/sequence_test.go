package translate

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// newEventCaptureDeps returns a baseDeps that captures broadcast events into
// the returned slice pointer. Tests read *events after exercising the
// translator.
func newEventCaptureDeps() (*baseDeps, *[]vibekit.ServerEvent) {
	events := &[]vibekit.ServerEvent{}
	deps := newBaseDeps()
	deps.onBroadcast = func(_ context.Context, evt vibekit.ServerEvent) {
		*events = append(*events, evt)
	}
	return deps, events
}

// --- Event sequence tests ---

func TestSequence_AssistantChunk_CreatesMessageThenChunks(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(deps, withIDGenerator(func() string { return "stub-msg-id" }))
	chatID := vibekit.ChatID("c1")

	// First chunk: should create message + emit chunk
	tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, map[string]any{
		"content": map[string]any{"type": "text", "text": "Hello"},
	}), false)

	if len(*events) < 2 {
		t.Fatalf("expected at least 2 events (message_created + message_chunk), got %d: %v",
			len(*events), eventTypes(*events))
	}
	if (*events)[0].Type != vibekit.EventMessageCreated {
		t.Errorf("event[0].Type = %q, want message_created", (*events)[0].Type)
	}
	if (*events)[1].Type != vibekit.EventMessageChunk {
		t.Errorf("event[1].Type = %q, want message_chunk", (*events)[1].Type)
	}

	// Second chunk: should only emit chunk (no duplicate message_created)
	*events = nil
	tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, map[string]any{
		"content": map[string]any{"type": "text", "text": " world"},
	}), false)

	if len(*events) != 1 {
		t.Fatalf("expected 1 event (message_chunk only), got %d: %v",
			len(*events), eventTypes(*events))
	}
	if (*events)[0].Type != vibekit.EventMessageChunk {
		t.Errorf("event[0].Type = %q, want message_chunk", (*events)[0].Type)
	}
}

func TestSequence_ToolCall_EmitsToolCallEvent(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(deps, withIDGenerator(func() string { return "stub-msg-id" }))
	chatID := vibekit.ChatID("c1")

	// Start a streaming turn first (tool calls require an active buffer)
	tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, map[string]any{
		"content": map[string]any{"type": "text", "text": "Let me check..."},
	}), false)
	*events = nil

	// Tool call
	tr.HandleToolCall(t.Context(), chatID, mustJSON(t, map[string]any{
		"tool_call_id": "tc1",
		"title":        "readFile",
		"kind":         "read",
		"status":       "in_progress",
	}), "")

	found := false
	for _, evt := range *events {
		if evt.Type == vibekit.EventToolCall {
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
	chatID := vibekit.ChatID("c1")

	// Start turn + add tool call
	tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, map[string]any{
		"content": map[string]any{"type": "text", "text": "x"},
	}), false)
	tr.HandleToolCall(t.Context(), chatID, mustJSON(t, map[string]any{
		"tool_call_id": "tc1",
		"title":        "readFile",
		"kind":         "read",
		"status":       "in_progress",
	}), "")
	*events = nil

	// Update tool call status
	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"tool_call_id": "tc1",
		"status":       "completed",
	}), "")

	found := false
	for _, evt := range *events {
		if evt.Type == vibekit.EventToolCallUpdate {
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
	tr.HandleMCPStatus(t.Context(), "", &vibekit.RPCResponse{
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

// TestSequence_MCPStatus_RoutesDisabledToTheRecorder pins the amendment: the
// "disabled" status reaches the recorder instead of falling into the default arm
// that discarded it. Whether it produces a row is the recorder's call (only an
// unconfigured server gets one) — but the frame has to arrive for that call to
// be possible at all.
func TestSequence_MCPStatus_RoutesDisabledToTheRecorder(t *testing.T) {
	deps, _ := newEventCaptureDeps()
	var disabled []string
	tr := &Translator{deps: &mcpCaptureDeps{baseDeps: deps, disabled: &disabled}}

	tr.HandleMCPStatus(t.Context(), "", &vibekit.RPCResponse{
		Params: mustJSON(t, map[string]any{
			"servers": []map[string]any{
				{"name": "off-server", "status": "disabled"},
				// "connecting" is transient, not terminal: it must stay discarded,
				// or a row would be painted that the next frame replaces.
				{"name": "starting-server", "status": "connecting"},
				// A nameless entry is unaddressable and skipped before the switch.
				{"name": "", "status": "disabled"},
			},
		}),
	})

	if len(disabled) != 1 || disabled[0] != "off-server" {
		t.Errorf("RecordDisabled calls = %v, want just [off-server]", disabled)
	}
}

func TestSequence_MCPStatus_CapturesPromptsAndResources(t *testing.T) {
	deps, _ := newEventCaptureDeps()
	var connected string
	var prompts []vibekit.MCPPromptInfo
	var resources []vibekit.MCPResourceInfo
	wrapper := &mcpCaptureDeps{baseDeps: deps, connected: &connected, prompts: &prompts, resources: &resources}
	tr := &Translator{deps: wrapper}

	tr.HandleMCPStatus(t.Context(), "", &vibekit.RPCResponse{
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
	prompts   *[]vibekit.MCPPromptInfo
	resources *[]vibekit.MCPResourceInfo
	disabled  *[]string
}

func (d *mcpCaptureDeps) MCPRecorder() MCPRecorder {
	return &captureMCPRecorder{
		connected: d.connected, prompts: d.prompts,
		resources: d.resources, disabled: d.disabled,
	}
}

type captureMCPRecorder struct {
	connected *string
	prompts   *[]vibekit.MCPPromptInfo
	resources *[]vibekit.MCPResourceInfo
	disabled  *[]string
}

func (r *captureMCPRecorder) RecordConnected(_ context.Context, name string, _ []string, prompts []vibekit.MCPPromptInfo, resources []vibekit.MCPResourceInfo) {
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

func (r *captureMCPRecorder) RecordDisabled(_ context.Context, name string) {
	if r.disabled != nil {
		*r.disabled = append(*r.disabled, name)
	}
}

func TestSequence_ReasoningChunk_RoutesToReasoningBuilder(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(deps, withIDGenerator(func() string { return "stub-msg-id" }))
	chatID := vibekit.ChatID("c-reason")

	// Send a reasoning chunk (isReasoning=true)
	tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, map[string]any{
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
	tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, map[string]any{
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

func eventTypes(events []vibekit.ServerEvent) []string {
	types := make([]string, len(events))
	for i, e := range events {
		types[i] = string(e.Type)
	}
	return types
}
