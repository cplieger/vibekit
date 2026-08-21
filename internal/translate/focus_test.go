package translate

import (
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
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
func chatStatusPayloads(t *testing.T, events *[]vibekit.ServerEvent) []vibekit.ChatStatusPayload {
	t.Helper()
	var out []vibekit.ChatStatusPayload
	for _, e := range *events {
		if e.Type != vibekit.EventChatStatus {
			continue
		}
		p, ok := e.Payload.(vibekit.ChatStatusPayload)
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
	tr := New(rolesOf(deps))

	tr.HandleSessionInfoUpdate(t.Context(), "c1", focusFrame(t, map[string]any{
		"title":       "Photo organizer CLI setup",
		"description": "Planning module layout and creating the stub main.",
		"status":      "in_progress",
	}), "")

	c, ok := store.Get(t.Context(), "c1")
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
	tr := New(rolesOf(deps))

	tr.HandleSessionInfoUpdate(t.Context(), "c1", focusFrame(t, map[string]any{
		"description": "Step 1 complete.",
		"status":      "completed",
	}), "")

	c, _ := store.Get(t.Context(), "c1")
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
	tr := New(rolesOf(deps))

	tr.HandleSessionInfoUpdate(t.Context(), "c1", focusFrame(t, map[string]any{
		"title": "Sub focus", "status": "in_progress",
	}), "sub-1")

	c, _ := store.Get(t.Context(), "c1")
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
				if err := store.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
					c.Messages = append(c.Messages, vibekit.Message{ID: "m1", Role: vibekit.RoleUser, Content: tc.userMsg})
					return true
				}); err != nil {
					t.Fatal(err)
				}
			}
			tr := New(rolesOf(deps))

			tr.HandleSessionInfoUpdate(t.Context(), "c1", focusFrame(t, map[string]any{"title": tc.title}), "")

			c, _ := store.Get(t.Context(), "c1")
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
	chat := &vibekit.Chat{Messages: []vibekit.Message{
		{Role: vibekit.RoleUser, Content: "  padded prompt text  "},
		{Role: vibekit.RoleAssistant, Content: "assistant text"},
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

// A title exactly at the rune cap is adopted; one rune past it is not. The cap
// exists to keep a runaway title out of the chat list, and rejecting a title
// that sits exactly on it discards a legitimate one.
func TestHandleSessionInfoUpdate_FocusTitleRuneCapIsInclusive(t *testing.T) {
	tests := []struct {
		name     string
		title    string
		wantName string
	}{
		{name: "exactly_at_the_cap", title: strings.Repeat("t", maxFocusTitleRunes), wantName: strings.Repeat("t", maxFocusTitleRunes)},
		{name: "one_rune_past_the_cap", title: strings.Repeat("t", maxFocusTitleRunes+1), wantName: "A"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deps, _, store := depsWithStore(t, "c1")
			tr := New(rolesOf(deps))

			tr.HandleSessionInfoUpdate(t.Context(), "c1", focusFrame(t, map[string]any{
				"title": tc.title, "status": "in_progress",
			}), "")

			c, _ := store.Get(t.Context(), "c1")
			if c.Name != tc.wantName {
				t.Errorf("chat name after a %d-rune focus title = %q, want %q",
					len([]rune(tc.title)), c.Name, tc.wantName)
			}
		})
	}
}

// chat_status is broadcast when the focus update carries either half of it, and
// only then. A status with no description is the ordinary turn-completion shape,
// and an update with neither is the one that must stay silent — a status event
// with two empty fields blanks the client's status line.
func TestHandleSessionInfoUpdate_FocusBroadcastsOnlyWhenItHasSomethingToSay(t *testing.T) {
	tests := []struct {
		name          string
		focus         map[string]any
		wantBroadcast bool
		wantStatus    string
	}{
		{
			name:          "status_without_a_description",
			focus:         map[string]any{"status": "completed"},
			wantBroadcast: true,
			wantStatus:    "completed",
		},
		{
			name:          "description_without_a_status",
			focus:         map[string]any{"description": "Step 1 complete."},
			wantBroadcast: true,
		},
		{
			name:          "neither_status_nor_description",
			focus:         map[string]any{"title": "Some title"},
			wantBroadcast: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deps, events, _ := depsWithStore(t, "c1")
			tr := New(rolesOf(deps))

			tr.HandleSessionInfoUpdate(t.Context(), "c1", focusFrame(t, tc.focus), "")

			got := chatStatusPayloads(t, events)
			if !tc.wantBroadcast {
				if len(got) != 0 {
					t.Fatalf("chat_status payloads = %+v, want none for %v", got, tc.focus)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("chat_status payloads = %+v, want exactly one for %v", got, tc.focus)
			}
			if got[0].Status != tc.wantStatus {
				t.Errorf("chat_status status = %q, want %q", got[0].Status, tc.wantStatus)
			}
		})
	}
}
