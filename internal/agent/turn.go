package agent

// Every terminal step of the per-chat turn lifecycle goes through finalizeTurn.

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/cplieger/vibekit/internal/buffer"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// TurnState is the per-chat turn state machine.
type TurnState int

const (
	turnIdle TurnState = iota
	turnOpen
	// turnFinalizing IS the exclusion: an operation that finds it WAITS, so a
	// finalize's persistence and broadcast need no held lock.
	turnFinalizing
)

// CreditBaseline is the chat's cumulative credit reading when a turn opened; the
// turn's own spend is the difference against it at finalize.
type CreditBaseline float64

// turnCarrier is the persisted message that carries a turn's outcome, plus the one
// fact the lost-claim amend turns on. A struct rather than a `(string, bool)` pair
// because a transposed pair at its one call site compiles and is silent in both
// directions.
type turnCarrier struct {
	// MessageID is the carrier's id. Empty when the turn persisted no carrier: a
	// prime turn, an unstarted discard, and every turn closed before this existed.
	MessageID string
	// ReasonSupplied is whether a CLOSER supplied the carrier's reason rather than
	// it being defaulted from the outcome. THE specificity test, exact rather than
	// a heuristic: reasonFor defaults only when the closer supplied nothing.
	ReasonSupplied bool
}

// Turn is everything true of one turn, for its whole life, whoever opened it.
// Epoch, Chat, Opened, Source, Model, Credits, Buf and done are written once at
// open and read without the lock; every other field is guarded by the owning
// chatLifecycle's mutex.
type Turn struct {
	// Buf is this turn's own content buffer, so one turn's partial reply cannot
	// extend the next turn's message.
	Buf *buffer.Buffer
	// lc is the lifecycle that OWNS this turn, carried rather than re-resolved from
	// Chat: a forgotten chat's lookup mints a FRESH lifecycle, so re-resolving hands
	// an operation a different mutex and a different wake channel.
	lc *chatLifecycle
	// done is closed once, at finalize.
	done chan struct{}
	// Opened is when the turn began, and the only source of its elapsed time.
	Opened time.Time
	// Model is the model answering, captured at open for every source.
	Model string
	// carrier is the persisted message holding this turn's outcome, recorded at close
	// so a closer that LOST the claim can still reach it. See amendLostReason.
	carrier turnCarrier
	Chat    vibekit.ChatID
	// Interrupt is the first cause claimed for this turn.
	Interrupt vibekit.InterruptCause
	// result is immutable once done is closed.
	result  vibekit.TurnResult
	Epoch   vibekit.TurnEpoch
	Credits CreditBaseline
	// NeedSeq is the read loop position a LOCAL settle of this turn must wait for:
	// where the session/prompt response arrived. Zero means no settle is waiting.
	NeedSeq uint64
	// needGen is the forward generation NeedSeq belongs to: a position minted
	// against one bridge means nothing against the next.
	needGen uint64
	// stagedElapsedMs sums the durations the engine's turn_completion frames report,
	// so several frames for one turn add instead of the last one winning.
	stagedElapsedMs float64
	stagedSummaries int
	// holds is how many completion handles are outstanding, and what bounds
	// retention: a result a waiter still holds a handle for is never evicted.
	holds  int
	Source vibekit.TurnOpenSource
	// acked is whether a wire turn_start has bound to this turn. Provisional for an
	// acknowledgeable source, since the bracket cannot tell a prompted turn from an
	// agent-initiated one — see reclassify.
	acked bool
}

// chatLifecycle is one chat's turn state machine.
type chatLifecycle struct {
	cur *Turn
	// pending is the one PRE-OPENED prompt turn no wire bracket has bound yet,
	// usually the same record as cur (see reclassify). One rather than a queue: a
	// prime awaits its own epoch and admission control refuses a second prompt.
	pending *Turn
	// retained holds the FINALIZED turns whose handles have not all been released,
	// so a waiter can still read a result after the chat has moved on. Several
	// handles can be outstanding at once, hence a map.
	retained map[vibekit.TurnEpoch]*Turn
	// changed is closed and REPLACED on EVERY state change, under mu. A channel
	// rather than a Cond, which re-acquires the mutex to return, so a parked waiter
	// would hold the lock the finalize needs.
	changed   chan struct{}
	nextEpoch vibekit.TurnEpoch
	// observedSeq is the read loop position the FOLDER has reached. Advanced for
	// every frame consumed, not only the ones that touch a turn — see observe.
	observedSeq uint64
	// fwdGen counts the forward goroutines attached for this chat: a new bridge
	// restarts its sequence at zero, so positions compare only within a generation.
	fwdGen uint64
	// reservedSource is who holds the admission slot, meaningful only while reserved
	// is true. The reservation is NOT a Turn: it is the bare per-chat admission taken
	// before any bridge exists, and the record is minted at StartTurn.
	reservedSource vibekit.TurnOpenSource
	// forwardGone is whether the attached forward goroutine has exited, so a settle
	// waiting on a position that can no longer advance stops waiting.
	forwardGone bool
	// reserved is whether the admission slot is held.
	reserved bool
	state    TurnState
	mu       sync.Mutex
}

// turnRegistry holds one lifecycle per chat. Lock order is registry.mu ->
// lifecycle.mu and never the reverse, and neither is held across chat.Store.Mutate,
// which takes a per-chat mutex across its callback.
type turnRegistry struct {
	chats map[vibekit.ChatID]*chatLifecycle
	mu    sync.Mutex
}

func newTurnRegistry() *turnRegistry {
	return &turnRegistry{chats: make(map[vibekit.ChatID]*chatLifecycle)}
}

// lifecycleFor returns the chat's lifecycle, creating it on first use.
func (r *turnRegistry) lifecycleFor(chatID vibekit.ChatID) *chatLifecycle {
	r.mu.Lock()
	defer r.mu.Unlock()
	lc, ok := r.chats[chatID]
	if !ok {
		lc = &chatLifecycle{changed: make(chan struct{})}
		r.chats[chatID] = lc
	}
	return lc
}

// lookup returns the chat's lifecycle WITHOUT creating one, reporting false when
// the chat has none. The read-only twin of lifecycleFor: an entry leaves the map
// only through forget, so a predicate on an HTTP read path would otherwise leave a
// lifecycle and its `changed` channel behind for every chat merely opened.
func (r *turnRegistry) lookup(chatID vibekit.ChatID) (*chatLifecycle, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	lc, ok := r.chats[chatID]
	return lc, ok
}

// forget drops a chat's lifecycle. A turn already open keeps the dropped one, since
// every operation holding a *Turn goes through that turn's own lc, so an in-flight
// finalize still publishes where its waiters are parked.
func (r *turnRegistry) forget(chatID vibekit.ChatID) {
	r.mu.Lock()
	delete(r.chats, chatID)
	r.mu.Unlock()
}

// wakeLocked wakes every waiter without moving the state. Caller holds mu.
func (lc *chatLifecycle) wakeLocked() {
	close(lc.changed)
	lc.changed = make(chan struct{})
}

// setStateLocked moves the state and wakes every waiter. Caller holds mu.
func (lc *chatLifecycle) setStateLocked(s TurnState) {
	lc.state = s
	lc.wakeLocked()
}

// awaitNotFinalizing blocks while the chat is finalizing and returns with the
// mutex HELD, or reports false when ctx died first (mutex released). The next
// epoch is not observable as open until the previous turn's effects completed.
func (lc *chatLifecycle) awaitNotFinalizing(ctx context.Context) bool {
	lc.mu.Lock()
	for lc.state == turnFinalizing {
		changed := lc.changed
		lc.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return false
		}
		lc.mu.Lock()
	}
	return true
}

// openLocked mints the next turn, with its own content buffer. Caller holds mu
// and has established that the chat is not finalizing.
func (lc *chatLifecycle) openLocked(chatID vibekit.ChatID, source vibekit.TurnOpenSource, model string, credits CreditBaseline) *Turn {
	lc.nextEpoch++
	t := &Turn{
		Opened:  time.Now(),
		Model:   model,
		Chat:    chatID,
		Epoch:   lc.nextEpoch,
		Credits: credits,
		Buf:     buffer.New(),
		Source:  source,
		done:    make(chan struct{}),
		lc:      lc,
	}
	// A prime's frames fold but never publish.
	t.Buf.SetMuted(source == vibekit.TurnSourcePrime)
	lc.cur = t
	if source.Acknowledgeable() {
		lc.pending = t
	}
	lc.setStateLocked(turnOpen)
	return t
}

// open opens a turn for chatID and ALLOCATES a completion handle on it, so a caller
// holding the returned epoch can never be told the turn does not exist. A turn
// already open is answered per source: localShell refuses, an acknowledgeable source
// opens anyway, since the turn it finds is routinely an agent-initiated one holding
// no prompt slot. A turn the WIRE started goes through openWire.
func (r *turnRegistry) open(ctx context.Context, chatID vibekit.ChatID, source vibekit.TurnOpenSource, model string, credits CreditBaseline) *Turn {
	lc := r.lifecycleFor(chatID)
	if !lc.awaitNotFinalizing(ctx) {
		return nil
	}
	defer lc.mu.Unlock()
	if source == vibekit.TurnSourceLocalShell && lc.state == turnOpen && lc.cur != nil {
		return nil
	}
	if source.Acknowledgeable() && lc.pending != nil {
		// A second unacknowledged pre-open means admission control let a second
		// prompt through; the older one keeps its record and its own settle.
		slog.Warn("a second prompt pre-opened a turn while one was still unacknowledged",
			"chat_id", chatID, "pending_epoch", lc.pending.Epoch)
	}
	t := lc.openLocked(chatID, source, model, credits)
	t.holds++
	return t
}

// claimOpen claims the chat's OPEN turn for finalizing, reporting false when the
// chat has none. First-wins, so two closers racing one turn produce one set of
// effects.
func (r *turnRegistry) claimOpen(ctx context.Context, chatID vibekit.ChatID) (*Turn, bool) {
	lc := r.lifecycleFor(chatID)
	if !lc.awaitNotFinalizing(ctx) {
		return nil, false
	}
	defer lc.mu.Unlock()
	if lc.state != turnOpen || lc.cur == nil {
		return nil, false
	}
	return lc.claimLocked(lc.cur), true
}

// claimEpoch claims ONE named turn for finalizing, reporting false when that epoch
// is not live on this chat. Epoch-scoped, so a closer armed for turn N is harmless
// once turn N+1 has opened, and it still reaches a pre-open that is no longer the
// folding turn.
func (r *turnRegistry) claimEpoch(ctx context.Context, chatID vibekit.ChatID, epoch vibekit.TurnEpoch) (*Turn, bool) {
	lc := r.lifecycleFor(chatID)
	if !lc.awaitNotFinalizing(ctx) {
		return nil, false
	}
	defer lc.mu.Unlock()
	switch {
	case lc.cur != nil && lc.cur.Epoch == epoch:
	case lc.pending != nil && lc.pending.Epoch == epoch:
	default:
		return nil, false
	}
	t := lc.cur
	if t == nil || t.Epoch != epoch {
		t = lc.pending
	}
	return lc.claimLocked(t), true
}

// claimLocked moves the chat into turnFinalizing for t. Caller holds mu and has
// established that t is live.
func (lc *chatLifecycle) claimLocked(t *Turn) *Turn {
	lc.setStateLocked(turnFinalizing)
	return t
}

// finish publishes a claimed turn's result and returns the chat to idle.
//
// Ordering is the contract: the result is stored and done closed before the state
// moves, so a waiter woken by the transition always finds it. It publishes on the
// turn's OWN lifecycle, since a chat forgotten mid-finalize would be handed a fresh
// one here and leave every parked waiter in turnFinalizing forever.
func (r *turnRegistry) finish(t *Turn, result vibekit.TurnResult) {
	lc := t.lc
	lc.mu.Lock()
	defer lc.mu.Unlock()
	result.Epoch = t.Epoch
	t.result = result
	close(t.done)
	if lc.cur == t {
		lc.cur = nil
	}
	// Or the NEXT prompt's turn_start binds to this dead turn.
	if lc.pending == t {
		lc.pending = nil
	}
	if t.holds > 0 {
		if lc.retained == nil {
			lc.retained = make(map[vibekit.TurnEpoch]*Turn)
		}
		lc.retained[t.Epoch] = t
	}
	// A pre-open left over from a revised binding is still owed a bracket, so the
	// chat is not idle just because the folding turn closed.
	if lc.cur != nil {
		lc.setStateLocked(turnOpen)
		return
	}
	lc.setStateLocked(turnIdle)
}

// turnLocked finds the record for one epoch, open or retained. Caller holds mu.
func (lc *chatLifecycle) turnLocked(epoch vibekit.TurnEpoch) *Turn {
	if lc.cur != nil && lc.cur.Epoch == epoch {
		return lc.cur
	}
	if lc.pending != nil && lc.pending.Epoch == epoch {
		return lc.pending
	}
	return lc.retained[epoch]
}

// release gives up one completion handle, dropping a finalized record once its
// last handle goes. An OPEN turn's record is dropped by finish, not here.
func (r *turnRegistry) release(chatID vibekit.ChatID, epoch vibekit.TurnEpoch) {
	lc := r.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	t := lc.turnLocked(epoch)
	if t == nil {
		return
	}
	t.holds--
	if t.holds <= 0 {
		delete(lc.retained, epoch)
	}
}

// await blocks until the turn named by epoch has finalized and returns its result,
// or reports ErrNoSuchTurn for an epoch this chat has no record of. The done channel
// is taken under the mutex and waited on with it RELEASED, or the waiter blocks the
// finalize it waits for; close-before-receive publishes finish's write.
func (r *turnRegistry) await(ctx context.Context, chatID vibekit.ChatID, epoch vibekit.TurnEpoch) (vibekit.TurnResult, error) {
	lc := r.lifecycleFor(chatID)
	lc.mu.Lock()
	t := lc.turnLocked(epoch)
	lc.mu.Unlock()
	if t == nil {
		return vibekit.TurnResult{}, vibekit.ErrNoSuchTurn
	}
	select {
	case <-t.done:
		return t.result, nil
	case <-ctx.Done():
		return vibekit.TurnResult{}, ctx.Err()
	}
}

// openedAfter reports whether any turn on this chat was opened after epoch: the
// STRUCTURAL clause of the empty-turn gate, since a mis-bound pre-open satisfies
// every other clause. Read when the caller decides, never stamped on the result —
// the later turn frequently opens after the awaited one finalized.
func (r *turnRegistry) openedAfter(chatID vibekit.ChatID, epoch vibekit.TurnEpoch) bool {
	lc := r.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return lc.nextEpoch > epoch
}

// openEpoch reports the chat's open turn's epoch, or false when none is open.
// turnFinalizing answers false, unlike openTurns: this one is asked by a caller
// about to ACT on the turn, and a claimed turn's effects are already running.
func (r *turnRegistry) openEpoch(chatID vibekit.ChatID) (vibekit.TurnEpoch, bool) {
	lc := r.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.state != turnOpen || lc.cur == nil {
		return 0, false
	}
	return lc.cur.Epoch, true
}

// stepTurnEpoch reports the epoch of an open turn a workflow STEP opened, and false
// when this chat's open turn is anything else. It is the read a run's TERMINAL
// transition acts on: a step's own turn_end is dropped by the attribution gate, so
// the run ending is the only thing left that can close such a turn.
//
// NOT displaceableEngineTurn, which also matches TurnSourceWireTurnStart: closing
// the chat's OWN agent-initiated turn because some run ended is a different defect,
// since that turn's bracket is still coming. turnFinalizing answers false.
func (r *turnRegistry) stepTurnEpoch(chatID vibekit.ChatID) (vibekit.TurnEpoch, bool) {
	lc, ok := r.lookup(chatID)
	if !ok {
		return 0, false
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.state != turnOpen || lc.cur == nil {
		return 0, false
	}
	if lc.cur.Source != vibekit.TurnSourceWorkflowStep {
		return 0, false
	}
	return lc.cur.Epoch, true
}

// currentEpoch reports which turn a chat's activity belongs to right now — the turn
// that is open, or the one finalizing — and false when the chat is idle. Wider than
// openEpoch on purpose: a turn whose effects are still running is still the turn that
// spawned a process.
func (r *turnRegistry) currentEpoch(chatID vibekit.ChatID) (vibekit.TurnEpoch, bool) {
	lc := r.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	facts, open := lc.openFactsLocked()
	return facts.Epoch, open
}

// hasOpenTurn reports whether this chat has a turn the RECORD does not yet describe.
// The in-flight reply lives in an in-memory buffer appended to the chat file once at
// turn end, so `GET /api/chats/{id}` carries no carrier for it and a reader would
// answer `unknown` for a running turn.
//
// turnFinalizing counts as OPEN, exactly the state this exists for. A PRIME turn
// answers false, matching replayTurnState — it persists no carrier, so it makes no
// record provisional. The read goes through `lookup`: this one is an HTTP read path.
func (r *turnRegistry) hasOpenTurn(chatID vibekit.ChatID) bool {
	lc, ok := r.lookup(chatID)
	if !ok {
		return false
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	facts, open := lc.openFactsLocked()
	return open && facts.Source != vibekit.TurnSourcePrime
}

// openTurnFacts is what a chat's open turn IS, taken in ONE acquisition: two reads
// can straddle a finalize and pair turn N's epoch with turn N+1's buffer.
type openTurnFacts struct {
	// Buf is snapshotted by the caller AFTER the mutex is released — the buffer
	// guards itself, and lc.mu is never held across it.
	Buf    *buffer.Buffer
	Epoch  vibekit.TurnEpoch
	Source vibekit.TurnOpenSource
}

// openFactsLocked reports the chat's open turn, or false when none is. Caller holds
// mu. turnFinalizing counts as OPEN: a client told the chat is idle would then get a
// turn_ended it was never sent.
func (lc *chatLifecycle) openFactsLocked() (openTurnFacts, bool) {
	if lc.state == turnIdle || lc.cur == nil {
		return openTurnFacts{}, false
	}
	return openTurnFacts{Buf: lc.cur.Buf, Epoch: lc.cur.Epoch, Source: lc.cur.Source}, true
}

// openTurns returns the open turn of every chat that has one, so a connect replay
// reads the turn rather than the prompt slot, which is empty for every turn vibekit
// did not prompt.
func (r *turnRegistry) openTurns() map[vibekit.ChatID]openTurnFacts {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[vibekit.ChatID]openTurnFacts, len(r.chats))
	for id, lc := range r.chats {
		lc.mu.Lock()
		facts, open := lc.openFactsLocked()
		lc.mu.Unlock()
		if open {
			out[id] = facts
		}
	}
	return out
}

// interrupt records why the turn named by epoch was interrupted, first cause wins.
// Epoch-scoped so a cause cannot land on a turn it did not describe; first-wins so a
// user cancel and the tool-use filter firing in one window do not relabel each other.
func (r *turnRegistry) interrupt(chatID vibekit.ChatID, epoch vibekit.TurnEpoch, cause vibekit.InterruptCause) bool {
	lc := r.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	t := lc.cur
	if t == nil || t.Epoch != epoch || t.Interrupt != "" {
		return false
	}
	t.Interrupt = cause
	return true
}

// interruptCause reads a claimed turn's cause, under the turn's OWN lifecycle:
// re-resolving one by chat id would read the field under a different mutex than the
// writer's.
func (r *turnRegistry) interruptCause(t *Turn) vibekit.InterruptCause {
	lc := t.lc
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return t.Interrupt
}

// recordCarrier remembers which persisted message carries t's outcome, under the
// turn's OWN lifecycle for interruptCause's reason. Called by the closer that WON
// before finish publishes, which orders it ahead of a loser's read: a loser's claim
// parks in awaitNotFinalizing until that publish.
func (r *turnRegistry) recordCarrier(t *Turn, carrier turnCarrier) {
	lc := t.lc
	lc.mu.Lock()
	defer lc.mu.Unlock()
	t.carrier = carrier
}

// carrierOf reports the carrier one epoch's closer recorded, reporting false when
// this chat has no record of that epoch or that turn wrote none. It reaches a
// RETAINED record as well as an open one, which is what makes it answerable at all:
// the winner has already finalized by the time a loser asks.
func (r *turnRegistry) carrierOf(chatID vibekit.ChatID, epoch vibekit.TurnEpoch) (turnCarrier, bool) {
	lc := r.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	t := lc.turnLocked(epoch)
	if t == nil || t.carrier.MessageID == "" {
		return turnCarrier{}, false
	}
	return t.carrier, true
}

// stageTurnSummary adds one turn_completion frame's duration to the chat's open turn
// and reports the total plus whether this was the FIRST summary for it, so several
// frames for one turn sum and count as one conversation turn. With no turn open the
// frame stands alone.
func (r *turnRegistry) stageTurnSummary(chatID vibekit.ChatID, elapsedMs float64) (total float64, first bool) {
	lc := r.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	t := lc.cur
	if t == nil {
		return elapsedMs, true
	}
	t.stagedElapsedMs += elapsedMs
	t.stagedSummaries++
	return t.stagedElapsedMs, t.stagedSummaries == 1
}
