package command

// The op ledger: what a repeated create resolves to.
//
// Server-minting a chat id removes an idempotency the client-minted id gave for
// free. When the client chose the id, a retry carried the SAME id, and every
// creating handler's `if exists { return false }` made the second attempt a
// no-op. Minting server-side, a retry mints again — two chats for one gesture.
//
// The Idempotency-Key header covers most of that already (one middleware over
// every mutating route, and @cplieger/actions threads one key through every retry
// attempt of a dispatch). It does not cover all of it: the cache's TTL is 5
// minutes and eviction is LAZY, so a lookup past the TTL falls through and the
// handler runs for real (internal/server/idempotency.go). A user-driven retry —
// the error toast's Retry button re-dispatches with the same args, minutes later
// — lands in exactly that window.
//
// So the ledger covers the fall-through and nothing else: op_id -> chat id,
// bounded, TTL'd, in memory. Deliberately NOT a field on the chat record, which
// the design considered and rejected: chat.Store has no by-field index and the
// directory listing IS the index, so a lookup could only scan every chat file.
// The window that needs covering is a retry window, not a chat lifetime.

import (
	"sync"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// createOpTTL is how long an op_id resolves to the chat it created.
//
// Longer than the Idempotency-Key cache's 5 minutes on purpose: covering that
// cache's fall-through is this ledger's entire job, so a shorter TTL would leave
// the gap it exists to close, and matching it exactly would leave the boundary
// racing. Short enough that the map is a retry window rather than history.
const createOpTTL = 10 * time.Minute

// maxCreateOps bounds the map. A create is a deliberate human gesture, so the
// live population inside one TTL is single digits; the cap is what stops a
// client looping op ids from growing it without limit.
const maxCreateOps = 512

// createLedger records which chat each create op_id produced.
//
// Safe for concurrent use. The zero value IS usable — a nil map is only read
// until the first record — but construct with newCreateLedger, which says so.
type createLedger struct {
	mu   sync.Mutex
	ops  map[string]createOp
	now  func() time.Time
	ttl  time.Duration
	maxN int
}

type createOp struct {
	expires time.Time
	chatID  vibekit.ChatID
}

func newCreateLedger() *createLedger {
	return &createLedger{
		ops:  make(map[string]createOp),
		now:  time.Now,
		ttl:  createOpTTL,
		maxN: maxCreateOps,
	}
}

// resolve returns the chat id op names, minting one with mint when this is the
// first time op has been seen. replay reports which happened, so a caller can
// tell a fresh create from a retry answering with the chat it already made.
//
// An empty op is a caller that sent no correlation id: it always mints and
// records nothing, because there is no key to record it under. Reserve and mint
// happen under ONE lock, so two attempts of one op cannot mint two chats even if
// they overlap — which is the whole property, and a check-then-mint pair spelled
// at the call site would not have it.
func (l *createLedger) resolve(op string, mint func() vibekit.ChatID) (id vibekit.ChatID, replay bool) {
	if op == "" {
		return mint(), false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ops == nil {
		l.ops = make(map[string]createOp)
	}
	now := l.now()
	if e, ok := l.ops[op]; ok && now.Before(e.expires) {
		return e.chatID, true
	}
	l.sweep(now)
	id = mint()
	l.ops[op] = createOp{chatID: id, expires: now.Add(l.ttl)}
	return id, false
}

// peek reports the chat op already resolved to, WITHOUT minting one and without
// extending its TTL. An op that has never been seen, has expired, or is empty
// reports false.
//
// It exists for the one caller that has to decide something before it is allowed
// to mint: a create whose capacity reservation must run before the mint needs to
// know whether this op already owns a tab, and fork_chat needs to know whether it
// may skip its session/fork round trip. Both are decisions ABOUT a possible
// mint, so neither can be spelled as a resolve.
func (l *createLedger) peek(op string) (vibekit.ChatID, bool) {
	if op == "" {
		return "", false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.ops[op]
	if !ok || !l.now().Before(e.expires) {
		return "", false
	}
	return e.chatID, true
}

// sweep drops expired entries and, if the map is still full, the entry closest
// to expiry. Caller holds l.mu.
//
// Eviction on write rather than on a timer: the map is only read by a create, so
// a goroutine sweeping it between creates would be a lifetime to own for no
// observable difference. The oldest-first fallback is what makes the bound hard
// — a TTL alone bounds nothing inside one window.
func (l *createLedger) sweep(now time.Time) {
	for k, e := range l.ops {
		if !now.Before(e.expires) {
			delete(l.ops, k)
		}
	}
	for len(l.ops) >= l.maxN {
		oldest, found := "", time.Time{}
		for k, e := range l.ops {
			if found.IsZero() || e.expires.Before(found) {
				oldest, found = k, e.expires
			}
		}
		if oldest == "" {
			return
		}
		delete(l.ops, oldest)
	}
}
