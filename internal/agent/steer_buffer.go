package agent

import (
	"cmp"
	"slices"
	"sync"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// steerBuffer is vibekit's projection of KAS's own steering buffer: the mid-turn
// steers the model has NOT read, replayed on every new SSE connection so a
// reconnecting browser gets its dock back.
//
// IT EXISTS BECAUSE NOTHING CAN READ THAT BUFFER BACK. `_session/steer` and
// `_session/steer/clear` are the whole verb set — there is no list — and
// `streamInitialState` replayed nothing steer-shaped, so a client that lost a
// frame lost the row while the message was still queued in KAS. The reader watched
// their correction vanish from the dock and reasonably re-sent it.
//
// THERE IS DELIBERATELY NO TTL, for pendingPermsTracker's own recorded reason: the
// removal signals are REAL. KAS clears its buffer at every turn boundary and says
// so (`steering_cleared`), and a steer the model reads says so too
// (`steering_injected`), so an expiry would invent a deadline nothing upstream has
// — and it would under-report a genuinely old unread steer while KAS still held
// it. Growth is bounded by those removal paths plus ClearForChat at
// cleanupChatState, plus a per-chat cap so a hostile producer cannot grow it.
//
// NOT command.SteerLedger, which answers a different question with a different
// lifetime: that one is TTL'd by design because it answers whose WORDS a steer
// carries (an id it has forgotten reads as the agent's, which is the safe answer
// there). A 31-minute-old unread steer would drop out of it while KAS still held
// the row, so one type answering both questions would have to pick one lifetime.
type steerBuffer struct {
	waiting map[steerKey]vibekit.SteerQueuedPayload
	maxN    int
	mu      sync.Mutex
}

// steerKey addresses one waiting steer: the chat that owns it plus KAS's own id.
// THE PAIR, never the id alone — an id is unique within one session and there is
// one session per chat, so two live chats holding one id is ordinary. A STRUCT key
// rather than a joined string, because a steer id is KAS's and composing one would
// put a separator inside a value vibekit does not own the shape of.
type steerKey struct {
	chat vibekit.ChatID
	id   string
}

// maxWaitingPerChat bounds one chat's waiting set. A steer is a deliberate human
// gesture typed into a running turn, so the live population is single digits; the
// bound exists so a producer that never sends a removal cannot grow the map
// without limit. At the cap the OLDEST entry by id is dropped, which costs one
// unreplayed row rather than refusing every later one.
const maxWaitingPerChat = 64

func newSteerBuffer() *steerBuffer {
	return &steerBuffer{waiting: make(map[steerKey]vibekit.SteerQueuedPayload), maxN: maxWaitingPerChat}
}

// SteerWaiting records a steer KAS has buffered and the model has not read.
//
// Idempotent by key, which is required rather than defensive: a reconnect replays
// the queued frame for a steer still waiting, and this is what stops the same row
// being counted twice against the cap.
func (b *steerBuffer) SteerWaiting(chatID vibekit.ChatID, p vibekit.SteerQueuedPayload) {
	if p.SteerID == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	k := steerKey{chat: chatID, id: p.SteerID}
	if _, held := b.waiting[k]; !held {
		b.evictOldestLocked(chatID)
	}
	b.waiting[k] = p
}

// SteerRead drops the one steer an injected frame names: the model has read it, so
// replaying it would offer a delivered message back to the dock.
func (b *steerBuffer) SteerRead(chatID vibekit.ChatID, steerID string) {
	b.SteerForgotten(chatID, []string{steerID})
}

// SteerForgotten drops every named steer, for the frame that says KAS's buffer no
// longer holds them. Named ids only, matching the wire: a turn boundary reports
// exactly which ids it cleared.
func (b *steerBuffer) SteerForgotten(chatID vibekit.ChatID, steerIDs []string) {
	if len(steerIDs) == 0 {
		return
	}
	b.mu.Lock()
	for _, id := range steerIDs {
		delete(b.waiting, steerKey{chat: chatID, id: id})
	}
	b.mu.Unlock()
}

// ClearForChat drops every waiting steer owned by chatID, at its teardown. A
// steer's lifetime is one turn, so a chat that is gone can only ever hold ids no
// frame will arrive for.
func (b *steerBuffer) ClearForChat(chatID vibekit.ChatID) {
	if chatID == "" {
		return
	}
	b.mu.Lock()
	for k := range b.waiting {
		if k.chat == chatID {
			delete(b.waiting, k)
		}
	}
	b.mu.Unlock()
}

// List returns the waiting steers as the events a connect replay writes, filtered
// to one chat when chatFilter is set.
//
// ORDER IS PART OF THE CONTRACT, ascending by chat then id, for
// pendingPermsTracker's own reason: two tabs reconnecting must stack the same dock
// rather than two different arbitrary orders. It is not SEND order — the wire
// carries no sequence for a steer — but it is stable, which is what a reader
// comparing two devices needs.
func (b *steerBuffer) List(chatFilter vibekit.ChatID) []vibekit.ServerEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	keys := make([]steerKey, 0, len(b.waiting))
	for k := range b.waiting {
		if chatFilter != "" && k.chat != chatFilter {
			continue
		}
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, c steerKey) int {
		return cmp.Or(cmp.Compare(a.chat, c.chat), cmp.Compare(a.id, c.id))
	})
	out := make([]vibekit.ServerEvent, 0, len(keys))
	for _, k := range keys {
		out = append(out, vibekit.NewEvent(vibekit.EventSteerQueued, k.chat, b.waiting[k]))
	}
	return out
}

// evictOldestLocked makes room for one new entry in chatID's set. Caller holds
// b.mu. Bounded PER CHAT rather than globally, so a busy chat cannot evict a
// quiet one's rows.
func (b *steerBuffer) evictOldestLocked(chatID vibekit.ChatID) {
	held := make([]string, 0, b.maxN)
	for k := range b.waiting {
		if k.chat == chatID {
			held = append(held, k.id)
		}
	}
	if len(held) < b.maxN {
		return
	}
	slices.Sort(held)
	for _, id := range held[:len(held)-b.maxN+1] {
		delete(b.waiting, steerKey{chat: chatID, id: id})
	}
}

// SteerWaiting / SteerRead / SteerForgotten on the bus are the translate-side
// role: the three arms of the steering cascade feed the tracker as they broadcast,
// so a replay and a live frame carry the same payload.

func (b *bus) SteerWaiting(chatID vibekit.ChatID, p vibekit.SteerQueuedPayload) {
	b.steers.SteerWaiting(chatID, p)
}

func (b *bus) SteerRead(chatID vibekit.ChatID, steerID string) {
	b.steers.SteerRead(chatID, steerID)
}

func (b *bus) SteerForgotten(chatID vibekit.ChatID, steerIDs []string) {
	b.steers.SteerForgotten(chatID, steerIDs)
}

// ClearWaitingSteersForChat drops every waiting steer owned by chatID.
func (b *bus) ClearWaitingSteersForChat(chatID vibekit.ChatID) {
	b.steers.ClearForChat(chatID)
}
