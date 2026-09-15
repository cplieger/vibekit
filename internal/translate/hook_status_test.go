package translate

import (
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// hookUpdateFrame builds the `update` object of a hook_update session_info_update with
// the hook block where KAS puts it, under `_meta.kiro`.
func hookUpdateFrame(t *testing.T, name, status string) map[string]any {
	t.Helper()
	return map[string]any{
		"sessionUpdate": "session_info_update",
		"_meta": map[string]any{"kiro": map[string]any{
			"kind": "hook_update",
			"hook": map[string]any{
				"hookId":      "h1",
				"operationId": "op-1",
				"name":        name,
				"status":      status,
				"actionType":  "runCommand",
			},
		}},
	}
}

// hookCardCase drives one hook_update through the translator and returns the events it
// broadcast and the calls it buffered.
func hookCardCase(t *testing.T, enabled bool, frame map[string]any, attr FrameAttribution) (*[]vibekit.ServerEvent, []vibekit.ToolCall) {
	t.Helper()
	base, events := newEventCaptureDeps()
	deps := &hookStatusDeps{baseDeps: base, enabled: enabled}
	tr := New(rolesOf(deps), withIDGenerator(func() string { return "id" }))
	chatID := vibekit.ChatID("c1")
	tr.HandleSessionInfoUpdate(t.Context(), chatID, mustJSON(t, frame), attr)
	return events, base.bufStore.GetOrInit(chatID).ToolCalls
}

// TestHandleSessionInfoUpdate_HookUpdateCard pins the `Hook fired` card: one settled
// tool call per hook_update frame, gated on hooks.showStatus, naming the hook and
// carrying no outcome text.
func TestHandleSessionInfoUpdate_HookUpdateCard(t *testing.T) {
	t.Run("ShownWhenEnabled", func(t *testing.T) {
		events, calls := hookCardCase(t, true, hookUpdateFrame(t, "probe-save", hookStatusCompleted), FrameAttribution{})
		if !hasToolCallEvent(events) {
			t.Fatal("hook_update broadcast no tool_call event; want one Hook fired card")
		}
		if len(calls) != 1 {
			t.Fatalf("buffered tool calls = %d, want 1", len(calls))
		}
		got := calls[0]
		if got.ID != "hook-op-1" {
			t.Errorf("ID = %q, want %q", got.ID, "hook-op-1")
		}
		if got.Kind != vibekit.ToolKindHook {
			t.Errorf("Kind = %q, want %q", got.Kind, vibekit.ToolKindHook)
		}
		if got.Title != "Hook fired: probe-save" {
			t.Errorf("Title = %q, want %q", got.Title, "Hook fired: probe-save")
		}
		if got.Status != vibekit.ToolCompleted {
			t.Errorf("Status = %q, want %q", got.Status, vibekit.ToolCompleted)
		}
		if got.Output != "" || got.Input != nil {
			t.Errorf("Output = %q, Input = %s; want no outcome text on the card", got.Output, got.Input)
		}
		for _, word := range []string{"status", "exit", hookStatusCompleted} {
			if strings.Contains(strings.ToLower(got.Title), word) {
				t.Errorf("Title %q carries %q; the card must show no outcome text", got.Title, word)
			}
		}
	})

	t.Run("FailsOnAnyOtherStatus", func(t *testing.T) {
		for _, status := range []string{"", "running", "failed", "canceled", "awaiting_approval", "Success"} {
			t.Run("status_"+status, func(t *testing.T) {
				_, calls := hookCardCase(t, true, hookUpdateFrame(t, "probe", status), FrameAttribution{})
				if len(calls) != 1 {
					t.Fatalf("buffered tool calls = %d, want 1", len(calls))
				}
				if calls[0].Status != vibekit.ToolFailed {
					t.Errorf("hookToolStatus(%q) = %q, want %q", status, calls[0].Status, vibekit.ToolFailed)
				}
			})
		}
	})

	t.Run("NothingWhenDisabled", func(t *testing.T) {
		events, calls := hookCardCase(t, false, hookUpdateFrame(t, "probe-save", hookStatusCompleted), FrameAttribution{})
		if hasToolCallEvent(events) {
			t.Error("hook_update broadcast a tool_call event with hooks.showStatus off; want nothing")
		}
		if len(calls) != 0 {
			t.Errorf("buffered tool calls = %d, want 0", len(calls))
		}
	})

	t.Run("NothingWhenBlockNestedAtWrongLevel", func(t *testing.T) {
		hook := map[string]any{
			"hookId": "h1", "operationId": "op-1", "name": "probe", "status": hookStatusCompleted, "actionType": "runCommand",
		}
		frames := map[string]map[string]any{
			// The block under params._meta rather than params.update._meta: the
			// update object the handler receives then carries no kiro block at all.
			"no_meta_on_update": {"sessionUpdate": "session_info_update"},
			// One level too shallow: a decoder reading update._meta.hook.
			"under_meta_not_kiro": {
				"sessionUpdate": "session_info_update",
				"_meta":         map[string]any{"hook": hook, "kiro": map[string]any{"kind": "hook_update"}},
			},
			// Two levels too shallow: a decoder reading update.hook.
			"under_update_root": {
				"sessionUpdate": "session_info_update",
				"hook":          hook,
				"_meta":         map[string]any{"kiro": map[string]any{"kind": "hook_update"}},
			},
		}
		for name, frame := range frames {
			t.Run(name, func(t *testing.T) {
				events, calls := hookCardCase(t, true, frame, FrameAttribution{})
				if hasToolCallEvent(events) || len(calls) != 0 {
					t.Errorf("a hook block outside update._meta.kiro produced events=%v calls=%d; want nothing",
						hasToolCallEvent(events), len(calls))
				}
			})
		}
	})

	t.Run("TitleIsSingleLineAndBounded", func(t *testing.T) {
		name := "a\nb\x1b[31mc" + strings.Repeat("x", 2000)
		_, calls := hookCardCase(t, true, hookUpdateFrame(t, name, hookStatusCompleted), FrameAttribution{})
		if len(calls) != 1 {
			t.Fatalf("buffered tool calls = %d, want 1", len(calls))
		}
		title := calls[0].Title
		if !strings.HasPrefix(title, "Hook fired: ") {
			t.Errorf("Title = %q, want the Hook fired: prefix", title)
		}
		if strings.ContainsAny(title, "\n\x1b") {
			t.Errorf("Title = %q carries a newline or ESC; want single-line", title)
		}
		// The bound plus runesafe's three-byte truncation marker.
		if maxLen := len("Hook fired: ") + maxDisplayTextBytes + len("..."); len(title) > maxLen {
			t.Errorf("len(Title) = %d, want <= %d", len(title), maxLen)
		}
	})

	t.Run("DroppedForSubagentAndStep", func(t *testing.T) {
		for _, attr := range []FrameAttribution{{SubSessionID: "sub"}, {Step: true}} {
			events, calls := hookCardCase(t, true, hookUpdateFrame(t, "probe", hookStatusCompleted), attr)
			if hasToolCallEvent(events) || len(calls) != 0 {
				t.Errorf("attribution %+v produced events=%v calls=%d; want nothing", attr, hasToolCallEvent(events), len(calls))
			}
		}
	})
}

// TestKnownSessionInfoKinds_HookUpdateIsConsumed pins hook_update out of the
// deliberately-ignored table: a consumed kind listed there would log a decode miss as a
// known drop.
func TestKnownSessionInfoKinds_HookUpdateIsConsumed(t *testing.T) {
	if _, ok := knownSessionInfoKinds["hook_update"]; ok {
		t.Fatal("knownSessionInfoKinds lists hook_update, which is consumed by handleHookUpdate")
	}
}
