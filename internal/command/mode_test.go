package command

import (
	"encoding/json"
	"errors"
	"fmt"
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

// TestSessionConfig_ColdSpawnPersistsAndASessionRefusalDoesNot pins the one
// distinction applySessionConfig exists to make, from both sides and for both config
// commands.
//
// A bridge that exists but has not STARTED is a chat with no session, because the
// manager registers the record before Start so concurrent opens coalesce — so a click
// during a cold spawn must persist for the session door exactly like a bridgeless
// chat, not answer 502. A refusal by the SESSION is the opposite: reporting it is the
// whole reason the live call leads, and persisting it would leave the record claiming
// a setting the session never took.
func TestSessionConfig_ColdSpawnPersistsAndASessionRefusalDoesNot(t *testing.T) {
	tests := map[string]struct {
		callErr     error
		wantStatus  int
		wantApplied bool
	}{
		"a cold-spawning bridge persists": {
			callErr:     fmt.Errorf("write frame: %w", vibekit.ErrBridgeNotStarted),
			wantApplied: true,
		},
		"a session refusal is reported and persists nothing": {
			callErr:    errors.New("-32602 effortLevel is not available for this model"),
			wantStatus: http.StatusBadGateway,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Run("set_mode", func(t *testing.T) {
				store := testsupport.NewInMemoryChatStore()
				host := newBridgeHost(store, &recordingBridge{callErr: test.callErr})

				_, err := CmdSetMode(t.Context(), host, host, host, setModeReq(t, "c1", "spec"))

				assertConfigOutcome(t, err, test.wantStatus)
				c, ok := store.Get(t.Context(), "c1")
				if got := ok && c.CurrentModeID == "spec"; got != test.wantApplied {
					t.Errorf("mode persisted = %v, want %v", got, test.wantApplied)
				}
			})

			t.Run("set_effort", func(t *testing.T) {
				store := testsupport.NewInMemoryChatStore()
				host := newBridgeHost(store, &recordingBridge{callErr: test.callErr})
				payload, err := json.Marshal(vibekit.SetEffortCommand{Level: vibekit.EffortMax})
				if err != nil {
					t.Fatalf("marshal set_effort payload: %v", err)
				}
				cmd := &vibekit.ClientCommand{Type: vibekit.CmdSetEffort, ChatID: "c1", Payload: payload}

				_, err = CmdSetEffort(t.Context(), host, host, cmd)

				assertConfigOutcome(t, err, test.wantStatus)
				c, ok := store.Get(t.Context(), "c1")
				if got := ok && c.Effort == string(vibekit.EffortMax); got != test.wantApplied {
					t.Errorf("effort persisted = %v, want %v", got, test.wantApplied)
				}
			})

			// The third config command, and the one that used to answer this state
			// differently: it persisted FIRST, called the bridge best-effort, discarded
			// the outcome and answered 200 whatever the session said — so a refused
			// toggle left the record, ChatHeader.supervised_mode and every client's
			// checkbox claiming supervised over a session in autopilot, with the
			// client's own optimistic rollback unreachable because a 200 is not an
			// error. It seeds the chat because set_supervised_mode does NOT auto-create
			// one, unlike its two siblings.
			t.Run("set_supervised_mode", func(t *testing.T) {
				store := testsupport.NewInMemoryChatStore()
				seedEmptyChat(t, store, "c1")
				host := newBridgeHost(store, &recordingBridge{callErr: test.callErr})

				_, err := CmdSetSupervisedMode(t.Context(), host, host, supervisedReq(t, "c1", true))

				assertConfigOutcome(t, err, test.wantStatus)
				c, ok := store.Get(t.Context(), "c1")
				if got := ok && c.SupervisedMode; got != test.wantApplied {
					t.Errorf("supervised persisted = %v, want %v", got, test.wantApplied)
				}
			})
		})
	}
}

// assertConfigOutcome grades a config command's error against the status it owes:
// wantStatus 0 means the command must have succeeded.
func assertConfigOutcome(t *testing.T, err error, wantStatus int) {
	t.Helper()
	if wantStatus == 0 {
		if err != nil {
			t.Fatalf("a cold-spawning bridge answered %v; the pick is lost and the pill rolls back", err)
		}
		return
	}
	if err == nil {
		t.Fatalf("a session refusal answered no error, so the record now claims a setting the session refused")
	}
	if got := statusOf(err); got != wantStatus {
		t.Errorf("status = %d, want %d", got, wantStatus)
	}
}
