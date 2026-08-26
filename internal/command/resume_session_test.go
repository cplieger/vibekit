package command

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/testsupport"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// storeDeps is benchDeps with a real chat store, so a handler that mutates the
// store can be asserted on. benchDeps returns nil for ChatStore().
type storeDeps struct {
	*benchDeps
	store ChatStore
}

// The six store methods are promoted from the embedded store, not handed back
// through a ChatStore() getter: Roles holds the interface directly now, so a
// double that only overrode the getter left benchDeps' no-op methods winning and
// silently stored nothing.
func (d *storeDeps) Get(ctx context.Context, id vibekit.ChatID) (*vibekit.Chat, bool) {
	return d.store.Get(ctx, id)
}

func (d *storeDeps) Mutate(ctx context.Context, id vibekit.ChatID, fn func(*vibekit.Chat, bool) bool) error {
	return d.store.Mutate(ctx, id, fn)
}

func (d *storeDeps) AppendMessage(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.Message) error {
	return d.store.AppendMessage(ctx, chatID, msg)
}

func (d *storeDeps) SetDraft(ctx context.Context, id vibekit.ChatID, text string) (*vibekit.ComposerState, error) {
	return d.store.SetDraft(ctx, id, text)
}

func (d *storeDeps) SetAttachments(ctx context.Context, id vibekit.ChatID, paths []string) (*vibekit.ComposerState, error) {
	return d.store.SetAttachments(ctx, id, paths)
}

func (d *storeDeps) Delete(ctx context.Context, id vibekit.ChatID) error {
	return d.store.Delete(ctx, id)
}

func newTestHost(t *testing.T, store ChatStore) hostDouble {
	t.Helper()
	return &storeDeps{benchDeps: newBenchDeps(), store: store}
}

// resumeReq builds a resume_session command envelope.
func resumeReq(t *testing.T, chatID vibekit.ChatID, sessionID, name string) *vibekit.ClientCommand {
	t.Helper()
	payload, err := json.Marshal(vibekit.ResumeSessionCommand{SessionID: sessionID, Name: name})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return &vibekit.ClientCommand{
		Type:    vibekit.CmdResumeSession,
		ChatID:  chatID,
		Payload: payload,
	}
}

// TestCmdResumeSession_BindsTheSession is the point of the command: the chat is
// created ALREADY bound to the KAS session, so the next bridge takes the
// session/load path and the replay projection supplies the transcript. vibekit
// copies no messages.
func TestCmdResumeSession_BindsTheSession(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	host := newTestHost(t, store)
	ctx := t.Context()

	_, err := CmdResumeSession(ctx, newTestMembership(t, host), resumeReq(t, "c1", "sess_abc-123", "Earlier work"))

	if statusOf(err) != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", statusOf(err), errText(err))
	}
	c, ok := store.Get(ctx, "c1")
	if !ok {
		t.Fatal("chat was not created")
	}
	if c.ACPSessionID != "sess_abc-123" {
		t.Errorf("acp_session_id = %q, want sess_abc-123", c.ACPSessionID)
	}
	if c.Name != "Earlier work" {
		t.Errorf("name = %q, want the session title", c.Name)
	}
	// The chain is what the reaper's keep-list reads, so an adopted session
	// must be IN it or the next sweep deletes the transcript being resumed.
	chain := c.SessionChain()
	if len(chain) != 1 || chain[0] != "sess_abc-123" {
		t.Errorf("session chain = %v, want [sess_abc-123]", chain)
	}
	if len(c.Messages) != 0 {
		t.Errorf("chat carries %d messages, want 0: the replay supplies the transcript",
			len(c.Messages))
	}
}

// TestCmdResumeSession_RefusesToRebindAnExistingChat pins the guard. Pointing a
// live chat at another session would strand its own session — transcript still
// on disk, no longer referenced, so the reaper sweeps it — and hand the user a
// chat whose history silently changed.
func TestCmdResumeSession_RefusesToRebindAnExistingChat(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	ctx := t.Context()
	if err := store.Mutate(ctx, "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "Existing"
		c.RecordSession("sess_original")
		return true
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	host := newTestHost(t, store)

	_, _ = CmdResumeSession(ctx, newTestMembership(t, host), resumeReq(t, "c1", "sess_other", "Hijack"))

	c, _ := store.Get(ctx, "c1")
	if c.ACPSessionID != "sess_original" {
		t.Errorf("acp_session_id = %q, want sess_original — an existing chat was rebound",
			c.ACPSessionID)
	}
	if c.Name != "Existing" {
		t.Errorf("name = %q, want Existing", c.Name)
	}
}

// TestCmdResumeSession_RejectsPathUnsafeIDs covers the validation, and the case
// list documents what the guard does and does NOT promise.
//
// ids.ValidSessionID is a PATH-SAFETY guard: non-empty, <= 128 bytes, no
// `/ \ NUL`, no `..`. It deliberately does not constrain the alphabet or
// require the `sess_` prefix, which is why this test does not assert those.
// Two consequences worth stating rather than discovering later:
//
//   - `abc-123` (no prefix) and `sess_a b` (a space) are ACCEPTED here. They
//     are path-safe but not real session ids, so session/load fails on them
//     downstream — a dead chat, not a security problem, and the picker only
//     ever offers ids KAS itself reported.
//   - internal/kirosession has its OWN stricter validSessionID (requires the
//     prefix, rejects anything outside [A-Za-z0-9_-]) because it decides
//     whether a DIRECTORY NAME is a reapable session. The two validators
//     answer different questions; do not "unify" them.
func TestCmdResumeSession_RejectsPathUnsafeIDs(t *testing.T) {
	cases := map[string]string{
		"empty":          "",
		"traversal":      "sess_../../etc/passwd",
		"path separator": "sess_a/b",
		"backslash":      "sess_a\\b",
		"nul byte":       "sess_a\x00b",
		"dot dot":        "..",
	}
	for name, sid := range cases {
		t.Run(name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			host := newTestHost(t, store)

			_, err := CmdResumeSession(t.Context(), newTestMembership(t, host), resumeReq(t, "c1", sid, ""))

			if statusOf(err) != http.StatusBadRequest {
				t.Errorf("status = %d for session id %q, want 400", statusOf(err), sid)
			}
			if _, ok := store.Get(t.Context(), "c1"); ok {
				t.Errorf("a chat was created for invalid session id %q", sid)
			}
		})
	}
}

// A name of exactly MaxChatNameBytes is a legal name and must be stored as
// given; one byte more is refused. The cap is on the RECORD's name field, so
// the boundary decides whether a chat is created at all.
func TestCmdResumeSession_NameLengthCap(t *testing.T) {
	atCap := strings.Repeat("n", vibekit.MaxChatNameBytes)
	overCap := strings.Repeat("n", vibekit.MaxChatNameBytes+1)

	t.Run("a name at the cap is accepted", func(t *testing.T) {
		store := testsupport.NewInMemoryChatStore()
		host := newTestHost(t, store)

		_, err := CmdResumeSession(t.Context(), newTestMembership(t, host), resumeReq(t, "c1", "sess_abc", atCap))
		if err != nil {
			t.Fatalf("CmdResumeSession with a %d-byte name = %v, want it accepted", len(atCap), err)
		}
		c, ok := store.Get(t.Context(), "c1")
		if !ok {
			t.Fatal("no chat was created for an accepted name")
		}
		if c.Name != atCap {
			t.Errorf("stored name is %d bytes, want the %d-byte name as given", len(c.Name), len(atCap))
		}
	})

	t.Run("a name past the cap is refused", func(t *testing.T) {
		store := testsupport.NewInMemoryChatStore()
		host := newTestHost(t, store)

		_, err := CmdResumeSession(t.Context(), newTestMembership(t, host), resumeReq(t, "c1", "sess_abc", overCap))

		if statusOf(err) != http.StatusBadRequest {
			t.Errorf("status = %d for a %d-byte name, want 400", statusOf(err), len(overCap))
		}
		if _, ok := store.Get(t.Context(), "c1"); ok {
			t.Error("a chat was created for an over-long name")
		}
	})
}
