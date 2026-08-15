package command

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/testsupport"
)

// storeDeps is benchDeps with a real chat store, so a handler that mutates the
// store can be asserted on. benchDeps returns nil for ChatStore().
type storeDeps struct {
	*benchDeps
	store api.ChatStore
}

func (d *storeDeps) ChatStore() api.ChatStore { return d.store }

func newTestDispatcher(t *testing.T, store api.ChatStore) *Dispatcher {
	t.Helper()
	return New(&storeDeps{benchDeps: newBenchDeps(), store: store})
}

// resumeReq builds a resume_session command envelope.
func resumeReq(t *testing.T, chatID api.ChatID, sessionID, name string) *api.ClientCommand {
	t.Helper()
	payload, err := json.Marshal(api.ResumeSessionCommand{SessionID: sessionID, Name: name})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return &api.ClientCommand{
		Type:      api.CmdResumeSession,
		ChatID:    chatID,
		RequestID: "r1",
		Payload:   payload,
	}
}

// TestCmdResumeSession_BindsTheSession is the point of the command: the chat is
// created ALREADY bound to the KAS session, so the next bridge takes the
// session/load path and the replay projection supplies the transcript. vibekit
// copies no messages.
func TestCmdResumeSession_BindsTheSession(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	d := newTestDispatcher(t, store)
	ctx := t.Context()
	w := httptest.NewRecorder()

	CmdResumeSession(d, ctx, w, resumeReq(t, "c1", "sess_abc-123", "Earlier work"))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
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
	if err := store.Mutate(ctx, "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "Existing"
		c.RecordSession("sess_original")
		return true
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	d := newTestDispatcher(t, store)
	w := httptest.NewRecorder()

	CmdResumeSession(d, ctx, w, resumeReq(t, "c1", "sess_other", "Hijack"))

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
// api.ValidSessionID is a PATH-SAFETY guard: non-empty, <= 128 bytes, no
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
			d := newTestDispatcher(t, store)
			w := httptest.NewRecorder()

			CmdResumeSession(d, t.Context(), w, resumeReq(t, "c1", sid, ""))

			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d for session id %q, want 400", w.Code, sid)
			}
			if _, ok := store.Get(t.Context(), "c1"); ok {
				t.Errorf("a chat was created for invalid session id %q", sid)
			}
		})
	}
}
