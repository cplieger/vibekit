package hub

import (
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// Tests for bridge_lifecycle.go: spawn, lookup, reuse, and teardown of
// per-chat ACP bridges. Shared fixtures live in shared_test.go.

func TestGetBridge_ReturnsNilForUnknown(t *testing.T) {
	h, _, _ := newTestHub()
	if sb := h.coord.GetBridge("no-such-chat"); sb != nil {
		t.Errorf("getBridge returned %+v for missing chat", sb)
	}
}

func TestGetOrCreateBridge_ReusesExisting(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(t.Context(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	sb1, err := h.coord.GetOrCreateBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("first create error = %v", err)
	}
	sb2, err := h.coord.GetOrCreateBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("second create error = %v", err)
	}
	if sb1 != sb2 {
		t.Errorf("second call returned a different bridge")
	}
}

func TestGetOrCreateBridge_MissingChatIsError(t *testing.T) {
	h, _, _ := newTestHub()
	_, err := h.coord.GetOrCreateBridge(t.Context(), "no-chat", "")
	if err == nil {
		t.Fatal("expected error for missing chat")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %v, want 'not found'", err)
	}
}

func TestCloseBridge_RemovesAndStops(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(t.Context(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	sb, err := h.coord.GetOrCreateBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatal(err)
	}
	fb := sb.bridge.(*fakeBridge)

	h.coord.CloseBridge("c1")

	if h.coord.GetBridge("c1") != nil {
		t.Error("bridge still in map after closeBridge")
	}
	if !fb.stopped {
		t.Error("bridge.Stop not called")
	}
}
