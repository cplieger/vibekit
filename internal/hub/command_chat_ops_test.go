package hub

// Tests for the archive teardown path (OnChatArchiving / OnChatArchived) and
// the delete teardown (CleanupChatState): archiving a chat with a live bridge
// tears it down (bridge closed, pending perms + supervised trust cleared,
// assistant buffer dropped) but PRESERVES checkpoints (archive is reversible), while
// a delete reaps them.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// newTestHubWithConfig builds a hub with a real configDir, so config-dir
// crash-recovery files are wired.
func newTestHubWithConfig(t *testing.T) (*Hub, *fakeChatStore, string) {
	t.Helper()
	cfg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cfg, "chats"), 0o700); err != nil {
		t.Fatalf("mkdir chats: %v", err)
	}
	cs := newFakeChatStore()
	br := newFakeBridge()
	factory := func() api.ACPBridge { return br }
	h := New(t.TempDir(), factory, cs, WithConfigDir(cfg))
	cs.SetBroadcaster(h)
	h.mcpRegistry.signalReady()
	return h, cs, cfg
}

// TestOnChatArchiving_TearsDownState is the core of G1 invariant #3: the
// pre-archive hook runs the same in-memory teardown a delete performs —
// close the bridge, drop the assistant buffer, clear pending perms +
// supervised trust — before the chat file moves.
func TestOnChatArchiving_TearsDownState(t *testing.T) {
	h, cs, _ := newTestHubWithConfig(t)
	ctx := context.Background()
	_ = cs.Mutate(ctx, "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	// A live bridge for the chat (invariant #3: a live bridge implies a live
	// chat record — archiving must tear it down before the file moves).
	if _, err := h.coord.GetOrCreateBridge(ctx, "c1", ""); err != nil {
		t.Fatalf("GetOrCreateBridge: %v", err)
	}
	if h.coord.GetBridge("c1") == nil {
		t.Fatal("bridge not created")
	}

	// A mid-turn assistant buffer, which archiving must tear down.
	buf := h.bridge.assistantBufs.GetOrInit("c1")
	buf.MessageID = "m1"
	buf.Content.WriteString("streaming…")

	// Pending permission + supervised trust for the chat.
	h.sse.pendingPerms.Add(101, api.NewEvent(api.EventPermissionNeeded, "c1", struct{}{}))
	h.perm.supervised.SetTrust("c1")

	h.OnChatArchiving("c1")

	if h.coord.GetBridge("c1") != nil {
		t.Error("bridge not closed by pre-archive teardown")
	}
	if h.bridge.assistantBufs.Get("c1") != nil {
		t.Error("assistant buffer not dropped by pre-archive teardown")
	}
	if h.perm.supervised.HasTrust("c1") {
		t.Error("supervised trust not cleared")
	}
	if got := h.sse.pendingPerms.List("c1"); len(got) != 0 {
		t.Errorf("pending perms not cleared: %d remain", len(got))
	}
}
