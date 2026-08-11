package hub

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/cplieger/vibekit/internal/api"
)

// bridgeState represents the lifecycle state of a sharedBridge.
type bridgeState int

const (
	bridgeIdle      bridgeState = iota // ready for a new prompt
	bridgeStarting                     // bridge.Start in progress (held during getOrCreateBridge)
	bridgePrompting                    // prompt in flight
)

// Compile-time assertion: sharedBridge satisfies api.CommandBridge.
var _ api.CommandBridge = (*sharedBridge)(nil)

// sharedBridge wraps an ACP bridge with the hub's per-chat state.
// The mu mutex protects field access; the state field encodes the
// lifecycle phase so callers can check "busy" via a readable state
// comparison rather than relying on TryLock as a signal.
type sharedBridge struct {
	bridge api.ACPBridge

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

// --- api.CommandBridge implementation on sharedBridge ---

func (sb *sharedBridge) Call(ctx context.Context, method string, params any) (*api.RPCResponse, error) {
	return sb.bridge.Call(ctx, method, params)
}

func (sb *sharedBridge) Notify(ctx context.Context, method string, params any) error {
	return sb.bridge.Notify(ctx, method, params)
}

func (sb *sharedBridge) Respond(ctx context.Context, requestID int64, result any, err error) error {
	return sb.bridge.Respond(ctx, requestID, result, err)
}

func (sb *sharedBridge) SessionID() api.SessionID {
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

func (sb *sharedBridge) IsPrimed() bool {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.primed
}

func (sb *sharedBridge) SetPrimed() {
	sb.mu.Lock()
	sb.primed = true
	sb.mu.Unlock()
}
