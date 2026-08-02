package hub

// Tests for the archive teardown path (OnChatArchiving / OnChatArchived) and
// the delete teardown (CleanupChatState): archiving a chat with a live bridge
// tears it down (bridge closed, pending perms + supervised trust cleared,
// .partial removed) but PRESERVES checkpoints (archive is reversible), while
// a delete reaps them.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// newTestHubWithConfig builds a hub with a real configDir, so .partial
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
// close the bridge, clear pending perms + supervised trust, close+remove
// the .partial — before the chat file moves.
func TestOnChatArchiving_TearsDownState(t *testing.T) {
	h, cs, cfg := newTestHubWithConfig(t)
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

	// An on-disk .partial recovery file (a mid-turn archive would otherwise
	// leave it for RecoverPartials to resurrect as a ghost active chat).
	// The writer creates the file on the first flush (atomic replacement),
	// not at open, so write one snapshot to materialize it.
	buf := h.bridge.assistantBufs.GetOrInit("c1")
	h.openPartialFile(ctx, "c1", buf)
	buf.MessageID = "m1"
	buf.Content.WriteString("streaming…")
	buf.WritePartial(ctx)
	partialPath := filepath.Join(cfg, "chats", "c1.partial")
	if _, err := os.Stat(partialPath); err != nil {
		t.Fatalf("partial file not created: %v", err)
	}

	// Pending permission + supervised trust for the chat.
	h.sse.pendingPerms.Add(101, api.NewEvent(api.EventPermissionNeeded, "c1", struct{}{}))
	h.perm.supervised.SetTrust("c1")

	h.OnChatArchiving("c1")

	if h.coord.GetBridge("c1") != nil {
		t.Error("bridge not closed by pre-archive teardown")
	}
	if _, err := os.Stat(partialPath); !os.IsNotExist(err) {
		t.Errorf(".partial not removed by pre-archive teardown: stat err = %v", err)
	}
	if h.perm.supervised.HasTrust("c1") {
		t.Error("supervised trust not cleared")
	}
	if got := h.sse.pendingPerms.List("c1"); len(got) != 0 {
		t.Errorf("pending perms not cleared: %d remain", len(got))
	}
}
