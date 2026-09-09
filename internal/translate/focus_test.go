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
	}), FrameAttribution{})

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
	}), FrameAttribution{})

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
	}), FrameAttribution{SubSessionID: "sub-1"})

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

			tr.HandleSessionInfoUpdate(t.Context(), "c1", focusFrame(t, map[string]any{"title": tc.title}), FrameAttribution{})

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
			}), FrameAttribution{})

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

			tr.HandleSessionInfoUpdate(t.Context(), "c1", focusFrame(t, tc.focus), FrameAttribution{})

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

// realFocusDescription is the longest of the 57 non-empty focus descriptions measured
// over 336 live session files, at 303 bytes. It is the fixture that stops the bound and
// the sanitizer mangling honest text: nothing in it is unsafe and nothing is over 512.
const realFocusDescription = "Investigated vibekit's empty agent-initiated turns. Root cause is " +
	"workflow-step frames folding onto the launching chat and opening a turn whose blocks " +
	"the client drops. Found an uncommitted fix already in the tree; now measuring whether " +
	"it eliminates the empty cards without costing the auto-wake label."

// The description is untrusted model-authored text with three rendering surfaces (the tab
// tooltip, the in-app toast, the OS notification body) and a retained cache entry that
// outlives the turn, and nothing on the wire bounds it. displayText is the one owner of
// that policy, at the door, so no sink can drift from it.
func TestHandleSessionInfoUpdate_FocusSanitizesAndBoundsTheDescription(t *testing.T) {
	tests := []struct {
		name string
		desc string
		want string
	}{
		{
			// U+202E reverses the rendered tooltip; U+202C pops it. Both become spaces,
			// so the deception is on screen rather than deleted with its evidence.
			name: "a_bidi_override_becomes_a_space",
			desc: "Running \u202Ednuof-eman- ecapskrow/ fr- mr\u202C now",
			want: "Running  dnuof-eman- ecapskrow/ fr- mr  now",
		},
		{
			// The two zero-width runes the policy DOES cover, because they are
			// Bidi_Control: U+200E LEFT-TO-RIGHT MARK and U+200F RIGHT-TO-LEFT MARK.
			name: "zero_width_bidi_marks_become_spaces",
			desc: "Write \u200Eshalom\u200F now",
			want: "Write  shalom  now",
		},
		{
			// C0, ESC and DEL. The ESC is not stripped as a sequence here (that is
			// sanitize.Output's job on terminal bytes); each byte becomes a space.
			name: "control_characters_become_spaces",
			desc: "Done\x07 with\x1b[31m the\x7f pass",
			want: "Done  with [31m the  pass",
		},
		{
			// U+0085 NEL and U+009B CSI: encoding/json and slog's JSONHandler emit C1
			// raw, so escaping C0 alone would leave a single-rune escape introducer.
			name: "c1_controls_become_spaces",
			desc: "Step\u0085one\u009Btwo",
			want: "Step one two",
		},
		{
			// renderLines splits the tooltip on \n and emits a <br> per segment, so a
			// newline breaks the surface's single-line contract.
			name: "an_embedded_newline_becomes_a_space",
			desc: "line one\nline two\r\n",
			want: "line one line two",
		},
		{
			// Legal unescaped in JSON, line terminators to a JS viewer.
			name: "paragraph_separators_become_spaces",
			desc: "a\u2028b\u2029c",
			want: "a b c",
		},
		{
			name: "a_real_description_passes_through_byte_identical",
			desc: realFocusDescription,
			want: realFocusDescription,
		},
		{
			// The marker rides OUTSIDE the cap, so the bound is maxDisplayTextBytes
			// retained plus three. Asserting the exact string is what pins the NUMBER.
			name: "an_over_long_description_is_marked_at_the_bound",
			desc: strings.Repeat("x", 700),
			want: strings.Repeat("x", maxDisplayTextBytes) + "...",
		},
		{
			// A zero-width rune that is NOT Bidi_Control survives, and that is the
			// preset's documented non-goal rather than a gap: rewriting U+200B/C/D
			// breaks a ZWJ emoji sequence in a legitimate description, and deleting
			// them is what display_text.go rules out for this surface.
			name: "a_non_bidi_zero_width_rune_survives_by_design",
			desc: "read\u200Bme \U0001F468\u200D\U0001F469\u200D\U0001F467",
			want: "read\u200Bme \U0001F468\u200D\U0001F469\u200D\U0001F467",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deps, events, _ := depsWithStore(t, "c1")
			tr := New(rolesOf(deps))

			tr.HandleSessionInfoUpdate(t.Context(), "c1", focusFrame(t, map[string]any{
				"status": "in_progress", "description": tc.desc,
			}), FrameAttribution{})

			got := chatStatusPayloads(t, events)
			if len(got) != 1 {
				t.Fatalf("chat_status payloads = %+v, want exactly one", got)
			}
			if got[0].Description != tc.want {
				t.Errorf("broadcast description for %q =\n\t%q, want\n\t%q", tc.desc, got[0].Description, tc.want)
			}
		})
	}
}

// The sanitizer and the bound both run BEFORE the both-empty return, and that ordering is
// the whole interaction with FU3's merge. chatStatusCache.Merge reads a both-empty payload
// as a CLEAR and deletes the retained entry, so a description of nothing but control
// characters must not reach it. The early return is what keeps that door shut on one side
// of the bound, and the truncation marker surviving TrimSpace is what keeps it shut on the
// other. A fix that sanitized at the broadcast instead would publish a both-empty frame and
// wipe a retained waiting_on_user while the agent is still waiting.
func TestHandleSessionInfoUpdate_FocusDescriptionThatSanitizesToEmpty(t *testing.T) {
	tests := []struct {
		name          string
		focus         map[string]any
		wantBroadcast bool
		wantStatus    string
		wantDesc      string
	}{
		{
			// Under the bound, so it empties and the frame is dropped outright.
			name:          "description_only_publishes_nothing",
			focus:         map[string]any{"description": "\x07\x1b\x7f\u202E"},
			wantBroadcast: false,
		},
		{
			// The status half is a real declaration, so the frame goes out carrying an
			// EMPTY description, indistinguishable from an omitted one. That is what
			// makes Merge carry the retained description forward instead of clearing it.
			name:          "with_a_status_it_publishes_the_status_alone",
			focus:         map[string]any{"status": "in_progress", "description": "\x07\x07\x07"},
			wantBroadcast: true,
			wantStatus:    "in_progress",
		},
		{
			// Past the bound the marker is appended after the cap and TrimSpace cannot
			// strip it, so this reduces to the bare marker rather than to empty and can
			// no more reach Merge's delete than the short one can. Non-empty is the safe
			// direction, and the marker is honest: bytes really were dropped.
			name:          "an_over_long_control_only_description_keeps_its_marker",
			focus:         map[string]any{"description": strings.Repeat("\x07", maxDisplayTextBytes+1)},
			wantBroadcast: true,
			wantDesc:      "...",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deps, events, _ := depsWithStore(t, "c1")
			tr := New(rolesOf(deps))

			tr.HandleSessionInfoUpdate(t.Context(), "c1", focusFrame(t, tc.focus), FrameAttribution{})

			got := chatStatusPayloads(t, events)
			if !tc.wantBroadcast {
				if len(got) != 0 {
					// %q, not %+v: a description of nothing but control characters
					// prints as an empty field under %+v, so the failure reads as a
					// both-empty payload rather than as the unsanitized one it is.
					t.Fatalf("published %d chat_status frames for %v, want none (status=%q description=%q): "+
						"a both-empty payload deletes the retained entry",
						len(got), tc.focus, got[0].Status, got[0].Description)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("chat_status payloads = %+v, want exactly one", got)
			}
			if got[0].Status != tc.wantStatus {
				t.Errorf("chat_status status = %q, want %q", got[0].Status, tc.wantStatus)
			}
			if got[0].Description != tc.wantDesc {
				t.Errorf("chat_status description = %q, want %q", got[0].Description, tc.wantDesc)
			}
		})
	}
}

func TestHandleSessionInfoUpdate_FocusSanitizesTitle(t *testing.T) {
	deps, _, store := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))

	tr.HandleSessionInfoUpdate(t.Context(), "c1", focusFrame(t, map[string]any{
		"title": "\x1b[31mRelease\x1b[0m\ncheck",
	}), FrameAttribution{})

	c, _ := store.Get(t.Context(), "c1")
	if c.Name != "Release check" {
		t.Errorf("chat name = %q, want %q", c.Name, "Release check")
	}
}
