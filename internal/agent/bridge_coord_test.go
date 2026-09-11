package agent

// Tests for bridge_coord.go: override application, fast model switch, registry
// teardown on the last bridge, turn-ended push behaviour, and the silent successes.

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
	"github.com/cplieger/vibekit/internal/translate"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// --- helpers ---

// recordingStartBridge records the StartOpts passed to Start, else a fakeBridge.
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
// the most recent one, read only after a body arrives so the field is ordered.
type recordingPush struct {
	sends chan string
	// reloads counts ReloadPreferences calls, for the SSE reconnect rule. Atomic
	// because the handler that calls it may not be on the test's goroutine.
	reloads atomic.Int32
	// noSubs flips HasSubscribers to false for the drop path. The zero value keeps a
	// subscriber present, so every fixture that predates it is unchanged.
	noSubs  atomic.Bool
	subject vibekit.PushSubject
}

func (p *recordingPush) RegisterRoutes(*http.ServeMux)            {}
func (p *recordingPush) Subscribe(vibekit.PushSubscription)       {}
func (p *recordingPush) Unsubscribe(string)                       {}
func (p *recordingPush) HasSubscribers() bool                     { return !p.noSubs.Load() }
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

// On a fresh session/new path the override model wins over the chat's stored value,
// and the persisted model is copied from the started bridge's ModelID.
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
// level is re-applied after the swap. The re-apply is the load-bearing half: KAS
// reconciles the session's effortLevel against the NEW model's tier list, so a chat
// at max dropped to the new default while the record and the pill still read max.
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

// The fast path CLOSES whatever turn is open before it swaps, which is what the
// restart fallback has always done through its own flush.
//
// An engine-opened turn holds no admission reservation, so nothing refuses a
// switch that lands mid-turn — a second device, or the client's queue draining
// while a workflow step's turn folds onto the launching chat. The
// `model_switched` row this path persists is not turn-terminal, so without the
// flush it is written into the body of a turn the switch had nothing to do with.
func TestTryFastModelSwitch_ClosesTheTurnInFlight(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()
	_ = cs.Mutate(ctx, "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; c.Model = "m-old"; return true })
	if _, err := h.coord.OpenBridge(ctx, "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	h.coord.StartTurn(ctx, "c1", vibekit.TurnSourceWireTurnStart)
	buf := h.stageTurnBuffer(t, "c1")
	buf.Started = true
	buf.MessageID = newMessageID()
	buf.Content.WriteString("the step's reply, still streaming")
	if _, open := h.coord.turns.openEpoch("c1"); !open {
		t.Fatal("the fixture left no turn open, so there is nothing for the switch to close")
	}

	if got := h.coord.TryFastModelSwitch(ctx, "c1", "m-new", ""); !got {
		t.Fatalf("TryFastModelSwitch = %v, want true", got)
	}

	if epoch, open := h.coord.turns.openEpoch("c1"); open {
		t.Errorf("turn %d is still open after the fast switch; the model_switched row lands in its body", epoch)
	}
	// The content already on every client's screen is persisted rather than
	// discarded: the session survives the fast swap, so nothing licenses dropping
	// a reply somebody else's turn produced.
	if got := assistantMessages(t, cs, "c1"); len(got) != 1 {
		t.Errorf("persisted %d assistant messages, want the displaced step's reply", len(got))
	}
}

// The other side of that guard: the caller's OWN prompt turn survives the swap.
//
// The restart fallback discards an in-flight turn because the bridge goes with
// it; the fast path keeps the session, so a turn that was going to finish still
// finishes. Widening the guard to any open turn destroyed exactly this.
func TestTryFastModelSwitch_LeavesThePromptsOwnTurnOpen(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()
	_ = cs.Mutate(ctx, "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; c.Model = "m-old"; return true })
	if _, err := h.coord.OpenBridge(ctx, "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	epoch := h.coord.StartTurn(ctx, "c1", vibekit.TurnSourcePrompt)
	buf := h.stageTurnBuffer(t, "c1")
	buf.Started = true
	buf.MessageID = newMessageID()
	buf.Content.WriteString("the user's own reply, still streaming")

	if got := h.coord.TryFastModelSwitch(ctx, "c1", "m-new", ""); !got {
		t.Fatalf("TryFastModelSwitch = %v, want true", got)
	}

	if open, ok := h.coord.turns.openEpoch("c1"); !ok || open != epoch {
		t.Errorf("open turn = %d (open %t), want the prompt's own %d", open, ok, epoch)
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

// A prompt on an ALREADY-OPEN bridge re-asserts the chat's level, the only
// checkpoint that catches a level KAS moved without vibekit asking:
// pinSessionModelId settling an unset model on the first prompt, or a switch made
// from the Kiro IDE or TUI on a shared session. Neither is a vibekit action.
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

// A chat that has chosen no level, and has no seed to follow, asks for nothing: a
// call would only re-impose a level nobody picked.
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
// picked anywhere — but only when that pick was made under the chat's OWN model —
// and refuses a level too malformed to be a tier id. Shape only: the tier
// vocabulary is per model and KAS's to judge, so a well-formed unknown seed flows.
func TestEffortFor_PrefersTheChatThenTheSeed(t *testing.T) {
	tests := map[string]struct {
		chatEffort string
		chatModel  string
		setting    string
		seedModel  string
		want       string
	}{
		"chat choice wins over the seed":       {chatEffort: "max", chatModel: "m1", setting: "low", seedModel: "m1", want: "max"},
		"seed answers for an unset chat":       {chatEffort: "", chatModel: "m1", setting: "xhigh", seedModel: "m1", want: "xhigh"},
		"no choice and no seed sends none":     {chatEffort: "", chatModel: "m1", setting: "", seedModel: "", want: ""},
		"a malformed seed level is refused":    {chatEffort: "", chatModel: "m1", setting: "TURBO", seedModel: "m1", want: ""},
		"a well-formed unknown level flows":    {chatEffort: "", chatModel: "m1", setting: "none", seedModel: "m1", want: "none"},
		"a seed picked under ANOTHER model":    {chatEffort: "", chatModel: "m2", setting: "max", seedModel: "m1", want: ""},
		"a pairless seed (pre-pair install)":   {chatEffort: "", chatModel: "m1", setting: "max", seedModel: "", want: ""},
		"a modelless chat never takes a seed":  {chatEffort: "", chatModel: "", setting: "max", seedModel: "m1", want: ""},
		"the choice survives a model mismatch": {chatEffort: "high", chatModel: "m2", setting: "max", seedModel: "m1", want: "high"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if test.setting != "" {
				body := `{"last_effort":"` + test.setting + `","last_effort_model":"` + test.seedModel + `"}`
				if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600); err != nil {
					t.Fatalf("write config.json: %v", err)
				}
			}
			h, _, _ := newTestHub()
			h.coord.lifecycle.configDir = dir

			got := h.coord.effortFor(t.Context(), &vibekit.Chat{ID: "c1", Effort: test.chatEffort, Model: test.chatModel})

			if got != test.want {
				t.Errorf("effortFor(chat=%q, model=%q, last_effort=%q under %q) = %q, want %q",
					test.chatEffort, test.chatModel, test.setting, test.seedModel, got, test.want)
			}
		})
	}
}

// EffortForSwitch resolves against the TARGET model: the seed when it was picked
// under that model, else the target's own default from the WORKSPACE catalog.
func TestEffortForSwitch_SeedThenModelDefault(t *testing.T) {
	catalog := []vibekit.SessionModel{
		{ID: "m1", DefaultEffortLevel: "high"},
		{ID: "m2", DefaultEffortLevel: "medium"},
	}
	tests := map[string]struct {
		setting   string
		seedModel string
		target    string
		want      string
	}{
		"seed picked under the target wins":       {setting: "max", seedModel: "m2", target: "m2", want: "max"},
		"seed under another model yields default": {setting: "max", seedModel: "m1", target: "m2", want: "medium"},
		"no seed yields the target's default":     {setting: "", seedModel: "", target: "m1", want: "high"},
		"unknown target yields nothing":           {setting: "", seedModel: "", target: "m9", want: ""},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if test.setting != "" {
				body := `{"last_effort":"` + test.setting + `","last_effort_model":"` + test.seedModel + `"}`
				if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600); err != nil {
					t.Fatalf("write config.json: %v", err)
				}
			}
			h, _, _ := newTestHub()
			h.coord.lifecycle.configDir = dir
			h.coord.catalog.SetModels(catalog)

			got := h.coord.EffortForSwitch(t.Context(), test.target)

			if got != test.want {
				t.Errorf("EffortForSwitch(target=%q, seed=%q under %q) = %q, want %q — the chat's own choice must never leak into a switch",
					test.target, test.setting, test.seedModel, got, test.want)
			}
		})
	}
}

// The seed is a fallback, never a write: resolving it must not stamp the level
// onto the chat record, or that chat stops following the setting forever.
func TestEffortFor_DoesNotWriteTheSeedOntoTheChat(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"last_effort":"max","last_effort_model":"m1"}`), 0o600); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
	h, _, _ := newTestHub()
	h.coord.lifecycle.configDir = dir
	chat := &vibekit.Chat{ID: "c1", Model: "m1"}

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

// KAS reviews a whole turn at once, so there is no per-turn trust gate to test.

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

// TestAdoptKASTitle pins all four arms of the guard. Every refusal here is a bug
// that compiles cleanly: adopting KAS's "New Session" placeholder makes the chat
// non-default-named, which then rejects the real title that arrives later, and
// adopting over an existing name clobbers a label that outranks this channel.
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
			title: translate.KASDefaultSessionTitle,
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
			title: translate.KASDefaultSessionTitle,
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

// This rung reads what KAS STORED, so it gets the SAME door treatment the live focus
// channel gets — sanitizer, bound, rune cap and shape rules — and the cases below are
// one per part of it. A stored title is not the safer input: KAS keeps its own session
// title independently of vibekit's chat name, nothing bounded or sanitized it on the
// way in, a session titled by a pre-gate build re-offers that string on every resume,
// and a rename from the IDE or the TUI can put one there at any time. It is also the
// worse door, because a resume names a chat whose record was recreated and is
// therefore default-named, which is exactly the state this rung adopts into.
func TestAdoptKASTitle_AppliesTheWholeDoorTreatment(t *testing.T) {
	tests := []struct {
		name  string
		title string
		want  string
	}{
		{
			// Verbatim from the live volume's poisoned chat record.
			name:  "a_stored_model_refusal",
			title: "I need more context to generate a title. Could you share the user's first mes...",
			want:  vibekit.DefaultChatName,
		},
		{
			name:  "a_stored_truncation_of_the_first_prompt",
			title: "Safari on Mac throws this console error for vibekit: [Error] ResizeObserver l...",
			want:  vibekit.DefaultChatName,
		},
		{
			// Nothing on the wire bounds this field and a stored title is not
			// Ete-capped, so the OUTCOME is what this pins: an arbitrarily long
			// string never reaches Chat.Name. Two rules refuse it independently —
			// the sanitizer's bound leaves a "..." marker the truncation rule
			// catches, and 515 runes is over the cap either way — so retuning one
			// of them cannot open it. The log test below is what pins the bound.
			name:  "an_unbounded_stored_title",
			title: strings.Repeat("x", 700),
			want:  vibekit.DefaultChatName,
		},
		{
			// The sanitizer, and the reason it runs before the rules rather than
			// after them: the stored form is what gets compared and kept.
			name:  "a_stored_title_carrying_ansi_and_a_newline",
			title: "\x1b[31mRelease\x1b[0m\ncheck",
			want:  "Release check",
		},
		{
			name:  "a_real_stored_title_is_still_adopted",
			title: "Fix ResizeObserver Error In Safari",
			want:  "Fix ResizeObserver Error In Safari",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &vibekit.Chat{Name: vibekit.DefaultChatName}
			adoptKASTitle(c, tc.title)
			if c.Name != tc.want {
				t.Errorf("adoptKASTitle(%q) left name %q, want %q", tc.title, c.Name, tc.want)
			}
		})
	}
}

// The refusal line is the one place a title vibekit did NOT adopt still reaches an
// operator, and it is the same untrusted string: nothing on the wire bounds the field
// and a stored title is not Ete-capped. So the line carries the SANITIZED form plus the
// rule that fired. Logging the raw argument instead is a one-word edit that puts
// unbounded control-bearing text into the log store, and nothing else would notice.
func TestAdoptKASTitle_LogsTheSanitizedTitleWithItsReason(t *testing.T) {
	logs := captureLogs(t)
	stored := "\x1b[31m" + strings.Repeat("x", 700) + "\nmore"

	adoptKASTitle(&vibekit.Chat{Name: vibekit.DefaultChatName}, stored)

	var rec struct {
		Title  string `json:"title"`
		Reason string `json:"reason"`
	}
	line := strings.TrimSpace(logs.String())
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("adoptKASTitle logged %q, want one JSON record: %v", line, err)
	}
	if rec.Reason == "" {
		t.Errorf("adoptKASTitle logged reason %q, want the rule that fired", rec.Reason)
	}
	if strings.ContainsAny(rec.Title, "\x1b\n") {
		t.Errorf("adoptKASTitle logged title %q, want it sanitized of ANSI and newlines", rec.Title)
	}
	if len(rec.Title) >= len(stored) {
		t.Errorf("adoptKASTitle logged %d title bytes for a %d-byte stored title, want it bounded",
			len(rec.Title), len(stored))
	}
}

// --- sweepSessionsOnce: the keep-list is chat-referenced UNION live ---

// testReaperWorkDir is the workspace root the reaper fixtures are built for: both
// the runtime's workDir and the root every fixture session claims in its own
// session.json, because the reaper reaps only for the workspace it was built with.
const testReaperWorkDir = "/tmp/work"

// writeSessionRecord writes the session.json the reaper reads to decide whether a
// session belongs to its workspace. A fixture without one is DOUBT, which the
// reaper answers by retaining — correct in production and vacuous in a reap test.
func writeSessionRecord(t *testing.T, sessionDir, workspaceRoot string) {
	t.Helper()
	body := `{"workspacePaths":["` + workspaceRoot + `"]}`
	if err := os.WriteFile(filepath.Join(sessionDir, "session.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write session record in %s: %v", sessionDir, err)
	}
}

// TestSweepSessionsOnce_KeepListCompleteness pins doubt-retains at the sweep boundary against
// a real orphan on disk: a partial keep-list means some chat's sessions are missing from it,
// so sweeping anyway deletes them, where not sweeping only postpones reclaiming disk. The
// control arm proves the orphan really was reapable. Its keep-list names a session that EXISTS
// on disk rather than being empty, because an empty keep-list is refused outright by the
// reaper — using it as the control would make this test assert the opposite of that guard.
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

// TestLiveSessionIDs_CoversEveryBridge pins that the exemption is general: any
// bridge holding a session no chat references would otherwise have its on-disk
// state deleted from under it once the session ages past the 10-minute guard,
// which is a create-race cushion and not a liveness test.
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
// the mode contract: a fact the load result did not carry must not be written. A
// resumed bridge is freshly constructed, so it answers the zero value for anything
// absent, and writing those zeros wiped what the chat file had carried since its
// previous session. The CATALOGS are not written here — Catalog owns that rule.
func TestApplyLoadedSessionFacts_KeepsWhatTheResultOmitted(t *testing.T) {
	cases := map[string]struct {
		mode     string
		wantMode string
	}{
		"a silent result changes nothing":       {mode: "", wantMode: "spec"},
		"what the result DOES carry is written": {mode: "vibe", wantMode: "vibe"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := &vibekit.Chat{Name: "A", CurrentModeID: "spec"}
			br := &fakeBridge{currentMode: tc.mode}

			applyLoadedSessionFacts(c, br, "")

			if c.CurrentModeID != tc.wantMode {
				t.Errorf("CurrentModeID = %q, want %q", c.CurrentModeID, tc.wantMode)
			}
		})
	}
}

// TestApplyLoadedSessionFacts_KeepsContextThresholds pins the same keep-on-absent
// contract one layer up: a resumed bridge is freshly constructed, so it answers 0 for a
// threshold the load result omitted, and writing that zero would replace a pair the chat
// file has carried since its previous session.
func TestApplyLoadedSessionFacts_KeepsContextThresholds(t *testing.T) {
	cases := map[string]struct {
		summarization, truncation         float64
		wantSummarization, wantTruncation float64
	}{
		"a silent result keeps both":         {wantSummarization: 80, wantTruncation: 95},
		"what the result carries is written": {summarization: 85, truncation: 97, wantSummarization: 85, wantTruncation: 97},
		"one carried member keeps the other": {summarization: 85, wantSummarization: 85, wantTruncation: 95},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := &vibekit.Chat{Name: "A"}
			c.Usage.SummarizationThresholdPct = 80
			c.Usage.TruncationThresholdPct = 95
			br := &fakeBridge{summarizationPct: tc.summarization, truncationPct: tc.truncation}

			applyLoadedSessionFacts(c, br, "")

			if c.Usage.SummarizationThresholdPct != tc.wantSummarization {
				t.Errorf("SummarizationThresholdPct = %v, want %v",
					c.Usage.SummarizationThresholdPct, tc.wantSummarization)
			}
			if c.Usage.TruncationThresholdPct != tc.wantTruncation {
				t.Errorf("TruncationThresholdPct = %v, want %v",
					c.Usage.TruncationThresholdPct, tc.wantTruncation)
			}
		})
	}
}

// TestPersistNewSessionMetadata_ReportsAModeThatWasNotApplied pins the visibility half of the
// mode contract. applyInitialMode warns and continues when session/set_mode is refused, so the
// session runs the engine's default, and persistNewSessionMetadata then writes the ACTUAL mode
// onto the chat — right, because the pill must not claim a role the agent is not running under,
// but also the only record of the request. So one transient refusal permanently converts a chat
// pinned to "spec" into a default-mode chat: at the next spawn the ids match, so the guard
// skips the retry and nothing says why.
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

// Closing a chat must NOT reap its durable KAS session; deleting one must. Sharing the delete
// path breaks the contract twice: the chat record survives with nothing left to
// `session/load`, and the History page — which lists KAS's sessions, not vibekit's chat files
// — can only ever show chats that are still open. The delete arm is the control: without it, a
// close-preserves assertion would also pass if the reaper were simply unwired.
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

// TestChatTeardown_DeleteByChainReapsWithoutTheRecord is the close escalation's
// grade: the record is already deleted when the teardown runs, so the reap is
// driven from the chain captured before the commit. The record-reading grade is the
// control — on a recordless chat it must leave the session.
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

// TestSessionLoad_HealsTheChatsRestartPausedRuns is the recovery model for agent-launched
// runs, and the reason there is no Resume button anywhere. A restart kills a chat's bridge,
// which KAS reconciles by PAUSING the runs that bridge launched; the user's next message
// respawns it and this sweep makes the run heal with the chat. The sweep runs OFF the spawn
// path deliberately — the prompt must not wait behind a run-list round trip — so the resume is
// awaited rather than assumed, and the wait fails closed.
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

// TurnFoldTarget reads the chat store only when it has to OPEN a turn, not on every folded
// frame. The two facts a turn records at open — the answering model and the credit baseline —
// come from chat.Store.Get: a per-chat mutex, a whole-file read and a json.Unmarshal of the
// entire history, per streamed delta and per tool frame. The cost scales with the TRANSCRIPT,
// it contends with every persist on the same chat, and it runs on the only consumer of a
// 256-slot channel. No benchmark sees it: the translate benchmarks' fold target is a fake.
func TestTurnFoldTarget_ReadsTheChatOnlyWhenItOpensATurn(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()
	const chatID vibekit.ChatID = "c1"
	_ = cs.Mutate(ctx, chatID, func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })

	// The first frame has no turn to fold into, so it opens one and pays for the facts.
	h.coord.TurnFoldTarget(ctx, chatID, vibekit.TurnSourceWireTurnStart)
	before := cs.Gets.Load()

	for range 20 {
		h.coord.TurnFoldTarget(ctx, chatID, vibekit.TurnSourceWireTurnStart)
	}

	if got := cs.Gets.Load(); got != before {
		t.Errorf("chat reads = %d after 20 folded frames, want %d: the fold path reads and "+
			"unmarshals the whole chat file per frame", got-before, 0)
	}
}

func TestOpenTurnBuffer_DoesNotOpenATurn(t *testing.T) {
	h, cs, _ := newTestHub()
	before := cs.Gets.Load()

	if buf, ok := h.coord.OpenTurnBuffer("c1"); ok || buf != nil {
		t.Errorf("OpenTurnBuffer(no open turn) = (%v, %t), want (nil, false)", buf, ok)
	}
	if _, open := h.coord.turns.openEpoch("c1"); open {
		t.Error("OpenTurnBuffer opened a turn")
	}
	if got := cs.Gets.Load(); got != before {
		t.Errorf("chat reads = %d, want %d", got, before)
	}
}

func TestApplyLoadedSessionFacts_RefreshesTheEntitlementSet(t *testing.T) {
	cases := map[string]struct {
		catalog []vibekit.SessionModel
		want    []string
	}{
		"absent keeps the seed": {want: []string{"seed"}},
		"present replaces the seed": {
			catalog: []vibekit.SessionModel{{ID: "old", Description: "[Deprecated]"}, {ID: "new"}},
			want:    []string{"old", "new"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			chat := &vibekit.Chat{ServedModelIDs: []string{"seed"}}
			applyLoadedSessionFacts(chat, &fakeBridge{catalog: tc.catalog}, "")
			if !slices.Equal(chat.ServedModelIDs, tc.want) {
				t.Errorf("ServedModelIDs = %v, want %v", chat.ServedModelIDs, tc.want)
			}
		})
	}
}
