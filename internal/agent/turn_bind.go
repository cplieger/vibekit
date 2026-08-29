package agent

// Provisional acknowledgement: binding a wire turn_start to a pre-opened turn,
// and revising it when a frame proves it wrong. vibekit cannot tell a prompted
// turn_start from an agent-initiated one — `agentInitiated` rides content frames,
// never the bracket — so a binding must be revisable.

import (
	"context"
	"log/slog"

	"github.com/cplieger/vibekit/internal/buffer"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// bindPending binds an unacknowledged wire turn_start to this chat's single
// pending pre-open, reporting whether one took it and, when it did not, the epoch
// of a DIFFERENT turn the caller must close first — cur cannot express two folding
// targets. The pre-open leaves the pending set, so a second start cannot bind the
// same turn, and it BECOMES the folding turn: after a revised binding it is
// referenced by pending alone, so clearing pending alone orphans that record.
func (r *turnRegistry) bindPending(chatID vibekit.ChatID) (bound bool, displaced vibekit.TurnEpoch) {
	lc := r.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	p := lc.pending
	if p == nil || p.acked {
		return false, 0
	}
	if lc.cur != nil && lc.cur != p {
		return false, lc.cur.Epoch
	}
	p.acked = true
	lc.pending = nil
	if lc.cur == nil {
		lc.cur = p
		lc.setStateLocked(turnOpen)
	}
	return true, 0
}

// displaceableWireTurn reports the epoch of an open turn the WIRE started, which a
// prompt-shaped open must close before taking the chat. A prompt source CAN meet
// one: a wireTurnStart turn holds no prompt slot, so admission control never
// refused it, and no closer can claim it — displacing it without closing it loses
// content already broadcast to every client. Only a wire-started turn: a local
// shell turn already refuses to begin while a turn is open.
func (r *turnRegistry) displaceableWireTurn(chatID vibekit.ChatID) (vibekit.TurnEpoch, bool) {
	lc := r.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.state != turnOpen || lc.cur == nil {
		return 0, false
	}
	if lc.cur.Source != vibekit.TurnSourceWireTurnStart {
		return 0, false
	}
	return lc.cur.Epoch, true
}

// foldTarget is the open turn's buffer, or false when the chat has none — the fast
// path for every folded frame, answering without the chat-store read an open needs.
// False also for a chat mid-finalize, so the caller falls through to openWire
// rather than folding into a turn whose closer already took its content.
func (r *turnRegistry) foldTarget(chatID vibekit.ChatID) (*buffer.Buffer, bool) {
	lc := r.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.state != turnOpen || lc.cur == nil {
		return nil, false
	}
	return lc.cur.Buf, true
}

// openWire opens a turn the WIRE started: a turn_start with nothing pending to
// bind, or a fold arriving with no turn open at all. It takes no completion
// handle — nothing awaits a turn it did not open, and a handle nobody releases
// retains the record for the life of the process.
func (r *turnRegistry) openWire(ctx context.Context, chatID vibekit.ChatID, model string, credits CreditBaseline) *Turn {
	lc := r.lifecycleFor(chatID)
	if !lc.awaitNotFinalizing(ctx) {
		return nil
	}
	defer lc.mu.Unlock()
	if lc.state == turnOpen && lc.cur != nil {
		return lc.cur
	}
	t := lc.openLocked(chatID, vibekit.TurnSourceWireTurnStart, model, credits)
	t.acked = true
	return t
}

// reclassify undoes a provisional binding on the evidence that the started turn
// was the AGENT's, and reports whether it did. The started turn keeps the buffer
// the frames were folded into; the pre-open drops back to pending with a fresh
// one. The agent's turn takes a LATER epoch, which the empty-turn gate reads.
func (r *turnRegistry) reclassify(ctx context.Context, chatID vibekit.ChatID) bool {
	lc := r.lifecycleFor(chatID)
	if !lc.awaitNotFinalizing(ctx) {
		return false
	}
	defer lc.mu.Unlock()
	pre := lc.cur
	if pre == nil || !pre.acked || !pre.Source.Acknowledgeable() {
		return false
	}
	agentTurn := lc.openLocked(chatID, vibekit.TurnSourceWireTurnStart, pre.Model, pre.Credits)
	agentTurn.acked = true
	// Opened moves with the buffer: the agent's turn began when those frames did,
	// not when this frame revealed it.
	agentTurn.Buf = pre.Buf
	agentTurn.Opened = pre.Opened
	// Publication policy follows the turn that now owns the buffer: a mis-bound
	// PRIME withheld frames that are the AGENT's, so the taker may publish them.
	agentTurn.Buf.SetMuted(false)
	pre.Buf = buffer.New()
	pre.Buf.SetMuted(pre.Source == vibekit.TurnSourcePrime)
	pre.acked = false
	// Back into the pending set: the pre-open is owed its own bracket again.
	lc.pending = pre
	slog.Info("a frame revised a provisional turn binding to agent-initiated",
		"chat_id", chatID, "pre_open_epoch", pre.Epoch, "agent_epoch", agentTurn.Epoch)
	return true
}
