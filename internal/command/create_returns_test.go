package command

// fork_chat and resume_session RETURN the chat they created.
//
// Both used to take the new chat's id on the envelope and answer with something
// that did not name it — fork with `{outcome, session_id}`, resume with `{ok}`.
// That was sufficient only while the client chose the id. Once the server mints
// one, an answer that omits it leaves the caller holding a session it adopted or a
// tangent it forked with nothing to open, so the return is the conversion rather
// than a convenience on top of it.
//
// The op ledger reaches both for a reason each has separately: a retried resume
// would bind a SECOND chat to one KAS session (two chats claiming one transcript,
// two entries in the reaper's keep-list for one chain), and a retried fork would
// ask KAS to fork again, creating a session nothing binds and, on the primed
// fallback, spending the priming budget twice.

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/cplieger/vibekit/internal/ids"
	"github.com/cplieger/vibekit/internal/testsupport"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// resumeReqOp and forkReqOp are the op-carrying envelopes these tests need: the
// existing helpers next door take an explicit chat id, which is the shape being
// replaced.
func resumeReqOp(t *testing.T, sessionID, name, opID string) *vibekit.ClientCommand {
	t.Helper()
	payload, err := json.Marshal(vibekit.ResumeSessionCommand{SessionID: sessionID, Name: name, OpID: opID})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return &vibekit.ClientCommand{Type: vibekit.CmdResumeSession, Payload: payload}
}

func forkReqOp(t *testing.T, parent vibekit.ChatID, title, opID string) *vibekit.ClientCommand {
	t.Helper()
	payload, err := json.Marshal(vibekit.ForkChatCommand{ParentChatID: parent, Title: title, OpID: opID})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return &vibekit.ClientCommand{Type: vibekit.CmdForkChat, Payload: payload}
}

func TestCmdResumeSession_MintsAndReturnsTheChat(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	host := newTestHost(t, store)

	body, err := CmdResumeSession(t.Context(), newTestMembership(t, host),
		resumeReqOp(t, "sess_abc-123", "Earlier work", "op-1"))

	if statusOf(err) != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", statusOf(err), errText(err))
	}
	id := chatIDOfResponse(t, body)
	if !ids.ValidChatID(string(id)) {
		t.Errorf("returned id %q is not a valid chat id", id)
	}
	c, ok := store.Get(t.Context(), id)
	if !ok {
		t.Fatalf("the returned chat %q is not in the store", id)
	}
	if c.ACPSessionID != "sess_abc-123" {
		t.Errorf("acp_session_id = %q, want the adopted session", c.ACPSessionID)
	}
	if c.Name != "Earlier work" {
		t.Errorf("name = %q, want the title the picker reported", c.Name)
	}
}

// The consequence of losing the client-minted id's free idempotency, for the case
// where it costs the most: two chats bound to one session.
func TestCmdResumeSession_RepeatOpBindsOneChat(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	host := newTestHost(t, store)
	ops := newTestMembership(t, host)

	first, err := CmdResumeSession(t.Context(), ops, resumeReqOp(t, "sess_abc", "", "op-same"))
	if err != nil {
		t.Fatalf("first attempt: %v", err)
	}
	second, err := CmdResumeSession(t.Context(), ops, resumeReqOp(t, "sess_abc", "", "op-same"))
	if err != nil {
		t.Fatalf("retry: %v", err)
	}

	if got, want := chatIDOfResponse(t, second), chatIDOfResponse(t, first); got != want {
		t.Errorf("retry returned %q, want the first attempt's %q", got, want)
	}
	if got := storedIDs(t, store); len(got) != 1 {
		t.Errorf("store holds %d chats (%v), want 1: two chats on one session strand a transcript",
			len(got), got)
	}
}

func TestCmdForkChat_MintsAndReturnsTheChat(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedParent(t, store, "c-parent")
	br := &recordingBridge{sessionID: "sess_parent", result: map[string]any{"sessionId": "sess_tangent"}}
	host := newForkHost(store, br)

	body, err := CmdForkChat(t.Context(), host, host, testWorkspace(t), newTestMembership(t, host),
		forkReqOp(t, "c-parent", "Reaper detour", "op-1"))

	if statusOf(err) != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", statusOf(err), errText(err))
	}
	id := chatIDOfResponse(t, body)
	if id == "c-parent" || !ids.ValidChatID(string(id)) {
		t.Fatalf("returned id %q is not a freshly minted chat id", id)
	}
	c, ok := store.Get(t.Context(), id)
	if !ok {
		t.Fatalf("the returned chat %q is not in the store", id)
	}
	if c.ACPSessionID != "sess_tangent" {
		t.Errorf("acp_session_id = %q, want the forked sess_tangent", c.ACPSessionID)
	}
	// The outcome still travels beside the chat: it is what lets a report about a
	// vague answer say whether the context was forked or re-narrated.
	m, _ := body.(map[string]any)
	if m["outcome"] != vibekit.ForkOutcomeForked {
		t.Errorf("outcome = %v, want %q", m["outcome"], vibekit.ForkOutcomeForked)
	}
	if m["session_id"] != "sess_tangent" {
		t.Errorf("session_id = %v, want sess_tangent", m["session_id"])
	}
}

// The op is resolved BEFORE session/fork, so a retry does not fork twice. Asserted
// through the bridge's call count, because a second fork is invisible in the store
// (its session id is never bound) and shows up only as a leaked KAS session.
func TestCmdForkChat_RepeatOpDoesNotForkTwice(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedParent(t, store, "c-parent")
	br := &recordingBridge{sessionID: "sess_parent", result: map[string]any{"sessionId": "sess_tangent"}}
	host := newForkHost(store, br)
	ops := newTestMembership(t, host)

	first, err := CmdForkChat(t.Context(), host, host, testWorkspace(t), ops,
		forkReqOp(t, "c-parent", "", "op-same"))
	if err != nil {
		t.Fatalf("first attempt: %v", err)
	}
	callsAfterFirst := br.callCount
	second, err := CmdForkChat(t.Context(), host, host, testWorkspace(t), ops,
		forkReqOp(t, "c-parent", "", "op-same"))
	if err != nil {
		t.Fatalf("retry: %v", err)
	}

	if got, want := chatIDOfResponse(t, second), chatIDOfResponse(t, first); got != want {
		t.Errorf("retry returned %q, want the first attempt's %q", got, want)
	}
	if br.callCount != callsAfterFirst {
		t.Errorf("the bridge took %d calls after the retry, want the %d it had already made: "+
			"a repeat must not fork again", br.callCount, callsAfterFirst)
	}
	if got := storedIDs(t, store); len(got) != 2 {
		t.Errorf("store holds %d chats (%v), want 2 (the parent and one tangent)", len(got), got)
	}
}

// A retried fork whose FIRST attempt primed rather than forked must report
// `primed`, derived from the record. Restating this attempt's outcome would make
// the field a guess in precisely the case a reader is consulting it.
func TestCmdForkChat_RepeatOpReportsThePathTheFirstAttemptTook(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedParent(t, store, "c-parent")
	// No live session on the parent, so the fork degrades to priming and the
	// tangent is created unbound.
	host := newForkHost(store, nil)
	ops := newTestMembership(t, host)

	if _, err := CmdForkChat(t.Context(), host, host, testWorkspace(t), ops,
		forkReqOp(t, "c-parent", "", "op-same")); err != nil {
		t.Fatalf("first attempt: %v", err)
	}
	body, err := CmdForkChat(t.Context(), host, host, testWorkspace(t), ops,
		forkReqOp(t, "c-parent", "", "op-same"))
	if err != nil {
		t.Fatalf("retry: %v", err)
	}

	m, _ := body.(map[string]any)
	if m["outcome"] != vibekit.ForkOutcomePrimed {
		t.Errorf("outcome = %v, want %q: the first attempt had no session to fork",
			m["outcome"], vibekit.ForkOutcomePrimed)
	}
	if m["session_id"] != "" {
		t.Errorf("session_id = %v, want empty: a primed tangent has no forked session to name",
			m["session_id"])
	}
}
