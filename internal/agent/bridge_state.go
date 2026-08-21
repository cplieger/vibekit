package agent

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// bridgeState represents the lifecycle state of a sharedBridge.
type bridgeState int

const (
	bridgeIdle      bridgeState = iota // ready for a new prompt
	bridgeStarting                     // bridge.Start in progress (held during getOrCreateBridge)
	bridgePrompting                    // prompt in flight
)

// sharedBridge wraps an ACP bridge with the runtime's per-chat state.
// The mu mutex protects field access; the state field encodes the
// lifecycle phase so callers can check "busy" via a readable state
// comparison rather than relying on TryLock as a signal.
type sharedBridge struct {
	bridge ACPBridge

	// Unresponsive-cancel tracking. session/cancel is a NOTIFICATION, so
	// nothing acks it: the turn ends only when KAS answers the pending
	// session/prompt with a cancelled stop reason. If it never does, the
	// prompt Call blocks forever, the deferred ReleaseAfterPrompt never
	// runs, and the chat is stuck in bridgePrompting refusing new prompts.
	//
	// promptCancel is the in-flight prompt context's cancel func; tripping
	// it makes Call return ctx.Err() and lets the EXISTING prompt-failure
	// path finalize the turn. turnGen guards against tripping a LATER
	// turn: an unacked cancel whose grace expires after the turn ended and
	// a new one started must not touch the new one.
	promptCancel context.CancelFunc
	cancelTimer  *time.Timer

	// interruptReason names the cause when this turn is being ended by
	// interruptTurn rather than by the user or by a failed call. Read once by the
	// teardown, which puts it on the transcript's divider. Empty means the turn
	// ended for one of the ordinary causes, so an ordinary cause is the default
	// and no caller has to clear it.
	//
	// A string, so its pointer word sits inside the pointer-bearing prefix above
	// for govet fieldalignment — same constraint as primeReason below.
	interruptReason string

	// primeReason is a string, so its pointer word must sit inside the
	// pointer-bearing prefix above for govet fieldalignment.
	primeReason primeReason

	// primeFrom names the chat whose transcript primes this session, when that
	// is NOT the chat the bridge belongs to. Set only on primeReasonFork: a
	// tangent whose fork was refused needs its PARENT's history, which is the one
	// case where BuildHistory must be asked about a different chat. Empty
	// everywhere else, and PrimeIfNeeded reads the bridge's own chat then.
	primeFrom vibekit.ChatID

	turnGen uint64
	mu      sync.Mutex
	state   bridgeState
	primed  bool
}

// tryAcquireForPrompt attempts to transition from idle to prompting.
// Returns true if the transition succeeded (caller owns the prompt slot).
func (sb *sharedBridge) tryAcquireForPrompt() bool {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	if sb.state != bridgeIdle {
		return false
	}
	sb.state = bridgePrompting
	sb.turnGen++
	return true
}

// releaseAfterPrompt transitions from prompting back to idle.
func (sb *sharedBridge) releaseAfterPrompt() {
	sb.mu.Lock()
	sb.state = bridgeIdle
	sb.stopCancelTimerLocked()
	sb.mu.Unlock()
}

// stopCancelTimerLocked disarms a pending cancel-grace timer. Caller holds mu.
func (sb *sharedBridge) stopCancelTimerLocked() {
	if sb.cancelTimer != nil {
		sb.cancelTimer.Stop()
		sb.cancelTimer = nil
	}
}

// --- command.Bridge implementation on sharedBridge ---
//
// Four explicit forwards, and NOT `ACPBridge` embedded, which would supply all
// four for free. The narrowing is the point: ACPBridge also carries Start, Stop,
// NotifCh and SetModel, and sharedBridge is the state machine that owns when
// those may run — the cancel-grace timer and the idle/prompting transitions above
// exist because nothing else may drive the subprocess's lifetime. Embedding would
// hand every holder of a command.Bridge the ability to Stop the bridge behind the
// state machine's back, and would silently widen this surface again each time
// ACPBridge gains a method.
//
// So a forward-hunting sweep will flag these four. They are the answer, not the
// finding.

func (sb *sharedBridge) Call(ctx context.Context, method string, params any) (*vibekit.RPCResponse, error) {
	return sb.bridge.Call(ctx, method, params)
}

func (sb *sharedBridge) Notify(ctx context.Context, method string, params any) error {
	return sb.bridge.Notify(ctx, method, params)
}

func (sb *sharedBridge) Respond(ctx context.Context, requestID int64, result any, err error) error {
	return sb.bridge.Respond(ctx, requestID, result, err)
}

func (sb *sharedBridge) SessionID() vibekit.SessionID {
	return sb.bridge.SessionID()
}

func (sb *sharedBridge) TryAcquireForPrompt() bool {
	return sb.tryAcquireForPrompt()
}

func (sb *sharedBridge) ReleaseAfterPrompt() {
	sb.releaseAfterPrompt()
}

// BeginPromptCall records the in-flight prompt's cancel func and returns the
// generation of the turn it belongs to.
func (sb *sharedBridge) BeginPromptCall(cancel context.CancelFunc) uint64 {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	sb.promptCancel = cancel
	return sb.turnGen
}

// EndPromptCall forgets the in-flight prompt's cancel func and disarms any
// pending cancel-grace timer.
func (sb *sharedBridge) EndPromptCall() {
	sb.mu.Lock()
	sb.promptCancel = nil
	sb.stopCancelTimerLocked()
	sb.mu.Unlock()
}

// PromptGeneration returns the current turn generation.
func (sb *sharedBridge) PromptGeneration() uint64 {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.turnGen
}

// ArmCancelGrace starts the unresponsive-cancel budget for the turn named by
// gen. time.AfterFunc parks no goroutine while waiting, and the timer is
// disarmed by EndPromptCall/releaseAfterPrompt on the ordinary path, so a
// cancel that IS acked costs one stopped timer and nothing else.
func (sb *sharedBridge) ArmCancelGrace(gen uint64, d time.Duration) bool {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	if sb.state != bridgePrompting || sb.turnGen != gen || sb.promptCancel == nil {
		return false
	}
	sb.stopCancelTimerLocked()
	sb.cancelTimer = time.AfterFunc(d, func() {
		// Timer.Stop does not halt a func that is already running, so the
		// decision is re-taken under the lock rather than trusted from the
		// arming moment.
		cancel, ok := sb.shouldTripCancelGrace(gen)
		if !ok {
			return
		}
		slog.Warn("cancel unacked within grace; unblocking the turn",
			"grace", d, "turn_gen", gen)
		cancel()
	})
	return true
}

// shouldTripCancelGrace reports whether an expired grace budget still applies
// to the turn it was armed for, returning that turn's prompt-cancel func.
//
// It refuses in three cases, each a turn the budget must not touch: the chat is
// no longer prompting (the turn ended), the generation moved on (this turn
// ended and ANOTHER began, so cancelling would kill work the user just
// started), or no prompt context is registered.
func (sb *sharedBridge) shouldTripCancelGrace(gen uint64) (context.CancelFunc, bool) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	if sb.state != bridgePrompting || sb.turnGen != gen || sb.promptCancel == nil {
		return nil, false
	}
	return sb.promptCancel, true
}

// interruptTurn ends the in-flight turn and records why, for a cause that is
// neither a user cancel nor a failed call: kiro-cli abandoning a turn without
// answering it (see translate/interrupt_marker.go).
//
// Refuses on the same three conditions as shouldTripCancelGrace, plus a fourth
// that makes this a ONE-SHOT per turn. Two writers can reach a turn's cancel at
// once — a user pressing Cancel and the filter firing in the same window — and
// the generation guard stops cross-turn damage without deciding which of them
// gets the attribution. First writer wins, so a reason cannot be overwritten by
// a later one, and a user cancel that got there first is not relabelled as a
// filter stop. run_bounds.go's claimTermination is the same rule at run scope,
// and it exists because a bound rewriting a deliberate stop is what it looked
// like before.
//
// Returns whether the interrupt was taken, so a caller can log the no-op rather
// than assume it landed.
func (sb *sharedBridge) interruptTurn(reason string) bool {
	sb.mu.Lock()
	if sb.state != bridgePrompting || sb.promptCancel == nil || sb.interruptReason != "" {
		sb.mu.Unlock()
		return false
	}
	sb.interruptReason = reason
	cancel := sb.promptCancel
	sb.mu.Unlock()
	// Outside the lock: cancel runs arbitrary registered funcs, and holding a
	// bridge's mutex across them would put this goroutine's ordering inside
	// someone else's callback.
	cancel()
	return true
}

// takeInterruptReason returns and clears the reason this turn was interrupted,
// or "" when it ended for any other cause.
//
// Take rather than read, because the value describes ONE turn: leaving it set
// would relabel the next turn's ordinary failure with this turn's cause. Cleared
// on the read rather than at turn start so the two orderings that matter — the
// teardown reading it, and the next turn arming its own — cannot race for it.
func (sb *sharedBridge) takeInterruptReason() string {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	reason := sb.interruptReason
	sb.interruptReason = ""
	return reason
}

// claimPriming reports whether the caller won the right to prime this session,
// setting the flag if so. Test-and-set under one lock: the two separate
// IsPrimed/SetPrimed methods this replaced were called back to back by the one
// consumer, so the gap between them was a window with no purpose.
func (sb *sharedBridge) claimPriming() bool {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	if sb.primed {
		return false
	}
	sb.primed = true
	return true
}
