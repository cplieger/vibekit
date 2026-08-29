package agent

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
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/kirosession"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// --- helpers ---

// recordingStartBridge records the StartOpts passed to Start while
// behaving like a fakeBridge for every other method.
type recordingStartBridge struct {
	*fakeBridge
	lastStart vibekit.StartOpts
	recMu     sync.Mutex
}

func newRecordingStartBridge() *recordingStartBridge {
	return &recordingStartBridge{fakeBridge: newFakeBridge()}
}

func (b *recordingStartBridge) Start(ctx context.Context, opts *vibekit.StartOpts) error {
	b.recMu.Lock()
	b.lastStart = *opts
	b.recMu.Unlock()
	return b.fakeBridge.Start(ctx, opts)
}

func (b *recordingStartBridge) startOpts() vibekit.StartOpts {
	b.recMu.Lock()
	defer b.recMu.Unlock()
	return b.lastStart
}

func newRecordingStartHub(t *testing.T) (*Runtime, *fakeChatStore, *recordingStartBridge) {
	t.Helper()
	cs := newFakeChatStore()
	rb := newRecordingStartBridge()
	h := New(t.Context(), "/tmp/rec-start", func() ACPBridge { return rb }, cs)
	cs.Bus = h
	h.mcpRegistry.SignalReady()
	return h, cs, rb
}

// recordingPush records the body of each Send on a channel, plus the subject of
// the most recent one (read only after a body has been received, so the
// unsynchronised field is ordered behind the channel handoff).
type recordingPush struct {
	sends chan string
	// reloads counts ReloadPreferences calls, for the SSE reconnect rule. Atomic
	// because the handler that calls it may not be on the test's goroutine.
	reloads atomic.Int32
	subject vibekit.PushSubject
}

func (p *recordingPush) RegisterRoutes(*http.ServeMux)            {}
func (p *recordingPush) Subscribe(vibekit.PushSubscription)       {}
func (p *recordingPush) Unsubscribe(string)                       {}
func (p *recordingPush) HasSubscribers() bool                     { return true }
func (p *recordingPush) SetPreferences(map[vibekit.PushKind]bool) {}
func (p *recordingPush) ReloadPreferences(context.Context)        { p.reloads.Add(1) }
func (p *recordingPush) Close()                                   {}
func (p *recordingPush) Send(_ context.Context, _, body string, _ vibekit.PushKind, subject vibekit.PushSubject) {
	p.subject = subject
	select {
	case p.sends <- body:
	default:
	}
}

// --- OpenBridge overrides + persisted model ---

// On a fresh session/new path the override model wins over the chat's
// stored value, and the persisted chat model is copied from the started
// bridge's ModelID.
func TestGetOrCreateBridge_AppliesOverrides(t *testing.T) {
	h, cs, rb := newRecordingStartHub(t)
	ctx := t.Context()
	_ = cs.Mutate(ctx, "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		c.Model = "m-chat"
		return true // no ACPSessionID -> fresh session/new path
	})

	if _, err := h.coord.OpenBridge(ctx, "c1", "model-override"); err != nil {
		t.Fatalf("OpenBridge: %v", err)
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

// A successful in-session SetModel returns true, and the chat's reasoning-effort
// level is re-applied after the swap.
//
// The re-apply is the load-bearing half. KAS reconciles the session's
// effortLevel against the NEW model's tier list inside its own model handler and
// replaces it with that model's default when the current level is not in the
// list, so a chat sitting at max dropped to the new model's default while the
// chat record and the pill both still read max.
func TestTryFastModelSwitch_SucceedsAndReAppliesEffort(t *testing.T) {
	h, cs, br := newTestHub()
	ctx := t.Context()
	_ = cs.Mutate(ctx, "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; c.Model = "m-old"; return true })
	if _, err := h.coord.OpenBridge(ctx, "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}

	if got := h.coord.TryFastModelSwitch(ctx, "c1", "m-new", "max"); got != true {
		t.Errorf("TryFastModelSwitch(success) = %v, want true", got)
	}
	if got := br.lastEffort(); got != "max" {
		t.Errorf("effort re-applied after the swap = %q, want %q; KAS resets the level inside the model swap", got, "max")
	}
}

// A chat that has chosen no level sends no effort call: there is nothing to
// re-assert, and the service's own reconciliation is the right answer.
func TestTryFastModelSwitch_NoEffortChoiceSendsNoEffortCall(t *testing.T) {
	h, cs, br := newTestHub()
	ctx := t.Context()
	_ = cs.Mutate(ctx, "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; c.Model = "m-old"; return true })
	if _, err := h.coord.OpenBridge(ctx, "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}

	if got := h.coord.TryFastModelSwitch(ctx, "c1", "m-new", ""); got != true {
		t.Errorf("TryFastModelSwitch(success) = %v, want true", got)
	}
	if got := br.lastEffort(); got != "" {
		t.Errorf("effort applied = %q, want none for a chat that chose no level", got)
	}
}

// --- repairEffort: the level KAS changed on its own ---

// A prompt on an ALREADY-OPEN bridge re-asserts the chat's level, which is the
// only checkpoint that catches a level KAS moved without vibekit asking.
//
// Two ways that happens: KAS's own pinSessionModelId settles an unset model on the
// first prompt and reconciles the effort against it, and a model switch made from
// the Kiro IDE or the TUI on a shared session does the same. Neither is a vibekit
// action, so neither the session doors nor the model-switch re-assert sees it.
func TestOpenBridge_RepairsTheEffortOnAnOpenBridge(t *testing.T) {
	h, cs, br := newTestHub()
	ctx := t.Context()
	_ = cs.Mutate(ctx, "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		c.Effort = "max"
		return true
	})
	if _, err := h.coord.OpenBridge(ctx, "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	// Stand in for KAS moving the level underneath vibekit.
	br.mu.Lock()
	br.effort = "high"
	br.mu.Unlock()

	if _, err := h.coord.OpenBridge(ctx, "c1", ""); err != nil {
		t.Fatalf("OpenBridge (reopen): %v", err)
	}

	if got := br.lastEffort(); got != "max" {
		t.Errorf("effort after a prompt on the open bridge = %q, want %q", got, "max")
	}
}

// A chat that has chosen no level, and has no seed to follow, asks for nothing:
// the service's own reconciliation is the right answer and a call would only
// re-impose a level nobody picked.
func TestOpenBridge_RepairsNothingWithoutAChoice(t *testing.T) {
	h, cs, br := newTestHub()
	ctx := t.Context()
	_ = cs.Mutate(ctx, "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })
	if _, err := h.coord.OpenBridge(ctx, "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	br.mu.Lock()
	br.effort = ""
	br.mu.Unlock()

	if _, err := h.coord.OpenBridge(ctx, "c1", ""); err != nil {
		t.Fatalf("OpenBridge (reopen): %v", err)
	}

	if got := br.lastEffort(); got != "" {
		t.Errorf("effort applied = %q, want none for a chat with no choice and no seed", got)
	}
}

// --- effortFor ---

// effortFor prefers the chat's own choice, falls back to the last level the user
// picked anywhere, and refuses a level this build does not know.
func TestEffortFor_PrefersTheChatThenTheSeed(t *testing.T) {
	tests := map[string]struct {
		chatEffort string
		setting    string
		want       string
	}{
		"chat choice wins over the seed":   {chatEffort: "max", setting: "low", want: "max"},
		"seed answers for an unset chat":   {chatEffort: "", setting: "xhigh", want: "xhigh"},
		"no choice and no seed sends none": {chatEffort: "", setting: "", want: ""},
		"an unknown seed level is refused": {chatEffort: "", setting: "turbo", want: ""},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if test.setting != "" {
				body := `{"last_effort":"` + test.setting + `"}`
				if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600); err != nil {
					t.Fatalf("write config.json: %v", err)
				}
			}
			h, _, _ := newTestHub()
			h.coord.lifecycle.configDir = dir

			got := h.coord.effortFor(t.Context(), &vibekit.Chat{ID: "c1", Effort: test.chatEffort})

			if got != test.want {
				t.Errorf("effortFor(chat=%q, last_effort=%q) = %q, want %q",
					test.chatEffort, test.setting, got, test.want)
			}
		})
	}
}

// The seed is a fallback, never a write: resolving it must not stamp the level
// onto the chat record, or that chat stops following the setting forever.
func TestEffortFor_DoesNotWriteTheSeedOntoTheChat(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"last_effort":"max"}`), 0o600); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
	h, _, _ := newTestHub()
	h.coord.lifecycle.configDir = dir
	chat := &vibekit.Chat{ID: "c1"}

	if got := h.coord.effortFor(t.Context(), chat); got != "max" {
		t.Fatalf("effortFor = %q, want max", got)
	}
	if chat.Effort != "" {
		t.Errorf("chat.Effort = %q, want it left empty; the seed is resolved per spawn, not persisted", chat.Effort)
	}
}

// --- Forward clears the registry only when the last bridge exits ---

// When the forwarded bridge is the last one, Forward clears the MCP
// registry; when another bridge remains registered, it must not.
func TestForward_ClearsRegistryOnlyWhenLastBridge(t *testing.T) {
	seed := func(h *Runtime) {
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
		h.bridge.mgr.orInsert("keep")
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
	h := New(t.Context(), "/tmp/push", func() ACPBridge { return newFakeBridge() }, cs, WithPush(fp))
	cs.Bus = h
	h.mcpRegistry.SignalReady()
	ctx := t.Context()
	_ = cs.Mutate(ctx, "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })

	epoch := h.StartTurn(ctx, "c1", vibekit.TurnSourcePrompt)
	resp := &vibekit.RPCResponse{Result: mustJSON(t, map[string]any{"stopReason": "end_turn"})}
	h.SettleTurnOnResponse(ctx, "c1", epoch, 0, resp)

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
	_ = cs.Mutate(ctx, "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		c.Messages = []vibekit.Message{{Role: vibekit.RoleUser, Content: "hi"}}
		return true
	})
	sb, err := h.coord.OpenBridge(ctx, "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	sb.primeReason = primeReasonSwitch

	logs := captureLogs(t)
	h.coord.PrimeIfNeeded(ctx, "c1")
	if got := logs.String(); strings.Contains(got, "prime failed") {
		t.Errorf("unexpected error log on prime success: %s", got)
	}
}

// EmitTurnEndedWithStats logs no persist error when the assistant-turn
// and cancel-event appends both succeed.
func TestEmitTurnEnded_NoPersistErrorLogOnSuccess(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()
	_ = cs.Mutate(ctx, "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })

	epoch, buf := h.stagePromptTurn(t, "c1")
	buf.Started = true
	buf.MessageID = "m-asst"

	logs := captureLogs(t)
	resp := &vibekit.RPCResponse{Result: mustJSON(t, map[string]any{"stopReason": "cancelled"})}
	h.SettleTurnOnResponse(ctx, "c1", epoch, 0, resp)

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
	_ = cs.Mutate(ctx, "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; c.Model = "m-old"; return true })

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
			start: vibekit.DefaultChatName,
			title: "Vibekit conversational surface",
			want:  "Vibekit conversational surface",
		},
		{
			name:  "refuses KAS's own placeholder",
			start: vibekit.DefaultChatName,
			title: kasDefaultSessionTitle,
			want:  vibekit.DefaultChatName,
		},
		{
			name:  "refuses an empty title",
			start: vibekit.DefaultChatName,
			title: "",
			want:  vibekit.DefaultChatName,
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
			c := &vibekit.Chat{Name: tc.start}
			adoptKASTitle(c, tc.title)
			if c.Name != tc.want {
				t.Errorf("adoptKASTitle(%q, %q) left name %q, want %q",
					tc.start, tc.title, c.Name, tc.want)
			}
		})
	}
}

// --- sweepSessionsOnce: the keep-list is chat-referenced UNION live ---

// testReaperWorkDir is the workspace root the reaper fixtures below are built
// for. It is both the runtime's workDir and the root every fixture session claims
// in its own session.json, because the reaper reaps only for the workspace it was
// constructed with.
const testReaperWorkDir = "/tmp/work"

// writeSessionRecord writes the session.json the reaper reads to decide whether a
// session belongs to its workspace. A fixture without one is DOUBT, which the
// reaper answers by retaining — correct in production and vacuous in a test that
// wants to observe a reap.
func writeSessionRecord(t *testing.T, sessionDir, workspaceRoot string) {
	t.Helper()
	body := `{"workspacePaths":["` + workspaceRoot + `"]}`
	if err := os.WriteFile(filepath.Join(sessionDir, "session.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write session record in %s: %v", sessionDir, err)
	}
}

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
				// The reaper reads each candidate's own workspacePaths and skips
				// anything that does not name the workspace it was built for, so a
				// fixture without this record is retained whatever the keep-list
				// says — which would make the control arm pass for the wrong reason.
				writeSessionRecord(t, p, testReaperWorkDir)
				if err := os.Chtimes(p, old, old); err != nil {
					t.Fatalf("chtimes %s: %v", p, err)
				}
			}

			// Wire the reaper at CONSTRUCTION, not after: New starts
			// sweepSessionsLoop, which reads these fields, so assigning them
			// afterwards is a data race (caught by -race, not by plain go test).
			cs := newFakeChatStore()
			h := New(t.Context(), testReaperWorkDir, func() ACPBridge { return newFakeBridge() }, cs,
				WithSessionReaper(
					kirosession.New(sessionsDir, testReaperWorkDir),
					func(context.Context) (map[string]struct{}, bool) {
						return map[string]struct{}{"sess_ref": {}}, tc.complete
					},
				))
			cs.Bus = h
			t.Cleanup(func() { shutdownHub(t, h) })

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
	// this test needs bridges with distinct session ids, so build the runtime with
	// a per-spawn factory instead.
	cs := newFakeChatStore()
	h := New(t.Context(), testReaperWorkDir, func() ACPBridge { return newFakeBridge() }, cs)
	cs.Bus = h

	setSession := func(chatID vibekit.ChatID, sessionID string) {
		t.Helper()
		sb, _ := h.bridge.mgr.orInsert(chatID)
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

// TestApplyLoadedSessionFacts_KeepsWhatTheResultOmitted pins the resume half of
// the catalog contract: a fact the load result did not carry must not be written.
//
// A resumed bridge is freshly constructed, so it answers the zero value for
// anything absent — and `session/load` omits the model catalog routinely, because
// KAS resolves ListAvailableModels asynchronously (measured on kiro-cli 2.20.0).
// Writing those zeros wiped the catalog the chat file had carried since its
// previous session. The mode half is the one with no repair channel afterwards.
func TestApplyLoadedSessionFacts_KeepsWhatTheResultOmitted(t *testing.T) {
	seededModes := []vibekit.SessionMode{{ID: "spec", Name: "Spec"}}
	seededModels := []vibekit.SessionModel{{ID: "seeded-model", Name: "Seeded"}}

	cases := map[string]struct {
		mode       string
		modes      []vibekit.SessionMode
		models     []vibekit.SessionModel
		wantMode   string
		wantModes  []vibekit.SessionMode
		wantModels []vibekit.SessionModel
	}{
		"a silent result changes nothing": {
			mode: "", modes: nil, models: nil,
			wantMode: "spec", wantModes: seededModes, wantModels: seededModels,
		},
		"an empty list is not an empty catalog": {
			mode: "", modes: []vibekit.SessionMode{}, models: []vibekit.SessionModel{},
			wantMode: "spec", wantModes: seededModes, wantModels: seededModels,
		},
		"what the result DOES carry is written": {
			mode:  "vibe",
			modes: []vibekit.SessionMode{{ID: "vibe", Name: "Default"}},
			models: []vibekit.SessionModel{
				{ID: "fresh-model", Name: "Fresh"},
			},
			wantMode:   "vibe",
			wantModes:  []vibekit.SessionMode{{ID: "vibe", Name: "Default"}},
			wantModels: []vibekit.SessionModel{{ID: "fresh-model", Name: "Fresh"}},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := &vibekit.Chat{
				Name:            "A",
				CurrentModeID:   "spec",
				AvailableModes:  seededModes,
				AvailableModels: seededModels,
			}
			br := &fakeBridge{currentMode: tc.mode, modes: tc.modes, models: tc.models}

			applyLoadedSessionFacts(c, br, "")

			if c.CurrentModeID != tc.wantMode {
				t.Errorf("CurrentModeID = %q, want %q", c.CurrentModeID, tc.wantMode)
			}
			if !slices.Equal(c.AvailableModes, tc.wantModes) {
				t.Errorf("AvailableModes = %v, want %v", c.AvailableModes, tc.wantModes)
			}
			if !slices.Equal(c.AvailableModels, tc.wantModels) {
				t.Errorf("AvailableModels = %v, want %v", c.AvailableModels, tc.wantModels)
			}
		})
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
			_ = cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
				c.Name = "A"
				c.CurrentModeID = tc.requested
				return true
			})

			_, since := h.bus.fanout.Bounds()
			h.coord.persistNewSessionMetadata(t.Context(), "c1", br)

			// The record always holds the mode the session is really in.
			c, _ := cs.Get(t.Context(), "c1")
			if c.CurrentModeID != tc.actual {
				t.Errorf("chat.CurrentModeID = %q, want the actual mode %q", c.CurrentModeID, tc.actual)
			}

			var reported bool
			for _, e := range bufferedSince(h, since) {
				var msg vibekit.ServerEvent
				if json.Unmarshal(e.Event.Data, &msg) != nil || msg.Type != vibekit.EventError {
					continue
				}
				// ServerEvent.Payload is an `any`, so round-trip it to read the
				// typed payload back out.
				raw, mErr := json.Marshal(msg.Payload)
				if mErr != nil {
					continue
				}
				var p vibekit.ErrorPayload
				if json.Unmarshal(raw, &p) == nil && p.Code == vibekit.ErrCodeModeNotApplied {
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
		teardown    func(h *Runtime, ctx context.Context, id vibekit.ChatID)
		wantSurvive bool
	}{
		{
			name:        "close keeps the session on disk",
			teardown:    func(h *Runtime, ctx context.Context, id vibekit.ChatID) { h.CloseChatState(ctx, id) },
			wantSurvive: true,
		},
		{
			name:        "delete reaps it (control)",
			teardown:    func(h *Runtime, ctx context.Context, id vibekit.ChatID) { h.DeleteChatState(ctx, id) },
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
			writeSessionRecord(t, sessDir, testReaperWorkDir)

			cs := newFakeChatStore()
			h := New(t.Context(), testReaperWorkDir, func() ACPBridge { return newFakeBridge() }, cs,
				WithSessionReaper(
					kirosession.New(sessionsDir, testReaperWorkDir),
					func(context.Context) (map[string]struct{}, bool) {
						return map[string]struct{}{"sess_owned": {}}, true
					},
				))
			cs.Bus = h
			t.Cleanup(func() { shutdownHub(t, h) })

			ctx := t.Context()
			if err := cs.Mutate(ctx, "c-owner", func(c *vibekit.Chat, _ bool) bool {
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

// TestChatTeardown_DeleteByChainReapsWithoutTheRecord is the close
// escalation's grade: the record is already deleted when the teardown runs, so
// the reap is driven from the chain captured before the commit. The
// record-reading grade is the control — on a recordless chat it must leave the
// session, which is precisely the silent no-op the chain-shaped seam bypasses.
func TestChatTeardown_DeleteByChainReapsWithoutTheRecord(t *testing.T) {
	cases := []struct {
		name        string
		teardown    func(h *Runtime, ctx context.Context, id vibekit.ChatID)
		wantSurvive bool
	}{
		{
			name: "the captured chain reaps with the record gone",
			teardown: func(h *Runtime, ctx context.Context, id vibekit.ChatID) {
				h.DeleteChatStateByChain(ctx, id, []string{"sess_owned"})
			},
			wantSurvive: false,
		},
		{
			name:        "the record-reading grade no-ops without one (control)",
			teardown:    func(h *Runtime, ctx context.Context, id vibekit.ChatID) { h.DeleteChatState(ctx, id) },
			wantSurvive: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sessionsDir := t.TempDir()
			sessDir := filepath.Join(sessionsDir, "hash01", "sess_owned")
			if err := os.MkdirAll(sessDir, 0o700); err != nil {
				t.Fatalf("mkdir session: %v", err)
			}
			writeSessionRecord(t, sessDir, testReaperWorkDir)

			cs := newFakeChatStore()
			h := New(t.Context(), testReaperWorkDir, func() ACPBridge { return newFakeBridge() }, cs,
				WithSessionReaper(
					kirosession.New(sessionsDir, testReaperWorkDir),
					func(context.Context) (map[string]struct{}, bool) {
						return map[string]struct{}{"sess_owned": {}}, true
					},
				))
			cs.Bus = h
			t.Cleanup(func() { shutdownHub(t, h) })

			// NO chat record: the escalation deleted it inside the close commit.
			tc.teardown(h, t.Context(), "c-doomed")

			_, err := os.Stat(sessDir)
			survived := err == nil
			if survived != tc.wantSurvive {
				t.Errorf("session survived = %v, want %v", survived, tc.wantSurvive)
			}
		})
	}
}

// TestSessionLoad_HealsTheChatsRestartPausedRuns is the recovery model for
// agent-launched runs, and the reason there is no Resume button anywhere.
//
// A restart kills a chat's bridge, which KAS reconciles by PAUSING the runs that
// bridge launched. The user's next message respawns the bridge, and this sweep is
// what makes the run heal with the chat. Without it a restart leaves every
// agent-launched run parked with nothing in the product able to restart it.
//
// The sweep runs off the spawn path deliberately — the user's prompt must not wait
// behind a run-list round trip — so the resume is awaited rather than assumed. The
// wait fails closed: a sweep that never ran reports that, instead of passing
// whenever the goroutine happened to win.
func TestSessionLoad_HealsTheChatsRestartPausedRuns(t *testing.T) {
	h, cs, br := newTestHub()
	const chatID vibekit.ChatID = "c1"
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowList: kasRuns(t, map[string]any{
			"workflowId": "wf_1", "status": "paused", "parentSessionId": "sess_owned",
		}),
		methodKiroWorkflowInspect: inspectPaused(t, "wf_1", stalePauseReason),
		methodKiroWorkflowResume:  json.RawMessage(`{}`),
	}
	if err := cs.Mutate(t.Context(), chatID, func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		c.RecordSession("sess_owned")
		return true
	}); err != nil {
		t.Fatalf("seed the chat: %v", err)
	}

	if _, err := h.coord.OpenBridge(t.Context(), chatID, ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}

	stop := time.Now().Add(5 * time.Second)
	for !slices.Contains(br.callLog(), methodKiroWorkflowResume) {
		if time.Now().After(stop) {
			t.Fatalf("a rehydrated session never resumed the run a restart paused; calls were %v",
				br.callLog())
		}
		time.Sleep(time.Millisecond)
	}
}

// TurnFoldTarget reads the chat store only when it has to OPEN a turn, not on
// every folded frame.
//
// It opened by asking for the two facts a turn records at open — the answering
// model and the credit baseline — and that read is chat.Store.Get: a per-chat
// mutex, a whole-file read and a json.Unmarshal of the entire message history,
// per streamed delta and per tool frame. The cost scales with the TRANSCRIPT
// rather than the frame, it contends with every persist on the same chat, and it
// runs on the only consumer of a 256-slot channel, so a long conversation could
// stall the read loop under its own bookkeeping. No benchmark could see it: the
// fold target in the translate benchmarks is a fake over a local map.
func TestTurnFoldTarget_ReadsTheChatOnlyWhenItOpensATurn(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()
	const chatID vibekit.ChatID = "c1"
	_ = cs.Mutate(ctx, chatID, func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })

	// The first frame has no turn to fold into, so it opens one and pays for the facts.
	h.coord.TurnFoldTarget(ctx, chatID)
	before := cs.Gets.Load()

	for range 20 {
		h.coord.TurnFoldTarget(ctx, chatID)
	}

	if got := cs.Gets.Load(); got != before {
		t.Errorf("chat reads = %d after 20 folded frames, want %d: the fold path reads and "+
			"unmarshals the whole chat file per frame", got-before, 0)
	}
}
