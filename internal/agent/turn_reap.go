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
	if lc.observedSeq != seq || lc.fwdGen != gen || turn.Buf.HasToolInFlight() {
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
