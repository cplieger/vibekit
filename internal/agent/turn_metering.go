package agent

// Per-turn accounting, split by owner: credits are the CHAT's whoever spent them
// (a workflow step's spend is the launching chat's), while the conversation turn
// count and duration are the CONVERSATION's, so a step must not move them.

import (
	"context"
	"errors"
	"log/slog"

	"github.com/cplieger/vibekit/internal/chat"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// AccumulateSpend adds a turn_completion's credit spend to the chat, step frame or
// not. Satisfies translate.TurnMetering. A zero-credit summary is skipped, since
// HasRealData is what switches the context popup from "unknown" to a figure and
// must not report a measured 0.00 the account never confirmed.
func (bc *BridgeCoordinator) AccumulateSpend(ctx context.Context, chatID vibekit.ChatID, credits float64) {
	if credits <= 0 {
		return
	}
	bc.mutateUsage(ctx, chatID, func(u *vibekit.Usage) {
		u.Credits += credits
		u.HasRealData = true
	})
}

// StageConversationTurnSummary records a CONVERSATION turn's reported duration on
// the chat's open turn and writes the accumulated result. Satisfies
// translate.TurnMetering. Several frames can describe one turn, so the duration
// accumulates and staging on the TURN is what bounds that sum and counts one
// conversation turn per turn; a turn reporting no duration leaves the previous
// measurement alone.
func (bc *BridgeCoordinator) StageConversationTurnSummary(ctx context.Context, chatID vibekit.ChatID, elapsedMs float64) {
	total, first := bc.turns.stageTurnSummary(chatID, elapsedMs)
	bc.mutateUsage(ctx, chatID, func(u *vibekit.Usage) {
		if first {
			u.TurnCount++
		}
		if total > 0 {
			u.LastTurnMs = total
		}
	})
}

// mutateUsage applies a usage write to the chat. Every write here is LATE — it
// lands after the frame that caused it, on a chat the user may already have
// deleted — so chat.ErrTombstoned is the designed outcome rather than a fault.
func (bc *BridgeCoordinator) mutateUsage(ctx context.Context, chatID vibekit.ChatID, apply func(*vibekit.Usage)) {
	err := bc.chatStore.Mutate(ctx, chatID, func(c *vibekit.Chat, exists bool) bool {
		if !exists {
			return false
		}
		apply(&c.Usage)
		return true
	})
	if err == nil || errors.Is(err, chat.ErrTombstoned) {
		return
	}
	slog.Error("persist turn metering", "chat_id", chatID, "error", err)
}
