package chat

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/atomicfile/v3"
	"github.com/cplieger/vibekit/internal/chat/archive"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// The projection round-trips what the store WROTE: one record, so the reader
// cannot report a stamp, a chain or a draft the writer did not store.
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

// Drafting is the DRAFT's presence and nothing else, which is what the purge's
// draft exemption rests on. Staged attachments are the row that matters: they are
// paths to files that exist on disk in their own right, so the exemption is
// narrower than "the composer holds something".
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

// An empty draft PRESENT on disk defends nothing. It needs a hand-written fixture
// because Chat.Draft carries `omitempty`, so the store cannot write the key at all
// and the round-trip test above never reaches emptiness. Reading an empty draft as
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

// The projection matches keys the way encoding/json does, CASE-INSENSITIVELY,
// because encoding/json is the other reader of this same file (the store's full
// load) and a key the two disagree about loses data silently: a missed
// `updated_at` falls back to the file mtime, a missed `acp_session_id` leaves a
// KAS session directory unreaped, and a missed `draft` lets the reaper UNLINK a
// chat with unsent words in it.
func TestLoadRetentionHeader_MatchesKeysTheWayEncodingJSONDoes(t *testing.T) {
	cases := []struct {
		name string
		body string
		want archive.RetentionHeader
	}{
		{
			name: "a capitalised draft still defends the chat",
			body: `{"id":"c1","Draft":"unsent words","messages":[]}`,
			want: archive.RetentionHeader{Drafting: true},
		},
		{
			name: "a screaming draft too, since Unmarshal folds the whole key",
			body: `{"id":"c1","DRAFT":"unsent words","messages":[]}`,
			want: archive.RetentionHeader{Drafting: true},
		},
		{
			name: "a capitalised stamp is read rather than falling back to the mtime",
			body: `{"id":"c1","Updated_At":1730000000000,"messages":[]}`,
			want: archive.RetentionHeader{UpdatedAt: 1730000000000},
		},
		{
			name: "a capitalised session id reaches the reap set",
			body: `{"id":"c1","ACP_Session_ID":"sess_new","messages":[]}`,
			want: archive.RetentionHeader{SessionChain: []string{"sess_new"}},
		},
		{
			name: "and the prior-session list, which is what unreaps a whole history",
			body: `{"id":"c1","Prior_ACP_Session_IDs":["sess_old"],"messages":[]}`,
			want: archive.RetentionHeader{SessionChain: []string{"sess_old"}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, _ := newTestStore(t)
			path := filepath.Join(s.dir, "c1"+chatFileSuffix)
			if err := os.WriteFile(path, []byte(c.body), 0o600); err != nil {
				t.Fatalf("Setup: write chat: %v", err)
			}
			h, err := s.LoadRetentionHeader("c1")
			if err != nil {
				t.Fatalf("LoadRetentionHeader: %v", err)
			}
			if h.Drafting != c.want.Drafting {
				t.Errorf("Drafting = %v, want %v for %s", h.Drafting, c.want.Drafting, c.body)
			}
			if h.UpdatedAt != c.want.UpdatedAt {
				t.Errorf("UpdatedAt = %d, want %d for %s", h.UpdatedAt, c.want.UpdatedAt, c.body)
			}
			if !slices.Equal(h.SessionChain, c.want.SessionChain) {
				t.Errorf("SessionChain = %v, want %v for %s", h.SessionChain, c.want.SessionChain, c.body)
			}
		})
	}
}

// The premise the test above rests on, asserted rather than assumed:
// encoding/json really does fold these keys. A Go release that tightened field
// matching would otherwise make the folding above wrong rather than red.
func TestUnmarshalFoldsAChatsFieldNames(t *testing.T) {
	var c vibekit.Chat
	body := `{"Draft":"unsent words","Updated_At":1730000000000,"ACP_Session_ID":"sess_new"}`
	if err := json.Unmarshal([]byte(body), &c); err != nil {
		t.Fatalf("Setup: unmarshal: %v", err)
	}
	if c.Draft != "unsent words" {
		t.Errorf(`Chat.Draft = %q for "Draft", want the value; the projection's EqualFold would be over-matching`, c.Draft)
	}
	if c.UpdatedAt != 1730000000000 {
		t.Errorf("Chat.UpdatedAt = %d for \"Updated_At\", want 1730000000000", c.UpdatedAt)
	}
	if c.ACPSessionID != "sess_new" {
		t.Errorf("Chat.ACPSessionID = %q for \"ACP_Session_ID\", want sess_new", c.ACPSessionID)
	}
}

// The messages array is SKIPPED, not decoded: replace the projection with a full
// load and this test goes red. Operationally it is what keeps a chat file a newer
// build wrote retention-managed instead of falling back to its mtime forever.
func TestLoadRetentionHeader_SkipsMessagesRatherThanDecodingThem(t *testing.T) {
	s, _ := newTestStore(t)
	path := filepath.Join(s.dir, "c1"+chatFileSuffix)
	// Valid JSON, invalid vibekit.Message: role is a number, block field mistyped.
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

// A chat with no activity stamp reports zero, which is what makes purgeOne fall
// back to the file mtime. An invented stamp would date the chat to the epoch; an
// error would make it unpurgeable.
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

// A FIFO at a chat file name blocks in open(2) with no deadline able to rescue it,
// and the purge reads EVERY chat file on every pass, so this is the one read that
// would wedge retention permanently.
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
