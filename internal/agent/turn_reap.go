package agent

import (
	"log/slog"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// KiroCrew uses the same silence budget for upstream issue #3583.
const compactionFailedTurnBudget = 60 * time.Second

// CompactionFailed bounds a turn that may never receive its response after a
// failed compaction. Backend activity and live tools restart the silence budget.
func (bc *BridgeCoordinator) CompactionFailed(chatID vibekit.ChatID, detail string) {
	lc := bc.turns.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.state != turnOpen || lc.cur == nil {
		slog.Debug("compaction reap: no turn open", "chat_id", chatID)
		return
	}
	// The chain's origin, set here rather than in armCompactionReapLocked so it
	// survives every re-arm: it is what a live tool is judged against, and a
	// per-arm cutoff would retire a tool that has been running since the failure
	// the moment the first budget elapsed. Set once, so a second failure report
	// restarts the 60s window without narrowing that judgement — the tighter
	// direction is the one that interrupts a turn that is genuinely working.
	if lc.cur.reapChainAt.IsZero() {
		lc.cur.reapChainAt = time.Now()
	}
	bc.armCompactionReapLocked(lc, lc.cur, detail)
}

func (bc *BridgeCoordinator) armCompactionReapLocked(lc *chatLifecycle, turn *Turn, detail string) {
	lc.stopReapLocked(turn)
	turn.reapArmID++
	turn.reapArmedSeq = lc.observedSeq
	turn.reapArmedGen = lc.fwdGen
	// The callback carries value copies only. Capturing the timer for an
	// identity check races the armer's own assignment (the callback can run
	// before AfterFunc returns); the arm id is the identity, compared under
	// lc.mu like every other reap field.
	armID := turn.reapArmID
	epoch := turn.Epoch
	seq := turn.reapArmedSeq
	gen := turn.reapArmedGen
	chatID := turn.Chat
	turn.reapTimer = time.AfterFunc(compactionFailedTurnBudget, func() {
		bc.expireCompactionReap(chatID, epoch, armID, seq, gen, detail)
	})
}

func (bc *BridgeCoordinator) expireCompactionReap(chatID vibekit.ChatID, epoch vibekit.TurnEpoch, armID, seq, gen uint64, detail string) {
	lc := bc.turns.lifecycleFor(chatID)
	lc.mu.Lock()
	if lc.state != turnOpen || lc.cur == nil || lc.cur.Epoch != epoch {
		lc.mu.Unlock()
		return
	}
	turn := lc.cur
	// reapTimer nil means a closer already stopped this reap; a different arm
	// id means a newer arm owns the pending timer and this fire is stale.
	if turn.reapTimer == nil || turn.reapArmID != armID {
		lc.mu.Unlock()
		return
	}
	if turn.reapArmedSeq != seq || turn.reapArmedGen != gen {
		lc.mu.Unlock()
		return
	}
	if lc.observedSeq != seq || lc.fwdGen != gen || turn.Buf.HasToolInFlightSince(turn.reapChainAt) {
		bc.armCompactionReapLocked(lc, turn, detail)
		lc.mu.Unlock()
		return
	}
	turn.reapTimer = nil
	lc.mu.Unlock()

	bc.InterruptTurn(chatID, detail)
}

func (lc *chatLifecycle) stopReapLocked(turn *Turn) {
	if turn.reapTimer == nil {
		return
	}
	turn.reapTimer.Stop()
	turn.reapTimer = nil
}
