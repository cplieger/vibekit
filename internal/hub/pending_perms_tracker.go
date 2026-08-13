package hub

import (
	"sync"
	"time"

	"github.com/cplieger/vibekit/internal/api"
)

// pendingPermTTL is how long an unanswered decision stays live here: 5 minutes,
// the window the agent server keeps a request open for.
//
// Two things go wrong without it. A request the agent server has ALREADY
// abandoned is still replayed to every reconnecting client, so the user answers
// a card whose answer goes nowhere; and an entry nothing ever answers (a bridge
// that died mid-request, a run that was torn down) is never removed, so the map
// only grows for the life of the process.
//
// Matching the agent server's window is what makes the expiry safe rather than a
// second opinion: vibekit stops offering a decision at the point the answer
// would stop being accepted anyway. Shortening it would refuse answers the agent
// server would still take, which is why the number belongs beside that one.
const pendingPermTTL = 5 * time.Minute

// pendingPerm is one unanswered decision and the instant it stops being one.
// Expiry is stored rather than the insertion time so the TTL is applied once,
// where the entry is created, and every read is a single comparison.
type pendingPerm struct {
	expires time.Time
	evt     api.ServerEvent
}

// pendingPermsTracker tracks permission_needed events that haven't been
// resolved yet. Keyed by request_id. Replayed on every new SSE
// connection so permissions survive reconnects even if the ring buffer
// has wrapped. Owns its own mutex to avoid contending with Hub.mu.
//
// Entries expire (pendingPermTTL). Expiry is enforced on READ — an expired
// entry is neither replayed nor claimable — and the map is swept in Add, which
// is the only operation that grows it and therefore the only one that has to
// bound it. There is deliberately no goroutine and no ticker: the map holds at
// most a handful of entries, so a background sweeper would spend a timer for the
// life of the process to reclaim a few hundred bytes that the next Add reclaims
// for free.
type pendingPermsTracker struct {
	perms map[int64]pendingPerm
	mu    sync.Mutex
}

func newPendingPermsTracker() *pendingPermsTracker {
	return &pendingPermsTracker{perms: make(map[int64]pendingPerm)}
}

// Add records a permission_needed event, and sweeps whatever has expired.
func (t *pendingPermsTracker) Add(id int64, evt api.ServerEvent) {
	t.mu.Lock()
	now := time.Now()
	for existing, e := range t.perms {
		if now.After(e.expires) {
			delete(t.perms, existing)
		}
	}
	t.perms[id] = pendingPerm{evt: evt, expires: now.Add(pendingPermTTL)}
	t.mu.Unlock()
}

// TakeIfPresent claims a request: it deletes the entry and returns it, and
// reports false when the request was already answered by somebody else.
//
// The lock spans BOTH the lookup and the delete, which is the whole point. It
// replaces a Has-then-Remove pair whose window let two surfaces each see the
// request as pending and both answer it — two browser tabs, or a human racing
// the unattended floor's deadline. The agent server discards the second answer
// silently, so before this the winner was decided there rather than here.
//
// An EXPIRED entry is not claimable either: the agent server stopped waiting for
// this answer, so sending one is at best ignored. It is deleted on the way out,
// because a request nobody may answer has nothing left to hold it for.
//
// The returned event is the tracked permission_needed / elicitation_needed /
// user_input_needed frame, which is what lets the caller announce WHICH kind of
// decision was settled without holding a second index.
func (t *pendingPermsTracker) TakeIfPresent(id int64) (api.ServerEvent, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.perms[id]
	if !ok {
		return api.ServerEvent{}, false
	}
	delete(t.perms, id)
	if time.Now().After(e.expires) {
		return api.ServerEvent{}, false
	}
	return e.evt, true
}

// ClearForChat drops every unresolved permission_needed entry owned by chatID.
func (t *pendingPermsTracker) ClearForChat(chatID api.ChatID) {
	if chatID == "" {
		return
	}
	t.mu.Lock()
	for id, e := range t.perms {
		if e.evt.ChatID == chatID {
			delete(t.perms, id)
		}
	}
	t.mu.Unlock()
}

// List returns a snapshot of the pending permission events that are still
// answerable, optionally filtered to a single chat. An expired entry is omitted:
// this feeds the connect-time replay, and replaying one would put a card in
// front of the user that the agent server has already given up on.
func (t *pendingPermsTracker) List(chatFilter api.ChatID) []api.ServerEvent {
	t.mu.Lock()
	now := time.Now()
	result := make([]api.ServerEvent, 0, len(t.perms))
	for _, e := range t.perms {
		if now.After(e.expires) {
			continue
		}
		if chatFilter != "" && e.evt.ChatID != "" && e.evt.ChatID != chatFilter {
			continue
		}
		result = append(result, e.evt)
	}
	t.mu.Unlock()
	return result
}
