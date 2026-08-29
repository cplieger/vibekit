package agent

// command role implementation methods for Runtime.
// These expose Runtime internals to the command package handlers.

import (
	"context"
	"time"

	"github.com/cplieger/vibekit/internal/command"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// Runtime satisfies each of the command package's role interfaces; the assertions
// are the ones the Roles literal in command_dispatch.go already forces, so they
// are not repeated here. There is no assertion for an envelope seam any more:
// the dispatcher takes no collaborator at all now that idempotency is the
// header middleware's.

// bridgeRole adapts the bridge coordinator to command.BridgeAccess.
//
// The interface returns command.Bridge; the coordinator returns *sharedBridge.
// Go has no covariant returns, so something must convert, and the conversion is
// not free: a nil *sharedBridge assigned to a command.Bridge produces a NON-NIL
// interface holding a nil pointer, so each method below checks before returning.
// That is the same trap requirePopulated exists for, and it is why this cannot
// just be `Bridges: rt.coord`.
//
// A named type rather than five methods on Runtime, which is where they were.
// The runtime does not own bridges — it was a name in the path performing a type
// conversion, which is the shape the rest of this pass removed everywhere else.
// Now the adaptation is one declaration whose only job is the adaptation, and
// Runtime advertises five fewer operations it does not own.
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
func (b bridgeRole) OpenBridge(ctx context.Context, chatID vibekit.ChatID, model string) (command.Bridge, error) {
	sb, err := b.coord.OpenBridge(ctx, chatID, model)
	if err != nil {
		return nil, err
	}
	return sb, nil
}

// CloseBridge tears down the bridge for a chat.
func (b bridgeRole) CloseBridge(chatID vibekit.ChatID) { b.coord.CloseBridge(chatID) }

// PrimeIfNeeded primes the chat's session with history if it needs it.
func (b bridgeRole) PrimeIfNeeded(ctx context.Context, chatID vibekit.ChatID) {
	b.coord.PrimeIfNeeded(ctx, chatID)
}

// PrimeFromChat notes that a chat's first session should be primed with another
// chat's transcript — the tangent's fork-refused fallback.
func (b bridgeRole) PrimeFromChat(chatID, sourceChatID vibekit.ChatID) {
	b.coord.PrimeFromChat(chatID, sourceChatID)
}

// DeleteChatState tears down all in-memory state for a chat being permanently
// deleted, cancelling its runs first and reaping its durable KAS session too.
// The record-first delete path's grade: the chat file still exists, so the run
// cancel and the reap both resolve the session chain off the record.
func (rt *Runtime) DeleteChatState(ctx context.Context, chatID vibekit.ChatID) {
	rt.runs.CancelForChat(ctx, chatID)
	rt.cleanupChatState(ctx, chatID, true)
}

// DeleteChatStateByChain is DeleteChatState for a chat whose RECORD is already
// gone: the close escalation deletes the record inside its commit and tears
// down afterwards, so everything the record used to answer — which runs are
// the chat's, which KAS sessions hold its history — travels in as the chain
// captured before the commit. Nothing here re-reads the record; the
// record-reading forms (CancelForChat, reapChatSession) would silently no-op.
func (rt *Runtime) DeleteChatStateByChain(ctx context.Context, chatID vibekit.ChatID, sessionChain []string) {
	rt.runs.CancelForSessions(ctx, chatID, sessionChain)
	rt.cleanupChatState(ctx, chatID, false)
	rt.reapSessions(sessionChain)
}

// CloseChatState tears down a chat's in-memory state WITHOUT touching its
// durable KAS session.
//
// This is the close path, and the distinction is the whole point: closing a tab
// kills the process, not the history. Close used to share the delete path, so
// the × on a tab reaped the chat's whole session chain off disk — which
// contradicted its own contract ("the chat RECORD is untouched … reopening it
// session/loads everything back") and cost the user the transcript twice over:
// the reopened chat had no session to load, and the History page, which lists
// KAS's sessions, could only ever show chats that were still open.
func (rt *Runtime) CloseChatState(ctx context.Context, chatID vibekit.ChatID) {
	rt.runs.CancelForChat(ctx, chatID)
	rt.cleanupChatState(ctx, chatID, false)
}

// StartTurn opens the chat's turn at bridge-ready, immediately before the call
// that drives it, returning the epoch the caller holds a completion handle on.
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

// SettleTurnOnResponse closes the turn on the response that settled it — once the
// folder has consumed everything queued behind that response, and only if the
// wire's own turn_end did not close it first.
//
// It no longer advances the terminal registry's boundary, and neither does any
// other caller: the WINNING closer publishes it from inside finalizeTurn
// (BridgeCoordinator.onTurnClosed). A wrapper here could only speak for the
// prompt path, which is exactly why an agent-initiated turn's terminals used to
// stay attributed to the next prompted turn.
func (rt *Runtime) SettleTurnOnResponse(ctx context.Context, chatID vibekit.ChatID, epoch vibekit.TurnEpoch, seq uint64, resp *vibekit.RPCResponse) {
	rt.coord.SettleTurnOnResponse(ctx, chatID, epoch, seq, resp)
}

// TurnOpenedAfter reports whether any turn on the chat opened after epoch.
func (rt *Runtime) TurnOpenedAfter(chatID vibekit.ChatID, epoch vibekit.TurnEpoch) bool {
	return rt.coord.TurnOpenedAfter(chatID, epoch)
}

// AdmissionHolderSource reports who holds the chat's admission slot: the open
// turn when one is open, else the reservation. The steer refusal reads the
// prime window off it, and the prompt-refusal arm keys on it.
func (rt *Runtime) AdmissionHolderSource(chatID vibekit.ChatID) (vibekit.TurnOpenSource, bool) {
	return rt.coord.AdmissionHolderSource(chatID)
}

// FinalizeLocalShellTurn closes a `!cmd` turn vibekit ran itself.
func (rt *Runtime) FinalizeLocalShellTurn(ctx context.Context, chatID vibekit.ChatID, epoch vibekit.TurnEpoch) {
	rt.coord.FinalizeLocalShellTurn(ctx, chatID, epoch)
}

// AbandonInFlightTurn finalizes a turn that failed before it could end. The
// terminal boundary rides the finalizer, as SettleTurnOnResponse records.
func (rt *Runtime) AbandonInFlightTurn(ctx context.Context, chatID vibekit.ChatID, epoch vibekit.TurnEpoch, reason string) {
	rt.coord.AbandonInFlightTurn(ctx, chatID, epoch, reason)
}
