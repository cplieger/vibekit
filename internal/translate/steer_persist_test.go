package translate

// A steer's DURABLE row: what a page reload rebuilds the note from.
//
// The dock rows and the transcript marks both die with the page, and a plain F5 on
// a live bridge triggers no session/load, so before these the only writer of a
// steer row was the REPLAY projection — a landed steer survived a container restart
// and not a refresh.

import (
	"testing"

	"github.com/cplieger/vibekit/internal/testsupport"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// steerRows returns the chat's persisted steer rows, in file order.
func steerRows(t *testing.T, store *testsupport.InMemoryChatStore, chatID vibekit.ChatID) []vibekit.Message {
	t.Helper()
	c, ok := store.Get(t.Context(), chatID)
	if !ok {
		t.Fatalf("chat %q not in the store", chatID)
	}
	var out []vibekit.Message
	for i := range c.Messages {
		if c.Messages[i].UserKind == vibekit.UserKindSteer {
			out = append(out, c.Messages[i])
		}
	}
	return out
}

func TestSteeringInjected_PersistsAReadSteerRow(t *testing.T) {
	deps, _, store := depsWithStore(t, "c1")
	deps.userSteers = map[string]bool{"steer-1": true}
	tr := New(rolesOf(deps))

	tr.HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_injected", map[string]any{
			"messageId": "steer-1",
			"content":   "use tabs",
		}), FrameAttribution{})

	rows := steerRows(t, store, "c1")
	if len(rows) != 1 {
		t.Fatalf("persisted %d steer rows, want 1 — a read steer must survive a reload", len(rows))
	}
	row := rows[0]
	// The id is KAS's own steer id, which is also the id the replay projection
	// stamps, so a later session/load dedupes on it rather than doubling the note.
	if row.ID != "steer-1" {
		t.Errorf("ID = %q, want the steer id", row.ID)
	}
	if row.Role != vibekit.RoleUser {
		t.Errorf("Role = %q, want %q", row.Role, vibekit.RoleUser)
	}
	if row.Content != "use tabs" {
		t.Errorf("Content = %q, want the steer's text", row.Content)
	}
	if row.SteerState != vibekit.SteerStateRead {
		t.Errorf("SteerState = %q, want %q", row.SteerState, vibekit.SteerStateRead)
	}
	if row.SteerOrigin != vibekit.SteerOriginUser {
		t.Errorf("SteerOrigin = %q, want %q", row.SteerOrigin, vibekit.SteerOriginUser)
	}
	if row.TurnOutcome != "" {
		t.Errorf("TurnOutcome = %q, want empty — a steer row must not close a turn", row.TurnOutcome)
	}
}

// The half that matters most: a correction the agent NEVER READ. Rendering it
// after a reload as though it had landed is a false statement about the user's
// own message, which is worse than the note being absent.
func TestSteeringCleared_PersistsAnUndeliveredSteerRow(t *testing.T) {
	deps, _, store := depsWithStore(t, "c1")
	deps.userSteers = map[string]bool{"steer-1": true}
	tr := New(rolesOf(deps))

	// The queued frame is what puts the text in reach: the cleared frame carries
	// ids and nothing else.
	tr.HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_queued", map[string]any{
			"messageId": "steer-1",
			"content":   "use tabs",
		}), FrameAttribution{})
	tr.HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_cleared", map[string]any{
			"messageIds": []string{"steer-1"},
		}), FrameAttribution{})

	rows := steerRows(t, store, "c1")
	if len(rows) != 1 {
		t.Fatalf("persisted %d steer rows, want 1", len(rows))
	}
	if rows[0].Content != "use tabs" {
		t.Errorf("Content = %q, want the steer's text", rows[0].Content)
	}
	if rows[0].SteerState != vibekit.SteerStateDropped {
		t.Errorf("SteerState = %q, want %q", rows[0].SteerState, vibekit.SteerStateDropped)
	}
}

// KAS clears its buffer at EVERY turn boundary, so the cleared frame names ids the
// model already read. That arrival is housekeeping, and reading it as a drop would
// overwrite a delivered steer with "never read".
func TestSteeringCleared_DoesNotOverwriteAReadSteer(t *testing.T) {
	deps, _, store := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))

	for _, f := range []struct {
		kind   string
		fields map[string]any
	}{
		{"steering_queued", map[string]any{"messageId": "steer-1", "content": "use tabs"}},
		{"steering_injected", map[string]any{"messageId": "steer-1", "content": "use tabs"}},
		{"steering_cleared", map[string]any{"messageIds": []string{"steer-1"}}},
	} {
		tr.HandleSessionInfoUpdate(t.Context(), "c1", steerFrame(t, f.kind, f.fields), FrameAttribution{})
	}

	rows := steerRows(t, store, "c1")
	if len(rows) != 1 {
		t.Fatalf("persisted %d steer rows, want 1 — the clear is housekeeping", len(rows))
	}
	if rows[0].SteerState != vibekit.SteerStateRead {
		t.Errorf("SteerState = %q, want %q", rows[0].SteerState, vibekit.SteerStateRead)
	}
}

// An agent-origin steer keeps its own origin, or the note's title claims a
// workflow's report is something the reader typed — the defect SteerOrigin exists
// to prevent, which the durable row would otherwise reintroduce on every reload.
func TestSteeringInjected_PersistsTheAgentOrigin(t *testing.T) {
	deps, _, store := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))

	tr.HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_injected", map[string]any{
			"messageId": "notify-wf-9",
			"content":   "A workflow you launched completed.",
		}), FrameAttribution{})

	rows := steerRows(t, store, "c1")
	if len(rows) != 1 {
		t.Fatalf("persisted %d steer rows, want 1", len(rows))
	}
	if rows[0].SteerOrigin != vibekit.SteerOriginAgent {
		t.Errorf("SteerOrigin = %q, want %q", rows[0].SteerOrigin, vibekit.SteerOriginAgent)
	}
}

// A cleared id the buffer never held has no text anywhere, so there is nothing to
// write. Two shapes reach it: the housekeeping clear above, and a clear for a
// steer queued before this process started.
func TestSteeringCleared_UnknownIDPersistsNothing(t *testing.T) {
	deps, _, store := depsWithStore(t, "c1")
	New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_cleared", map[string]any{
			"messageIds": []string{"steer-never-seen"},
		}), FrameAttribution{})

	if rows := steerRows(t, store, "c1"); len(rows) != 0 {
		t.Errorf("persisted %d steer rows, want 0", len(rows))
	}
}

// Idempotent by id: a frame arriving twice is one row, or a reconnect or a repeat
// would stack duplicate notes in the transcript.
func TestSteeringInjected_IsIdempotentByID(t *testing.T) {
	deps, _, store := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))
	frame := steerFrame(t, "steering_injected", map[string]any{
		"messageId": "steer-1",
		"content":   "use tabs",
	})

	tr.HandleSessionInfoUpdate(t.Context(), "c1", frame, FrameAttribution{})
	tr.HandleSessionInfoUpdate(t.Context(), "c1", frame, FrameAttribution{})

	if rows := steerRows(t, store, "c1"); len(rows) != 1 {
		t.Errorf("persisted %d steer rows, want 1", len(rows))
	}
}
