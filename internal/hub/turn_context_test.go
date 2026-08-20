package hub

// Coverage for Hub.TurnContext, the lifecycle seam command handlers reach for
// instead of the hub's raw shutdown context. These four cases moved here from
// internal/command with the derivation itself: the hub is what knows its own
// lifetime, so this is where the property lives.

import (
	"context"
	"testing"
	"time"
)

// hubOnLifetime builds a hub whose lifetime the test controls, so a case can end
// the APP's lifetime without going through Shutdown.
func hubOnLifetime(t *testing.T) (*Hub, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	h := New(ctx, "/tmp/work", func() ACPBridge { return newFakeBridge() }, newFakeChatStore())
	return h, cancel
}

// TestTurnContext_SurvivesRequestCancel is the regression test for the
// mid-turn-disconnect bug: a client drop cancels the prompt POST's request
// context, but the turn's context (which the bridge Call runs under) must NOT be
// cancelled — otherwise the Call aborts, prompt_failed fires before
// EmitTurnEndedWithStats, and the assistant buffer is lost while kiro-cli keeps
// running the turn.
func TestTurnContext_SurvivesRequestCancel(t *testing.T) {
	h, cancelLifetime := hubOnLifetime(t)
	defer cancelLifetime()

	reqCtx, reqCancel := context.WithCancel(t.Context())
	turnCtx, cleanup := h.lifecycle.TurnContext(reqCtx)
	defer cleanup()

	// Simulate a mid-turn client disconnect.
	reqCancel()

	select {
	case <-turnCtx.Done():
		t.Fatal("turn context cancelled by request disconnect: the bridge Call would abort before turn_ended")
	case <-time.After(50 * time.Millisecond):
	}
	if err := turnCtx.Err(); err != nil {
		t.Fatalf("turn context Err = %v after request cancel, want nil", err)
	}
}

// TestTurnContext_CancelsOnShutdown verifies hub shutdown still tears the turn
// down — cancellation must move from the request context to the hub's lifetime,
// not disappear entirely.
func TestTurnContext_CancelsOnShutdown(t *testing.T) {
	h, _, _ := newTestHub()

	turnCtx, cleanup := h.lifecycle.TurnContext(t.Context())
	defer cleanup()

	shutdownHub(t, h)

	select {
	case <-turnCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("turn context not cancelled on hub shutdown")
	}
}

// TestTurnContext_CancelsOnAppLifetimeEnd is the case that only exists because
// the hub's lifetime is now a parameter rather than a context.Background() it
// invented: ending the APP's lifetime must reach a turn, without App.Shutdown
// having to remember to call Hub.Shutdown first.
func TestTurnContext_CancelsOnAppLifetimeEnd(t *testing.T) {
	h, cancelLifetime := hubOnLifetime(t)

	turnCtx, cleanup := h.lifecycle.TurnContext(t.Context())
	defer cleanup()

	cancelLifetime()

	select {
	case <-turnCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("turn context not cancelled when the app's lifetime ended; the hub's " +
			"shutdown context is not derived from the context New was given")
	}
}

// TestTurnContext_CleanupCancels verifies the returned cleanup cancels the turn
// context (normal handler-return teardown, and unregisters the lifetime
// AfterFunc so it can't leak).
func TestTurnContext_CleanupCancels(t *testing.T) {
	h, cancelLifetime := hubOnLifetime(t)
	defer cancelLifetime()

	turnCtx, cleanup := h.lifecycle.TurnContext(t.Context())
	cleanup()
	select {
	case <-turnCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("cleanup did not cancel the turn context")
	}
}

// TestTurnContext_PreservesValues verifies request-scoped values survive the
// WithoutCancel detachment (only cancellation is severed, not values).
func TestTurnContext_PreservesValues(t *testing.T) {
	h, cancelLifetime := hubOnLifetime(t)
	defer cancelLifetime()

	type ctxKey string
	const k ctxKey = "trace-id"
	reqCtx := context.WithValue(t.Context(), k, "abc123")

	turnCtx, cleanup := h.lifecycle.TurnContext(reqCtx)
	defer cleanup()

	if got := turnCtx.Value(k); got != "abc123" {
		t.Fatalf("turn context lost request-scoped value: got %v, want abc123", got)
	}
}
