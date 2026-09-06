package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/chat"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// openTestTurn opens a turn on reg and returns it, failing when the open was
// refused. A helper that can fail, so it marks t.Helper() and t.Fatals itself.
func openTestTurn(t *testing.T, reg *turnRegistry, chatID vibekit.ChatID) *Turn {
	t.Helper()
	turn := reg.open(t.Context(), chatID, vibekit.TurnSourcePrompt, "", 0)
	if turn == nil {
		t.Fatalf("open(%q) was refused", chatID)
	}
	return turn
}

// TestTurnRegistry_InterruptIsFirstWinsPerEpoch pins both guards on the cause a turn ends
// with. FIRST-WINS because two writers reach one turn — a user pressing Cancel and
// kiro-cli's tool-use filter — and neither may relabel the other. EPOCH-SCOPED because a
// cause offered for a finished epoch must land nowhere; while it lived on the bridge,
// turn A's cause survived into turn B.
func TestTurnRegistry_InterruptIsFirstWinsPerEpoch(t *testing.T) {
	reg := newTurnRegistry()
	turn := openTestTurn(t, reg, "c1")

	if !reg.interrupt("c1", turn.Epoch, "the filter stopped it") {
		t.Fatal("the first cause on an open turn must be taken")
	}
	if reg.interrupt("c1", turn.Epoch, "a later cause") {
		t.Error("a second cause was taken; the turn's cause is already decided")
	}
	if got := reg.interruptCause(turn); got != "the filter stopped it" {
		t.Errorf("cause = %q, want the FIRST one to win", got)
	}

	// The turn ends and another opens. The old epoch names a turn that is over,
	// so a cause armed for it must not reach the new one.
	reg.finish(turn, vibekit.TurnResult{})
	next := openTestTurn(t, reg, "c1")
	if reg.interrupt("c1", turn.Epoch, "stale cause") {
		t.Error("a cause for a finished epoch was accepted")
	}
	if got := reg.interruptCause(next); got != "" {
		t.Errorf("the new turn carries %q; a previous turn's cause must not survive into it", got)
	}
}

// TestTurnRegistry_StagedSummaryAccumulatesWithinOneTurn is why the metering summary is
// staged on the record: several turn_completion frames can describe one turn, so last-wins
// threw earlier measurements away. The TURN is also what bounds the sum — one count per
// turn rather than per frame, restarting when the next turn opens.
func TestTurnRegistry_StagedSummaryAccumulatesWithinOneTurn(t *testing.T) {
	reg := newTurnRegistry()
	turn := openTestTurn(t, reg, "c1")

	total, first := reg.stageTurnSummary("c1", 1200)
	if total != 1200 || !first {
		t.Errorf("first frame = (%v, %v), want (1200, true)", total, first)
	}
	total, first = reg.stageTurnSummary("c1", 300)
	if total != 1500 {
		t.Errorf("second frame total = %v, want 1500 (both frames describe this turn)", total)
	}
	if first {
		t.Error("the second frame reported itself first; the turn would be counted twice")
	}

	reg.finish(turn, vibekit.TurnResult{})
	openTestTurn(t, reg, "c1")
	total, first = reg.stageTurnSummary("c1", 700)
	if total != 700 || !first {
		t.Errorf("first frame of the next turn = (%v, %v), want (700, true) — the previous "+
			"turn's duration must not carry over", total, first)
	}
}

// TestTurnRegistry_StagedSummaryWithNoTurnOpen keeps the interim honest: a frame
// arriving for a turn vibekit never opened has nothing to stage onto, so it
// stands alone and is counted.
func TestTurnRegistry_StagedSummaryWithNoTurnOpen(t *testing.T) {
	reg := newTurnRegistry()

	total, first := reg.stageTurnSummary("c1", 900)
	if total != 900 || !first {
		t.Errorf("stageTurnSummary with no turn open = (%v, %v), want (900, true)", total, first)
	}
}

// TestTurnRegistry_ClaimIsFirstWins pins the exclusion the finalizer rests on: two closers
// reaching one turn persist and announce it once. The loser WAITS rather than being refused
// — the winner's persistence and broadcast run with no lock held, so the loser observes the
// result of that work rather than a half-finalized turn.
func TestTurnRegistry_ClaimIsFirstWins(t *testing.T) {
	reg := newTurnRegistry()
	openTestTurn(t, reg, "c1")

	turn, won := reg.claimOpen(t.Context(), "c1")
	if !won {
		t.Fatal("claiming an open turn must be taken")
	}

	second := make(chan bool, 1)
	go func() {
		_, ok := reg.claimOpen(context.WithoutCancel(t.Context()), "c1")
		second <- ok
	}()
	select {
	case <-second:
		t.Fatal("a second closer resolved while the first was still finalizing")
	case <-time.After(20 * time.Millisecond):
	}

	reg.finish(turn, vibekit.TurnResult{})
	select {
	case ok := <-second:
		if ok {
			t.Error("a second closer claimed the same turn; its effects would run twice")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the second closer never resolved after the finalize completed")
	}
}

// TestTurnRegistry_FinalizeWakesAParkedOpen is the normal-path deadlock guard, and the
// reason the waitable is a channel closed on EVERY state change: armed only where a frame
// folds, a finalize woke nobody and the user's next prompt parked in StartTurn forever.
func TestTurnRegistry_FinalizeWakesAParkedOpen(t *testing.T) {
	reg := newTurnRegistry()
	first := openTestTurn(t, reg, "c1")
	if _, won := reg.claimOpen(t.Context(), "c1"); !won {
		t.Fatal("claiming the open turn must be taken")
	}

	opened := make(chan *Turn, 1)
	go func() {
		opened <- reg.open(context.WithoutCancel(t.Context()), "c1", vibekit.TurnSourcePrompt, "", 0)
	}()

	// The parked open must not proceed while the chat is finalizing: that is what
	// keeps the next epoch unobservable until the previous turn's persistence and
	// broadcast have completed.
	select {
	case <-opened:
		t.Fatal("an open proceeded while the chat was finalizing")
	case <-time.After(20 * time.Millisecond):
	}

	reg.finish(first, vibekit.TurnResult{})
	select {
	case turn := <-opened:
		if turn == nil {
			t.Fatal("the woken open was refused")
		}
		if turn.Epoch == first.Epoch {
			t.Errorf("the woken open reused epoch %d instead of minting the next one", turn.Epoch)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a finalize did not wake the parked open; the next prompt never reaches KAS")
	}
}

// TestTurnRegistry_OpenIsCancellable pins the other half of choosing a channel
// over a sync.Cond: a waiter can be given up on. Cond.Wait composes with no
// cancellation at all, so a chat wedged in finalizing held its caller for good.
func TestTurnRegistry_OpenIsCancellable(t *testing.T) {
	reg := newTurnRegistry()
	openTestTurn(t, reg, "c1")
	if _, won := reg.claimOpen(t.Context(), "c1"); !won {
		t.Fatal("claiming the open turn must be taken")
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if turn := reg.open(ctx, "c1", vibekit.TurnSourcePrompt, "", 0); turn != nil {
		t.Error("open on a cancelled context returned a turn")
	}
}

// TestTurnRegistry_ForgetDropsTheChat: the registry must not outlive the chats it
// describes, or a deleted chat's lifecycle is retained for the process's life.
func TestTurnRegistry_ForgetDropsTheChat(t *testing.T) {
	reg := newTurnRegistry()
	openTestTurn(t, reg, "c1")

	reg.forget("c1")

	if _, open := reg.openEpoch("c1"); open {
		t.Error("a forgotten chat still reports an open turn")
	}
	reg.mu.Lock()
	n := len(reg.chats)
	reg.mu.Unlock()
	// openEpoch recreates the lifecycle on read, so the count is 1 rather than 0;
	// what must be gone is the TURN.
	if n != 1 {
		t.Errorf("registry holds %d lifecycles, want 1 (the one openEpoch just recreated)", n)
	}
}

// stubMeteringStore is a bridgeChatRecords whose Mutate fails with a chosen
// error, so the metering write's error handling can be driven without a real
// store.
type stubMeteringStore struct{ mutateErr error }

func (s stubMeteringStore) Get(context.Context, vibekit.ChatID) (*vibekit.Chat, bool) {
	return nil, false
}
func (s stubMeteringStore) BuildHistory(context.Context, vibekit.ChatID) string { return "" }
func (s stubMeteringStore) AppendMessage(context.Context, vibekit.ChatID, *vibekit.Message) error {
	return nil
}

func (s stubMeteringStore) Mutate(context.Context, vibekit.ChatID, func(*vibekit.Chat, bool) bool) error {
	return s.mutateErr
}

func (s stubMeteringStore) UpdateMessage(context.Context, vibekit.ChatID, string, func(*vibekit.Message)) error {
	return nil
}

// TestMutateUsage_TombstonedRefusalIsNotAnError pins the drop the tombstone was designed
// for. ErrTombstoned means the write was DECLINED for a chat id deleted inside the window,
// so nothing reached disk — and a metering frame lands once per turn on every chat, so
// surfacing it would put an ERROR line in the log for the mechanism working as intended.
func TestMutateUsage_TombstonedRefusalIsNotAnError(t *testing.T) {
	bc := &BridgeCoordinator{chatStore: stubMeteringStore{mutateErr: chat.ErrTombstoned}, turns: newTurnRegistry()}
	logs := captureLogs(t)

	bc.AccumulateSpend(t.Context(), "c1", 0.5)
	bc.StageConversationTurnSummary(t.Context(), "c1", 1200)

	if out := logs.String(); strings.Contains(out, `"level":"ERROR"`) {
		t.Errorf("a tombstoned metering write logged an error: %s", out)
	}
}

// TestMutateUsage_OtherErrorsStillLog is the other half, and it is what keeps the
// drop narrow: matching the sentinel must not swallow a real persist failure — a
// full disk, a permission fault, a corrupt chat file.
func TestMutateUsage_OtherErrorsStillLog(t *testing.T) {
	bc := &BridgeCoordinator{chatStore: stubMeteringStore{mutateErr: errors.New("disk full")}, turns: newTurnRegistry()}
	logs := captureLogs(t)

	bc.AccumulateSpend(t.Context(), "c1", 0.5)

	if out := logs.String(); !strings.Contains(out, "persist turn metering") {
		t.Errorf("a real metering write failure was swallowed: %s", out)
	}
}

// A chat forgotten mid-finalize still has its turn published on the lifecycle its waiters
// are parked on. Re-resolving from the chat id creates a FRESH lifecycle on a miss, so a
// finish in flight after a forget set that one idle and left the old one in turnFinalizing
// forever — parking Forward's own goroutine there for the life of the process, with no
// seal, no replay-projection settle and no exit tail. The turn carries its lifecycle now.
func TestForget_DoesNotStrandAnInFlightFinalizeOnAnotherLifecycle(t *testing.T) {
	r := newTurnRegistry()
	ctx := t.Context()
	const chatID vibekit.ChatID = "c1"

	lc := r.lifecycleFor(chatID)
	if r.open(ctx, chatID, vibekit.TurnSourcePrompt, "", 0) == nil {
		t.Fatal("open returned no turn")
	}
	claimed, won := r.claimOpen(ctx, chatID)
	if !won {
		t.Fatal("claimOpen lost the claim on a freshly opened turn")
	}

	// The chat goes away while its turn is finalizing.
	r.forget(chatID)

	// A fold arriving on Forward parks on the lifecycle it already resolved.
	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	woken := make(chan bool, 1)
	go func() {
		ok := lc.awaitNotFinalizing(waitCtx)
		if ok {
			lc.mu.Unlock()
		}
		woken <- ok
	}()

	r.finish(claimed, vibekit.TurnResult{Stop: vibekit.StopReasonEndTurn})

	if !<-woken {
		t.Fatal("the waiter on the forgotten lifecycle was never woken, so Forward is parked " +
			"on a channel nothing will close")
	}
	lc.mu.Lock()
	state, cur := lc.state, lc.cur
	lc.mu.Unlock()
	if state != turnIdle || cur != nil {
		t.Errorf("the forgotten lifecycle is state %v with cur %v, want idle and nil: the finalize "+
			"published somewhere else", state, cur)
	}
}
