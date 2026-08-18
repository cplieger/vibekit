package hub

import (
	"slices"
	"sync"

	"github.com/cplieger/vibekit/internal/api"
)

// pendingPermsTracker tracks permission_needed events that haven't been
// resolved yet. Keyed by request_id. Replayed on every new SSE
// connection so permissions survive reconnects even if the ring buffer
// has wrapped. Owns its own mutex to avoid contending with Hub.mu.
//
// THERE IS DELIBERATELY NO TTL, and the reason is a measurement rather than a
// preference, because a 5-minute expiry was added here once and had to come out.
//
// The argument for one was that the agent server abandons a request after five
// minutes (PENDING_PERMISSION_TTL_MS), so vibekit could stop offering a decision
// at the point the answer would stop being accepted. Read off the 2.18.0 bundle,
// that is wrong twice over:
//
//   - The constant is read in exactly two places, sweepStalePermissions and
//     sweepStaleUserInputs, whose own docblocks say they are "called
//     opportunistically when new permissions are stored". So it is not a wall
//     clock: an otherwise idle request is never swept. handlePermissionRespond
//     never consults createdAt at all — it checks presence in the map and
//     nothing else — so an aged entry is answered normally.
//   - Both live on MultiplexStream, which is constructed ONLY inside
//     startWebSocket(). vibekit spawns `kiro-cli acp` over stdio, i.e.
//     startStdio(), which builds KiroAgent with no mux. pendingPermissions is
//     referenced nowhere outside that WebSocket path, so the mechanism does not
//     run for vibekit at all.
//
// On vibekit's transport a session/request_permission is a plain JSON-RPC
// request that stays open until it is answered or the turn is cancelled. An
// expiry here therefore invents a deadline nothing upstream has: the card would
// vanish from the connect-time replay and the answer would be refused as
// already-answered, while the agent server sat waiting for a response that can
// now never be sent — a turn wedged by its own client.
//
// Growth is bounded by lifecycle events instead, which is the honest bound
// because each one PROVES the request is no longer answerable: a successful
// TakeIfPresent deletes the entry, and ClearForChat drops a chat's entries from
// CmdCancel, CmdCloseChat and cleanupChatState (delete and archive). A handful of
// structs per live chat, freed when that chat's turn ends.
type pendingPermsTracker struct {
	perms map[int64]api.ServerEvent
	mu    sync.Mutex
}

func newPendingPermsTracker() *pendingPermsTracker {
	return &pendingPermsTracker{perms: make(map[int64]api.ServerEvent)}
}

// Add records a permission_needed event.
func (t *pendingPermsTracker) Add(id int64, evt api.ServerEvent) {
	t.mu.Lock()
	t.perms[id] = evt
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
// Presence is the ONLY test. See the type comment for why there is no age check:
// the agent server holds a stdio request open until it is answered, so a request
// still in this map is still answerable however old it is.
//
// The returned event is the tracked permission_needed / elicitation_needed /
// user_input_needed frame, which is what lets the caller announce WHICH kind of
// decision was settled without holding a second index.
func (t *pendingPermsTracker) TakeIfPresent(id int64) (api.ServerEvent, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	evt, ok := t.perms[id]
	if !ok {
		return api.ServerEvent{}, false
	}
	delete(t.perms, id)
	return evt, true
}

// ClearForChat drops every unresolved permission_needed entry owned by chatID.
func (t *pendingPermsTracker) ClearForChat(chatID api.ChatID) {
	if chatID == "" {
		return
	}
	t.mu.Lock()
	for id, evt := range t.perms {
		if evt.ChatID == chatID {
			delete(t.perms, id)
		}
	}
	t.mu.Unlock()
}

// List returns a snapshot of the unresolved permission events, optionally
// filtered to a single chat. This feeds the connect-time replay, and it returns
// every tracked entry: the set it offers is exactly the set TakeIfPresent will
// still accept an answer for.
//
// ORDER IS PART OF THE CONTRACT, ascending by request id. The ids come from the
// JSON-RPC boundary, which assigns them monotonically, so ascending id is the
// order the agent asked — and a replay that hands the cards over in ask order is
// the only way a reconnecting surface renders the same queue the one before it
// did. Iterating the map directly gave Go's randomized order, so two tabs
// reconnecting to the same chat could stack the same three cards differently and
// a single tab reordered them on every reconnect. The server is canonical here:
// ordering a replay client-side would need a request id every client agrees to
// sort on, which is the server's own key by another name.
//
// There is deliberately NO per-bridge grouping. A bridge is an implementation
// detail of which chat a card belongs to, and chatFilter already answers that;
// grouping by it would lift a newer chat's ask above an older one's for no
// reason a reader could see. The one sequence that matters is the whole queue's,
// and it covers permission, elicitation and structured-question cards alike
// because all three are tracked here under the same id space.
func (t *pendingPermsTracker) List(chatFilter api.ChatID) []api.ServerEvent {
	t.mu.Lock()
	ids := make([]int64, 0, len(t.perms))
	for id, evt := range t.perms {
		if chatFilter != "" && evt.ChatID != "" && evt.ChatID != chatFilter {
			continue
		}
		ids = append(ids, id)
	}
	slices.Sort(ids)
	result := make([]api.ServerEvent, 0, len(ids))
	for _, id := range ids {
		result = append(result, t.perms[id])
	}
	t.mu.Unlock()
	return result
}
