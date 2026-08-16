package command

// The composer-draft command. What is pinned here is the boundary: the caps and
// the UTF-8 check that keep an unloadable draft off the chat file, and the two
// refusals to create anything — a draft must not turn a client-side chat into a
// sidebar row, and it must not reach the bridge at all.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/testsupport"
)

func draftReq(t *testing.T, chatID api.ChatID, text string) *api.ClientCommand {
	t.Helper()
	payload, err := json.Marshal(api.SetDraftCommand{Text: text})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return &api.ClientCommand{
		Type:      api.CmdSetDraft,
		ChatID:    chatID,
		RequestID: "r1",
		Payload:   payload,
	}
}

func seedEmptyChat(t *testing.T, store api.ChatStore, id api.ChatID) {
	t.Helper()
	if err := store.Mutate(t.Context(), id, func(c *api.Chat, _ bool) bool {
		c.Name = "a chat"
		return true
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func TestCmdSetDraft(t *testing.T) {
	tests := []struct {
		name       string
		text       string
		wantStatus int
		wantStored string
	}{
		{name: "stores the text", text: "half a question", wantStatus: http.StatusOK, wantStored: "half a question"},
		// Empty is a VALUE, not a missing field: it is how a sent or abandoned
		// message is cleared, so it must be accepted rather than rejected.
		{name: "accepts empty as a clear", text: "", wantStatus: http.StatusOK, wantStored: ""},
		{name: "accepts a draft at exactly the cap", text: strings.Repeat("x", api.MaxDraftBytes), wantStatus: http.StatusOK, wantStored: strings.Repeat("x", api.MaxDraftBytes)},
		{name: "refuses one byte over the cap", text: strings.Repeat("x", api.MaxDraftBytes+1), wantStatus: http.StatusRequestEntityTooLarge, wantStored: ""},
		{name: "keeps multibyte text intact", text: "日本語のドラフト", wantStatus: http.StatusOK, wantStored: "日本語のドラフト"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			seedEmptyChat(t, store, "c1")
			b := &recordingBridge{sessionID: "sess-1"}
			d := newBridgeDispatcher(store, b)
			w := httptest.NewRecorder()

			CmdSetDraft(d, t.Context(), w, draftReq(t, "c1", tc.text))

			if w.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d (body %s)", w.Code, tc.wantStatus, w.Body.String())
			}
			c, ok := store.Get(t.Context(), "c1")
			if !ok {
				t.Fatal("chat vanished")
			}
			if c.Draft != tc.wantStored {
				t.Errorf("stored draft len = %d, want %d", len(c.Draft), len(tc.wantStored))
			}
			// A draft is not a session config option: nothing about it belongs on
			// the wire to KAS, and a call per 600ms of typing would be the busiest
			// traffic in the app.
			if b.callCount != 0 {
				t.Errorf("bridge called %d times; a draft save must not reach the agent", b.callCount)
			}
		})
	}
}

// Why the handler carries no UTF-8 check: encoding/json coerces every invalid
// byte sequence in a string literal to U+FFFD as it decodes, so a draft arriving
// through the envelope is valid by construction and the check could not fail.
// Pinned so a future reader does not add one back as a missing guard.
func TestCmdSetDraft_JSONDecodingSanitizesInvalidUTF8(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedEmptyChat(t, store, "c1")
	d := newBridgeDispatcher(store, &recordingBridge{})
	w := httptest.NewRecorder()

	// Raw bytes, not json.Marshal: marshalling would sanitize them before the
	// handler ever saw them, which is the same coercion under test.
	CmdSetDraft(d, t.Context(), w, &api.ClientCommand{
		Type:      api.CmdSetDraft,
		ChatID:    "c1",
		RequestID: "r1",
		Payload:   append(append([]byte(`{"text":"`), 0xff, 0xfe), []byte(`"}`)...),
	})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	c, _ := store.Get(t.Context(), "c1")
	if !utf8.ValidString(c.Draft) {
		t.Errorf("stored draft %q is not valid UTF-8; the chat file would not round-trip", c.Draft)
	}
}

func TestCmdSetDraft_RefusesAMissingChatID(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	d := newBridgeDispatcher(store, &recordingBridge{})
	w := httptest.NewRecorder()

	CmdSetDraft(d, t.Context(), w, draftReq(t, "", "text"))

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestCmdSetDraft_RejectsAMalformedPayload(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedEmptyChat(t, store, "c1")
	d := newBridgeDispatcher(store, &recordingBridge{})
	w := httptest.NewRecorder()

	CmdSetDraft(d, t.Context(), w, &api.ClientCommand{
		Type:      api.CmdSetDraft,
		ChatID:    "c1",
		RequestID: "r1",
		Payload:   json.RawMessage(`{"text":42}`),
	})

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// A chat is a server record from its first prompt onward. Every chat is
// client-side until then, so a draft typed into a brand-new one has nowhere to
// land — and creating the record here would put a row in every connected
// client's sidebar for a conversation nobody has started. Unlike set_mode, which
// DOES auto-create, this is typing rather than a deliberate pick.
func TestCmdSetDraft_DoesNotCreateAChat(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	d := newBridgeDispatcher(store, &recordingBridge{})
	w := httptest.NewRecorder()

	CmdSetDraft(d, t.Context(), w, draftReq(t, "c-never-prompted", "typed but nothing sent"))

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200: a draft on an unsaved chat is a no-op, not an error", w.Code)
	}
	if _, ok := store.Get(t.Context(), "c-never-prompted"); ok {
		t.Error("a draft created a chat record")
	}
}

// The prompt path clears the draft in the same Mutate that appends the user
// message. Belt to the client's own set_draft("") braces: if that POST is lost,
// a reload would otherwise put the sent message back in the box.
func TestAppendUserMessage_ClearsTheDraft(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedEmptyChat(t, store, "c1")
	if err := store.SetDraft(t.Context(), "c1", "the message about to be sent"); err != nil {
		t.Fatalf("SetDraft: %v", err)
	}
	deps := &storeDeps{benchDeps: newBenchDeps(), store: store}

	err := appendUserMessage(deps, t.Context(), "c1", &api.PromptCommand{
		Text:      "the message about to be sent",
		MessageID: "m-1",
	})
	if err != nil {
		t.Fatalf("appendUserMessage: %v", err)
	}

	c, ok := store.Get(t.Context(), "c1")
	if !ok {
		t.Fatal("chat vanished")
	}
	if c.Draft != "" {
		t.Errorf("draft = %q, want cleared by the send", c.Draft)
	}
}
