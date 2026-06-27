package translate

import (
	"context"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// mcpRec is a recording MCPRecorder counting SetKnownTools calls.
type mcpRec struct {
	setKnownCalls int
}

func (*mcpRec) RecordConnected(context.Context, string)           {}
func (*mcpRec) RecordOAuth(context.Context, string, string)       {}
func (*mcpRec) RecordInitFailure(context.Context, string, string) {}
func (*mcpRec) SignalReady()                                      {}
func (r *mcpRec) SetKnownTools(_ context.Context, _ string, _ []string) {
	r.setKnownCalls++
}

// mcpDeps wraps baseDeps and swaps in the recording MCPRecorder.
type mcpDeps struct {
	*baseDeps
	rec *mcpRec
}

func (d *mcpDeps) MCPRecorder() MCPRecorder { return d.rec }

var (
	_ MCPRecorder = (*mcpRec)(nil)
	_ Deps        = (*mcpDeps)(nil)
)

// TestCommandsAvailable_SetKnownToolsGate pins that per-server tool
// names are persisted only when the server reports a non-empty tool
// list; an empty list is skipped (no SetKnownTools call).
func TestCommandsAvailable_SetKnownToolsGate(t *testing.T) {
	tests := []struct {
		name      string
		tools     []string
		wantCalls int
	}{
		{name: "NonEmptyToolsPersisted", tools: []string{"a", "b"}, wantCalls: 1},
		{name: "EmptyToolsSkipped", tools: []string{}, wantCalls: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &mcpRec{}
			deps := &mcpDeps{baseDeps: newBaseDeps(), rec: rec}
			tr := New(deps, "/tmp", WithIDGenerator(func() string { return "id" }))
			tr.HandleCommandsAvailable(context.Background(), api.ChatID("c1"), &api.RPCResponse{
				Params: mustJSON(t, map[string]any{
					"mcpServers": []map[string]any{
						{"name": "srv", "status": "running", "tools": tt.tools},
					},
				}),
			})
			if rec.setKnownCalls != tt.wantCalls {
				t.Errorf("SetKnownTools calls = %d, want %d (tools=%v)", rec.setKnownCalls, tt.wantCalls, tt.tools)
			}
		})
	}
}

// TestToAvailableCommands_EmptyVsNonEmpty pins that an empty/nil input
// returns a nil slice (not a non-nil empty slice) and a non-empty input
// is converted element-for-element.
func TestToAvailableCommands_EmptyVsNonEmpty(t *testing.T) {
	if got := toAvailableCommands(nil); got != nil {
		t.Errorf("toAvailableCommands(nil) len=%d = %v, want nil", len(got), got)
	}
	if got := toAvailableCommands([]map[string]any{}); got != nil {
		t.Errorf("toAvailableCommands(empty) len=%d = %v, want nil", len(got), got)
	}
	got := toAvailableCommands([]map[string]any{{"name": "x"}})
	if len(got) != 1 {
		t.Fatalf("toAvailableCommands(1 elem) len = %d, want 1", len(got))
	}
	if got[0].Name != "x" {
		t.Errorf("toAvailableCommands(1 elem)[0].Name = %q, want %q", got[0].Name, "x")
	}
}

// TestToAvailableCommands_MetaExcludesNameAndDescription pins that the
// "name" and "description" keys are lifted into typed fields and never
// leak into the Meta passthrough map, which carries only unknown keys.
func TestToAvailableCommands_MetaExcludesNameAndDescription(t *testing.T) {
	got := toAvailableCommands([]map[string]any{
		{"name": "cmd-x", "description": "desc-d", "extra": "val-e"},
	})
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	ac := got[0]
	if ac.Name != "cmd-x" {
		t.Errorf("Name = %q, want %q", ac.Name, "cmd-x")
	}
	if ac.Description != "desc-d" {
		t.Errorf("Description = %q, want %q", ac.Description, "desc-d")
	}
	if v, ok := ac.Meta["extra"]; !ok || v != "val-e" {
		t.Errorf("Meta[extra] = %v (ok=%v), want %q", v, ok, "val-e")
	}
	if _, ok := ac.Meta["name"]; ok {
		t.Errorf("Meta must not contain key %q (it is extracted to Name)", "name")
	}
	if _, ok := ac.Meta["description"]; ok {
		t.Errorf("Meta must not contain key %q (it is extracted to Description)", "description")
	}
	if len(ac.Meta) != 1 {
		t.Errorf("len(Meta) = %d, want 1 (only the extra key)", len(ac.Meta))
	}
}

// TestToAvailableCommands_MetaAssignedOnlyWhenNonEmpty pins that Meta is
// left nil when a command has no extra keys, and populated only when at
// least one unknown key is present.
func TestToAvailableCommands_MetaAssignedOnlyWhenNonEmpty(t *testing.T) {
	got := toAvailableCommands([]map[string]any{{"name": "n", "extra": "e"}})
	if got[0].Meta == nil {
		t.Fatal("ac.Meta = nil, want non-nil when an extra key is present")
	}
	if got[0].Meta["extra"] != "e" {
		t.Errorf("Meta[extra] = %v, want %q", got[0].Meta["extra"], "e")
	}
	got2 := toAvailableCommands([]map[string]any{{"name": "n", "description": "d"}})
	if got2[0].Meta != nil {
		t.Errorf("ac.Meta = %v, want nil when there are no extra keys", got2[0].Meta)
	}
}
