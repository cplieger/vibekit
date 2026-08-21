package command

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/testsupport"
	"github.com/cplieger/vibekit/internal/vibekit"
)

func TestValidatePromptPayload(t *testing.T) {
	valid := func(text, msgID, model string) []byte {
		b, _ := json.Marshal(vibekit.PromptCommand{Text: text, MessageID: msgID, Model: model})
		return b
	}

	tests := []struct {
		name       string
		payload    []byte
		wantStatus int
		wantErr    bool
	}{
		{"valid minimal", valid("hello", "msg-1", ""), 0, false},
		{"valid with model", valid("hi", "msg-2", "claude"), 0, false},
		{"empty text", valid("", "msg-1", ""), http.StatusBadRequest, true},
		{"text at exact cap", valid(strings.Repeat("a", maxPromptBytes), "msg-1", ""), 0, false},
		{"oversized text", valid(strings.Repeat("x", maxPromptBytes+1), "msg-1", ""), http.StatusRequestEntityTooLarge, true},
		{"missing message_id", valid("hi", "", ""), http.StatusBadRequest, true},
		{"invalid message_id", valid("hi", "msg id/bad", ""), http.StatusBadRequest, true},
		{"invalid model", valid("hi", "msg-1", "bad model!"), http.StatusBadRequest, true},
		{"malformed json", []byte(`{not json`), http.StatusBadRequest, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := &vibekit.ClientCommand{Payload: tc.payload}
			_, status, err := validatePromptPayload(cmd)
			if tc.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if status != tc.wantStatus {
				t.Errorf("status = %d, want %d", status, tc.wantStatus)
			}
		})
	}
}

// A chat's first prompt names it, because a tab labelled "New chat" forever is
// a tab the user cannot find again. Only the FIRST message on a chat still
// carrying the default name may rename it: a chat the user named, or one that
// already holds a turn, keeps what it has.
func TestAppendUserMessage_DerivesTheChatNameFromTheFirstMessage(t *testing.T) {
	const eighty = "12345678901234567890123456789012345678901234567890123456789012345678901234567890"
	cases := []struct {
		name     string
		seed     bool // seed the chat first, so it already carries a name
		text     string
		wantName string
	}{
		{name: "the first message becomes the name", text: "fix the flaky purge test", wantName: "fix the flaky purge test"},
		{name: "eighty runes is the last length kept whole", text: eighty, wantName: eighty},
		{name: "longer text is cut and marked", text: eighty + " and then some more", wantName: eighty + "..."},
		{name: "a chat that already has a name keeps it", seed: true, text: "fix the flaky purge test", wantName: "a chat"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			if tc.seed {
				seedEmptyChat(t, store, "c1")
			}
			deps := &storeDeps{benchDeps: newBenchDeps(), store: store}

			err := appendUserMessage(t.Context(), deps, deps,
				Workspace{Dir: t.TempDir(), ConfigDir: t.TempDir()}, "c1",
				&vibekit.PromptCommand{Text: tc.text, MessageID: "m-1"})
			if err != nil {
				t.Fatalf("appendUserMessage: %v", err)
			}

			c, ok := store.Get(t.Context(), "c1")
			if !ok {
				t.Fatal("chat vanished")
			}
			if c.Name != tc.wantName {
				t.Errorf("name after a %d-byte first message = %q, want %q", len(tc.text), c.Name, tc.wantName)
			}
		})
	}
}

// The second message must not rename the chat: the name belongs to the opening
// question, not to whatever was said last.
func TestAppendUserMessage_LeavesTheNameAloneAfterTheFirstMessage(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	deps := &storeDeps{benchDeps: newBenchDeps(), store: store}
	ws := Workspace{Dir: t.TempDir(), ConfigDir: t.TempDir()}

	for _, m := range []*vibekit.PromptCommand{
		{Text: "the opening question", MessageID: "m-1"},
		{Text: "a follow up nobody wants in the tab title", MessageID: "m-2"},
	} {
		if err := appendUserMessage(t.Context(), deps, deps, ws, "c1", m); err != nil {
			t.Fatalf("appendUserMessage(%s): %v", m.MessageID, err)
		}
	}

	c, ok := store.Get(t.Context(), "c1")
	if !ok {
		t.Fatal("chat vanished")
	}
	if c.Name != "the opening question" {
		t.Errorf("name = %q, want it fixed by the first message", c.Name)
	}
}
