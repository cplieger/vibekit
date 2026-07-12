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
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/buffer"
)

// newHubWithConfigDir wires a Hub pointed at an explicit configDir so
// we can drop <configDir>/chats/*.partial files and exercise the
// recovery path end-to-end.
func newHubWithConfigDir(t *testing.T, cfg string) (*Hub, *fakeChatStore) {
	t.Helper()
	cs := newFakeChatStore()
	factory := func() api.ACPBridge { return newFakeBridge() }
	h := New(t.TempDir(), factory, cs, WithConfigDir(cfg))
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

func TestRecoverPartials_ReasoningSnapshot_PopulatesReasoningField(t *testing.T) {
	t.Parallel()
	cfg := t.TempDir()
	h, cs := newHubWithConfigDir(t, cfg)
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	writePartialFile(t, cfg, "c1", buffer.PartialSnapshot{
		MessageID: "m-r",
		Reasoning: "let me think...",
	})

	h.RecoverPartials()

	c, _ := cs.Get(context.Background(), "c1")
	if len(c.Messages) < 1 {
		t.Fatal("no messages recovered")
	}
	if c.Messages[0].Reasoning != "let me think..." {
		t.Errorf("Reasoning = %q, want %q", c.Messages[0].Reasoning, "let me think...")
	}
	if c.Messages[0].Content != "" {
		t.Errorf("Content = %q, want empty", c.Messages[0].Content)
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

// On the success path (store appends succeed, file removes cleanly)
// RecoverPartials logs none of its three error messages. Not parallel:
// captureLogs mutates the global slog default.
func TestRecoverPartials_NoErrorLogsOnSuccess(t *testing.T) {
	cfg := t.TempDir()
	h, cs := newHubWithConfigDir(t, cfg)
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	writePartialFile(t, cfg, "c1", buffer.PartialSnapshot{
		MessageID: "m",
		Content:   "half-written content",
		Ts:        1_700_000_000_000,
	})

	logs := captureLogs(t)
	h.RecoverPartials()

	got := logs.String()
	for _, bad := range []string{
		"partial recovery: append failed",
		"partial recovery: append interrupted",
		"partial recovery: remove and rename failed",
	} {
		if strings.Contains(got, bad) {
			t.Errorf("unexpected error log on RecoverPartials success path: %q in %s", bad, got)
		}
	}

	// Sanity: recovery actually ran (so the success branches were taken).
	c, _ := cs.Get(context.Background(), "c1")
	if len(c.Messages) != 2 {
		t.Fatalf("recovered messages = %d, want 2 (assistant + interrupted)", len(c.Messages))
	}
	if _, err := os.Stat(filepath.Join(cfg, "chats", "c1.partial")); !os.IsNotExist(err) {
		t.Errorf("partial file still present after recovery: err=%v", err)
	}
}

// TestRecoverPartials_ToolFirstTurnOpensAndRecovers pins fix #8: a turn
// whose FIRST streamed event is a tool call must still open the .partial
// as soon as any content/reasoning arrives, so the whole turn is
// crash-durable. Before the fix, the tool-first ensureTurnStarted set
// Started=true without opening the file and the later content chunk's
// ensureTurnStarted early-returned, so WritePartial was a no-op for the
// entire turn and a crash lost it with no .partial.
func TestRecoverPartials_ToolFirstTurnOpensAndRecovers(t *testing.T) {
	t.Parallel()
	cfg := t.TempDir()
	h, cs := newHubWithConfigDir(t, cfg)
	if err := os.MkdirAll(filepath.Join(cfg, "chats"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	// First event is a TOOL CALL (in_progress), then a content chunk.
	h.translateACPEvent("c1", newToolCallMsg(t, "tc1", "Reading file", "in_progress"))
	h.translateACPEvent("c1", newChunkMsg(t, "text after a tool-first start"))

	// The .partial must now exist and carry both the content and the tool call.
	partial := filepath.Join(cfg, "chats", "c1.partial")
	data, err := os.ReadFile(partial) // #nosec G304 -- test path
	if err != nil {
		t.Fatalf("tool-first turn did not open .partial: %v", err)
	}
	var snap buffer.PartialSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("parse .partial: %v", err)
	}
	if snap.Content != "text after a tool-first start" {
		t.Errorf("partial content = %q, want the text chunk", snap.Content)
	}
	if len(snap.ToolCalls) != 1 || snap.ToolCalls[0].ID != "tc1" {
		t.Fatalf("partial did not capture the earlier tool call: %+v", snap.ToolCalls)
	}

	// Recover (simulating restart): the turn merges into the chat, and the
	// in_progress tool is normalized to failed (fix #10).
	h.RecoverPartials()
	c, _ := cs.Get(context.Background(), "c1")
	if len(c.Messages) != 2 {
		t.Fatalf("recovered messages = %d, want 2 (assistant + interrupted)", len(c.Messages))
	}
	got := c.Messages[0]
	if got.Content != "text after a tool-first start" {
		t.Errorf("recovered content = %q", got.Content)
	}
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].Status != api.ToolFailed {
		t.Errorf("recovered tool call = %+v, want single failed", got.ToolCalls)
	}
	if c.Messages[1].EventKind != api.EventInterrupted {
		t.Errorf("second message = %+v, want interrupted event", c.Messages[1])
	}
}

// TestRecoverPartials_NormalizesNonTerminalToolStatus pins fix #10:
// recovered tool calls left pending / in_progress at crash time are
// normalized to failed so replay doesn't render a permanently-spinning
// chip; terminal statuses are untouched.
func TestRecoverPartials_NormalizesNonTerminalToolStatus(t *testing.T) {
	t.Parallel()
	cfg := t.TempDir()
	h, cs := newHubWithConfigDir(t, cfg)
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	writePartialFile(t, cfg, "c1", buffer.PartialSnapshot{
		MessageID: "m-tools",
		Content:   "partial with tools",
		ToolCalls: []api.ToolCall{
			{ID: "done", Status: api.ToolCompleted},
			{ID: "running", Status: api.ToolInProgress},
			{ID: "queued", Status: api.ToolPending},
		},
	})

	h.RecoverPartials()

	c, _ := cs.Get(context.Background(), "c1")
	if len(c.Messages) < 1 {
		t.Fatal("no messages recovered")
	}
	got := c.Messages[0].ToolCalls
	if len(got) != 3 {
		t.Fatalf("recovered tool calls = %d, want 3", len(got))
	}
	for _, tc := range got {
		switch tc.ID {
		case "done":
			if tc.Status != api.ToolCompleted {
				t.Errorf("done status = %q, want completed (terminal, untouched)", tc.Status)
			}
		case "running", "queued":
			if tc.Status != api.ToolFailed {
				t.Errorf("%s status = %q, want failed (non-terminal normalized)", tc.ID, tc.Status)
			}
		}
	}
}

// TestRecoverPartials_IdempotentSkipsCommittedTurn pins fix #9's
// idempotency: a .partial whose MessageID is already committed (a crash
// after AppendMessage but before the .partial delete) must NOT
// double-append — recovery skips it and removes the orphan file.
func TestRecoverPartials_IdempotentSkipsCommittedTurn(t *testing.T) {
	t.Parallel()
	cfg := t.TempDir()
	h, cs := newHubWithConfigDir(t, cfg)
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.Messages = []api.Message{{ID: "m-committed", Role: api.RoleAssistant, Content: "done"}}
		return true
	})
	// The .partial survived a crash-after-commit with the SAME message id.
	writePartialFile(t, cfg, "c1", buffer.PartialSnapshot{MessageID: "m-committed", Content: "done"})

	h.RecoverPartials()

	c, _ := cs.Get(context.Background(), "c1")
	if len(c.Messages) != 1 {
		t.Fatalf("messages = %d, want 1 (already-committed turn must not be re-appended)", len(c.Messages))
	}
	if c.Messages[0].ID != "m-committed" {
		t.Errorf("message id = %q, want m-committed", c.Messages[0].ID)
	}
	if _, err := os.Stat(filepath.Join(cfg, "chats", "c1.partial")); !os.IsNotExist(err) {
		t.Errorf("orphan .partial not removed after idempotent skip: %v", err)
	}
}

// TestEmitTurnEnded_PersistsBeforeRemovingPartial pins fix #9's ordering:
// a normal turn end persists the finalized message BEFORE removing the
// .partial, and a subsequent recovery does not double-append the
// committed turn.
func TestEmitTurnEnded_PersistsBeforeRemovingPartial(t *testing.T) {
	t.Parallel()
	cfg := t.TempDir()
	h, cs := newHubWithConfigDir(t, cfg)
	if err := os.MkdirAll(filepath.Join(cfg, "chats"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	// Content chunk opens the .partial; turn_ended finalizes it.
	h.translateACPEvent("c1", newChunkMsg(t, "final answer"))
	partial := filepath.Join(cfg, "chats", "c1.partial")
	if _, err := os.Stat(partial); err != nil {
		t.Fatalf(".partial not opened by content chunk: %v", err)
	}

	h.EmitTurnEndedWithStats(context.Background(), "c1",
		&api.RPCResponse{Result: json.RawMessage(`{"stopReason":"end_turn"}`)}, 0, 0)

	c, _ := cs.Get(context.Background(), "c1")
	if len(c.Messages) != 1 || c.Messages[0].Content != "final answer" {
		t.Fatalf("turn not persisted on turn_ended: %+v", c.Messages)
	}
	if _, err := os.Stat(partial); !os.IsNotExist(err) {
		t.Errorf(".partial not removed after commit: %v", err)
	}

	// A fresh recovery must find nothing to double-append.
	h.RecoverPartials()
	c, _ = cs.Get(context.Background(), "c1")
	if len(c.Messages) != 1 {
		t.Errorf("recovery double-appended a committed turn: %d messages", len(c.Messages))
	}
}
