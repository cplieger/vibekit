package command

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/cplieger/vibekit/internal/testsupport"
	"github.com/cplieger/vibekit/internal/vibekit"
)

func setModeReq(t *testing.T, chatID vibekit.ChatID, modeID string) *vibekit.ClientCommand {
	t.Helper()
	payload, err := json.Marshal(vibekit.SetModeCommand{ModeID: modeID})
	if err != nil {
		t.Fatalf("marshal set_mode payload: %v", err)
	}
	return &vibekit.ClientCommand{Type: vibekit.CmdSetMode, ChatID: chatID, Payload: payload}
}

// A mode pick on a tombstoned id is a 404, and it now comes from the store's own
// refusal rather than being inferred.
//
// The inference it replaces was a no-op mutation plus an absent record, which was
// the only reading available while a refused write reported nil. It also could not
// tell that case apart from a store that lost the chat between the two calls, and
// it made every no-op pick pay a second read.
func TestCmdSetMode_TombstonedChatIs404(t *testing.T) {
	host := newTestHost(t, tombstonedChats{testsupport.NewInMemoryChatStore()})

	_, err := CmdSetMode(t.Context(), host, host, host, setModeReq(t, "c1", "spec"))

	if err == nil {
		t.Fatal("CmdSetMode on a tombstoned chat returned no error; the pill flips for a chat that does not exist")
	}
	if got := statusOf(err); got != http.StatusNotFound {
		t.Errorf("CmdSetMode on a tombstoned chat = %d, want %d", got, http.StatusNotFound)
	}
}

// The two ordinary outcomes, together, because they are what stops the refusal
// above from being spelled as "anything that changed nothing is a 404".
//
// A repeat pick of the mode already in force changes nothing and must still
// succeed silently — no error, and no mode_changed frame for a mode that did not
// move. A pick on a chat that is not a server record yet must AUTO-CREATE it, or
// every mode chosen before the first prompt is lost.
func TestCmdSetMode_NoOpAndAutoCreate(t *testing.T) {
	t.Run("a repeat pick succeeds and says nothing", func(t *testing.T) {
		store := testsupport.NewInMemoryChatStore()
		spy := &promptSpy{hostDouble: newTestHost(t, store)}

		if _, err := CmdSetMode(t.Context(), spy, spy, spy, setModeReq(t, "c1", "spec")); err != nil {
			t.Fatalf("first pick: %v", err)
		}
		before := len(spy.events)
		if _, err := CmdSetMode(t.Context(), spy, spy, spy, setModeReq(t, "c1", "spec")); err != nil {
			t.Fatalf("repeat pick: %v", err)
		}
		for _, evt := range spy.events[before:] {
			if evt.Type == vibekit.EventModeChanged {
				t.Error("a repeat pick of the mode already in force broadcast mode_changed")
			}
		}
	})

	t.Run("a chat with no record yet is created", func(t *testing.T) {
		store := testsupport.NewInMemoryChatStore()
		host := newTestHost(t, store)

		if _, err := CmdSetMode(t.Context(), host, host, host, setModeReq(t, "c1", "spec")); err != nil {
			t.Fatalf("CmdSetMode on a fresh chat: %v", err)
		}

		c, ok := store.Get(t.Context(), "c1")
		if !ok {
			t.Fatal("set_mode on a chat with no record did not create one; the pick cannot reach session/new")
		}
		if c.CurrentModeID != "spec" {
			t.Errorf("CurrentModeID = %q, want %q", c.CurrentModeID, "spec")
		}
	})
}
