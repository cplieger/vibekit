package command

// The steer ledger: which mid-turn steers are the USER's own words.
//
// Nothing on the wire separates them from a workflow's report (see
// vibekit.SteerOrigin), so CmdSteer records the id KAS returned for every steer
// this server sent. In-memory, TTL'd and bounded like createLedger next door,
// because a steer's whole lifetime is one turn.

// ACCEPTED COST: a restart mid-turn loses the set, so a steer sent before it and
// read after labels as the agent's — unreachable in practice, since the restart
// kills the turn that would have read it.

import (
	"sync"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// steerTTL is how long a recorded steer id answers "the user's".
//
// A steer is consumed at the next node boundary, so minutes is already
// generous; it is deliberately not the turn's own length, because nothing tells
// this ledger when a turn ended and a turn can legitimately run for hours.
const steerTTL = 30 * time.Minute

// maxSteerOps bounds the map. A steer is a deliberate human gesture typed into
// a running turn, so the live population inside one TTL is single digits; the
// bound exists so a pathological producer cannot grow it without limit.
const maxSteerOps = 512

// steerKey addresses one steer. A STRUCT key rather than a joined string: a
// steer id is KAS's, so composing one would put a separator inside a value
// vibekit does not own the shape of.
type steerKey struct {
	chat vibekit.ChatID
	id   string
}

// SteerLedger records the steers this server sent. Safe for concurrent use.
// Construct with NewSteerLedger.
type SteerLedger struct {
	sent map[steerKey]time.Time
	now  func() time.Time
	ttl  time.Duration
	maxN int
	mu   sync.Mutex
}

// NewSteerLedger returns an empty ledger.
func NewSteerLedger() *SteerLedger {
	return &SteerLedger{
		sent: make(map[steerKey]time.Time),
		now:  time.Now,
		ttl:  steerTTL,
		maxN: maxSteerOps,
	}
}

// RecordUserSteer records that this server sent steerID for chatID.
//
// Called with the id KAS RETURNED, never the one vibekit derived: the reply's
// `messageId` is what every later frame is keyed by, so recording anything else
// would file the steer under a name no frame carries.
func (l *SteerLedger) RecordUserSteer(chatID vibekit.ChatID, steerID string) {
	if l == nil || steerID == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweep(now)
	l.sent[steerKey{chat: chatID, id: steerID}] = now.Add(l.ttl)
}

// SteerOrigin answers whose words the steer is. A recorded, unexpired id is the
// user's; everything else is the agent's.
//
// Deliberately NOT a lookup that can fail: absence is a real answer here, and
// returning "unknown" would push a decision the client has no vocabulary for
// onto every consumer.
func (l *SteerLedger) SteerOrigin(chatID vibekit.ChatID, steerID string) vibekit.SteerOrigin {
	if l == nil || steerID == "" {
		return vibekit.SteerOriginAgent
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if expires, ok := l.sent[steerKey{chat: chatID, id: steerID}]; ok && l.now().Before(expires) {
		return vibekit.SteerOriginUser
	}
	return vibekit.SteerOriginAgent
}

// ForgetChat drops every steer recorded for one chat, at its teardown.
//
// A linear scan over a map the bound above keeps in the low hundreds, because
// the alternative — a second index by chat — is a second thing to keep in step
// with the first for a sweep that runs once per chat close.
func (l *SteerLedger) ForgetChat(chatID vibekit.ChatID) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for k := range l.sent {
		if k.chat == chatID {
			delete(l.sent, k)
		}
	}
}

// sweep drops expired entries and, if the map is still full, the entry closest
// to expiry — createLedger's own shape, so losing one record costs one
// mislabelled note rather than every later one. Caller holds l.mu.
func (l *SteerLedger) sweep(now time.Time) {
	for k, expires := range l.sent {
		if !now.Before(expires) {
			delete(l.sent, k)
		}
	}
	for len(l.sent) >= l.maxN {
		var oldest steerKey
		var found time.Time
		for k, expires := range l.sent {
			if found.IsZero() || expires.Before(found) {
				oldest, found = k, expires
			}
		}
		if found.IsZero() {
			return
		}
		delete(l.sent, oldest)
	}
}
