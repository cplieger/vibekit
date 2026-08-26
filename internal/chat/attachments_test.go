package chat

// The staged-attachment writer. It is the draft's twin, so what is pinned here is
// that the twinning actually holds: the same absent-UpdatedAt contract, the same
// refusal to create a chat, the same refusal on a file holding another chat's id,
// plus the caps that keep an unloadable list off disk.

import (
	"strings"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

func TestSetAttachments(t *testing.T) {
	t.Parallel()

	t.Run("round_trips_through_the_chat_file", func(t *testing.T) {
		t.Parallel()
		s, err := NewStore(t.TempDir())
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		newChat(t, s, "c1")
		want := []string{"docs/spec.pdf", "out/shot.png"}
		if _, err := s.SetAttachments(t.Context(), "c1", want); err != nil {
			t.Fatalf("SetAttachments: %v", err)
		}
		got, ok := s.Get(t.Context(), "c1")
		if !ok {
			t.Fatal("chat vanished")
		}
		if strings.Join(got.Attachments, ",") != strings.Join(want, ",") {
			t.Errorf("Attachments = %v, want %v", got.Attachments, want)
		}
	})

	// The property this method shares with SetDraft, and the reason it is not a
	// Mutate call. The retention purge ages a chat from UpdatedAt, and this write
	// rides the composer's 600ms debounce, so stamping it would push the cutoff
	// out by a whole window every time a pill row changed.
	t.Run("does_not_move_the_retention_clock", func(t *testing.T) {
		t.Parallel()
		s, err := NewStore(t.TempDir())
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		newChat(t, s, "c1")
		aged := time.Now().Add(-72 * time.Hour).UnixMilli()
		c, ok := s.Get(t.Context(), "c1")
		if !ok {
			t.Fatal("chat vanished")
		}
		c.UpdatedAt = aged
		if err := s.writeChat("c1", c); err != nil {
			t.Fatalf("writeChat: %v", err)
		}

		if _, err := s.SetAttachments(t.Context(), "c1", []string{"docs/spec.pdf"}); err != nil {
			t.Fatalf("SetAttachments: %v", err)
		}
		after, ok := s.Get(t.Context(), "c1")
		if !ok {
			t.Fatal("chat vanished")
		}
		if after.UpdatedAt != aged {
			t.Errorf("UpdatedAt moved %d -> %d; staging a file must not count as activity",
				aged, after.UpdatedAt)
		}
		if len(after.Attachments) != 1 {
			t.Errorf("Attachments = %v, want the one path saved", after.Attachments)
		}
	})

	// An empty list is a VALUE: it is how a send or an emptied pill row clears.
	// Stored as nil rather than as an empty array so `omitempty` keeps the field
	// out of the chat file entirely.
	t.Run("an_empty_list_clears_and_stores_nil", func(t *testing.T) {
		t.Parallel()
		s, err := NewStore(t.TempDir())
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		newChat(t, s, "c1")
		if _, err := s.SetAttachments(t.Context(), "c1", []string{"a.txt"}); err != nil {
			t.Fatalf("SetAttachments: %v", err)
		}
		if _, err := s.SetAttachments(t.Context(), "c1", []string{}); err != nil {
			t.Fatalf("SetAttachments clear: %v", err)
		}
		got, _ := s.Get(t.Context(), "c1")
		if got.Attachments != nil {
			t.Errorf("Attachments = %#v, want nil", got.Attachments)
		}
	})

	// It REPLACES rather than merging: the client's pill row is authoritative and
	// sends the whole list, so a removed file has to disappear.
	t.Run("replaces_rather_than_merges", func(t *testing.T) {
		t.Parallel()
		s, err := NewStore(t.TempDir())
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		newChat(t, s, "c1")
		if _, err := s.SetAttachments(t.Context(), "c1", []string{"a.txt", "b.txt"}); err != nil {
			t.Fatalf("SetAttachments: %v", err)
		}
		if _, err := s.SetAttachments(t.Context(), "c1", []string{"b.txt"}); err != nil {
			t.Fatalf("SetAttachments: %v", err)
		}
		got, _ := s.Get(t.Context(), "c1")
		if strings.Join(got.Attachments, ",") != "b.txt" {
			t.Errorf("Attachments = %v, want only b.txt", got.Attachments)
		}
	})

	// Same rule as the draft: staging a file must not turn a client-side chat into
	// a row in every connected client's sidebar.
	t.Run("no_op_on_a_chat_that_does_not_exist", func(t *testing.T) {
		t.Parallel()
		s, err := NewStore(t.TempDir())
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		state, err := s.SetAttachments(t.Context(), "c-nope", []string{"a.txt"})
		if err != nil {
			t.Fatalf("SetAttachments: %v", err)
		}
		if state != nil {
			t.Errorf("state = %+v, want nil so no draft_changed is broadcast", state)
		}
		if _, ok := s.Get(t.Context(), "c-nope"); ok {
			t.Error("SetAttachments created a chat record")
		}
	})

	t.Run("refuses_more_entries_than_the_cap", func(t *testing.T) {
		t.Parallel()
		s, err := NewStore(t.TempDir())
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		newChat(t, s, "c1")
		if _, err := s.SetAttachments(t.Context(), "c1", manyPaths(vibekit.MaxAttachments+1)); err == nil {
			t.Error("a list over the cap was accepted")
		}
		if _, err := s.SetAttachments(t.Context(), "c1", manyPaths(vibekit.MaxAttachments)); err != nil {
			t.Errorf("a list at exactly the cap was rejected: %v", err)
		}
	})

	t.Run("refuses_an_empty_or_oversized_path", func(t *testing.T) {
		t.Parallel()
		s, err := NewStore(t.TempDir())
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		newChat(t, s, "c1")
		if _, err := s.SetAttachments(t.Context(), "c1", []string{"a.txt", ""}); err == nil {
			t.Error("an empty path was accepted")
		}
		long := strings.Repeat("x", vibekit.MaxAttachmentPathBytes+1)
		if _, err := s.SetAttachments(t.Context(), "c1", []string{long}); err == nil {
			t.Error("an oversized path was accepted")
		}
		got, _ := s.Get(t.Context(), "c1")
		if got.Attachments != nil {
			t.Errorf("a refused list still wrote %#v", got.Attachments)
		}
	})

	// A path that cannot round-trip through JSON would make the chat unloadable,
	// which is the same reason the draft, the name and message content are checked.
	t.Run("refuses_invalid_utf8", func(t *testing.T) {
		t.Parallel()
		s, err := NewStore(t.TempDir())
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		newChat(t, s, "c1")
		if _, err := s.SetAttachments(t.Context(), "c1", []string{string([]byte{0xff, 0xfe})}); err == nil {
			t.Error("an invalid-UTF-8 path was accepted")
		}
	})

	// THE cross-chat corruption path, shared with SetDraft: this writer persists
	// the WHOLE loaded object, so a c1.json holding `"id":"c2"` would write
	// everything it loaded over c2.json under c1's mutex.
	t.Run("refuses_a_chat_file_holding_another_chats_id", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		s, err := NewStore(dir)
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		newChat(t, s, "c2")
		c2Before := readRawChat(t, dir, "c2")
		writeRawChat(t, dir, "c1", `{"id":"c2","name":"impostor","messages":[]}`)

		if _, err := s.SetAttachments(t.Context(), "c1", []string{"a.txt"}); err == nil {
			t.Error("SetAttachments accepted a chat file holding another chat's id")
		}
		// The clear is the same write with an empty list, and the planted file has
		// no attachments, so this is the one that reaches the no-change shortcut.
		// It must report the corruption rather than return nil.
		if _, err := s.SetAttachments(t.Context(), "c1", nil); err == nil {
			t.Error("SetAttachments returned nil for an empty list on a mismatched file; the corruption stayed silent")
		}
		if got := readRawChat(t, dir, "c2"); got != c2Before {
			t.Errorf("c2.json changed under a SetAttachments for c1\nbefore: %s\nafter:  %s", c2Before, got)
		}
	})
}

// The returned state is what the draft_changed broadcast is built from, so
// "nothing landed" and "this landed" have to be distinguishable — and the state
// has to carry BOTH halves whichever writer produced it, or a frame from one
// command would blank the other's field on every receiving device.
func TestComposerWritersReportTheWholeState(t *testing.T) {
	t.Parallel()
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	newChat(t, s, "c1")

	state, err := s.SetDraft(t.Context(), "c1", "half a question")
	if err != nil {
		t.Fatalf("SetDraft: %v", err)
	}
	if state == nil {
		t.Fatal("SetDraft reported no write for a draft it stored")
	}
	if state.Text != "half a question" || state.Attachments != nil {
		t.Errorf("state after SetDraft = %+v, want the text and no attachments", state)
	}

	// The attachment writer must report the draft that is already there, not an
	// empty one.
	state, err = s.SetAttachments(t.Context(), "c1", []string{"docs/spec.pdf"})
	if err != nil {
		t.Fatalf("SetAttachments: %v", err)
	}
	if state == nil {
		t.Fatal("SetAttachments reported no write for a list it stored")
	}
	if state.Text != "half a question" {
		t.Errorf("state.Text = %q, want the draft already on the record", state.Text)
	}
	if strings.Join(state.Attachments, ",") != "docs/spec.pdf" {
		t.Errorf("state.Attachments = %v, want docs/spec.pdf", state.Attachments)
	}

	// And the draft writer must report the attachments that are already there.
	state, err = s.SetDraft(t.Context(), "c1", "more of the question")
	if err != nil {
		t.Fatalf("SetDraft: %v", err)
	}
	if state == nil {
		t.Fatal("SetDraft reported no write for a draft it stored")
	}
	if strings.Join(state.Attachments, ",") != "docs/spec.pdf" {
		t.Errorf("state.Attachments = %v, want the list already on the record", state.Attachments)
	}
}

// A save that changes nothing reports nothing, which is what keeps a broadcast
// off the wire for the common no-change case: a blur flush right behind a
// debounced save, and the unload flush behind that.
func TestComposerWritersReportNoWriteWhenNothingChanged(t *testing.T) {
	t.Parallel()
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	newChat(t, s, "c1")

	if _, err := s.SetDraft(t.Context(), "c1", "same"); err != nil {
		t.Fatalf("SetDraft: %v", err)
	}
	state, err := s.SetDraft(t.Context(), "c1", "same")
	if err != nil {
		t.Fatalf("SetDraft repeat: %v", err)
	}
	if state != nil {
		t.Errorf("SetDraft reported a write for identical text: %+v", state)
	}

	if _, err := s.SetAttachments(t.Context(), "c1", []string{"a.txt"}); err != nil {
		t.Fatalf("SetAttachments: %v", err)
	}
	state, err = s.SetAttachments(t.Context(), "c1", []string{"a.txt"})
	if err != nil {
		t.Fatalf("SetAttachments repeat: %v", err)
	}
	if state != nil {
		t.Errorf("SetAttachments reported a write for an identical list: %+v", state)
	}

	// nil and an empty slice are the same value here, so neither counts as a
	// change against an already-empty list.
	if _, err := s.SetAttachments(t.Context(), "c1", nil); err != nil {
		t.Fatalf("SetAttachments clear: %v", err)
	}
	state, err = s.SetAttachments(t.Context(), "c1", []string{})
	if err != nil {
		t.Fatalf("SetAttachments empty: %v", err)
	}
	if state != nil {
		t.Errorf("an empty list over a nil one reported a write: %+v", state)
	}
}

// The state handed back is a COPY, so a caller building a broadcast payload from
// it cannot reach into the slice the next load will compare against.
func TestSetAttachments_ReturnedStateIsACopy(t *testing.T) {
	t.Parallel()
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	newChat(t, s, "c1")
	state, err := s.SetAttachments(t.Context(), "c1", []string{"a.txt"})
	if err != nil {
		t.Fatalf("SetAttachments: %v", err)
	}
	state.Attachments[0] = "hijacked"

	got, _ := s.Get(t.Context(), "c1")
	if strings.Join(got.Attachments, ",") != "a.txt" {
		t.Errorf("Attachments = %v; the returned state aliased the record", got.Attachments)
	}
}

// manyPaths builds n distinct workspace paths, for the cap cases.
func manyPaths(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "f" + strings.Repeat("x", i%3) + string(rune('a'+i%26)) + ".txt"
	}
	return out
}
