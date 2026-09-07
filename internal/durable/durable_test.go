package durable

// The one property this package exists for, from both sides: what Context keeps
// and what it drops. The siblings it mirrors are agent's TurnContext cases,
// which pin the same pair for a context that stays cancellable.

import (
	"context"
	"testing"
	"time"
)

type ctxKey struct{}

// TestContext_KeepsValuesAndDropsCancellation is the whole contract. A durable
// write still needs its caller's values, and it must not be refused by
// chat.Store.Mutate's entry guard because the shutdown that made the write
// necessary cancelled the context that carries it.
func TestContext_KeepsValuesAndDropsCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(context.WithValue(t.Context(), ctxKey{}, "kept"))
	dctx := Context(parent)

	cancel()

	if err := dctx.Err(); err != nil {
		t.Errorf("Err after the parent was cancelled = %v, want nil; Mutate's guard would refuse the write", err)
	}
	if got := context.Cause(dctx); got != nil {
		t.Errorf("Cause after the parent was cancelled = %v, want nil", got)
	}
	select {
	case <-dctx.Done():
		t.Error("Done fired on the parent's cancellation")
	default:
	}
	if got, _ := dctx.Value(ctxKey{}).(string); got != "kept" {
		t.Errorf("Value = %q, want %q; the caller's values must survive the detach", got, "kept")
	}
}

// TestContext_CarriesNoDeadline is the reversal worth pinning. The store's I/O
// already runs on context.Background(), so a deadline here gates nothing
// downstream and its only reachable effect is making Mutate's entry guard refuse
// the very write the detach exists to permit.
func TestContext_CarriesNoDeadline(t *testing.T) {
	parent, cancel := context.WithTimeout(t.Context(), time.Millisecond)
	defer cancel()

	dctx := Context(parent)

	if deadline, ok := dctx.Deadline(); ok {
		t.Errorf("Deadline = %v (ok=%t), want none: an expired one refuses the write", deadline, ok)
	}
	// And it stays refusal-free once the parent's own deadline has passed, which
	// is the state a slow shutdown actually reaches.
	<-parent.Done()
	if err := dctx.Err(); err != nil {
		t.Errorf("Err after the parent's deadline passed = %v, want nil", err)
	}
}
