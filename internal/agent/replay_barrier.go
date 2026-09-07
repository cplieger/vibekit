package agent

// The barrier a caller waits on before it REWRITES a chat's transcript:
// mergeProjection returns a settled replay's messages wholesale, so a
// truncation the swap lands after has undone nothing.

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// replayAdoptBudget bounds the wait, because the caller's context cannot: a
// settle can be slow to RUN rather than slow to be noticed — a bridge that dies
// mid-replay, an RPC that never returns, a swap blocked on a chat-file write.
const replayAdoptBudget = 45 * time.Second

// ErrReplayNotAdopted reports that replayAdoptBudget ran out. A diagnosis, not
// user-facing prose.
var ErrReplayNotAdopted = errors.New("session/load replay not adopted within the barrier budget")

// AwaitReplayAdopted blocks until a replay this chat may have in flight has been
// adopted into the record. Nil when there is none.
//
// A non-nil error means DO NOT REWRITE: ErrReplayNotAdopted for the budget, the
// caller's own context error otherwise.
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
		// Cause carries our sentinel for the budget and the caller's for a cancel.
		err := context.Cause(waitCtx)
		slog.Warn("replay adoption barrier expired",
			"chat_id", chatID, "error", err, "budget", replayAdoptBudget)
		return err
	}
}
