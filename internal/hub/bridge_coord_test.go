package hub

// Tests for bridge_coord.go: BridgeCoordinator override application,
// fast model switch, registry teardown on the last bridge, turn-ended
// trust-clear / push behaviour, and the persist success paths that must
// stay log-silent.

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/kirosession"
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
func (p *recordingPush) Send(_ context.Context, _, body string, _ api.PushKind, _ api.ChatID) {
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
	ctx := t.Context()
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
	ctx := t.Context()
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

// The per-turn trust-clear test is GONE with the trust it asserted on. Per-turn
// trust existed to let a user wave past vibekit's own staging queue for the rest
// of a turn; KAS reviews a whole turn at once, so there is no per-write gate to
// wave past and no reason to clear anything at turn end.

// A non-cancelled turn fires the "Agent finished" push.
func TestEmitTurnEnded_NonCancelledFiresPush(t *testing.T) {
	cs := newFakeChatStore()
	fp := &recordingPush{sends: make(chan string, 4)}
	h := New("/tmp/push", func() api.ACPBridge { return newFakeBridge() }, cs, WithPush(fp))
	cs.Bus = h
	h.mcpRegistry.signalReady()
	ctx := t.Context()
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
	ctx := t.Context()
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
	ctx := t.Context()
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
	ctx := t.Context()
	_ = cs.Mutate(ctx, "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; c.Model = "m-old"; return true })

	logs := captureLogs(t)
	h.coord.PersistModelSwitch(ctx, "c1", "m-new", 1234)
	if got := logs.String(); strings.Contains(got, "switch_model: append event") {
		t.Errorf("unexpected append-event error log on success: %s", got)
	}
}

// --- adoptKASTitle: the bottom of the chat-naming precedence ---

// TestAdoptKASTitle pins all four arms of the guard. Every refusal here is a
// bug that compiles cleanly: adopting KAS's "New Session" placeholder makes the
// chat non-default-named, which then rejects the real title that arrives later
// and leaves the chat reading "New Session" forever; adopting over an existing
// name clobbers either the user's first-prompt label or the agent's
// focus_update title, both of which outrank this channel.
func TestAdoptKASTitle(t *testing.T) {
	cases := []struct {
		name  string
		start string
		title string
		want  string
	}{
		{
			name:  "adopts a real title onto a default-named chat",
			start: api.DefaultChatName,
			title: "Vibekit conversational surface",
			want:  "Vibekit conversational surface",
		},
		{
			name:  "refuses KAS's own placeholder",
			start: api.DefaultChatName,
			title: kasDefaultSessionTitle,
			want:  api.DefaultChatName,
		},
		{
			name:  "refuses an empty title",
			start: api.DefaultChatName,
			title: "",
			want:  api.DefaultChatName,
		},
		{
			name:  "never overwrites a first-prompt label",
			start: "fix the reaper so it stops eating live sessions",
			title: "Reaper fix",
			want:  "fix the reaper so it stops eating live sessions",
		},
		{
			name:  "never overwrites an agent-authored focus title",
			start: "Reaper live-session exemption",
			title: kasDefaultSessionTitle,
			want:  "Reaper live-session exemption",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &api.Chat{Name: tc.start}
			adoptKASTitle(c, tc.title)
			if c.Name != tc.want {
				t.Errorf("adoptKASTitle(%q, %q) left name %q, want %q",
					tc.start, tc.title, c.Name, tc.want)
			}
		})
	}
}

// --- sweepSessionsOnce: the keep-list is chat-referenced UNION live ---

// TestSweepSessionsOnce_KeepListCompleteness pins doubt-retains at the sweep
// boundary, against a real orphan on disk.
//
// A partial keep-list means some chat's sessions are missing from it, so
// sweeping anyway deletes them. Not sweeping only postpones reclaiming disk
// until the next hourly tick. The control arm proves the orphan really was
// reapable, so the incomplete arm is not passing vacuously.
//
// The control's keep-list names a session that EXISTS on disk, rather than being
// empty. An empty keep-list is refused outright by the reaper now
// (kirosession.Sweep) because it is indistinguishable from a misconfigured
// store — that is a separate guard with its own test, and using it as the
// control here would have made this test assert the opposite of it.
func TestSweepSessionsOnce_KeepListCompleteness(t *testing.T) {
	cases := []struct {
		name        string
		complete    bool
		wantSurvive bool
	}{
		{name: "incomplete keep-list spares the orphan", complete: false, wantSurvive: true},
		{name: "complete keep-list reaps it (control)", complete: true, wantSurvive: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sessionsDir := t.TempDir()
			old := time.Now().Add(-24 * time.Hour)
			// An orphan old enough to clear the reaper's create-race guard, plus
			// a referenced sibling so the keep-list is non-empty and the sweep is
			// discriminating rather than refusing.
			orphan := filepath.Join(sessionsDir, "hash01", "sess_orphan")
			kept := filepath.Join(sessionsDir, "hash01", "sess_ref")
			for _, p := range []string{orphan, kept} {
				if err := os.MkdirAll(p, 0o700); err != nil {
					t.Fatalf("mkdir %s: %v", p, err)
				}
				if err := os.Chtimes(p, old, old); err != nil {
					t.Fatalf("chtimes %s: %v", p, err)
				}
			}

			// Wire the reaper at CONSTRUCTION, not after: New starts
			// sweepSessionsLoop, which reads these fields, so assigning them
			// afterwards is a data race (caught by -race, not by plain go test).
			cs := newFakeChatStore()
			h := New("/tmp/work", func() api.ACPBridge { return newFakeBridge() }, cs,
				WithSessionReaper(
					kirosession.New(sessionsDir),
					func(context.Context) (map[string]struct{}, bool) {
						return map[string]struct{}{"sess_ref": {}}, tc.complete
					},
				))
			cs.Bus = h
			t.Cleanup(func() { h.Shutdown() })

			h.sweepSessionsOnce()

			_, err := os.Stat(orphan)
			survived := err == nil
			if survived != tc.wantSurvive {
				t.Errorf("orphan survived = %v, want %v", survived, tc.wantSurvive)
			}
			if _, kErr := os.Stat(kept); kErr != nil {
				t.Errorf("referenced session was reaped: %v", kErr)
			}
		})
	}
}

// TestLiveSessionIDs_CoversEveryBridge pins that the exemption is general.
//
// It used to be one ad-hoc special case for the utility bridge, whose own
// comment named the failure mode: without it the sweep deletes on-disk state
// from under a live subprocess once it ages past the 10-minute guard, because
// that guard is a create-race cushion and not a liveness test. Any bridge
// holding a session no chat references hits the same bug — a parentless run tab
// is the case that made it general.
func TestLiveSessionIDs_CoversEveryBridge(t *testing.T) {
	// newTestHub's factory hands back ONE shared fake so tests can inspect it;
	// this test needs bridges with distinct session ids, so build the hub with
	// a per-spawn factory instead.
	cs := newFakeChatStore()
	h := New("/tmp/work", func() api.ACPBridge { return newFakeBridge() }, cs)
	cs.Bus = h

	setSession := func(chatID api.ChatID, sessionID string) {
		t.Helper()
		sb, _ := h.bridge.mgr.getOrInsert(chatID)
		fb, ok := sb.bridge.(*fakeBridge)
		if !ok {
			t.Fatalf("bridge for %s is not a *fakeBridge", chatID)
		}
		fb.mu.Lock()
		fb.sessionID = sessionID
		fb.mu.Unlock()
	}

	setSession("chatA", "sess_chatA")
	setSession("chatB", "sess_chatB")
	// A bridge that has not started a session yet contributes nothing.
	setSession("chatC", "")

	got := h.liveSessionIDs()
	slices.Sort(got)
	want := []string{"sess_chatA", "sess_chatB"}
	if !slices.Equal(got, want) {
		t.Errorf("liveSessionIDs() = %v, want %v", got, want)
	}
}

// TestPersistNewSessionMetadata_ReportsAModeThatWasNotApplied pins the visibility
// half of the mode contract, and the reason it is needed is subtle enough to state.
//
// applyInitialMode warns and continues when session/set_mode is refused, so the
// session runs the engine's default. persistNewSessionMetadata then writes the
// ACTUAL mode onto the chat, which is right — the mode pill must not claim a role
// the agent is not running under — but it was also the ONLY record of the request.
// So one transient refusal silently and permanently converted a chat pinned to
// "spec" into a default-mode chat: at the next spawn the requested id now EQUALS
// the current one, so applyInitialMode's own guard means no retry is attempted and
// nothing ever says why.
func TestPersistNewSessionMetadata_ReportsAModeThatWasNotApplied(t *testing.T) {
	cases := []struct {
		name       string
		requested  string
		actual     string
		wantReport bool
	}{
		{"a refused mode is reported", "spec", "vibe", true},
		{"the applied mode is not reported", "spec", "spec", false},
		{"a chat that asked for nothing is not reported", "", "vibe", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, cs, br := newTestHub()
			br.mu.Lock()
			br.currentMode = tc.actual
			br.mu.Unlock()
			_ = cs.Mutate(t.Context(), "c1", func(c *api.Chat, _ bool) bool {
				c.Name = "A"
				c.CurrentModeID = tc.requested
				return true
			})

			_, since := h.sse.hub.Bounds()
			h.coord.persistNewSessionMetadata(t.Context(), "c1", br)

			// The record always holds the mode the session is really in.
			c, _ := cs.Get(t.Context(), "c1")
			if c.CurrentModeID != tc.actual {
				t.Errorf("chat.CurrentModeID = %q, want the actual mode %q", c.CurrentModeID, tc.actual)
			}

			var reported bool
			for _, e := range bufferedSince(h, since) {
				var msg api.ServerEvent
				if json.Unmarshal(e.Event.Data, &msg) != nil || msg.Type != api.EventError {
					continue
				}
				// ServerEvent.Payload is an `any`, so round-trip it to read the
				// typed payload back out.
				raw, mErr := json.Marshal(msg.Payload)
				if mErr != nil {
					continue
				}
				var p api.ErrorPayload
				if json.Unmarshal(raw, &p) == nil && p.Code == api.ErrCodeModeNotApplied {
					reported = true
					if !strings.Contains(p.Message, tc.requested) {
						t.Errorf("message %q does not name the requested mode %q", p.Message, tc.requested)
					}
				}
			}
			if reported != tc.wantReport {
				t.Errorf("mode_not_applied reported = %v, want %v", reported, tc.wantReport)
			}
		})
	}
}

// Closing a chat must NOT reap its durable KAS session; deleting one must.
//
// Close shared the delete path, so the × on a tab reaped the chat's whole
// session chain off disk. That broke its own stated contract twice: the chat
// record survived with nothing left to `session/load`, and the History page —
// which lists KAS's sessions, not vibekit's chat files — could only ever show
// chats that were still open, which is exactly how it was reported ("it only
// shows active chats, when i close them they are gone").
//
// The delete arm is the control: without it, a close-preserves assertion would
// also pass if the reaper were simply unwired.
func TestChatTeardown_CloseKeepsSessionDeleteReapsIt(t *testing.T) {
	cases := []struct {
		name        string
		teardown    func(h *Hub, ctx context.Context, id api.ChatID)
		wantSurvive bool
	}{
		{
			name:        "close keeps the session on disk",
			teardown:    func(h *Hub, ctx context.Context, id api.ChatID) { h.CloseChatState(ctx, id) },
			wantSurvive: true,
		},
		{
			name:        "delete reaps it (control)",
			teardown:    func(h *Hub, ctx context.Context, id api.ChatID) { h.CleanupChatState(ctx, id) },
			wantSurvive: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sessionsDir := t.TempDir()
			sessDir := filepath.Join(sessionsDir, "hash01", "sess_owned")
			if err := os.MkdirAll(sessDir, 0o700); err != nil {
				t.Fatalf("mkdir session: %v", err)
			}

			cs := newFakeChatStore()
			h := New("/tmp/work", func() api.ACPBridge { return newFakeBridge() }, cs,
				WithSessionReaper(
					kirosession.New(sessionsDir),
					func(context.Context) (map[string]struct{}, bool) {
						return map[string]struct{}{"sess_owned": {}}, true
					},
				))
			cs.Bus = h
			t.Cleanup(func() { h.Shutdown() })

			ctx := t.Context()
			if err := cs.Mutate(ctx, "c-owner", func(c *api.Chat, _ bool) bool {
				c.Name = "owner"
				c.RecordSession("sess_owned")
				return true
			}); err != nil {
				t.Fatalf("seed chat: %v", err)
			}

			tc.teardown(h, ctx, "c-owner")

			_, err := os.Stat(sessDir)
			survived := err == nil
			if survived != tc.wantSurvive {
				t.Errorf("session survived = %v, want %v", survived, tc.wantSurvive)
			}
		})
	}
}
