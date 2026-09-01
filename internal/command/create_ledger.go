package command

// The op ledger: what a repeated create resolves to.
//
// Server-minting a chat id removes the idempotency a client-minted id gave
// for free: a retry now mints a second chat unless something remembers the
// first.
//
// The Idempotency-Key header covers most of this already, but its cache TTL
// is 5 minutes with lazy eviction, so a lookup past the TTL falls through
// and the handler runs for real. This ledger covers that fall-through:
// op_id -> chat id, bounded, TTL'd, in memory. Deliberately not a field on
// the chat record — chat.Store has no by-field index, so a lookup would
// scan every chat file for a retry window rather than a chat lifetime.

import (
	"sync"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// createOpTTL is how long an op_id resolves to the chat it created. Longer
// than the Idempotency-Key cache's 5 minutes on purpose, since covering that
// cache's fall-through is this ledger's whole job.
const createOpTTL = 10 * time.Minute

// maxCreateOps bounds the map: a create is a deliberate human gesture, so
// the live population inside one TTL is single digits.
const maxCreateOps = 512

// createLedger records which chat each create op_id produced. Safe for
// concurrent use. The zero value is usable, but construct with
// newCreateLedger, which says so.
type createLedger struct {
	ops  map[string]createOp
	now  func() time.Time
	ttl  time.Duration
	maxN int
	mu   sync.Mutex
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

// resolve returns the chat id op names, minting one with mint when this is
// the first time op has been seen. replay reports which happened.
//
// An empty op always mints and records nothing (no key to record it under).
// Reserve and mint happen under one lock, so two attempts of one op cannot
// mint two chats even if they overlap.
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

// peek reports the chat op already resolved to, without minting one and
// without extending its TTL. An op that has never been seen, has expired, or
// is empty reports false.
//
// Exists for a caller that must decide something before it is allowed to
// mint: a create whose capacity reservation must run before the mint needs
// to know whether this op already owns a tab.
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

// sweep drops expired entries and, if the map is still full, the entry
// closest to expiry. Caller holds l.mu.
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
