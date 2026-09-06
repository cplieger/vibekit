package agent

// A binding must be revisable: `agentInitiated` rides content frames, never the
// bracket, so a prompted turn_start cannot be told from an agent-initiated one.

import (
	"context"
	"log/slog"

	"github.com/cplieger/vibekit/internal/buffer"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// bindPending binds an unacknowledged wire turn_start to this chat's single pending
// pre-open, reporting whether one took it and, when it did not, the epoch of a
// DIFFERENT turn the caller must close first. The pre-open leaves the pending set and
// BECOMES the folding turn, so clearing pending alone would orphan that record.
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

// displaceableEngineTurn reports the epoch of an open turn the ENGINE started, which a
// prompt-shaped open must close before taking the chat: no closer can claim it through
// the BRACKET path, and displacing it without closing loses content already broadcast
// to every client. A workflow step's turn is the ordinary case rather than an edge — a
// chat-parented run's steps fold onto the launching chat for minutes after the
// launching turn ended.
func (r *turnRegistry) displaceableEngineTurn(chatID vibekit.ChatID) (vibekit.TurnEpoch, bool) {
	lc := r.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.state != turnOpen || lc.cur == nil {
		return 0, false
	}
	if !lc.cur.Source.EngineOpened() {
		return 0, false
	}
	return lc.cur.Epoch, true
}

// foldTarget is the open turn's buffer, or false when the chat has none. False also for
// a chat mid-finalize, so the caller falls through to openWire rather than folding into
// a turn whose closer already took its content.
func (r *turnRegistry) foldTarget(chatID vibekit.ChatID) (*buffer.Buffer, bool) {
	lc := r.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.state != turnOpen || lc.cur == nil {
		return nil, false
	}
	return lc.cur.Buf, true
}

// openWire opens a turn the ENGINE started. It takes no completion handle — a handle
// nobody releases retains the record for the life of the process. The source is the
// caller's: a fold carrying a workflow step's own marker opens the RUN's turn, and
// everything else opens this chat's.
func (r *turnRegistry) openWire(ctx context.Context, chatID vibekit.ChatID, source vibekit.TurnOpenSource, model string, credits CreditBaseline) *Turn {
	lc := r.lifecycleFor(chatID)
	if !lc.awaitNotFinalizing(ctx) {
		return nil
	}
	defer lc.mu.Unlock()
	if lc.state == turnOpen && lc.cur != nil {
		return lc.cur
	}
	t := lc.openLocked(chatID, source, model, credits)
	t.acked = true
	return t
}

// reclassify undoes a provisional binding on the evidence that the started turn was the
// AGENT's. The started turn keeps the buffer the frames were folded into; the pre-open
// drops back to pending with a fresh one, and the agent's turn takes a LATER epoch.
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
	// The agent's OWN turn, never a step's: a step's marker routes it elsewhere.
	agentTurn := lc.openLocked(chatID, vibekit.TurnSourceWireTurnStart, pre.Model, pre.Credits)
	agentTurn.acked = true
	// Opened moves with the buffer: the agent's turn began when those frames did.
	agentTurn.Buf = pre.Buf
	agentTurn.Opened = pre.Opened
	// A mis-bound PRIME withheld frames that are the AGENT's, so the taker may publish them.
	agentTurn.Buf.SetMuted(false)
	pre.Buf = buffer.New()
	pre.Buf.SetMuted(pre.Source == vibekit.TurnSourcePrime)
	pre.acked = false
	lc.pending = pre
	slog.Info("a frame revised a provisional turn binding to agent-initiated",
		"chat_id", chatID, "pre_open_epoch", pre.Epoch, "agent_epoch", agentTurn.Epoch)
	return true
}
