package chat

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/api"
)

// writeRawChat plants a chat file the store did not write. The store's own API
// cannot produce a file whose stored id disagrees with its name, which is exactly
// why the fixture goes around it: the hazard is a file arriving from somewhere
// else (a truncated write, an operator editing /config by hand).
func writeRawChat(t *testing.T, dir string, chatID api.ChatID, body string) {
	t.Helper()
	path := filepath.Join(dir, string(chatID)+chatFileSuffix)
	if err := os.WriteFile(path, []byte(body), fileMode); err != nil {
		t.Fatalf("plant %s: %v", path, err)
	}
}

// readRawChat returns a chat file's bytes, so a test can prove another chat's
// file was not touched. Compared as bytes rather than through Get: the claim is
// that nothing was written, and a decoded comparison would hide a rewrite that
// happened to round-trip.
func readRawChat(t *testing.T, dir string, chatID api.ChatID) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, string(chatID)+chatFileSuffix))
	if err != nil {
		t.Fatalf("read %s: %v", chatID, err)
	}
	return string(b)
}

// newChat seeds a persisted chat so SetDraft has a record to write to.
func newChat(t *testing.T, s *Store, id api.ChatID) {
	t.Helper()
	if err := s.Mutate(t.Context(), id, func(c *api.Chat, _ bool) bool {
		c.Name = "a chat"
		return true
	}); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

func TestSetDraft(t *testing.T) {
	t.Parallel()

	t.Run("round_trips_through_the_chat_file", func(t *testing.T) {
		t.Parallel()
		s, err := NewStore(t.TempDir())
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		newChat(t, s, "c1")
		if err := s.SetDraft(t.Context(), "c1", "half a question"); err != nil {
			t.Fatalf("SetDraft: %v", err)
		}
		got, ok := s.Get(t.Context(), "c1")
		if !ok {
			t.Fatal("chat vanished")
		}
		if got.Draft != "half a question" {
			t.Errorf("Draft = %q, want %q", got.Draft, "half a question")
		}
	})

	// THE property this method exists for. The retention purge ages a chat from
	// UpdatedAt (archive.purgeReferenceTime), so a debounced autosave that
	// stamped it would push the purge cutoff out by a whole window on every burst
	// of typing: a chat with an abandoned draft would never be purged, and a
	// draft can hold a credential. Mutate would stamp it; SetDraft must not.
	t.Run("does_not_move_the_retention_clock", func(t *testing.T) {
		t.Parallel()
		s, err := NewStore(t.TempDir())
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		newChat(t, s, "c1")
		// The clock has millisecond resolution, so a same-tick save would pass
		// this test even if it did stamp. Age the record past any retention window
		// first, writing through the no-stamp path so the fixture itself does not
		// reset what it is measuring.
		aged := time.Now().Add(-72 * time.Hour).UnixMilli()
		c, ok := s.Get(t.Context(), "c1")
		if !ok {
			t.Fatal("chat vanished")
		}
		c.UpdatedAt = aged
		if err := s.writeChat("c1", c); err != nil {
			t.Fatalf("writeChat: %v", err)
		}

		if err := s.SetDraft(t.Context(), "c1", "typed and walked away"); err != nil {
			t.Fatalf("SetDraft: %v", err)
		}
		after, ok := s.Get(t.Context(), "c1")
		if !ok {
			t.Fatal("chat vanished")
		}
		if after.UpdatedAt != aged {
			t.Errorf("UpdatedAt moved %d -> %d; a draft save must not count as activity, or a chat holding an abandoned draft never purges",
				aged, after.UpdatedAt)
		}
		if after.Draft != "typed and walked away" {
			t.Errorf("Draft = %q, want it saved", after.Draft)
		}
	})

	// Every OTHER write is activity and must keep stamping, or the purge stops
	// seeing real use.
	t.Run("mutate_still_stamps_the_clock", func(t *testing.T) {
		t.Parallel()
		s, err := NewStore(t.TempDir())
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		newChat(t, s, "c1")
		c, _ := s.Get(t.Context(), "c1")
		aged := time.Now().Add(-72 * time.Hour).UnixMilli()
		c.UpdatedAt = aged
		if err := s.writeChat("c1", c); err != nil {
			t.Fatalf("writeChat: %v", err)
		}
		if err := s.Mutate(t.Context(), "c1", func(ch *api.Chat, _ bool) bool {
			ch.Name = "renamed"
			return true
		}); err != nil {
			t.Fatalf("Mutate: %v", err)
		}
		after, _ := s.Get(t.Context(), "c1")
		if after.UpdatedAt == aged {
			t.Error("Mutate left UpdatedAt alone; ordinary mutations must record activity")
		}
	})

	t.Run("clears_on_empty", func(t *testing.T) {
		t.Parallel()
		s, err := NewStore(t.TempDir())
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		newChat(t, s, "c1")
		if err := s.SetDraft(t.Context(), "c1", "something"); err != nil {
			t.Fatalf("SetDraft: %v", err)
		}
		if err := s.SetDraft(t.Context(), "c1", ""); err != nil {
			t.Fatalf("SetDraft clear: %v", err)
		}
		got, _ := s.Get(t.Context(), "c1")
		if got.Draft != "" {
			t.Errorf("Draft = %q, want cleared", got.Draft)
		}
	})

	// A chat is a server record from its first prompt onward. Typing must not
	// create one, or every keystroke in a fresh chat puts a row in the sidebar.
	t.Run("no_op_on_a_chat_that_does_not_exist", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		s, err := NewStore(dir)
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		if err := s.SetDraft(t.Context(), "c-nope", "text"); err != nil {
			t.Fatalf("SetDraft: %v", err)
		}
		if _, ok := s.Get(t.Context(), "c-nope"); ok {
			t.Error("SetDraft created a chat record; typing must not create a conversation")
		}
	})

	t.Run("refuses_a_draft_over_the_cap", func(t *testing.T) {
		t.Parallel()
		s, err := NewStore(t.TempDir())
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		newChat(t, s, "c1")
		if err := s.SetDraft(t.Context(), "c1", strings.Repeat("x", api.MaxDraftBytes+1)); err == nil {
			t.Error("oversize draft accepted")
		}
		if err := s.SetDraft(t.Context(), "c1", strings.Repeat("x", api.MaxDraftBytes)); err != nil {
			t.Errorf("draft at exactly the cap rejected: %v", err)
		}
	})

	// A draft that cannot round-trip through JSON would make the chat unloadable,
	// which is the same reason Name and message content are validated.
	t.Run("refuses_invalid_utf8", func(t *testing.T) {
		t.Parallel()
		s, err := NewStore(t.TempDir())
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		newChat(t, s, "c1")
		if err := s.SetDraft(t.Context(), "c1", string([]byte{0xff, 0xfe})); err == nil {
			t.Error("invalid UTF-8 draft accepted")
		}
	})

	// THE cross-chat corruption path. A draft save writes the WHOLE loaded object,
	// and the destination used to be derived from that object's own id, so a
	// c1.json holding `"id":"c2"` made an autosave for c1 write everything it had
	// loaded over c2.json — under c1's mutex, racing any legitimate write to c2.
	// A file whose stored id is not its filename is reachable here: this container
	// invites the operator to reshape /config by hand, and a truncated write leaves
	// arbitrary bytes.
	t.Run("refuses_a_chat_file_holding_another_chats_id", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		s, err := NewStore(dir)
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		newChat(t, s, "c2")
		if err := s.SetDraft(t.Context(), "c2", "c2's own unsent question"); err != nil {
			t.Fatalf("SetDraft c2: %v", err)
		}
		c2Before := readRawChat(t, dir, "c2")

		// c1's file claims to be c2.
		writeRawChat(t, dir, "c1", `{"id":"c2","name":"impostor","messages":[]}`)

		if err := s.SetDraft(t.Context(), "c1", "a draft typed into c1"); err == nil {
			t.Error("SetDraft accepted a chat file holding another chat's id; an autosave for c1 writes the whole object over c2.json")
		}
		// Clearing the composer is the same save with empty text, and the planted
		// file has no draft, so this is the one that reaches the no-change
		// shortcut. It must report the corruption rather than return nil: this is
		// the save a user makes without thinking about it, and a silent success
		// here is a broken file nobody hears about.
		if err := s.SetDraft(t.Context(), "c1", ""); err == nil {
			t.Error("SetDraft returned nil for an empty draft on a mismatched file; the corruption stayed silent")
		}
		if got := readRawChat(t, dir, "c2"); got != c2Before {
			t.Errorf("c2.json changed under a SetDraft for c1\nbefore: %s\nafter:  %s", c2Before, got)
		}
		// A refusal, not a repair: nothing rewrites c1's file to agree with its
		// name either, because guessing which half is right would destroy the
		// other one.
		if got := readRawChat(t, dir, "c1"); !strings.Contains(got, `"impostor"`) {
			t.Errorf("c1.json = %s, want it left exactly as found", got)
		}
	})

	// Mutate refuses the same disagreement, whichever way it arrives: its own
	// check names a mutator, and this proves a corrupt file cannot walk past it
	// either. Both writers hold the same invariant, which is the point.
	t.Run("mutate_refuses_the_same_mismatch", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		s, err := NewStore(dir)
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		newChat(t, s, "c2")
		c2Before := readRawChat(t, dir, "c2")
		writeRawChat(t, dir, "c1", `{"id":"c2","name":"impostor","messages":[]}`)

		err = s.Mutate(t.Context(), "c1", func(c *api.Chat, _ bool) bool {
			c.Name = "renamed"
			return true
		})
		if err == nil {
			t.Error("Mutate accepted a chat file holding another chat's id")
		}
		if got := readRawChat(t, dir, "c2"); got != c2Before {
			t.Errorf("c2.json changed under a Mutate for c1\nbefore: %s\nafter:  %s", c2Before, got)
		}
	})

	// The guard belongs to the WRITE PRIMITIVE, not to its callers: that is what
	// stops the next no-stamp writer from reintroducing the bypass by forgetting
	// a check. Asserted directly on writeChat so it survives any future caller.
	t.Run("write_primitive_refuses_a_mismatched_object", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		s, err := NewStore(dir)
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		newChat(t, s, "c2")
		c2Before := readRawChat(t, dir, "c2")

		if err := s.writeChat("c1", &api.Chat{ID: "c2", Name: "impostor"}); err == nil {
			t.Error("writeChat accepted an object whose id is not its destination")
		}
		if got := readRawChat(t, dir, "c2"); got != c2Before {
			t.Errorf("c2.json changed under a writeChat for c1\nbefore: %s\nafter:  %s", c2Before, got)
		}
		if _, err := os.Stat(filepath.Join(dir, "c1"+chatFileSuffix)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("stat c1.json = %v, want it never created", err)
		}
	})

	t.Run("mutate_rejects_invalid_utf8_in_a_draft", func(t *testing.T) {
		t.Parallel()
		s, err := NewStore(t.TempDir())
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		newChat(t, s, "c1")
		err = s.Mutate(t.Context(), "c1", func(c *api.Chat, _ bool) bool {
			c.Draft = string([]byte{0xff})
			return true
		})
		if err == nil {
			t.Error("Mutate persisted an invalid-UTF-8 draft")
		}
	})
}
