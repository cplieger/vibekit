package agent

// The replay-adoption barrier a caller waits on before it REWRITES a chat's
// transcript. `mergeProjection` returns a settled replay's messages wholesale,
// so a truncation the swap lands after has undone nothing. Mechanism and the
// completion signal: load_projection.go's header.

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// replayAdoptBudget bounds the wait, because the caller's context cannot.
//
// It no longer covers a MISSING trigger — MarkReplayLoadedAt settles a replay that
// drained early (replay_drain.go). What survives is everything that stops the settle
// from RUNNING rather than from being noticed: a bridge that dies mid-replay, an RPC
// that never returns, a swap blocked on a chat-file write, a projection superseded by
// a reload whose own settle has to finish first. Sized like the package's other
// per-call budgets; a normal settle is a frame drain plus one chat-file write.
const replayAdoptBudget = 45 * time.Second

// ErrReplayNotAdopted reports that the budget above ran out. A diagnosis for the
// caller's own refusal, not user-facing prose.
var ErrReplayNotAdopted = errors.New("session/load replay not adopted within the barrier budget")

// AwaitReplayAdopted blocks until a replay this chat may have in flight has been
// adopted into the record. Nil when there is none, which is every chat that
// already had a live bridge.
//
// A non-nil error means DO NOT REWRITE: ErrReplayNotAdopted for the budget, the
// caller's own context error otherwise. The split exists so a caller can tell a
// refusal worth reporting from a client that stopped listening.
func (bc *BridgeCoordinator) AwaitReplayAdopted(ctx context.Context, chatID vibekit.ChatID) error {
	if bc.replayProjection == nil {
		return nil
	}
	settled := bc.replayProjection.ReplaySettled(chatID)
	// The common case: nothing open, or already adopted. No timer for it.
	select {
	case <-settled:
		return nil
	default:
	}

	waitCtx, cancel := context.WithTimeoutCause(ctx, replayAdoptBudget, ErrReplayNotAdopted)
	defer cancel()
	select {
	case <-settled:
		return nil
	case <-waitCtx.Done():
		// Cause carries our sentinel for the budget and the caller's for a
		// cancel, so one read separates them.
		err := context.Cause(waitCtx)
		slog.Warn("replay adoption barrier expired",
			"chat_id", chatID, "error", err, "budget", replayAdoptBudget)
		return err
	}
}
