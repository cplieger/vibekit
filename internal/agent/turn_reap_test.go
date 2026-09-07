package agent

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

func newCompactionReapFixture(t *testing.T) (*Runtime, vibekit.TurnEpoch, context.Context) {
	t.Helper()
	h, cs, _ := newTestHub()
	seedChat(t, cs, "c1")
	epoch, _ := h.stagePromptTurn(t, "c1")
	t.Cleanup(func() { h.ReleaseTurn("c1", epoch) })

	pctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	sb, _ := h.bridge.mgr.orInsert("c1")
	sb.mu.Lock()
	sb.state = bridgePrompting
	sb.promptCancel = cancel
	sb.mu.Unlock()
	return h, epoch, pctx
}

func TestCompactionReap_TripsTheSilentTurn(t *testing.T) {
	h, _, pctx := newCompactionReapFixture(t)
	synctest.Test(t, func(t *testing.T) {
		h.coord.CompactionFailed("c1", "compaction failed")
		time.Sleep(compactionFailedTurnBudget)
		synctest.Wait()
	})

	if pctx.Err() == nil {
		t.Error("silent failed-compaction turn kept its prompt context live")
	}
	lc := h.coord.turns.lifecycleFor("c1")
	lc.mu.Lock()
	turn := lc.cur
	lc.mu.Unlock()
	if got := h.coord.turns.interruptCause(turn); got != "compaction failed" {
		t.Errorf("interrupt cause = %q, want %q", got, "compaction failed")
	}
}

func TestCompactionReap_ReArmsWhenTheBackendSpoke(t *testing.T) {
	h, _, pctx := newCompactionReapFixture(t)
	gen := h.coord.turns.attachForward("c1")
	synctest.Test(t, func(t *testing.T) {
		h.coord.CompactionFailed("c1", "compaction failed")
		h.coord.turns.observe("c1", gen, 1)
		time.Sleep(compactionFailedTurnBudget)
		synctest.Wait()
		if pctx.Err() != nil {
			t.Error("backend activity did not restart the silence budget")
		}
		time.Sleep(compactionFailedTurnBudget)
		synctest.Wait()
	})
	if pctx.Err() == nil {
		t.Error("re-armed silence budget did not interrupt the turn")
	}
}

func TestCompactionReap_SuspendsWhileAToolIsInFlight(t *testing.T) {
	h, epoch, _ := newCompactionReapFixture(t)
	buf, _ := h.coord.OpenTurnBuffer("c1")
	buf.ToolCalls = append(buf.ToolCalls, vibekit.ToolCall{ID: "tool", Status: vibekit.ToolInProgress})

	synctest.Test(t, func(t *testing.T) {
		h.coord.CompactionFailed("c1", "compaction failed")
		time.Sleep(compactionFailedTurnBudget)
		synctest.Wait()
		lc := h.coord.turns.lifecycleFor("c1")
		lc.mu.Lock()
		turn := lc.cur
		lc.mu.Unlock()
		if cause := h.coord.turns.interruptCause(turn); cause != "" {
			t.Errorf("tool-running turn interrupt cause = %q, want empty", cause)
		}

		turn, ok := h.coord.turns.claimEpoch(t.Context(), "c1", epoch)
		if !ok {
			t.Fatal("normal closer could not claim the suspended turn")
		}
		h.coord.turns.finish(turn, vibekit.TurnResult{})
	})
}

func TestCompactionReap_WithNoOpenTurnArmsNothing(t *testing.T) {
	h, _, _ := newTestHub()
	h.coord.CompactionFailed("c1", "compaction failed")
	lc := h.coord.turns.lifecycleFor("c1")
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.cur != nil {
		t.Error("CompactionFailed with no open turn created turn state")
	}
}

func TestCompactionReap_StoppedWhenTheTurnClosesNormally(t *testing.T) {
	h, epoch, _ := newCompactionReapFixture(t)
	h.coord.CompactionFailed("c1", "compaction failed")
	turn, ok := h.coord.turns.claimEpoch(t.Context(), "c1", epoch)
	if !ok {
		t.Fatal("normal closer could not claim the armed turn")
	}
	if turn.reapTimer != nil {
		t.Error("claimed turn kept its compaction reap timer")
	}
	h.coord.turns.finish(turn, vibekit.TurnResult{})
}
