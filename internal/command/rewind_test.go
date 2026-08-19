package command

// Rewind is destructive and has no undo, so what is pinned here is the shape of
// the damage: which messages leave, that a refusal leaves the record untouched,
// and that the two id rules KAS enforces are honoured before the call is made.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/testsupport"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// recordingBridge is a CommandBridge that records the one call made through it
// and replies with a scripted result. Only Call is exercised; the rest satisfies
// the interface. Shared by every command test that needs to assert what went
// onto the wire.
type recordingBridge struct {
	callErr   error
	result    any
	gotMethod string
	gotParams map[string]any
	callCount int
	sessionID vibekit.SessionID
}

func (b *recordingBridge) Call(_ context.Context, method string, params any) (*vibekit.RPCResponse, error) {
	b.callCount++
	b.gotMethod = method
	if m, ok := params.(map[string]any); ok {
		b.gotParams = m
	}
	if b.callErr != nil {
		return nil, b.callErr
	}
	raw, err := json.Marshal(b.result)
	if err != nil {
		return nil, err
	}
	return &vibekit.RPCResponse{Result: raw}, nil
}

func (b *recordingBridge) Notify(context.Context, string, any) error        { return nil }
func (b *recordingBridge) Respond(context.Context, int64, any, error) error { return nil }
func (b *recordingBridge) SessionID() vibekit.SessionID                     { return b.sessionID }
func (b *recordingBridge) TryAcquireForPrompt() bool                        { return true }
func (b *recordingBridge) ReleaseAfterPrompt()                              {}
func (b *recordingBridge) BeginPromptCall(context.CancelFunc) uint64        { return 0 }
func (b *recordingBridge) EndPromptCall()                                   {}
func (b *recordingBridge) PromptGeneration() uint64                         { return 0 }
func (b *recordingBridge) ArmCancelGrace(uint64, time.Duration) bool        { return false }
func (b *recordingBridge) IsPrimed() bool                                   { return true }
func (b *recordingBridge) SetPrimed()                                       {}

// bridgeDeps adds a bridge to storeDeps so the outgoing call can be observed.
type bridgeDeps struct {
	*storeDeps
	bridge Bridge
}

func (d *bridgeDeps) GetBridge(vibekit.ChatID) Bridge { return d.bridge }

func newBridgeDispatcher(store ChatStore, bridge Bridge) *Dispatcher {
	return New(&bridgeDeps{
		storeDeps: &storeDeps{benchDeps: newBenchDeps(), store: store},
		bridge:    bridge,
	})
}

func rewindReq(t *testing.T, chatID vibekit.ChatID, messageID string) *vibekit.ClientCommand {
	t.Helper()
	payload, err := json.Marshal(vibekit.RewindChatCommand{MessageID: messageID})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return &vibekit.ClientCommand{
		Type:      vibekit.CmdRewindChat,
		ChatID:    chatID,
		RequestID: "r1",
		Payload:   payload,
	}
}

// seedChat writes a four-message transcript: u1, a1, u2, a2.
func seedChat(t *testing.T, store ChatStore, id vibekit.ChatID) {
	t.Helper()
	err := store.Mutate(t.Context(), id, func(c *vibekit.Chat, _ bool) bool {
		c.Messages = []vibekit.Message{
			{ID: "u1", Role: vibekit.RoleUser, Content: "first", Ts: 100},
			{ID: "a1", Role: vibekit.RoleAssistant, Content: "reply one", Ts: 200},
			{ID: "u2", Role: vibekit.RoleUser, Content: "second", Ts: 300},
			{ID: "a2", Role: vibekit.RoleAssistant, Content: "reply two", Ts: 400},
		}
		c.MessageCount = len(c.Messages)
		return true
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func okResult() map[string]any {
	return map[string]any{"success": true, "affectedFiles": []string{"a.go"}, "totalFiles": 2}
}

// The target message goes WITH its successors. Reverting to u2 must leave u1 and
// a1 only — not u2 — because KAS slices from the target inclusive, and a record
// that kept u2 would disagree with the session about what the transcript is.
func TestCmdRewindChat_DropsTheTargetAndEverythingAfter(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedChat(t, store, "c1")
	b := &recordingBridge{result: okResult(), sessionID: "sess-1"}
	d := newBridgeDispatcher(store, b)
	w := httptest.NewRecorder()

	CmdRewindChat(d, t.Context(), w, rewindReq(t, "c1", "u2"))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	c, ok := store.Get(t.Context(), "c1")
	if !ok {
		t.Fatal("chat vanished")
	}
	got := make([]string, 0, len(c.Messages))
	for i := range c.Messages {
		got = append(got, c.Messages[i].ID)
	}
	if len(got) != 2 || got[0] != "u1" || got[1] != "a1" {
		t.Errorf("messages = %v, want [u1 a1]", got)
	}
	if c.MessageCount != 2 {
		t.Errorf("message_count = %d, want 2", c.MessageCount)
	}
}

func TestCmdRewindChat_CallsTheRevertVerbWithTheSessionAndMessage(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedChat(t, store, "c1")
	b := &recordingBridge{result: okResult(), sessionID: "sess-1"}
	d := newBridgeDispatcher(store, b)

	CmdRewindChat(d, t.Context(), httptest.NewRecorder(), rewindReq(t, "c1", "u1"))

	if b.gotMethod != vibekit.MethodCheckpointRevertMultiple {
		t.Errorf("method = %q, want %q", b.gotMethod, vibekit.MethodCheckpointRevertMultiple)
	}
	if b.gotParams["messageId"] != "u1" {
		t.Errorf("messageId = %v, want u1", b.gotParams["messageId"])
	}
	// SessionParams supplies sessionId; KAS rejects the call without it.
	if b.gotParams["sessionId"] != vibekit.SessionID("sess-1") {
		t.Errorf("sessionId = %v, want sess-1", b.gotParams["sessionId"])
	}
}

// KAS refuses a non-user target in-band, so vibekit checks first rather than
// spending a round trip to be told. It also cannot address an assistant turn at
// all: only user ids are shared with KAS (an assistant turn carries KAS's own
// `<uuid>-say`).
func TestCmdRewindChat_RefusesANonUserTarget(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedChat(t, store, "c1")
	b := &recordingBridge{result: okResult(), sessionID: "sess-1"}
	d := newBridgeDispatcher(store, b)
	w := httptest.NewRecorder()

	CmdRewindChat(d, t.Context(), w, rewindReq(t, "c1", "a1"))

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	if b.callCount != 0 {
		t.Errorf("called the bridge %d times, want 0", b.callCount)
	}
	c, _ := store.Get(t.Context(), "c1")
	if len(c.Messages) != 4 {
		t.Errorf("transcript changed on a refused rewind: %d messages", len(c.Messages))
	}
}

func TestCmdRewindChat_RefusesAnUnknownTarget(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedChat(t, store, "c1")
	b := &recordingBridge{result: okResult(), sessionID: "sess-1"}
	d := newBridgeDispatcher(store, b)
	w := httptest.NewRecorder()

	CmdRewindChat(d, t.Context(), w, rewindReq(t, "c1", "nope"))

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	if b.callCount != 0 {
		t.Errorf("called the bridge %d times, want 0", b.callCount)
	}
}

func TestCmdRewindChat_RejectsAnEmptyMessageID(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedChat(t, store, "c1")
	d := newBridgeDispatcher(store, &recordingBridge{result: okResult()})
	w := httptest.NewRecorder()

	CmdRewindChat(d, t.Context(), w, rewindReq(t, "c1", ""))

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// The transcript and the files move together or not at all, so a revert with no
// live session is refused rather than truncating the record alone.
func TestCmdRewindChat_RequiresALiveBridge(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedChat(t, store, "c1")
	d := newBridgeDispatcher(store, nil)
	w := httptest.NewRecorder()

	CmdRewindChat(d, t.Context(), w, rewindReq(t, "c1", "u2"))

	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", w.Code)
	}
	c, _ := store.Get(t.Context(), "c1")
	if len(c.Messages) != 4 {
		t.Errorf("transcript truncated with no session to match: %d messages", len(c.Messages))
	}
}

// KAS's in-band refusal (a live turn, a concurrent revert, an unreadable
// snapshot) must leave the record ALONE. A truncated transcript against an
// un-reverted session is the one outcome worse than a failed rewind.
func TestCmdRewindChat_InBandRefusalLeavesTheRecordIntact(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedChat(t, store, "c1")
	b := &recordingBridge{
		result: map[string]any{
			"success": false,
			"error":   "Cannot revert while the agent is still running. Stop the turn and try again.",
		},
		sessionID: "sess-1",
	}
	d := newBridgeDispatcher(store, b)
	w := httptest.NewRecorder()

	CmdRewindChat(d, t.Context(), w, rewindReq(t, "c1", "u2"))

	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", w.Code)
	}
	// KAS's reason reaches the client: it is more specific than anything vibekit
	// could infer, and mid-turn is the case a user can actually act on.
	if body := w.Body.String(); !strings.Contains(body, "still running") {
		t.Errorf("response %s does not carry KAS's reason", body)
	}
	c, _ := store.Get(t.Context(), "c1")
	if len(c.Messages) != 4 {
		t.Errorf("transcript truncated after a refused revert: %d messages", len(c.Messages))
	}
}

func TestCmdRewindChat_TransportFailureLeavesTheRecordIntact(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedChat(t, store, "c1")
	b := &recordingBridge{callErr: errors.New("broken pipe"), sessionID: "sess-1"}
	d := newBridgeDispatcher(store, b)
	w := httptest.NewRecorder()

	CmdRewindChat(d, t.Context(), w, rewindReq(t, "c1", "u2"))

	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", w.Code)
	}
	c, _ := store.Get(t.Context(), "c1")
	if len(c.Messages) != 4 {
		t.Errorf("transcript truncated after a failed call: %d messages", len(c.Messages))
	}
}

// Reverting to the FIRST message empties the transcript. Legal, and the chat
// survives — it is the same chat, back at the start, not a deleted one.
func TestCmdRewindChat_ToTheFirstMessageEmptiesTheTranscript(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedChat(t, store, "c1")
	b := &recordingBridge{result: okResult(), sessionID: "sess-1"}
	d := newBridgeDispatcher(store, b)
	w := httptest.NewRecorder()

	CmdRewindChat(d, t.Context(), w, rewindReq(t, "c1", "u1"))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	c, ok := store.Get(t.Context(), "c1")
	if !ok {
		t.Fatal("chat was deleted; a rewind is not a delete")
	}
	if len(c.Messages) != 0 {
		t.Errorf("messages = %d, want 0", len(c.Messages))
	}
}

func TestUserMessageIndex(t *testing.T) {
	msgs := []vibekit.Message{
		{ID: "u1", Role: vibekit.RoleUser},
		{ID: "a1", Role: vibekit.RoleAssistant},
		{ID: "e1", Role: vibekit.RoleEvent},
		{ID: "u2", Role: vibekit.RoleUser},
	}
	cases := map[string]int{"u1": 0, "u2": 3, "a1": -1, "e1": -1, "missing": -1, "": -1}
	for id, want := range cases {
		if got := userMessageIndex(msgs, id); got != want {
			t.Errorf("userMessageIndex(%q) = %d, want %d", id, got, want)
		}
	}
}
