package agent

import (
	"context"
	"time"

	"github.com/cplieger/vibekit/internal/command"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// bridgeRole adapts the bridge coordinator to command.BridgeAccess.
//
// A named type rather than methods directly on Runtime: the coordinator
// returns *sharedBridge and the interface wants command.Bridge, and a nil
// *sharedBridge assigned there is a non-nil interface holding a nil pointer —
// each method below checks before returning to avoid that trap.
type bridgeRole struct{ coord *BridgeCoordinator }

// Bridge returns the active bridge for a chat, or nil.
func (b bridgeRole) Bridge(chatID vibekit.ChatID) command.Bridge {
	sb := b.coord.Bridge(chatID)
	if sb == nil {
		return nil
	}
	return sb
}

// OpenBridge ensures a bridge exists for the chat.
//
// The nil check is the trap this type's doc comment names, and OpenBridge used
// to be the one method missing it: a nil *sharedBridge returned as
// command.Bridge is a NON-nil interface, so a caller's `bridge == nil` guard
// reads false and the first method call panics.
func (b bridgeRole) OpenBridge(ctx context.Context, chatID vibekit.ChatID, model string) (command.Bridge, error) {
	sb, err := b.coord.OpenBridge(ctx, chatID, model)
	if err != nil || sb == nil {
		return nil, err
	}
	return sb, nil
}

// AwaitReplayAdopted waits for a session/load replay this chat may have in
// flight to be adopted into the record. A non-nil error means the caller must
// not rewrite the transcript.
func (b bridgeRole) AwaitReplayAdopted(ctx context.Context, chatID vibekit.ChatID) error {
	return b.coord.AwaitReplayAdopted(ctx, chatID)
}

// CloseBridge tears down the bridge for a chat.
func (b bridgeRole) CloseBridge(chatID vibekit.ChatID) { b.coord.CloseBridge(chatID) }

// PrimeIfNeeded primes the chat's session with history if it needs it.
func (b bridgeRole) PrimeIfNeeded(ctx context.Context, chatID vibekit.ChatID) {
	b.coord.PrimeIfNeeded(ctx, chatID)
}

// PrimeFromChat notes that a chat's first session should be primed with
// another chat's transcript — the tangent's fork-refused fallback.
func (b bridgeRole) PrimeFromChat(chatID, sourceChatID vibekit.ChatID) {
	b.coord.PrimeFromChat(chatID, sourceChatID)
}

// DeleteChatState tears down all in-memory state for a chat being permanently
// deleted, cancelling its runs and reaping its durable KAS session.
func (rt *Runtime) DeleteChatState(ctx context.Context, chatID vibekit.ChatID) {
	rt.runs.CancelForChat(ctx, chatID)
	rt.cleanupChatState(ctx, chatID, true)
}

// DeleteChatStateByChain is DeleteChatState for a chat whose record is
// already gone: the close escalation deletes the record inside its commit,
// so the session chain is captured beforehand rather than re-read from a
// record that would silently no-op.
func (rt *Runtime) DeleteChatStateByChain(ctx context.Context, chatID vibekit.ChatID, sessionChain []string) {
	rt.runs.CancelForSessions(ctx, chatID, sessionChain)
	rt.cleanupChatState(ctx, chatID, false)
	rt.reapSessions(sessionChain)
}

// CloseChatState tears down a chat's in-memory state WITHOUT touching its
// durable KAS session, so the chat record survives and reopening it
// session/loads the history back.
func (rt *Runtime) CloseChatState(ctx context.Context, chatID vibekit.ChatID) {
	rt.runs.CancelForChat(ctx, chatID)
	rt.cleanupChatState(ctx, chatID, false)
}

// StartTurn opens the chat's turn at bridge-ready, immediately before the
// call that drives it, returning the epoch the caller holds a completion
// handle on.
func (rt *Runtime) StartTurn(ctx context.Context, chatID vibekit.ChatID, source vibekit.TurnOpenSource) vibekit.TurnEpoch {
	return rt.coord.StartTurn(ctx, chatID, source)
}

// ReserveTurnForPrompt takes the chat's admission slot for a prompt, waiting up
// to wait while it is held, and keys a refusal on the holder's source.
func (rt *Runtime) ReserveTurnForPrompt(ctx context.Context, chatID vibekit.ChatID, wait time.Duration) command.AdmissionOutcome {
	return rt.coord.ReserveTurnForPrompt(ctx, chatID, wait)
}

// TryReserveTurn takes the chat's admission slot iff it is free.
func (rt *Runtime) TryReserveTurn(chatID vibekit.ChatID, source vibekit.TurnOpenSource) bool {
	return rt.coord.TryReserveTurn(chatID, source)
}

// ReleaseTurnReservation frees the chat's admission slot and wakes waiters.
func (rt *Runtime) ReleaseTurnReservation(chatID vibekit.ChatID) {
	rt.coord.ReleaseTurnReservation(chatID)
}

// AwaitTurn blocks until the named turn has finalized and reports what it did.
func (rt *Runtime) AwaitTurn(ctx context.Context, chatID vibekit.ChatID, epoch vibekit.TurnEpoch) (vibekit.TurnResult, error) {
	return rt.coord.AwaitTurn(ctx, chatID, epoch)
}

// ReleaseTurn gives up the completion handle StartTurn issued.
func (rt *Runtime) ReleaseTurn(chatID vibekit.ChatID, epoch vibekit.TurnEpoch) {
	rt.coord.ReleaseTurn(chatID, epoch)
}

// SettleTurnOnResponse closes the turn on the response that settled it, once
// everything queued behind that response has been consumed, and only if the
// wire's own turn_end did not close it first.
func (rt *Runtime) SettleTurnOnResponse(ctx context.Context, chatID vibekit.ChatID, epoch vibekit.TurnEpoch, seq uint64, resp *vibekit.RPCResponse) {
	rt.coord.SettleTurnOnResponse(ctx, chatID, epoch, seq, resp)
}

// TurnOpenedAfter reports whether any turn on the chat opened after epoch.
func (rt *Runtime) TurnOpenedAfter(chatID vibekit.ChatID, epoch vibekit.TurnEpoch) bool {
	return rt.coord.TurnOpenedAfter(chatID, epoch)
}

// AdmissionHolderSource reports who holds the chat's admission slot: the
// open turn when one is open, else the reservation.
func (rt *Runtime) AdmissionHolderSource(chatID vibekit.ChatID) (vibekit.TurnOpenSource, bool) {
	return rt.coord.AdmissionHolderSource(chatID)
}

// FinalizeLocalShellTurn closes a `!cmd` turn vibekit ran itself.
func (rt *Runtime) FinalizeLocalShellTurn(ctx context.Context, chatID vibekit.ChatID, epoch vibekit.TurnEpoch) {
	rt.coord.FinalizeLocalShellTurn(ctx, chatID, epoch)
}

// AbandonInFlightTurn finalizes a turn that failed before it could end.
func (rt *Runtime) AbandonInFlightTurn(ctx context.Context, chatID vibekit.ChatID, epoch vibekit.TurnEpoch, reason string) {
	rt.coord.AbandonInFlightTurn(ctx, chatID, epoch, reason)
}
