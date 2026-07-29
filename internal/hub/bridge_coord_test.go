package hub

// Tests for bridge_coord.go: BridgeCoordinator override application,
// fast model switch, registry teardown on the last bridge, turn-ended
// trust-clear / push behaviour, and the persist success paths that must
// stay log-silent.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/api"
)

// --- helpers ---

// recordingStartBridge records the StartOpts passed to Start while
// behaving like a fakeBridge for every other method.
type recordingStartBridge struct {
	*fakeBridge
	lastStart api.StartOpts
	recMu     sync.Mutex
}

func newRecordingStartBridge() *recordingStartBridge {
	return &recordingStartBridge{fakeBridge: newFakeBridge()}
}

func (b *recordingStartBridge) Start(ctx context.Context, opts *api.StartOpts) error {
	b.recMu.Lock()
	b.lastStart = *opts
	b.recMu.Unlock()
	return b.fakeBridge.Start(ctx, opts)
}

func (b *recordingStartBridge) startOpts() api.StartOpts {
	b.recMu.Lock()
	defer b.recMu.Unlock()
	return b.lastStart
}

func newRecordingStartHub(t *testing.T) (*Hub, *fakeChatStore, *recordingStartBridge) {
	t.Helper()
	cs := newFakeChatStore()
	rb := newRecordingStartBridge()
	h := New("/tmp/rec-start", func() api.ACPBridge { return rb }, cs)
	cs.Bus = h
	h.mcpRegistry.signalReady()
	return h, cs, rb
}

// recordingPush records the body of each Send on a channel.
type recordingPush struct {
	sends chan string
}

func (p *recordingPush) RegisterRoutes(*http.ServeMux)        {}
func (p *recordingPush) Subscribe(api.PushSubscription)       {}
func (p *recordingPush) Unsubscribe(string)                   {}
func (p *recordingPush) HasSubscribers() bool                 { return true }
func (p *recordingPush) SetPreferences(map[api.PushKind]bool) {}
func (p *recordingPush) ReloadPreferences(context.Context)    {}
func (p *recordingPush) Close()                               {}
func (p *recordingPush) Send(_ context.Context, _, body string, _ api.PushKind) {
	select {
	case p.sends <- body:
	default:
	}
}

// --- GetOrCreateBridge overrides + persisted model ---

// On a fresh session/new path the override model wins over the chat's
// stored value, and the persisted chat model is copied from the started
// bridge's ModelID.
func TestGetOrCreateBridge_AppliesOverrides(t *testing.T) {
	h, cs, rb := newRecordingStartHub(t)
	ctx := context.Background()
	_ = cs.Mutate(ctx, "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.Model = "m-chat"
		return true // no ACPSessionID -> fresh session/new path
	})

	if _, err := h.coord.GetOrCreateBridge(ctx, "c1", "model-override"); err != nil {
		t.Fatalf("GetOrCreateBridge: %v", err)
	}

	opts := rb.startOpts()
	if opts.Model != "model-override" {
		t.Errorf("StartOpts.Model = %q, want %q (override must beat chat.Model)", opts.Model, "model-override")
	}

	c, _ := cs.Get(ctx, "c1")
	if c.Model != "fake-model" {
		t.Errorf("persisted chat.Model = %q, want %q (bridge model must be copied into the chat)", c.Model, "fake-model")
	}
}

// --- TryFastModelSwitch ---

// A successful in-session SetModel returns true.
func TestTryFastModelSwitch_SucceedsReturnsTrue(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := context.Background()
	_ = cs.Mutate(ctx, "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; c.Model = "m-old"; return true })
	if _, err := h.coord.GetOrCreateBridge(ctx, "c1", ""); err != nil {
		t.Fatalf("GetOrCreateBridge: %v", err)
	}

	if got := h.coord.TryFastModelSwitch(ctx, "c1", "m-new"); got != true {
		t.Errorf("TryFastModelSwitch(success) = %v, want true", got)
	}
}

// --- Forward clears the registry only when the last bridge exits ---

// When the forwarded bridge is the last one, Forward clears the MCP
// registry; when another bridge remains registered, it must not.
func TestForward_ClearsRegistryOnlyWhenLastBridge(t *testing.T) {
	seed := func(h *Hub) {
		h.mcpRegistry.mu.Lock()
		h.mcpRegistry.servers["srv"] = &mcpServerRuntime{Name: "srv", State: mcpStateConnected}
		h.mcpRegistry.mu.Unlock()
	}

	t.Run("clears_when_no_bridges_remain", func(t *testing.T) {
		h, _, br := newTestHub()
		seed(h)
		br.Stop() // close notifCh so Forward's range exits immediately
		h.coord.Forward("nochat", br)
		if n := len(h.mcpRegistry.Snapshot()); n != 0 {
			t.Errorf("registry size = %d, want 0 (no bridges left must clearAll)", n)
		}
	})

	t.Run("keeps_when_a_bridge_remains", func(t *testing.T) {
		h, _, _ := newTestHub()
		seed(h)
		// A bridge that stays registered so count() stays >= 1.
		h.bridge.mgr.getOrInsert("keep")
		other := newFakeBridge()
		other.Stop()
		h.coord.Forward("other", other)
		if n := len(h.mcpRegistry.Snapshot()); n != 1 {
			t.Errorf("registry size = %d, want 1 (a remaining bridge must NOT clearAll)", n)
		}
	})
}

// --- EmitTurnEndedWithStats trust-clear reason ---

// A cancelled turn clears per-turn trust with the "cancelled" reason.
func TestEmitTurnEnded_CancelledUsesCancelledClearReason(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := context.Background()
	_ = cs.Mutate(ctx, "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	h.perm.supervised.SetTrust("c1") // so ClearTrust actually broadcasts

	resp := &api.RPCResponse{Result: mustJSON(t, map[string]any{"stopReason": "cancelled"})}
	h.EmitTurnEndedWithStats(ctx, "c1", resp, 0, 0)

	ev := lastReplayEventOfType(h, api.EventPendingTrustCleared)
	if ev == nil {
		t.Fatal("no pending_trust_cleared event broadcast")
	}
	raw, err := json.Marshal(ev.Payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var p api.PendingTrustClearedPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if p.Reason != api.ClearReasonCancelled {
		t.Errorf("ClearTrust reason = %q, want %q", p.Reason, api.ClearReasonCancelled)
	}
}

// A non-cancelled turn fires the "Agent finished" push.
func TestEmitTurnEnded_NonCancelledFiresPush(t *testing.T) {
	cs := newFakeChatStore()
	fp := &recordingPush{sends: make(chan string, 4)}
	h := New("/tmp/push", func() api.ACPBridge { return newFakeBridge() }, cs, WithPush(fp))
	cs.Bus = h
	h.mcpRegistry.signalReady()
	ctx := context.Background()
	_ = cs.Mutate(ctx, "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	resp := &api.RPCResponse{Result: mustJSON(t, map[string]any{"stopReason": "end_turn"})}
	h.EmitTurnEndedWithStats(ctx, "c1", resp, 0, 0)

	select {
	case body := <-fp.sends:
		if body != "Agent finished" {
			t.Errorf("push body = %q, want %q", body, "Agent finished")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no push sent for a non-cancelled turn")
	}
}

// --- success paths must not emit an error log ---

// PrimeIfNeeded logs nothing when the prime Call succeeds.
func TestPrimeIfNeeded_NoErrorLogOnSuccess(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := context.Background()
	_ = cs.Mutate(ctx, "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.Messages = []api.Message{{Role: api.RoleUser, Content: "hi"}}
		return true
	})
	sb, err := h.coord.GetOrCreateBridge(ctx, "c1", "")
	if err != nil {
		t.Fatalf("GetOrCreateBridge: %v", err)
	}
	sb.primeReason = primeReasonSwitch

	logs := captureLogs(t)
	h.coord.PrimeIfNeeded(ctx, "c1", sb)
	if got := logs.String(); strings.Contains(got, "prime failed") {
		t.Errorf("unexpected error log on prime success: %s", got)
	}
}

// EmitTurnEndedWithStats logs no persist error when the assistant-turn
// and cancel-event appends both succeed.
func TestEmitTurnEnded_NoPersistErrorLogOnSuccess(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := context.Background()
	_ = cs.Mutate(ctx, "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	buf := h.bridge.assistantBufs.GetOrInit("c1")
	buf.Started = true
	buf.MessageID = "m-asst"

	logs := captureLogs(t)
	resp := &api.RPCResponse{Result: mustJSON(t, map[string]any{"stopReason": "cancelled"})}
	h.EmitTurnEndedWithStats(ctx, "c1", resp, 0, 0)

	got := logs.String()
	if strings.Contains(got, "persist assistant turn") {
		t.Errorf("unexpected assistant-turn persist error log on success: %s", got)
	}
	if strings.Contains(got, "persist cancel event") {
		t.Errorf("unexpected cancel-event persist error log on success: %s", got)
	}
}

// PersistModelSwitch logs nothing when the event append succeeds.
func TestPersistModelSwitch_NoErrorLogOnSuccess(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := context.Background()
	_ = cs.Mutate(ctx, "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; c.Model = "m-old"; return true })

	logs := captureLogs(t)
	h.coord.PersistModelSwitch(ctx, "c1", "m-new", 1234)
	if got := logs.String(); strings.Contains(got, "switch_model: append event") {
		t.Errorf("unexpected append-event error log on success: %s", got)
	}
}
