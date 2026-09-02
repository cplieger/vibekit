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
	// effortHealed latches the one reactive reasoning-effort repair this bridge
	// allows (BridgeCoordinator.healEffort). The repair asserts a level and KAS
	// answers with another config_option_update, so an unbounded reactive repair
	// is a loop; one bool removes it, and the prompt-time repairEffort stays the
	// checkpoint for a level that keeps moving back.
	effortHealed bool
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

// startedPastSpawn reports whether the bridge has finished starting. The
// admission arm's "live bridge" is this, not map presence: the manager
// registers the record before Start so concurrent opens coalesce.
func (sb *sharedBridge) startedPastSpawn() bool {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.state != bridgeStarting
}

// setState publishes a lifecycle transition. The spawn used to write the field
// bare — safe while nothing could hold the bridge before OpenBridge returned —
// but the admission arm reads liveness DURING the spawn, so the write takes
// the lock like every other state transition.
func (sb *sharedBridge) setState(s bridgeState) {
	sb.mu.Lock()
	sb.state = s
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
// four for free. The narrowing is the point: ACPBridge also carries Start,
// Stop, NotifCh and SetModel, which only sharedBridge's state machine may
// drive. Embedding would hand every holder of a command.Bridge the ability to
// Stop the bridge behind that state machine's back.

func (sb *sharedBridge) Call(ctx context.Context, method string, params any) (*vibekit.RPCResponse, error) {
	return sb.bridge.Call(ctx, method, params)
}

func (sb *sharedBridge) CallAt(ctx context.Context, method string, params any) (*vibekit.RPCResponse, uint64, error) {
	return sb.bridge.CallAt(ctx, method, params)
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

// cancelPromptCall trips the in-flight prompt's context so the blocked Call
// returns and the ordinary failure path finalizes the turn. Reports whether
// there was a call to trip.
//
// It no longer records WHY: the cause lives on the turn record, epoch-scoped and
// first-wins, so this answers only the bridge's half of an interruption. Refuses
// when the chat is not prompting or no prompt context is registered.
func (sb *sharedBridge) cancelPromptCall() bool {
	sb.mu.Lock()
	if sb.state != bridgePrompting || sb.promptCancel == nil {
		sb.mu.Unlock()
		return false
	}
	cancel := sb.promptCancel
	sb.mu.Unlock()
	// Outside the lock: cancel runs arbitrary registered funcs, and holding a
	// bridge's mutex across them would put this goroutine's ordering inside
	// someone else's callback.
	cancel()
	return true
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

// claimEffortHeal reports whether the caller won the one reactive reasoning-effort
// repair this bridge allows, setting the latch if so. Test-and-set under one lock,
// like claimPriming: the two callers of a split IsHealed/SetHealed pair would sit
// back to back, so the gap between them would be a window with no purpose.
func (sb *sharedBridge) claimEffortHeal() bool {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	if sb.effortHealed {
		return false
	}
	sb.effortHealed = true
	return true
}
