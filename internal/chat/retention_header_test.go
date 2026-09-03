package chat

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/atomicfile/v3"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// The projection round-trips what the store WROTE. This is the whole claim behind
// choosing a projection over a header file beside the chat: there is one record,
// so the reader cannot report a stamp, a chain or a draft the writer did not
// store.
func TestLoadRetentionHeader_ReadsWhatTheStoreWrote(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := t.Context()
	if err := s.Mutate(ctx, "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "projected"
		c.RecordSession("sess_old")
		c.RecordSession("sess_new")
		c.Messages = append(c.Messages, vibekit.Message{
			ID:      "m1",
			Role:    vibekit.RoleAssistant,
			Content: strings.Repeat("tool output nobody reads to decide retention ", 500),
		})
		return true
	}); err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	full, err := s.load("c1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	h, err := s.LoadRetentionHeader("c1")
	if err != nil {
		t.Fatalf("LoadRetentionHeader: %v", err)
	}
	if h.UpdatedAt != full.UpdatedAt {
		t.Errorf("UpdatedAt = %d, want the record's %d", h.UpdatedAt, full.UpdatedAt)
	}
	if want := full.SessionChain(); !slices.Equal(h.SessionChain, want) {
		t.Errorf("SessionChain = %v, want the record's %v", h.SessionChain, want)
	}
	if h.Drafting {
		t.Error("Drafting = true for a chat with no draft")
	}
}

// Drafting is the DRAFT's presence and nothing else, read back through the
// composer writers that produce it. This is what the purge's draft exemption
// rests on.
//
// Staged attachments are the row that matters: they are paths to files that exist
// on disk in their own right, so purging the chat loses a reference rather than
// the content, and the exemption is deliberately narrower than "the composer
// holds something".
func TestLoadRetentionHeader_DraftingRoundTripsTheComposer(t *testing.T) {
	cases := map[string]struct {
		draft        string
		attachments  []string
		wantDrafting bool
	}{
		"unsent words":               {draft: "half a question", wantDrafting: true},
		"nothing typed":              {draft: "", wantDrafting: false},
		"attachments and no draft":   {attachments: []string{"docs/spec.pdf"}, wantDrafting: false},
		"attachments beside a draft": {draft: "see this", attachments: []string{"docs/spec.pdf"}, wantDrafting: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s, _ := newTestStore(t)
			ctx := t.Context()
			if err := s.Mutate(ctx, "c1", func(c *vibekit.Chat, _ bool) bool {
				c.Name = "composer"
				return true
			}); err != nil {
				t.Fatalf("Mutate: %v", err)
			}
			if _, err := s.SetDraft(ctx, "c1", tc.draft); err != nil {
				t.Fatalf("SetDraft: %v", err)
			}
			if tc.attachments != nil {
				if _, err := s.SetAttachments(ctx, "c1", tc.attachments); err != nil {
					t.Fatalf("SetAttachments: %v", err)
				}
			}

			h, err := s.LoadRetentionHeader("c1")
			if err != nil {
				t.Fatalf("LoadRetentionHeader: %v", err)
			}
			if h.Drafting != tc.wantDrafting {
				t.Errorf("Drafting = %v, want %v (draft %q, attachments %v)",
					h.Drafting, tc.wantDrafting, tc.draft, tc.attachments)
			}
		})
	}
}

// An empty draft PRESENT on disk defends nothing.
//
// It needs its own fixture because the store cannot write one: Chat.Draft carries
// `omitempty`, so clearing a draft removes the key entirely and the round-trip
// test above never reaches the emptiness test at all (verified by mutation — with
// `Drafting = true` hardcoded in the projection, every row of that table still
// passes). A chat written by another build or edited by hand can carry the key,
// and an empty draft is how a sent or abandoned message clears, so reading it as
// work in progress would make every chat that ever had one permanent.
func TestLoadRetentionHeader_AnExplicitEmptyDraftDefendsNothing(t *testing.T) {
	s, _ := newTestStore(t)
	path := filepath.Join(s.dir, "c1"+chatFileSuffix)
	if err := os.WriteFile(path, []byte(`{"id":"c1","draft":"","messages":[]}`), 0o600); err != nil {
		t.Fatalf("write chat: %v", err)
	}

	h, err := s.LoadRetentionHeader("c1")
	if err != nil {
		t.Fatalf("LoadRetentionHeader: %v", err)
	}
	if h.Drafting {
		t.Error("Drafting = true for a draft key holding the empty string")
	}
}

// The messages array is SKIPPED, not decoded, and this is the assertion that can
// tell the difference: a message whose fields do not fit vibekit.Message fails a
// full decode and is invisible to the projection. Replace the projection with a
// full load and this test goes red.
//
// It is also the behavior that matters operationally — a chat file a newer build
// wrote, or one somebody edited, stays retention-managed instead of falling back
// to its mtime forever.
func TestLoadRetentionHeader_SkipsMessagesRatherThanDecodingThem(t *testing.T) {
	s, _ := newTestStore(t)
	path := filepath.Join(s.dir, "c1"+chatFileSuffix)
	// Valid JSON, invalid vibekit.Message: role is a number and the block carries
	// a field of the wrong type.
	body := `{
  "id": "c1",
  "name": "odd",
  "acp_session_id": "sess_new",
  "draft": "unsent",
  "messages": [{"id": "m1", "role": 7, "blocks": [{"kind": {"nested": true}}]}],
  "prior_acp_session_ids": ["sess_old"],
  "updated_at": 1730000000000
}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write chat: %v", err)
	}
	if _, err := s.load("c1"); err == nil {
		t.Fatal("the full load accepted this fixture, so it cannot distinguish a " +
			"projection from a decode; make the messages array less decodable")
	}

	h, err := s.LoadRetentionHeader("c1")
	if err != nil {
		t.Fatalf("LoadRetentionHeader over an undecodable messages array: %v", err)
	}
	if h.UpdatedAt != 1730000000000 {
		t.Errorf("UpdatedAt = %d, want 1730000000000", h.UpdatedAt)
	}
	if want := []string{"sess_old", "sess_new"}; !slices.Equal(h.SessionChain, want) {
		t.Errorf("SessionChain = %v, want %v", h.SessionChain, want)
	}
	if !h.Drafting {
		t.Error("Drafting = false, want true: the draft precedes the messages array")
	}
}

// A chat that records no activity stamp reports zero, which is what makes
// purgeOne fall back to the file mtime. A projection that invented a stamp here —
// or errored — would either date the chat to the epoch or make it unpurgeable.
func TestLoadRetentionHeader_AbsentFieldsAreZero(t *testing.T) {
	s, _ := newTestStore(t)
	path := filepath.Join(s.dir, "c1"+chatFileSuffix)
	if err := os.WriteFile(path, []byte(`{"id":"c1","messages":[]}`), 0o600); err != nil {
		t.Fatalf("write chat: %v", err)
	}

	h, err := s.LoadRetentionHeader("c1")
	if err != nil {
		t.Fatalf("LoadRetentionHeader: %v", err)
	}
	if h.UpdatedAt != 0 || h.Drafting || len(h.SessionChain) != 0 {
		t.Errorf("header = %+v, want the zero projection", h)
	}
}

// A file that is not a chat at all fails rather than reporting a zero header: a
// silent zero would age every unreadable file from the epoch and delete it.
func TestLoadRetentionHeader_RejectsMalformedJSON(t *testing.T) {
	cases := map[string]string{
		"truncated object": `{"id":"c1","updated_at":`,
		"a JSON array":     `[{"id":"c1"}]`,
		"not JSON at all":  "\x00\x01binary",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			s, _ := newTestStore(t)
			path := filepath.Join(s.dir, "c1"+chatFileSuffix)
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatalf("write chat: %v", err)
			}
			if h, err := s.LoadRetentionHeader("c1"); err == nil {
				t.Errorf("LoadRetentionHeader accepted %q and returned %+v, want an error", body, h)
			}
		})
	}
}

// The projection opens through atomicfile.OpenRegular for the reason
// readCappedFile does: a FIFO at a chat file name blocks in open(2) with no
// deadline able to rescue it, and the purge reads EVERY chat file on every pass —
// so this is the one read that would wedge retention permanently, from one
// command in the agent's own shell.
func TestLoadRetentionHeader_RefusesAFifoInsteadOfBlockingForever(t *testing.T) {
	s, _ := newTestStore(t)
	id := mkfifoChat(t, s.dir)

	err := withinBudget(t, 3*time.Second, func() error {
		_, rerr := s.LoadRetentionHeader(id)
		return rerr
	})
	if !errors.Is(err, atomicfile.ErrNotRegular) {
		t.Errorf("LoadRetentionHeader over a FIFO = %v, want atomicfile.ErrNotRegular", err)
	}
}
