package hub

// Tests for models_snap.go: the Snapshotter contract used by the git
// handler to fetch a cheap model id from live bridges.

import (
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

type modelsBridge struct {
	*fakeBridge

	models []vibekit.SessionModel
}

func (b *modelsBridge) Models() []vibekit.SessionModel { return b.models }

func TestHubModels_EmptyWhenNoBridges(t *testing.T) {
	h, _, _ := newTestHub()
	if ms := h.Models(); ms != nil {
		t.Errorf("Models() = %+v, want nil", ms)
	}
}

func TestHubModels_ReturnsFirstNonEmpty(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })
	_ = cs.Mutate(t.Context(), "c2", func(c *vibekit.Chat, _ bool) bool { c.Name = "B"; return true })

	// Swap both chats' bridges to ones with distinct model sets.
	empty := &modelsBridge{fakeBridge: newFakeBridge()}
	populated := &modelsBridge{
		fakeBridge: newFakeBridge(),
		models:     []vibekit.SessionModel{{ID: "claude-3-sonnet", Name: "Sonnet"}},
	}
	h.bridge.mgr.mu.Lock()
	h.bridge.mgr.bridges["c1"] = &sharedBridge{bridge: empty}
	h.bridge.mgr.bridges["c2"] = &sharedBridge{bridge: populated}
	h.bridge.mgr.mu.Unlock()

	got := h.Models()
	if len(got) != 1 || got[0].ID != "claude-3-sonnet" {
		t.Errorf("Models() = %+v, want [claude-3-sonnet]", got)
	}
}

func TestHubModels_AllEmptyReturnsNil(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })

	empty := &modelsBridge{fakeBridge: newFakeBridge()}
	h.bridge.mgr.mu.Lock()
	h.bridge.mgr.bridges["c1"] = &sharedBridge{bridge: empty}
	h.bridge.mgr.mu.Unlock()

	if ms := h.Models(); ms != nil {
		t.Errorf("Models() = %+v, want nil", ms)
	}
}
