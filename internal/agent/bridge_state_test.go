package agent

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

// The grace is armed against a specific turn generation, so these tests use a
// short duration rather than the production cancelGrace.
const testGrace = 20 * time.Millisecond

// newPromptingBridge returns a bridge holding the prompt slot with a
// registered prompt context, plus that context.
func newPromptingBridge(t *testing.T) (*sharedBridge, context.Context, uint64) {
	t.Helper()
	sb := &sharedBridge{}
	if !sb.tryAcquireForPrompt() {
		t.Fatalf("fresh bridge must be acquirable")
	}
	// Not t.Context(): cancel runs from t.Cleanup, by which point t.Context()
	// is already cancelled, so the prompt context must have its own root.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	gen := sb.BeginPromptCall(cancel)
	return sb, ctx, gen
}

// TestArmCancelGrace_UnblocksAnUnackedCancel is the whole point of the budget:
// KAS never answers the pending prompt, so vibekit cancels its context itself
// and the blocked Call returns.
func TestArmCancelGrace_UnblocksAnUnackedCancel(t *testing.T) {
	// In a bubble the WHEN is assertable, not just the whether. On a real clock
	// this could only say "cancelled inside a 2s ceiling", which a budget armed
	// for any duration up to 2s satisfies — including one armed for zero. Here
	// the elapsed time is exactly the grace, so an arm that fires early or late
	// fails.
	synctest.Test(t, func(t *testing.T) {
		sb, ctx, gen := newPromptingBridge(t)
		start := time.Now()
		if !sb.ArmCancelGrace(gen, testGrace) {
			t.Fatalf("arming against an in-flight prompt must succeed")
		}
		<-ctx.Done()
		if elapsed := time.Since(start); elapsed != testGrace {
			t.Errorf("prompt context cancelled after %v, want exactly the armed grace %v",
				elapsed, testGrace)
		}
	})
}

// TestArmCancelGrace_AckedCancelLeavesContextAlone covers the ordinary path: the
// turn ended within the grace, so the timer must be disarmed rather than firing
// into a finished turn.
func TestArmCancelGrace_AckedCancelLeavesContextAlone(t *testing.T) {
	// A fake clock makes "the timer did NOT fire" exact. On a real clock this
	// assertion was only probabilistic: sleeping 4x the grace and finding the
	// context alive is evidence, not proof, and a loaded machine could return
	// either verdict. Inside the bubble the advance is complete by definition,
	// and synctest.Wait drains every goroutine the timer could have woken.
	synctest.Test(t, func(t *testing.T) {
		sb, ctx, gen := newPromptingBridge(t)
		sb.ArmCancelGrace(gen, testGrace)
		sb.releaseAfterPrompt() // KAS answered; the prompt handler's defer runs.

		// synctest.Sleep (Go 1.27) is time.Sleep + synctest.Wait in one call.
		synctest.Sleep(4 * testGrace)
		if ctx.Err() != nil {
			t.Errorf("context cancelled after the turn already ended: %v", ctx.Err())
		}
	})
}

// TestShouldTripCancelGrace_RefusesANewerTurn is the generation guard, tested at
// the decision rather than through the timer: Timer.Stop does not halt a func
// that is already running, so an expired budget CAN reach its decision after
// the turn ended and another began. Cancelling then would kill work the user
// just started.
func TestShouldTripCancelGrace_RefusesANewerTurn(t *testing.T) {
	sb, _, gen := newPromptingBridge(t)

	// Turn 1 ends, turn 2 starts — the state the racing timer func observes.
	sb.releaseAfterPrompt()
	if !sb.tryAcquireForPrompt() {
		t.Fatalf("bridge must be acquirable again after release")
	}
	_, nextCancel := context.WithCancel(t.Context())
	defer nextCancel()
	nextGen := sb.BeginPromptCall(nextCancel)
	if nextGen == gen {
		t.Fatalf("a new turn must get a new generation (got %d twice)", gen)
	}

	if _, ok := sb.shouldTripCancelGrace(gen); ok {
		t.Errorf("turn %d's grace must not apply to turn %d", gen, nextGen)
	}
	// The budget armed for the CURRENT turn still applies.
	if _, ok := sb.shouldTripCancelGrace(nextGen); !ok {
		t.Errorf("turn %d's own grace must still apply", nextGen)
	}
}

// TestShouldTripCancelGrace_RefusesAFinishedTurn covers the ordinary ack: the
// turn ended, so an already-running timer func must decline.
func TestShouldTripCancelGrace_RefusesAFinishedTurn(t *testing.T) {
	sb, _, gen := newPromptingBridge(t)
	sb.releaseAfterPrompt()
	if _, ok := sb.shouldTripCancelGrace(gen); ok {
		t.Errorf("grace must not apply to a turn that already ended")
	}
}

// TestArmCancelGrace_RefusesWhenNoPromptInFlight keeps a cancel on an idle chat
// from arming a timer against nothing.
func TestArmCancelGrace_RefusesWhenNoPromptInFlight(t *testing.T) {
	sb := &sharedBridge{}
	if sb.ArmCancelGrace(sb.PromptGeneration(), testGrace) {
		t.Errorf("arming on an idle bridge must report false")
	}

	// Prompting, but the prompt context was never registered.
	if !sb.tryAcquireForPrompt() {
		t.Fatalf("acquire failed")
	}
	if sb.ArmCancelGrace(sb.PromptGeneration(), testGrace) {
		t.Errorf("arming with no registered prompt cancel must report false")
	}
}

// TestEndPromptCall_DisarmsTheTimer covers the prompt handler's own defer path,
// which forgets the cancel func without changing the bridge state.
func TestEndPromptCall_DisarmsTheTimer(t *testing.T) {
	// Same negative-timer assertion as above; see that comment for why the
	// bubble is what makes it a proof rather than an observation.
	synctest.Test(t, func(t *testing.T) {
		sb, ctx, gen := newPromptingBridge(t)
		sb.ArmCancelGrace(gen, testGrace)
		sb.EndPromptCall()

		synctest.Sleep(4 * testGrace)
		if ctx.Err() != nil {
			t.Errorf("context cancelled after EndPromptCall disarmed the budget: %v", ctx.Err())
		}
	})
}

// TestInterruptTurn_ReleasesTheTurnAndRecordsWhy: the host half of the
// tool-interruption fix. kiro-cli abandoned the turn without answering it, so the
// prompt's context is what has to be tripped, and the cause has to survive the
// trip because the transcript's divider has no other source for it.
func TestInterruptTurn_ReleasesTheTurnAndRecordsWhy(t *testing.T) {
	sb, ctx, _ := newPromptingBridge(t)

	const reason = "Stopped by kiro-cli's tool-use security filter"
	if !sb.interruptTurn(reason) {
		t.Fatal("interrupting an in-flight turn must be taken")
	}

	select {
	case <-ctx.Done():
	default:
		t.Error("the prompt context is still live; the blocked Call never returns and the slot stays held")
	}
	if got := sb.takeInterruptReason(); got != reason {
		t.Errorf("takeInterruptReason() = %q, want %q", got, reason)
	}
	// TAKE, not read: the reason describes ONE turn, and leaving it set would
	// relabel the next turn's ordinary failure with this turn's cause.
	if got := sb.takeInterruptReason(); got != "" {
		t.Errorf("second takeInterruptReason() = %q, want it cleared by the first read", got)
	}
}

// TestInterruptTurn_IsOneShotPerTurn pins the race the latch exists for. A user
// pressing Cancel and the filter firing land on the same turn's cancel func, and
// the generation guard stops cross-turn damage without deciding which of them
// gets the attribution. First writer wins, so a later cause cannot overwrite the
// reason already recorded — run_bounds.go's claimTermination is the same rule at
// run scope, and it exists because a bound rewriting a deliberate stop is what it
// looked like before.
func TestInterruptTurn_IsOneShotPerTurn(t *testing.T) {
	sb, _, _ := newPromptingBridge(t)

	if !sb.interruptTurn("first cause") {
		t.Fatal("the first interrupt must be taken")
	}
	if sb.interruptTurn("second cause") {
		t.Error("a second interrupt was taken; the turn's cause is already decided")
	}
	if got := sb.takeInterruptReason(); got != "first cause" {
		t.Errorf("reason = %q, want the FIRST cause to win", got)
	}
}

// TestInterruptTurn_RefusesWithNoTurnInFlight: the sentinel can arrive after a
// user cancel already ended the same turn, or on a chat whose turn is over. Both
// are benign races rather than failures, and acting on them would cancel a turn
// the user had just started.
func TestInterruptTurn_RefusesWithNoTurnInFlight(t *testing.T) {
	t.Run("idle bridge", func(t *testing.T) {
		sb := &sharedBridge{}
		if sb.interruptTurn("filter") {
			t.Error("an idle bridge accepted an interrupt")
		}
		if got := sb.takeInterruptReason(); got != "" {
			t.Errorf("a refused interrupt still recorded %q", got)
		}
	})

	t.Run("prompting but no prompt call registered", func(t *testing.T) {
		sb := &sharedBridge{}
		if !sb.tryAcquireForPrompt() {
			t.Fatal("fresh bridge must be acquirable")
		}
		// The window between TryAcquireForPrompt and BeginPromptCall: there is a
		// turn, but nothing to cancel yet.
		if sb.interruptTurn("filter") {
			t.Error("an interrupt was taken with no prompt context to trip")
		}
	})

	t.Run("after the turn ended", func(t *testing.T) {
		sb, _, _ := newPromptingBridge(t)
		sb.EndPromptCall()
		if sb.interruptTurn("filter") {
			t.Error("an interrupt was taken after the prompt call ended")
		}
	})
}
