package hub

// Tests for primeIfNeeded branch coverage. Three paths:
//   1. primeReasonNone → no-op (restart recovery uses session/load,
//      silent catch-up must NOT replay history).
//   2. primeReasonSwitch with non-empty history → session/prompt
//      fires with the prime text.
//   3. Empty BuildHistory → early return regardless of primeReason.

import (
	"context"
	"slices"
	"testing"

	"vibekit/internal/api"
)

func TestPrimeIfNeeded_NoneIsNoOp(t *testing.T) {
	t.Parallel()
	h, cs, br := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.Messages = []api.Message{{Role: api.RoleUser, Content: "hi"}}
		return true
	})
	sb, err := h.coord.GetOrCreateBridge(context.Background(), "c1", "", "")
	if err != nil {
		t.Fatalf("getOrCreateBridge: %v", err)
	}
	sb.primeReason = primeReasonNone

	br.mu.Lock()
	before := append([]string(nil), br.calls...)
	br.mu.Unlock()

	h.coord.PrimeIfNeeded(context.Background(), "c1", sb)

	br.mu.Lock()
	after := append([]string(nil), br.calls...)
	br.mu.Unlock()
	if len(after) != len(before) {
		t.Errorf("primeIfNeeded(None) issued Calls: before=%v after=%v", before, after)
	}
}

func TestPrimeIfNeeded_SwitchSendsPromptWithHistory(t *testing.T) {
	t.Parallel()
	h, cs, br := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.Messages = []api.Message{
			{Role: api.RoleUser, Content: "what time is it?"},
			{Role: api.RoleAssistant, Content: "it's late"},
		}
		return true
	})
	sb, err := h.coord.GetOrCreateBridge(context.Background(), "c1", "", "")
	if err != nil {
		t.Fatalf("getOrCreateBridge: %v", err)
	}
	sb.primeReason = primeReasonSwitch

	h.coord.PrimeIfNeeded(context.Background(), "c1", sb)

	br.mu.Lock()
	calls := append([]string(nil), br.calls...)
	br.mu.Unlock()

	if !slices.Contains(calls, "session/prompt") {
		t.Errorf("primeIfNeeded(Switch): session/prompt not issued; calls=%v", calls)
	}
}

func TestPrimeIfNeeded_EmptyHistoryEarlyReturn(t *testing.T) {
	t.Parallel()
	h, cs, br := newTestHub()
	// Chat with no messages → BuildHistory returns "" → primeIfNeeded
	// must return before inspecting primeReason.
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	sb, err := h.coord.GetOrCreateBridge(context.Background(), "c1", "", "")
	if err != nil {
		t.Fatalf("getOrCreateBridge: %v", err)
	}
	sb.primeReason = primeReasonSwitch // would normally prime

	br.mu.Lock()
	before := len(br.calls)
	br.mu.Unlock()

	h.coord.PrimeIfNeeded(context.Background(), "c1", sb)

	br.mu.Lock()
	after := len(br.calls)
	br.mu.Unlock()
	if after != before {
		t.Errorf("primeIfNeeded fired on empty history: before=%d after=%d", before, after)
	}
}
