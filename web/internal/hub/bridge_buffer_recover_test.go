package hub

// Tests for RecoverPartials — the crash-recovery path that replays
// orphaned .partial files on startup. The batch-15 review flagged
// RecoverPartials as 0% covered; these tests pin every branch:
// valid snapshot → assistant+interrupted, empty file → removed,
// corrupt JSON → removed, empty content → removed, directory entry
// skipped, non-.partial files untouched, no-configDir no-op.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"vibekit/internal/api"
	"vibekit/internal/buffer"
)

// newHubWithConfigDir wires a Hub pointed at an explicit configDir so
// we can drop <configDir>/chats/*.partial files and exercise the
// recovery path end-to-end.
func newHubWithConfigDir(t *testing.T, cfg string) (*Hub, *fakeChatStore) {
	t.Helper()
	cs := newFakeChatStore()
	factory := func() api.ACPBridge { return newFakeBridge() }
	h := New(t.TempDir(), factory, cs, func() []string { return nil }, WithConfigDir(cfg))
	cs.SetBroadcaster(h)
	h.mcpRegistry.signalReady()
	return h, cs
}

// writePartialFile drops a .partial file at the expected path.
func writePartialFile(t *testing.T, cfg string, chatID api.ChatID, snap buffer.PartialSnapshot) {
	t.Helper()
	dir := filepath.Join(cfg, "chats")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, string(chatID)+".partial")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRecoverPartials_ValidSnapshot_AppendsMessageAndInterruptedEvent(t *testing.T) {
	t.Parallel()
	cfg := t.TempDir()
	h, cs := newHubWithConfigDir(t, cfg)
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	writePartialFile(t, cfg, "c1", buffer.PartialSnapshot{
		MessageID: "m-abc",
		Content:   "hello half-written world",
		ToolCalls: []api.ToolCall{{ID: "tc1", Status: api.ToolCompleted}},
		Ts:        1_700_000_000_000,
	})

	h.RecoverPartials()

	c, _ := cs.Get(context.Background(), "c1")
	if len(c.Messages) != 2 {
		t.Fatalf("len(messages) = %d, want 2 (assistant + interrupted)", len(c.Messages))
	}
	got := c.Messages[0]
	if got.Role != api.RoleAssistant {
		t.Errorf("messages[0].Role = %q, want %q", got.Role, api.RoleAssistant)
	}
	if got.ID != "m-abc" {
		t.Errorf("messages[0].ID = %q, want %q", got.ID, "m-abc")
	}
	if got.Content != "hello half-written world" {
		t.Errorf("messages[0].Content = %q", got.Content)
	}
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].ID != "tc1" {
		t.Errorf("messages[0].ToolCalls = %+v, want single tc1", got.ToolCalls)
	}
	evt := c.Messages[1]
	if evt.Role != api.RoleEvent || evt.EventKind != api.EventInterrupted {
		t.Errorf("messages[1] = %+v, want event/interrupted", evt)
	}

	if _, err := os.Stat(filepath.Join(cfg, "chats", "c1.partial")); !os.IsNotExist(err) {
		t.Errorf("partial file still present after recovery: err=%v", err)
	}
}

func TestRecoverPartials_ReasoningSnapshot_SetsOperationType(t *testing.T) {
	t.Parallel()
	cfg := t.TempDir()
	h, cs := newHubWithConfigDir(t, cfg)
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	writePartialFile(t, cfg, "c1", buffer.PartialSnapshot{
		MessageID:   "m-r",
		Content:     "let me think...",
		IsReasoning: true,
	})

	h.RecoverPartials()

	c, _ := cs.Get(context.Background(), "c1")
	if len(c.Messages) < 1 {
		t.Fatal("no messages recovered")
	}
	if c.Messages[0].OperationType != api.OperationTypeReasoning {
		t.Errorf("OperationType = %q, want %q", c.Messages[0].OperationType, api.OperationTypeReasoning)
	}
}

func TestRecoverPartials_NoMutation(t *testing.T) {
	tests := []struct {
		name        string
		fileContent []byte
		useHelper   bool
		wantRemoved bool
	}{
		{
			name:        "EmptyFile",
			fileContent: nil,
			wantRemoved: true,
		},
		{
			name:        "CorruptJSON",
			fileContent: []byte("{not json"),
			wantRemoved: true,
		},
		{
			name:        "EmptyContent",
			useHelper:   true,
			wantRemoved: true,
		},
		{
			name:        "IgnoresNonPartialFiles",
			fileContent: nil, // sentinel: test writes non-.partial files
		},
		{
			name:        "SkipsDirectoryEntries",
			fileContent: nil, // sentinel: test creates a directory
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := t.TempDir()
			h, cs := newHubWithConfigDir(t, cfg)
			_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

			dir := filepath.Join(cfg, "chats")
			_ = os.MkdirAll(dir, 0o755)
			path := filepath.Join(dir, "c1.partial")

			switch tt.name {
			case "EmptyContent":
				writePartialFile(t, cfg, "c1", buffer.PartialSnapshot{MessageID: "m", Content: ""})
			case "IgnoresNonPartialFiles":
				sibling := filepath.Join(dir, "c1.json")
				noise := filepath.Join(dir, "README")
				if err := os.WriteFile(sibling, []byte(`{"id":"c1"}`), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(noise, []byte("hi"), 0o600); err != nil {
					t.Fatal(err)
				}
				h.RecoverPartials()
				if _, err := os.Stat(sibling); err != nil {
					t.Errorf("sibling chat json removed: %v", err)
				}
				if _, err := os.Stat(noise); err != nil {
					t.Errorf("unrelated file removed: %v", err)
				}
				c, _ := cs.Get(context.Background(), "c1")
				if len(c.Messages) != 0 {
					t.Errorf("messages = %+v, want none", c.Messages)
				}
				return
			case "SkipsDirectoryEntries":
				weird := filepath.Join(dir, "stray.partial")
				if err := os.MkdirAll(weird, 0o755); err != nil {
					t.Fatal(err)
				}
				h.RecoverPartials()
				info, err := os.Stat(weird)
				if err != nil {
					t.Fatalf("directory removed by recovery: %v", err)
				}
				if !info.IsDir() {
					t.Error("directory became a file after recovery")
				}
				return
			default:
				if err := os.WriteFile(path, tt.fileContent, 0o600); err != nil {
					t.Fatal(err)
				}
			}

			h.RecoverPartials()

			c, _ := cs.Get(context.Background(), "c1")
			if len(c.Messages) != 0 {
				t.Errorf("messages = %+v, want none for %s", c.Messages, tt.name)
			}
			if tt.wantRemoved {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Errorf("partial file not removed: err=%v", err)
				}
			}
		})
	}
}

func TestRecoverPartials_NoConfigDir_IsNoOp(t *testing.T) {
	t.Parallel()
	h, _, _ := newTestHub() // configDir == ""
	// Must not panic or touch the filesystem.
	h.RecoverPartials()
}
