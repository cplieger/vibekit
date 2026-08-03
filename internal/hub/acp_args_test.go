package hub

import (
	"context"
	"slices"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// TestACPArgsReachChatBridges pins the delivery path: WithACPArgs → the
// coordinator → StartOpts.ExtraArgs on a chat spawn.
func TestACPArgsReachChatBridges(t *testing.T) {
	cs := newFakeChatStore()
	br := newFakeBridge()
	want := []string{"-v"}
	h := New("/tmp/work", func() api.ACPBridge { return br }, cs, WithACPArgs(want))
	cs.SetBroadcaster(h)
	h.mcpRegistry.signalReady()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	if _, err := h.coord.GetOrCreateBridge(context.Background(), "c1", ""); err != nil {
		t.Fatalf("GetOrCreateBridge: %v", err)
	}
	opts := br.lastStartOpts()
	if opts == nil {
		t.Fatal("the bridge was never started")
	}
	if !slices.Equal(opts.ExtraArgs, want) {
		t.Errorf("chat StartOpts.ExtraArgs = %v, want %v", opts.ExtraArgs, want)
	}
}

// TestACPArgsNeverReachTheUtilityBridge is the load-bearing half.
//
// The utility bridge generates chat titles and fetches the mode/model catalog.
// An operator `--effort max` there would spend real credits on a two-word
// summary, and it shares the SAME factory as chat bridges — so the exclusion
// cannot come from the factory and has to be a per-spawn decision. That makes it
// exactly the kind of thing a later refactor would "simplify" by threading the
// args through once.
func TestACPArgsNeverReachTheUtilityBridge(t *testing.T) {
	cs := newFakeChatStore()
	br := newFakeBridge()
	h := New("/tmp/work", func() api.ACPBridge { return br }, cs, WithACPArgs([]string{"--effort", "max"}))
	cs.SetBroadcaster(h)
	h.mcpRegistry.signalReady()

	u := h.ensureUtility()
	if _, err := u.session.acquire(context.Background()); err != nil {
		t.Fatalf("acquire utility session: %v", err)
	}
	defer u.session.Stop()

	opts := br.lastStartOpts()
	if opts == nil {
		t.Fatal("the utility bridge was never started")
	}
	if len(opts.ExtraArgs) != 0 {
		t.Errorf("utility StartOpts.ExtraArgs = %v, want empty: operator launch flags must not reach the utility bridge", opts.ExtraArgs)
	}
}

// TestACPArgsUnsetIsEmpty covers the default: no env var, no args, and nothing
// appended to any spawn.
func TestACPArgsUnsetIsEmpty(t *testing.T) {
	cs := newFakeChatStore()
	br := newFakeBridge()
	h := New("/tmp/work", func() api.ACPBridge { return br }, cs)
	cs.SetBroadcaster(h)
	h.mcpRegistry.signalReady()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	if _, err := h.coord.GetOrCreateBridge(context.Background(), "c1", ""); err != nil {
		t.Fatalf("GetOrCreateBridge: %v", err)
	}
	if opts := br.lastStartOpts(); opts != nil && len(opts.ExtraArgs) != 0 {
		t.Errorf("ExtraArgs = %v with no WithACPArgs, want empty", opts.ExtraArgs)
	}
}
