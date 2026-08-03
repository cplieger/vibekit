package translate

import (
	"context"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// focusFrame builds a session_info_update raw payload carrying a
// focus_update block, the shape probe-verified on the live 2.12.1 wire.
func focusFrame(t *testing.T, focus map[string]any) []byte {
	t.Helper()
	return mustJSON(t, map[string]any{
		"sessionUpdate": "session_info_update",
		"_meta": map[string]any{
			"kiro": map[string]any{
				"kind":  "focus_update",
				"focus": focus,
			},
		},
	})
}

// chatStatusPayloads filters the captured events down to chat_status
// payloads.
func chatStatusPayloads(t *testing.T, events *[]api.ServerEvent) []api.ChatStatusPayload {
	t.Helper()
	var out []api.ChatStatusPayload
	for _, e := range *events {
		if e.Type != api.EventChatStatus {
			continue
		}
		p, ok := e.Payload.(api.ChatStatusPayload)
		if !ok {
			t.Fatalf("chat_status payload type = %T", e.Payload)
		}
		out = append(out, p)
	}
	return out
}

// An agent-authored focus update adopts the title onto the chat record and
// broadcasts the status/description as an ephemeral chat_status event.
func TestHandleSessionInfoUpdate_FocusAdoptsTitleAndStatus(t *testing.T) {
	deps, events, store := depsWithStore(t, "c1")
	tr := New(deps)

	tr.HandleSessionInfoUpdate(context.Background(), "c1", focusFrame(t, map[string]any{
		"title":       "Photo organizer CLI setup",
		"description": "Planning module layout and creating the stub main.",
		"status":      "in_progress",
	}), "")

	c, ok := store.Get(context.Background(), "c1")
	if !ok {
		t.Fatal("chat c1 missing")
	}
	if c.Name != "Photo organizer CLI setup" {
		t.Errorf("chat name = %q, want the focus title", c.Name)
	}
	got := chatStatusPayloads(t, events)
	if len(got) != 1 || got[0].Status != "in_progress" || got[0].Description == "" {
		t.Fatalf("chat_status payloads = %+v, want one in_progress with description", got)
	}
}

// A status/description-only focus update (the turn-completion shape) leaves
// the title untouched and still broadcasts chat_status.
func TestHandleSessionInfoUpdate_FocusStatusOnly(t *testing.T) {
	deps, events, store := depsWithStore(t, "c1")
	tr := New(deps)

	tr.HandleSessionInfoUpdate(context.Background(), "c1", focusFrame(t, map[string]any{
		"description": "Step 1 complete.",
		"status":      "completed",
	}), "")

	c, _ := store.Get(context.Background(), "c1")
	if c.Name != "A" {
		t.Errorf("chat name = %q, want the seeded name untouched", c.Name)
	}
	got := chatStatusPayloads(t, events)
	if len(got) != 1 || got[0].Status != "completed" {
		t.Fatalf("chat_status payloads = %+v, want one completed", got)
	}
}

// Subagent focus frames are dropped by the parent-only gate.
func TestHandleSessionInfoUpdate_FocusDropsSubagent(t *testing.T) {
	deps, events, store := depsWithStore(t, "c1")
	tr := New(deps)

	tr.HandleSessionInfoUpdate(context.Background(), "c1", focusFrame(t, map[string]any{
		"title": "Sub focus", "status": "in_progress",
	}), "sub-1")

	c, _ := store.Get(context.Background(), "c1")
	if c.Name != "A" {
		t.Errorf("chat name = %q, want untouched", c.Name)
	}
	if got := chatStatusPayloads(t, events); len(got) != 0 {
		t.Fatalf("chat_status from a subagent frame: %+v", got)
	}
}

// KAS's first-prompt derivation (the prompt text or its "..."-truncation,
// emitted title-only) must not clobber the chat name — vibekit's own
// utility title is better. Agent-authored titles pass.
func TestHandleSessionInfoUpdate_FocusFiltersDerivedTitle(t *testing.T) {
	longPrompt := strings.Repeat("Fix the flaky retry test in the scheduler package. ", 4)
	cases := []struct {
		name    string
		userMsg string
		title   string
		adopt   bool
	}{
		{"short prompt verbatim", "Fix the retry test", "Fix the retry test", false},
		{"long prompt truncation", longPrompt, strings.TrimSpace(longPrompt)[:77] + "...", false},
		{"prime-derived (switch)", "", PrimePreambleSwitch[:77] + "...", false},
		{"agent-authored", "Fix the retry test", "Scheduler retry flake", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps, _, store := depsWithStore(t, "c1")
			if tc.userMsg != "" {
				if err := store.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
					c.Messages = append(c.Messages, api.Message{ID: "m1", Role: api.RoleUser, Content: tc.userMsg})
					return true
				}); err != nil {
					t.Fatal(err)
				}
			}
			tr := New(deps)

			tr.HandleSessionInfoUpdate(context.Background(), "c1", focusFrame(t, map[string]any{"title": tc.title}), "")

			c, _ := store.Get(context.Background(), "c1")
			if tc.adopt && c.Name != tc.title {
				t.Errorf("chat name = %q, want adopted title %q", c.Name, tc.title)
			}
			if !tc.adopt && c.Name != "A" {
				t.Errorf("chat name = %q, want seeded name (derived title filtered)", c.Name)
			}
		})
	}
}

// titleIsPromptDerived edge cases the integration table above doesn't hit.
func TestTitleIsPromptDerived(t *testing.T) {
	chat := &api.Chat{Messages: []api.Message{
		{Role: api.RoleUser, Content: "  padded prompt text  "},
		{Role: api.RoleAssistant, Content: "assistant text"},
	}}
	cases := []struct {
		name  string
		title string
		want  bool
	}{
		{"trims user message before compare", "padded prompt text", true},
		{"assistant text never matches", "assistant text", false},
		{"prefix without ellipsis is NOT derived", "padded prompt", false},
		{"ellipsized prefix of user msg", "padded prompt te...", true},
		{"unrelated", "Photo organizer CLI setup", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := titleIsPromptDerived(tc.title, chat); got != tc.want {
				t.Errorf("titleIsPromptDerived(%q) = %v, want %v", tc.title, got, tc.want)
			}
		})
	}
}
