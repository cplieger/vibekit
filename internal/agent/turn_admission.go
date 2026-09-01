package agent

// The per-chat ADMISSION slot. A reservation is minted synchronously, before
// any bridge exists, so a prompt is admitted or refused while the Turn record
// — model, metering baselines, opened-at — is stamped only at StartTurn, with
// the bridge live. The reservation is NOT a Turn: the priming turn's own
// open/finalize runs between reservation and StartTurn untouched, and a
// wire-started turn holds no reservation at all.

import (
	"context"
	"time"

	"github.com/cplieger/vibekit/internal/command"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// reserveLocked takes the admission slot iff it is free. Caller holds mu.
func (lc *chatLifecycle) reserveLocked(source vibekit.TurnOpenSource) bool {
	if lc.reserved {
		return false
	}
	lc.reserved = true
	lc.reservedSource = source
	return true
}

// holderSourceLocked names the admission holder. The OPEN turn wins over the
// reservation: during the prime window the reservation is a prompt's while the
// open turn is the prime, and the prime is what a refusal must describe — a
// steer delivered into it is consumed by a throwaway turn. Caller holds mu.
func (lc *chatLifecycle) holderSourceLocked() (vibekit.TurnOpenSource, bool) {
	if facts, open := lc.openFactsLocked(); open {
		return facts.Source, true
	}
	if lc.reserved {
		return lc.reservedSource, true
	}
	return 0, false
}

// tryReserve takes chatID's admission slot iff it is free — the shell door's
// and the recovery retry's form, never a wait.
func (r *turnRegistry) tryReserve(chatID vibekit.ChatID, source vibekit.TurnOpenSource) bool {
	lc := r.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return lc.reserveLocked(source)
}

// releaseReservation frees the admission slot and wakes every parked waiter,
// of which at most one acquires.
func (r *turnRegistry) releaseReservation(chatID vibekit.ChatID) {
	lc := r.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	lc.reserved = false
	lc.reservedSource = 0
	lc.wakeLocked()
}

// reserveOrHolder attempts the reservation and, when refused, names the holder
// and hands back the channel that closes on the chat's next state change — all
// in ONE acquisition, so a release landing between a failed try and the park
// cannot strand the waiter on a stale answer.
func (r *turnRegistry) reserveOrHolder(chatID vibekit.ChatID, source vibekit.TurnOpenSource) (ok bool, holder vibekit.TurnOpenSource, changed <-chan struct{}) {
	lc := r.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.reserveLocked(source) {
		return true, 0, nil
	}
	holder, _ = lc.holderSourceLocked()
	return false, holder, lc.changed
}

// admissionHolder reports the chat's admission holder's source: the open
// turn's when one is open, else the reservation's, and false when neither is
// held.
func (r *turnRegistry) admissionHolder(chatID vibekit.ChatID) (vibekit.TurnOpenSource, bool) {
	lc := r.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return lc.holderSourceLocked()
}

// wakeChat wakes every waiter parked on chatID without moving any state: the
// bridge-ready wake, so a parked prompt answers on the state the bridge's
// arrival created rather than on whatever changes next.
func (r *turnRegistry) wakeChat(chatID vibekit.ChatID) {
	lc := r.lifecycleFor(chatID)
	lc.mu.Lock()
	lc.wakeLocked()
	lc.mu.Unlock()
}

// TryReserveTurn takes the chat's admission slot iff it is free, minting NO
// Turn. The shell door reserves through this (a `!cmd` during any held slot
// refuses immediately), and the empty-turn recovery re-reserves through it (a
// user prompt that won the slot first abandons the retry).
func (bc *BridgeCoordinator) TryReserveTurn(chatID vibekit.ChatID, source vibekit.TurnOpenSource) bool {
	return bc.turns.tryReserve(chatID, source)
}

// ReleaseTurnReservation frees the admission slot TryReserveTurn or
// ReserveTurnForPrompt took, waking every waiter.
func (bc *BridgeCoordinator) ReleaseTurnReservation(chatID vibekit.ChatID) {
	bc.turns.releaseReservation(chatID)
}

// AdmissionHolderSource reports who holds the chat's admission: the open
// turn's source when one is open (a prime overrides the prompt reservation it
// runs under; a wire-started turn holds no reservation), else the
// reservation's. Satisfies command.TurnOutcomeAccess; the steer refusal and
// the prompt-refusal arm both key on it.
func (bc *BridgeCoordinator) AdmissionHolderSource(chatID vibekit.ChatID) (vibekit.TurnOpenSource, bool) {
	return bc.turns.admissionHolder(chatID)
}

// ReserveTurnForPrompt takes the chat's admission slot for a prompt, waiting up
// to wait while it is held; at most one waiter acquires per wake.
//
// The refusal arm keys on the HOLDER'S SOURCE, not bridge liveness alone: a
// prompt-class holder with a live bridge answers Busy immediately (the client's
// 409→steer conversion works, so waiting buys nothing); every other holder parks
// the waiter and answers Starting at the budget. A dead ctx also answers
// Starting: nothing reads the answer.
func (bc *BridgeCoordinator) ReserveTurnForPrompt(ctx context.Context, chatID vibekit.ChatID, wait time.Duration) command.AdmissionOutcome {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		ok, holder, changed := bc.turns.reserveOrHolder(chatID, vibekit.TurnSourcePrompt)
		if ok {
			return command.AdmissionAcquired
		}
		if holder.PromptClass() && bc.bridgeLive(chatID) {
			return command.AdmissionBusy
		}
		select {
		case <-changed:
		case <-timer.C:
			return bc.expiredAdmission(chatID)
		case <-ctx.Done():
			return command.AdmissionStarting
		}
	}
}

// expiredAdmission is the budget-expiry arm, keyed on the holder's source. One
// last try first: a release landing exactly at expiry is an acquisition, not a
// refusal.
func (bc *BridgeCoordinator) expiredAdmission(chatID vibekit.ChatID) command.AdmissionOutcome {
	ok, holder, _ := bc.turns.reserveOrHolder(chatID, vibekit.TurnSourcePrompt)
	if ok {
		return command.AdmissionAcquired
	}
	if holder.PromptClass() && bc.bridgeLive(chatID) {
		return command.AdmissionBusy
	}
	return command.AdmissionStarting
}

// bridgeLive reports whether the chat has a bridge PAST its spawn. The manager
// registers the record before Start so concurrent opens coalesce, so mere
// presence is not liveness — a bridge still starting is exactly the "starting"
// state the refusal names.
func (bc *BridgeCoordinator) bridgeLive(chatID vibekit.ChatID) bool {
	sb := bc.bridge.mgr.get(chatID)
	return sb != nil && sb.startedPastSpawn()
}
