package hub

// Mutant-killing tests for unit vibekit-u1 (internal/hub).
//
// Targets the surviving gremlins mutants in agent_terminal.go,
// bridge_coord.go, bridge_fs_path.go, bridge_fs_read.go, and
// bridge_fs_staging.go. Tests only; no production code is edited.
// Reuses the package's existing fakes/helpers (newTestHub, hubForFSTest,
// hubWithWorkDir, mustJSON, lastReplayEventOfType, etc.).

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/api"
)

// --- helpers ---

func gk_vibekit_u1_ptr(i int) *int { return &i }

// gk_vibekit_u1_safeBuf is a mutex-guarded byte buffer so the capturing
// slog handler (which may be written from background goroutines) and the
// test's read are race-free under -race.
type gk_vibekit_u1_safeBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *gk_vibekit_u1_safeBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *gk_vibekit_u1_safeBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// gk_vibekit_u1_captureLogs swaps the default slog logger for one that
// writes JSON into a buffer and restores it at test end. Tests using it
// must NOT call t.Parallel (global slog default).
func gk_vibekit_u1_captureLogs(t *testing.T) *gk_vibekit_u1_safeBuf {
	t.Helper()
	out := &gk_vibekit_u1_safeBuf{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(out, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return out
}

// gk_vibekit_u1_recBridge records the StartOpts passed to Start while
// behaving like a fakeBridge for every other method.
type gk_vibekit_u1_recBridge struct {
	*fakeBridge
	recMu     sync.Mutex
	lastStart api.StartOpts
}

func gk_vibekit_u1_newRecBridge() *gk_vibekit_u1_recBridge {
	return &gk_vibekit_u1_recBridge{fakeBridge: newFakeBridge()}
}

func (b *gk_vibekit_u1_recBridge) Start(ctx context.Context, opts *api.StartOpts) error {
	b.recMu.Lock()
	b.lastStart = *opts
	b.recMu.Unlock()
	return b.fakeBridge.Start(ctx, opts)
}

func (b *gk_vibekit_u1_recBridge) startOpts() api.StartOpts {
	b.recMu.Lock()
	defer b.recMu.Unlock()
	return b.lastStart
}

func gk_vibekit_u1_newRecHub(t *testing.T) (*Hub, *fakeChatStore, *gk_vibekit_u1_recBridge) {
	t.Helper()
	cs := newFakeChatStore()
	rb := gk_vibekit_u1_newRecBridge()
	h := New("/tmp/gk-vibekit-u1-rec", func() api.ACPBridge { return rb }, cs, func() []string { return nil })
	cs.SetBroadcaster(h)
	h.mcpRegistry.signalReady()
	return h, cs, rb
}

// gk_vibekit_u1_fakePush records the body of each Send on a channel.
type gk_vibekit_u1_fakePush struct {
	sends chan string
}

func (p *gk_vibekit_u1_fakePush) RegisterRoutes(*http.ServeMux)        {}
func (p *gk_vibekit_u1_fakePush) Subscribe(api.PushSubscription)       {}
func (p *gk_vibekit_u1_fakePush) Unsubscribe(string)                   {}
func (p *gk_vibekit_u1_fakePush) HasSubscribers() bool                 { return true }
func (p *gk_vibekit_u1_fakePush) SetPreferences(map[api.PushKind]bool) {}
func (p *gk_vibekit_u1_fakePush) ReloadPreferences(context.Context)    {}
func (p *gk_vibekit_u1_fakePush) Close()                               {}
func (p *gk_vibekit_u1_fakePush) Send(_ context.Context, _, body string, _ api.PushKind) {
	select {
	case p.sends <- body:
	default:
	}
}

// --- bridge_fs_read.go: sliceByLines (pure) ---

// Kills 134:26 (CONDITIONALS_NEGATION on limit==nil), 139:26
// (CONDITIONALS_BOUNDARY on *line>0), 152:28 (BOUNDARY on *limit>0),
// and both 152:47 mutants (ARITHMETIC_BASE / INVERT_NEGATIVES on
// end-start). Each case pins the exact output for an input where the
// mutated operator changes the result (or panics under mutation).
func TestGkVibekitU1_SliceByLines(t *testing.T) {
	cases := []struct {
		name        string
		content     string
		line, limit *int
		want        string
	}{
		// 134:26 — with line==nil and a non-nil limit, the original
		// must NOT early-return the full content; it narrows to `limit`
		// lines. The mutant (limit!=nil) early-returns the full content.
		{"limit_no_line_kills_134", "a\nb\nc\nd\n", nil, gk_vibekit_u1_ptr(2), "a\nb\n"},
		// 139:26 — boundary *line==0: original keeps start=0 (full);
		// mutant (>=0) sets start=-1 and panics on the negative slice.
		{"line_zero_kills_139", "a\nb\nc\n", gk_vibekit_u1_ptr(0), nil, "a\nb\nc\n"},
		// 152:28 — boundary *limit==0: original does not narrow (full);
		// mutant (>=0) narrows end to start, returning "".
		{"limit_zero_kills_152_28", "a\nb\nc\n", nil, gk_vibekit_u1_ptr(0), "a\nb\nc\n"},
		// 152:47 — start>0 with limit beyond the remaining window: the
		// original does not narrow; an ARITHMETIC `+` makes the
		// comparison true and narrows end past len → panic.
		{"limit_over_remaining_kills_152_47", "a\nb\nc\nd\ne\n", gk_vibekit_u1_ptr(3), gk_vibekit_u1_ptr(5), "c\nd\ne\n"},
		// 152:47 — start>0 with limit inside the window: a sign-flip /
		// modulo of end-start flips the comparison the other way and
		// stops narrowing, returning extra lines.
		{"narrow_midrange_kills_152_47", "a\nb\nc\nd\n", gk_vibekit_u1_ptr(2), gk_vibekit_u1_ptr(2), "b\nc\n"},
		// sanity: both nil returns the full content.
		{"both_nil_full", "x\ny\n", nil, nil, "x\ny\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sliceByLines(tc.content, tc.line, tc.limit)
			if got != tc.want {
				t.Errorf("%s: sliceByLines(%q) = %q, want %q", tc.name, tc.content, got, tc.want)
			}
		})
	}
}

// --- bridge_fs_path.go: fsErrorIsRoutine (pure) ---

// Kills 62:9 (CONDITIONALS_NEGATION on err==nil). For a routine error
// the original returns true; the mutant (err!=nil) returns false.
func TestGkVibekitU1_FsErrorIsRoutine(t *testing.T) {
	if got := fsErrorIsRoutine(errIgnored); got != true {
		t.Errorf("fsErrorIsRoutine(errIgnored) = %v, want true", got)
	}
	if got := fsErrorIsRoutine(nil); got != false {
		t.Errorf("fsErrorIsRoutine(nil) = %v, want false", got)
	}
	if got := fsErrorIsRoutine(context.Canceled); got != false {
		t.Errorf("fsErrorIsRoutine(context.Canceled) = %v, want false", got)
	}
}

// --- bridge_fs_read.go: respondFSRead ---

// Kills 114:13 (CONDITIONALS_NEGATION on statErr==nil). On a missing
// file statErr is non-nil and info is nil; the original short-circuits
// (no info.Size() call) and responds with a graceful error, while the
// mutant (statErr!=nil) evaluates info.Size() on a nil FileInfo and
// panics.
func TestGkVibekitU1_RespondFSRead_MissingFileRespondsGracefully(t *testing.T) {
	h, br := hubForFSTest(t, t.TempDir())
	id := int64(901)
	msg := &api.RPCResponse{
		ID:     &id,
		Method: api.MethodFSRead,
		Params: mustJSON(t, map[string]any{"path": "gk-ghost.txt"}),
	}
	h.respondFSRead(context.Background(), "c1", msg)
	<-br.done
	if br.response.err == nil {
		t.Errorf("respondFSRead(missing) err = nil, want a not-found error")
	}
}

// Kills 114:35 and 123:15 (CONDITIONALS_BOUNDARY on the > fsReadCap
// guards). A file of exactly fsReadCap bytes must read successfully:
// the original uses strict `>` so size==cap passes both guards. Either
// mutant flips a guard to `>=`, rejecting an exactly-cap file with a
// cap error.
func TestGkVibekitU1_RespondFSRead_ExactCapBoundarySucceeds(t *testing.T) {
	work := t.TempDir()
	data := bytes.Repeat([]byte("a"), fsReadCap)
	if err := os.WriteFile(filepath.Join(work, "gk-exact.txt"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	h, br := hubForFSTest(t, work)
	id := int64(902)
	msg := &api.RPCResponse{
		ID:     &id,
		Method: api.MethodFSRead,
		Params: mustJSON(t, map[string]any{"path": "gk-exact.txt"}),
	}
	h.respondFSRead(context.Background(), "c1", msg)
	<-br.done
	if br.response.err != nil {
		t.Fatalf("respondFSRead(exact cap) err = %v, want nil (boundary is strict >)", br.response.err)
	}
	res, ok := br.response.result.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T, want map[string]any", br.response.result)
	}
	content, _ := res["content"].(string)
	if len(content) != fsReadCap {
		t.Errorf("content length = %d, want %d", len(content), fsReadCap)
	}
}

// --- agent_terminal.go: drainAll ---

// Kills 83:16 (CONDITIONALS_NEGATION on len(terms)==0). With one
// already-exited terminal the original proceeds past the guard and
// clears the maps; the mutant (len(terms)!=0) returns early and leaves
// the terminal registered.
func TestGkVibekitU1_DrainAll_ClearsExitedTerminals(t *testing.T) {
	at := newAgentTerminals()
	done := make(chan struct{})
	close(done) // already exited
	at.terms["t1"] = &agentTerminal{done: done}
	at.byChatID["c1"] = []string{"t1"}

	at.drainAll()

	at.mu.Lock()
	n := len(at.terms)
	at.mu.Unlock()
	if n != 0 {
		t.Errorf("drainAll() left %d terminals, want 0 (mutant returns early on a non-empty map)", n)
	}
}

// --- bridge_coord.go: GetOrCreateBridge overrides + persisted model ---

// Kills 86:20 (agentOverride!=""), 90:20 (modelOverride!=""), 90:43
// (modelOverride!=modelAuto), and 181:17 (newModelID!="").
//   - The recording bridge captures the StartOpts: with both overrides
//     supplied, the original launches with the override agent+model;
//     any of the 86/90 mutants drops an override and falls back to the
//     chat's stored agent/model.
//   - persistNewSessionMetadata copies bridge.ModelID() ("fake-model")
//     into the chat; the 181 mutant skips that assignment and leaves the
//     pre-existing model.
func TestGkVibekitU1_GetOrCreateBridge_AppliesOverrides(t *testing.T) {
	h, cs, rb := gk_vibekit_u1_newRecHub(t)
	ctx := context.Background()
	_ = cs.Mutate(ctx, "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.Agent = "a-chat"
		c.Model = "m-chat"
		return true // no ACPSessionID -> fresh session/new path
	})

	if _, err := h.coord.GetOrCreateBridge(ctx, "c1", "agent-override", "model-override"); err != nil {
		t.Fatalf("GetOrCreateBridge: %v", err)
	}

	opts := rb.startOpts()
	if opts.Agent != "agent-override" {
		t.Errorf("StartOpts.Agent = %q, want %q (86 mutant drops agentOverride)", opts.Agent, "agent-override")
	}
	if opts.Model != "model-override" {
		t.Errorf("StartOpts.Model = %q, want %q (90 mutant drops modelOverride)", opts.Model, "model-override")
	}

	c, _ := cs.Get(ctx, "c1")
	if c.Model != "fake-model" {
		t.Errorf("persisted chat.Model = %q, want %q (181 mutant skips bridge model copy)", c.Model, "fake-model")
	}
}

// --- bridge_coord.go: TryFastModelSwitch ---

// Kills 367:48 (CONDITIONALS_NEGATION on the SetModel err check). The
// fake bridge's SetModel succeeds, so the original returns true; the
// mutant (err==nil) returns false on success.
func TestGkVibekitU1_TryFastModelSwitch_SucceedsReturnsTrue(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := context.Background()
	_ = cs.Mutate(ctx, "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; c.Model = "m-old"; return true })
	if _, err := h.coord.GetOrCreateBridge(ctx, "c1", "", ""); err != nil {
		t.Fatalf("GetOrCreateBridge: %v", err)
	}

	if got := h.coord.TryFastModelSwitch(ctx, "c1", "m-new"); got != true {
		t.Errorf("TryFastModelSwitch(success) = %v, want true", got)
	}
}

// --- bridge_coord.go: Forward clears the registry only when last bridge ---

// Kills 219:38 (CONDITIONALS_NEGATION on count()==0). Both directions:
// when no bridges remain the original clears the MCP registry; when a
// bridge remains it does not.
func TestGkVibekitU1_Forward_ClearsRegistryOnlyWhenLastBridge(t *testing.T) {
	seed := func(h *Hub) {
		h.mcpRegistry.mu.Lock()
		h.mcpRegistry.servers["gk-srv"] = &mcpServerRuntime{Name: "gk-srv", State: mcpStateConnected}
		h.mcpRegistry.mu.Unlock()
	}

	t.Run("clears_when_no_bridges_remain", func(t *testing.T) {
		h, _, br := newTestHub()
		seed(h)
		br.Stop() // close notifCh so Forward's range exits immediately
		h.coord.Forward("gk-nochat", br)
		if n := len(h.mcpRegistry.Snapshot()); n != 0 {
			t.Errorf("registry size = %d, want 0 (count()==0 path must clearAll)", n)
		}
	})

	t.Run("keeps_when_a_bridge_remains", func(t *testing.T) {
		h, _, _ := newTestHub()
		seed(h)
		// A bridge that stays registered so count() stays >= 1.
		h.bridge.mgr.getOrInsert("gk-keep")
		other := newFakeBridge()
		other.Stop()
		h.coord.Forward("gk-other", other)
		if n := len(h.mcpRegistry.Snapshot()); n != 1 {
			t.Errorf("registry size = %d, want 1 (a remaining bridge must NOT clearAll)", n)
		}
	})
}

// --- bridge_coord.go: EmitTurnEndedWithStats trust-clear reason ---

// Kills 351:16 (CONDITIONALS_NEGATION on stopReason==cancelled). With a
// cancelled turn the original clears per-turn trust with the "cancelled"
// reason; the mutant (!=) uses the default "turn_ended" reason.
func TestGkVibekitU1_EmitTurnEnded_CancelledUsesCancelledClearReason(t *testing.T) {
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

// --- bridge_coord.go: NotifyPush on non-cancelled turn ---

// Kills 356:16 (CONDITIONALS_NEGATION on stopReason!=cancelled). A
// non-cancelled turn must fire the "Agent finished" push; the mutant
// (==) skips it.
func TestGkVibekitU1_EmitTurnEnded_NonCancelledFiresPush(t *testing.T) {
	cs := newFakeChatStore()
	fp := &gk_vibekit_u1_fakePush{sends: make(chan string, 4)}
	h := New("/tmp/gk-vibekit-u1-push", func() api.ACPBridge { return newFakeBridge() }, cs,
		func() []string { return nil }, WithPush(fp))
	cs.SetBroadcaster(h)
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
		t.Fatal("no push sent for a non-cancelled turn (mutant skips NotifyPush)")
	}
}

// --- log-only mutants (success path must not emit the error log) ---

// Kills 249:9 (PrimeIfNeeded err check). The fake bridge's Call
// succeeds, so the original logs nothing; the mutant (err==nil) logs
// "prime failed" on the success path.
func TestGkVibekitU1_PrimeIfNeeded_NoErrorLogOnSuccess(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := context.Background()
	_ = cs.Mutate(ctx, "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.Messages = []api.Message{{Role: api.RoleUser, Content: "hi"}}
		return true
	})
	sb, err := h.coord.GetOrCreateBridge(ctx, "c1", "", "")
	if err != nil {
		t.Fatalf("GetOrCreateBridge: %v", err)
	}
	sb.primeReason = primeReasonSwitch

	logs := gk_vibekit_u1_captureLogs(t)
	h.coord.PrimeIfNeeded(ctx, "c1", sb)
	if got := logs.String(); strings.Contains(got, "prime failed") {
		t.Errorf("unexpected error log on prime success: %s", got)
	}
}

// Kills 324:64 (assistant-turn append) and 336:64 (cancel-event append).
// Both AppendMessage calls succeed against the recording store, so the
// original logs nothing; each mutant (err==nil) logs its persist-error
// message on the success path. A started buffer reaches 324; a cancelled
// turn reaches 336.
func TestGkVibekitU1_EmitTurnEnded_NoPersistErrorLogOnSuccess(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := context.Background()
	_ = cs.Mutate(ctx, "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	buf := h.bridge.assistantBufs.GetOrInit("c1")
	buf.Started = true
	buf.MessageID = "m-asst"

	logs := gk_vibekit_u1_captureLogs(t)
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

// Kills 387:63 (PersistModelSwitch append). The event AppendMessage
// succeeds, so the original logs nothing; the mutant (err==nil) logs
// "switch_model: append event" on success.
func TestGkVibekitU1_PersistModelSwitch_NoErrorLogOnSuccess(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := context.Background()
	_ = cs.Mutate(ctx, "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; c.Model = "m-old"; return true })

	logs := gk_vibekit_u1_captureLogs(t)
	h.coord.PersistModelSwitch(ctx, "c1", "m-new", 1234)
	if got := logs.String(); strings.Contains(got, "switch_model: append event") {
		t.Errorf("unexpected append-event error log on success: %s", got)
	}
}

// Kills bridge_fs_path.go 83:64 (respondBridge Respond err check). The
// bridge's Respond succeeds, so the original logs nothing; the mutant
// (wErr==nil) logs "fs response write failed" on success.
func TestGkVibekitU1_RespondBridge_NoErrorLogOnSuccess(t *testing.T) {
	h, _ := hubForFSTest(t, t.TempDir())
	id := int64(903)
	msg := &api.RPCResponse{ID: &id, Method: api.MethodFSRead, Params: mustJSON(t, map[string]any{})}

	logs := gk_vibekit_u1_captureLogs(t)
	h.respondBridge(context.Background(), "c1", msg, map[string]any{"ok": true}, nil)
	if got := logs.String(); strings.Contains(got, "fs response write failed") {
		t.Errorf("unexpected respond-failure error log on success: %s", got)
	}
}

// --- bridge_fs_staging.go: stageFSWrite rel fallback ---

// Kills 77:12 (CONDITIONALS_NEGATION on relErr!=nil). For a path that
// resolves cleanly ("./hello.txt"), workspace.RelPath succeeds and the
// original keeps the normalized "hello.txt"; the mutant (relErr==nil)
// overwrites it with the raw request path "./hello.txt".
func TestGkVibekitU1_StageFSWrite_KeepsNormalizedRelOnSuccess(t *testing.T) {
	h, cs, _ := hubWithWorkDir(t)
	ctx := context.Background()
	_ = cs.Mutate(ctx, "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.SupervisedMode = true
		return true
	})

	msg := newFSWriteMsg(t, "./hello.txt", "x", "gk-tc-staging")
	done := make(chan struct{})
	go func() {
		h.respondFSWrite(ctx, "c1", msg)
		close(done)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if h.perm.pending.CountForChat("c1") == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	snap, ok := h.perm.pending.Get("gk-tc-staging")
	if !ok {
		t.Fatal("staged op not found")
	}
	if snap.Path != "hello.txt" {
		t.Errorf("staged Path = %q, want %q (mutant uses the raw relArg)", snap.Path, "hello.txt")
	}

	// Unblock the staging goroutine.
	if _, err := h.perm.pending.Resolve(ctx, "gk-tc-staging", api.PendingActionAccept); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("respondFSWrite still blocked after Resolve")
	}
}
