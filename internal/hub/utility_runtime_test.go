package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/kiroauth"
	"pgregory.net/rapid"
)

func TestUtilityBridge_LazyStart(t *testing.T) {
	h, _, br := newTestHub()

	// Utility bridge should not exist yet.
	h.lifecycle.mu.Lock()
	if h.bridge.utility != nil && h.bridge.utility.session.started {
		t.Error("utility bridge started before first call")
	}
	h.lifecycle.mu.Unlock()

	// No chunks; the drain loop will exit on the idle timer since
	// kiro-cli's real behaviour is "response is the turn-end signal".
	// This test only cares that the bridge started.
	_, err := h.UtilityPrompt(t.Context(), "test prompt", "")
	if err != nil {
		t.Fatalf("UtilityPrompt error = %v", err)
	}
	_ = br // notifCh unused in this test

	h.lifecycle.mu.Lock()
	if h.bridge.utility == nil || !h.bridge.utility.session.started {
		t.Error("utility bridge not started after first call")
	}
	h.lifecycle.mu.Unlock()
}

func TestUtilityBridge_DrainCollectsChunks(t *testing.T) {
	h, _, br := newTestHub()

	// Deliver the agent's reply chunks in RESPONSE to the prompt Call
	// (chunksOnCall sends them on notifCh after the Call begins). Doing it
	// this way instead of pre-buffering keeps the test deterministic against
	// UtilityPrompt's at-start responseCh drain, which would otherwise race
	// and eat chunks that landed before the Call.
	br.chunksOnCall = map[string][]string{api.MethodPrompt: {"hello ", "world"}}

	result, err := h.UtilityPrompt(t.Context(), "test", "")
	if err != nil {
		t.Fatalf("UtilityPrompt error = %v", err)
	}
	if result != "hello world" {
		t.Errorf("result = %q, want %q", result, "hello world")
	}
}

func TestUtilityBridge_StopAndRestart(t *testing.T) {
	h, _, _ := newTestHub()

	// Manually set up a utility runtime with a started session.
	s := &utilitySession{shutdownCtx: context.Background(), started: true, bridge: newFakeBridge()}
	h.lifecycle.mu.Lock()
	h.bridge.utility = &utilityRuntime{session: s, agent: newUtilityAgent(s)}
	h.lifecycle.mu.Unlock()

	h.stopUtilityBridge()

	h.lifecycle.mu.Lock()
	if h.bridge.utility != nil {
		t.Error("utility bridge not nil after stop")
	}
	h.lifecycle.mu.Unlock()
}

// agentForDrainTest builds an agent over a preset started session, for
// exercising drainResponse directly.
func agentForDrainTest(bridge api.ACPBridge) *utilityAgent {
	s := &utilitySession{shutdownCtx: context.Background(), bridge: bridge, started: true, gen: 1}
	return newUtilityAgent(s)
}

func TestDrainUtilityResponse_NilResponse(t *testing.T) {
	_, _, _ = newTestHub()
	ua := agentForDrainTest(newFakeBridge())
	_, err := ua.drainResponse(context.Background(), sessionLease{gen: 1}, nil)
	if err == nil {
		t.Error("expected error for nil response")
	}
}

func TestDrainUtilityResponse_ChannelClose(t *testing.T) {
	_, _, _ = newTestHub()
	ua := agentForDrainTest(newFakeBridge())

	// A closed chunk channel ends the drain immediately with whatever
	// was collected (a culled session mid-drain looks exactly like this).
	chunks := make(chan utilityChunkPayload)
	close(chunks)

	resp := &api.RPCResponse{Result: json.RawMessage(`{}`)}
	result, err := ua.drainResponse(context.Background(), sessionLease{gen: 1, chunks: chunks}, resp)
	if err != nil {
		t.Fatalf("drainResponse on closed channel: %v", err)
	}
	if result != "" {
		t.Errorf("result = %q, want empty", result)
	}
}

func BenchmarkUtilityBridge_DrainResponse(b *testing.B) {
	chunk := func(text string) *api.RPCResponse {
		params, _ := json.Marshal(map[string]any{
			"sessionUpdate": "agent_message_chunk",
			"content":       map[string]any{"type": "text", "text": text},
		})
		return &api.RPCResponse{Method: "session/update", Params: params}
	}

	payload := "x]x]x]x]x]x]x]x]x]x]x]x]x]x]x]x]x]x]x]x]x]x]x]x]x]" // 50 bytes

	for _, n := range []int{5, 20, 100, 500, 1000} {
		b.Run(fmt.Sprintf("chunks=%d", n), func(b *testing.B) {
			msgs := make([]*api.RPCResponse, n)
			for i := range msgs {
				msgs[i] = chunk(payload)
			}

			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				// Feed the drain through a pre-filled, closed chunk channel:
				// the drain consumes all n chunks and returns on the close,
				// measuring the actual collection loop.
				ch := make(chan utilityChunkPayload, n)
				for _, m := range msgs {
					var c utilityChunkPayload
					_ = json.Unmarshal(m.Params, &c)
					ch <- c
				}
				close(ch)
				ua := agentForDrainTest(newFakeBridge())

				resp := &api.RPCResponse{Result: json.RawMessage(`{}`)}
				result, _ := ua.drainResponse(context.Background(), sessionLease{gen: 1, chunks: ch}, resp)
				_ = result
			}
		})
	}
}

func TestUtilityBridge_ConcurrentPrompts(t *testing.T) {
	h, _, br := newTestHub()

	// Pre-start the utility bridge so concurrent calls don't race on start.
	_, _ = h.UtilityPrompt(t.Context(), "warmup", "")

	// Replace the bridge with a fresh one that won't be stopped.
	freshBr := newFakeBridge()
	u := h.bridge.utility
	u.session.mu.Lock()
	u.session.bridge = freshBr
	u.session.mu.Unlock()
	u.agent.turnMu.Lock()
	u.agent.promptCount = 0
	u.agent.turnMu.Unlock()
	_ = br

	const goroutines = 5
	var wg sync.WaitGroup
	wg.Add(goroutines)
	results := make([]string, goroutines)
	errs := make([]error, goroutines)

	// Feed chunks for each goroutine's prompt. Since calls serialize,
	// each prompt drains the idle timer before the next starts.
	go func() {
		for i := range goroutines {
			// Wait a bit for each call to start draining.
			time.Sleep(20 * time.Millisecond)
			params, _ := json.Marshal(map[string]any{
				"sessionUpdate": "agent_message_chunk",
				"content":       map[string]any{"type": "text", "text": fmt.Sprintf("resp-%d", i)},
			})
			freshBr.mu.Lock()
			stopped := freshBr.stopped
			freshBr.mu.Unlock()
			if stopped {
				return
			}
			freshBr.notifCh <- &api.RPCResponse{Method: "session/update", Params: params}
		}
	}()

	for i := range goroutines {
		go func(idx int) {
			defer wg.Done()
			results[idx], errs[idx] = h.UtilityPrompt(t.Context(), fmt.Sprintf("prompt-%d", idx), "")
		}(i)
	}

	wg.Wait()

	// Assert: no panics occurred (test would have crashed), and all
	// goroutines got a result or error (no deadlock).
	for i := range goroutines {
		if errs[i] != nil {
			t.Logf("goroutine %d error: %v", i, errs[i])
		}
	}

	// Verify serialization: the fakeBridge call log should show exactly
	// goroutines session/prompt calls (one per goroutine, serialized).
	freshBr.mu.Lock()
	promptCalls := 0
	for _, c := range freshBr.calls {
		if c == "session/prompt" {
			promptCalls++
		}
	}
	freshBr.mu.Unlock()

	if promptCalls != goroutines {
		t.Errorf("expected %d session/prompt calls, got %d", goroutines, promptCalls)
	}
}

func TestCheapestModel_RapidInvariants(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(0, 20).Draw(rt, "catalogSize")
		catalog := make([]api.SessionModel, n)
		for i := range n {
			catalog[i] = api.SessionModel{
				ID:             rapid.StringMatching(`[a-z0-9-]{1,20}`).Draw(rt, fmt.Sprintf("id_%d", i)),
				Name:           rapid.String().Draw(rt, fmt.Sprintf("name_%d", i)),
				Description:    rapid.String().Draw(rt, fmt.Sprintf("desc_%d", i)),
				RateMultiplier: rapid.Float64Range(0, 10).Draw(rt, fmt.Sprintf("rate_%d", i)),
			}
		}

		ctx := context.Background()
		result := CheapestModel(ctx, catalog)

		if result == "" {
			// Either empty catalog or all models excluded/auto.
			return
		}

		// Result must be present in catalog.
		var found bool
		for _, m := range catalog {
			if m.ID == result {
				found = true
				// Must not be "auto".
				if m.ID == "auto" {
					rt.Fatal("selected 'auto' model")
				}
				// Must not be excluded.
				if modelExcluded(m.Name) || modelExcluded(m.Description) {
					rt.Fatalf("selected excluded model %q", m.ID)
				}
				break
			}
		}
		if !found {
			rt.Fatalf("result %q not in catalog", result)
		}
	})
}

// newTestUtilityRuntime builds a utility runtime whose factory hands out
// a fresh fakeBridge on each call (so a recycle visibly swaps the
// instance) and whose model catalog is empty.
func newTestUtilityRuntime() *utilityRuntime {
	return newUtilityRuntime(
		context.Background(),
		func() api.ACPBridge { return newFakeBridge() },
		func() []api.SessionModel { return nil },
		utilitySessionHooks{},
		false,
	)
}

// presetStartedSession marks the runtime's session as started on the given
// bridge at generation 1 and syncs the agent's counters to it, so a test
// can preset counters without the generation-mismatch resync zeroing them.
func presetStartedSession(u *utilityRuntime, bridge api.ACPBridge) {
	u.session.bridge = bridge
	u.session.started = true
	u.session.gen = 1
	u.agent.counterGen = 1
}

// At the prompt cap (promptCount == maxUtilityPrompts) with the session
// already started, the next UtilityPrompt recycles: resetIf stops the
// old subprocess, the re-acquire starts a fresh one (new generation), the
// counter resync zeroes, then the increment lands at 1.
func TestUtilityPrompt_RecyclesAtPromptCap(t *testing.T) {
	u := newTestUtilityRuntime()
	br0 := newFakeBridge()
	presetStartedSession(u, br0)
	u.agent.promptCount = maxUtilityPrompts // exact boundary
	defer u.session.Stop()

	if _, err := u.agent.UtilityPrompt(context.Background(), "p", ""); err != nil {
		t.Fatalf("UtilityPrompt error = %v, want nil", err)
	}

	if u.session.bridge == br0 {
		t.Errorf("UtilityPrompt at the prompt cap did not recycle the bridge")
	}
	if u.agent.promptCount != 1 {
		t.Errorf("promptCount after recycle = %d, want 1", u.agent.promptCount)
	}
}

// A fresh runtime (session not started) skips the recycle branch; after
// one prompt the counter is 1.
func TestUtilityPrompt_IncrementsPromptCount(t *testing.T) {
	u := newTestUtilityRuntime()
	defer u.session.Stop()

	if _, err := u.agent.UtilityPrompt(context.Background(), "p", ""); err != nil {
		t.Fatalf("UtilityPrompt error = %v, want nil", err)
	}

	if u.agent.promptCount != 1 {
		t.Errorf("promptCount after one prompt = %d, want 1", u.agent.promptCount)
	}
}

// TestAnswerUtilityHostRequest verifies the utility bridge ANSWERS the v3
// (KAS) host-mediated peer requests (auth token + shell type) rather than
// ignoring them. Regression guard: forwardUtility used to drop peer
// requests, which on v3 stalls session/new and hangs every UtilityPrompt.
func TestAnswerUtilityHostRequest(t *testing.T) {
	t.Run("shell_type answered with bash", func(t *testing.T) {
		rb := newRespondingBridge()
		id := int64(7)
		(&utilitySession{}).answerHostRequest(rb, &api.RPCResponse{ID: &id, Method: methodKiroShellType})
		rb.respMu.Lock()
		defer rb.respMu.Unlock()
		if rb.response.id != id {
			t.Fatalf("shell_type request not answered: got id %d, want %d", rb.response.id, id)
		}
		m, ok := rb.response.result.(map[string]any)
		if !ok || m["shellType"] != "bash" {
			t.Errorf("shell_type result = %v, want map{shellType: bash}", rb.response.result)
		}
	})

	t.Run("getAccessToken answered even when no token file", func(t *testing.T) {
		saved := kiroTokenReader
		kiroTokenReader = kiroauth.NewReader(filepath.Join(t.TempDir(), "absent.json"))
		defer func() { kiroTokenReader = saved }()
		rb := newRespondingBridge()
		id := int64(9)
		(&utilitySession{}).answerHostRequest(rb, &api.RPCResponse{ID: &id, Method: methodKiroGetAccessToken})
		rb.respMu.Lock()
		defer rb.respMu.Unlock()
		// Answered as a JSON-RPC error (no token present) — never dropped.
		if rb.response.id != id {
			t.Fatalf("auth request not answered: got id %d, want %d", rb.response.id, id)
		}
		if rb.response.err == nil {
			t.Errorf("expected an error result when the token file is absent")
		}
	})
}

// TestForwardChunk_NonBlockingDropsWhenFull verifies forwardChunk never
// blocks on a full responseCh. A blocking send would park the forward
// goroutine so it never observes notifCh closing, deadlocking reset()'s
// <-forwardDone (taken under ub.mu) and the whole utility subsystem.
func TestForwardChunk_NonBlockingDropsWhenFull(t *testing.T) {
	// Buffered size 1, pre-filled: the next chunk has nowhere to go.
	ch := make(chan utilityChunkPayload, 1)
	ch <- utilityChunkPayload{}

	msg := &api.RPCResponse{
		Method: api.MethodSessionUpdate,
		Params: mustJSON(t, map[string]any{
			"sessionUpdate": "agent_message_chunk",
			"content":       map[string]any{"type": "text", "text": "dropped"},
		}),
	}

	done := make(chan struct{})
	go func() {
		forwardChunk(msg, ch)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("forwardChunk blocked on a full responseCh; the send must be non-blocking")
	}
	if len(ch) != 1 {
		t.Errorf("responseCh len = %d, want 1 (the overflow chunk must be dropped)", len(ch))
	}
}

// TestUtilityPrompt_DrainsStaleResponseChAtStart verifies a chunk left in
// responseCh by a prior turn is drained before the next turn's Call, so it
// can't prepend to this task's output.
func TestUtilityPrompt_DrainsStaleResponseChAtStart(t *testing.T) {
	u := newTestUtilityRuntime()
	defer u.session.Stop()
	ctx := context.Background()

	// Warm up so the bridge + responseCh exist and are empty.
	if _, err := u.agent.UtilityPrompt(ctx, "warmup", ""); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	// Inject a residual chunk from a "prior turn" directly into responseCh.
	var stale utilityChunkPayload
	stale.Content.Text = "STALE"
	u.session.responseCh <- stale

	// The next prompt (no chunks delivered) must return empty — the stale
	// chunk was drained at the top, not collected by drainResponse.
	got, err := u.agent.UtilityPrompt(ctx, "real", "")
	if err != nil {
		t.Fatalf("UtilityPrompt: %v", err)
	}
	if got != "" {
		t.Errorf("result = %q, want empty; stale chunk bled into this task's output", got)
	}
}

// TestUtilityAgent_PromptCountResetOnRestart verifies the counter resync on
// a session-generation change, so a culled-then-restarted session doesn't
// recycle after fewer than maxUtilityPrompts. The cull marks the session
// stopped WITHOUT resetting the agent's counters; the restart bumps the
// generation and syncCounters zeroes them.
func TestUtilityAgent_PromptCountResetOnRestart(t *testing.T) {
	u := newTestUtilityRuntime()
	defer u.session.Stop()
	ctx := context.Background()

	if _, err := u.agent.UtilityPrompt(ctx, "p1", ""); err != nil {
		t.Fatalf("p1: %v", err)
	}
	if u.agent.promptCount != 1 {
		t.Fatalf("promptCount after p1 = %d, want 1", u.agent.promptCount)
	}

	// Mimic the cull: stop the session without touching agent counters.
	if !u.session.stopIfIdle(time.Now().Add(time.Minute)) {
		t.Fatal("stopIfIdle did not stop the just-active session with a future cutoff")
	}

	// The next prompt restarts the session (new generation); syncCounters
	// must zero the stale count so it lands at 1, not 2.
	if _, err := u.agent.UtilityPrompt(ctx, "p2", ""); err != nil {
		t.Fatalf("p2: %v", err)
	}
	if u.agent.promptCount != 1 {
		t.Errorf("promptCount after cull+restart = %d, want 1 (generation resync must zero it)", u.agent.promptCount)
	}
}

// TestAccountUsage_CallHasTimeout verifies AccountUsage bounds the utility
// bridge Call with a context deadline. Without it, a wedged getUsage holds
// the single utility mutex indefinitely, starving chat auto-rename etc.
func TestAccountUsage_CallHasTimeout(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroGetUsage: json.RawMessage(`{"success":true,"message":"managed by admin"}`),
	}
	if _, err := h.AccountUsage(context.Background()); err != nil {
		t.Fatalf("AccountUsage: %v", err)
	}
	if !br.callHadDeadline(methodKiroGetUsage) {
		t.Error("account usage Call ran without a deadline; a wedged getUsage would hold the utility mutex forever")
	}
}

// TestPolicyList_CallHasTimeout verifies PolicyList bounds its Call with a
// deadline (same mutex-starvation concern as AccountUsage).
func TestPolicyList_CallHasTimeout(t *testing.T) {
	h, _, br := newTestHub()
	seedPolicy(br, `{"rules":[]}`, `{}`)
	if _, err := h.PolicyList(context.Background(), ""); err != nil {
		t.Fatalf("PolicyList: %v", err)
	}
	if !br.callHadDeadline(methodV3PermissionsList) {
		t.Error("permissions/list Call ran without a deadline")
	}
}

// TestPolicyExplain_CallHasTimeout verifies PolicyExplain bounds its Call
// with a deadline.
func TestPolicyExplain_CallHasTimeout(t *testing.T) {
	h, _, br := newTestHub()
	seedPolicy(br, `{}`, `{"capability":"fs_write","effect":"ask"}`)
	if _, err := h.PolicyExplain(context.Background(), api.PolicyExplainRequest{Capability: "fs_write"}); err != nil {
		t.Fatalf("PolicyExplain: %v", err)
	}
	if !br.callHadDeadline(methodV3PermissionsExplain) {
		t.Error("permissions/explain Call ran without a deadline")
	}
}

// TestCullIdleBridgesOnce_StopsIdleUtilityBridge verifies the cull captures
// the idle utility session's bridge under the session mutex and stops that
// exact instance.
func TestCullIdleBridgesOnce_StopsIdleUtilityBridge(t *testing.T) {
	h, _, _ := newTestHub()
	u := h.ensureUtility()
	if _, err := u.agent.UtilityPrompt(context.Background(), "warm", ""); err != nil {
		t.Fatalf("warm: %v", err)
	}
	victim, ok := u.session.bridge.(*fakeBridge)
	if !ok {
		t.Fatal("utility bridge is not a *fakeBridge")
	}
	// Backdate activity so the cull considers it idle.
	u.session.mu.Lock()
	u.session.lastActiveAt = time.Now().Add(-bridgeIdleTimeout - time.Minute)
	u.session.mu.Unlock()

	h.cullIdleBridgesOnce()

	// cull stops the captured victim in a goroutine; poll for it.
	deadline := time.Now().Add(2 * time.Second)
	stopped := false
	for time.Now().Before(deadline) {
		victim.mu.Lock()
		stopped = victim.stopped
		victim.mu.Unlock()
		if stopped {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if !stopped {
		t.Error("cull did not stop the idle utility bridge")
	}
	u.session.mu.Lock()
	started := u.session.started
	u.session.mu.Unlock()
	if started {
		t.Error("cull did not mark the utility session stopped")
	}
}

// TestStopUtilityBridge_ConcurrentWithCull_NoRace exercises stopUtilityBridge
// concurrently with the cull. Both must coordinate on h.lifecycle.mu for the
// h.bridge.utility field read/nil; -race flags the fix's absence.
func TestStopUtilityBridge_ConcurrentWithCull_NoRace(t *testing.T) {
	h, _, _ := newTestHub()
	u := h.ensureUtility()
	if _, err := u.agent.UtilityPrompt(context.Background(), "warm", ""); err != nil {
		t.Fatalf("warm: %v", err)
	}
	u.session.mu.Lock()
	u.session.lastActiveAt = time.Now().Add(-bridgeIdleTimeout - time.Minute)
	u.session.mu.Unlock()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); h.cullIdleBridgesOnce() }()
	go func() { defer wg.Done(); h.stopUtilityBridge() }()
	wg.Wait()
}

// countCalls returns how many times the fake bridge received method.
func countCalls(b *fakeBridge, method string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, m := range b.calls {
		if m == method {
			n++
		}
	}
	return n
}

// At the byte budget (promptBytes >= maxUtilityPromptBytes) with the
// bridge started, the next UtilityPrompt recycles even though the prompt
// COUNT is far below its cap — the second recycle trigger, bounding the
// dead context a few large diffs would otherwise re-bill every turn.
func TestUtilityPrompt_RecyclesAtByteBudget(t *testing.T) {
	u := newTestUtilityRuntime()
	br0 := newFakeBridge()
	presetStartedSession(u, br0)
	u.agent.promptCount = 3
	u.agent.promptBytes = maxUtilityPromptBytes // exact boundary
	defer u.session.Stop()

	if _, err := u.agent.UtilityPrompt(context.Background(), "p", ""); err != nil {
		t.Fatalf("UtilityPrompt error = %v, want nil", err)
	}

	if u.session.bridge == br0 {
		t.Errorf("UtilityPrompt at the byte budget did not recycle the bridge")
	}
	if u.agent.promptBytes != 1 { // len("p") accumulated on the fresh session
		t.Errorf("promptBytes after recycle = %d, want 1", u.agent.promptBytes)
	}
}

// Per-task effort: the first prompt at a level issues one effortLevel
// set_config_option; a same-level follow-up issues none; a different
// level issues one more. Params carry configId=effortLevel + the value.
func TestUtilityPrompt_AppliesEffortPerTask(t *testing.T) {
	br := newFakeBridge()
	u := newUtilityRuntime(
		context.Background(),
		func() api.ACPBridge { return br },
		func() []api.SessionModel { return nil },
		utilitySessionHooks{},
		false,
	)
	defer u.session.Stop()

	for _, effort := range []api.EffortLevel{api.EffortLow, api.EffortLow, api.EffortMedium} {
		if _, err := u.agent.UtilityPrompt(context.Background(), "p", effort); err != nil {
			t.Fatalf("UtilityPrompt(%s) error = %v, want nil", effort, err)
		}
	}

	if n := countCalls(br, api.MethodSetConfigOption); n != 2 {
		t.Errorf("set_config_option calls = %d, want 2 (low once, medium once)", n)
	}
	p := br.paramsFor(api.MethodSetConfigOption)
	if p["configId"] != api.ConfigOptionEffort || p["value"] != string(api.EffortMedium) {
		t.Errorf("last set_config_option params = %v, want configId=%s value=%s", p, api.ConfigOptionEffort, api.EffortMedium)
	}
	if u.agent.currentEffort != api.EffortMedium {
		t.Errorf("currentEffort = %q, want %q", u.agent.currentEffort, api.EffortMedium)
	}
}

// A failed effortLevel set_config_option (model without a reasoning-effort
// config) latches effortUnsupported: the prompt still succeeds, and later
// tasks skip the round-trip entirely until the next session start.
func TestUtilityPrompt_EffortUnsupportedLatches(t *testing.T) {
	br := newFakeBridge()
	br.callErrs = map[string]error{api.MethodSetConfigOption: fmt.Errorf("no such config option")}
	u := newUtilityRuntime(
		context.Background(),
		func() api.ACPBridge { return br },
		func() []api.SessionModel { return nil },
		utilitySessionHooks{},
		false,
	)
	defer u.session.Stop()

	for range 2 {
		if _, err := u.agent.UtilityPrompt(context.Background(), "p", api.EffortMedium); err != nil {
			t.Fatalf("UtilityPrompt error = %v, want nil (effort failure must not fail the task)", err)
		}
	}

	if !u.agent.effortUnsupported {
		t.Error("effortUnsupported not latched after a failed set_config_option")
	}
	if n := countCalls(br, api.MethodSetConfigOption); n != 1 {
		t.Errorf("set_config_option calls = %d, want 1 (no retry after the latch)", n)
	}
}

// Tool-use requests on the utility session are actively refused so an
// unanswered request can never wedge the turn until the 60s ceiling:
// permission requests get a cancelled outcome, fs/terminal requests get
// an error, and unknown peer requests get an error.
func TestAnswerHostRequest_DeniesToolRequests(t *testing.T) {
	cases := []struct {
		method    string
		wantErr   bool // error response vs result response
		cancelled bool // result carries outcome=cancelled
	}{
		{method: api.MethodRequestPermission, cancelled: true},
		{method: api.MethodFSRead, wantErr: true},
		{method: api.MethodFSWrite, wantErr: true},
		{method: "terminal/create", wantErr: true},
		{method: "_kiro/some/future_request", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			rb := newRespondingBridge()
			id := int64(11)
			(&utilitySession{}).answerHostRequest(rb, &api.RPCResponse{ID: &id, Method: tc.method})
			rb.respMu.Lock()
			defer rb.respMu.Unlock()
			if rb.response.id != id {
				t.Fatalf("request %s not answered", tc.method)
			}
			if tc.wantErr {
				if rb.response.err == nil {
					t.Fatalf("%s: want error response, got result %v", tc.method, rb.response.result)
				}
				return
			}
			if rb.response.err != nil {
				t.Fatalf("%s: want result response, got error %v", tc.method, rb.response.err)
			}
			if tc.cancelled {
				o, ok := rb.response.result.(*api.PermissionOutcome)
				if !ok {
					t.Fatalf("%s: result type %T, want *api.PermissionOutcome", tc.method, rb.response.result)
				}
				if o.Outcome.Outcome != "cancelled" {
					t.Fatalf("%s: result = %+v, want outcome.outcome=cancelled", tc.method, o)
				}
			}
		})
	}
}

// The live utility session id is exempted from the orphan-session sweep's
// referenced-set computation; a stopped or never-created utility bridge
// contributes nothing.
func TestUtilityLiveSessionID(t *testing.T) {
	u := newTestUtilityRuntime()
	if got := u.session.liveSessionID(); got != "" {
		t.Errorf("liveSessionID before start = %q, want empty", got)
	}
	if _, err := u.agent.UtilityPrompt(context.Background(), "p", ""); err != nil {
		t.Fatalf("UtilityPrompt error = %v", err)
	}
	if got := u.session.liveSessionID(); got == "" {
		t.Error("liveSessionID after start = empty, want the fake session id")
	}
	u.session.Stop()
	if got := u.session.liveSessionID(); got != "" {
		t.Errorf("liveSessionID after Stop = %q, want empty", got)
	}
}

// TestRPCReadsDoNotQueueBehindTextTurn pins the refactor's point: the
// session's stateless RPC reads (account usage, specs, policy, hooks,
// knowledge) complete while a text-generation turn is in flight on the
// same session. Under the old single-mutex utilityBridge this deadlined:
// the RPC queued behind the turn's held mutex.
func TestRPCReadsDoNotQueueBehindTextTurn(t *testing.T) {
	br := newFakeBridge()
	release := make(chan struct{})
	br.blockOn = map[string]chan struct{}{api.MethodPrompt: release}
	u := newUtilityRuntime(
		context.Background(),
		func() api.ACPBridge { return br },
		func() []api.SessionModel { return nil },
		utilitySessionHooks{},
		false,
	)
	defer u.session.Stop()

	// Start a text turn that parks inside its prompt Call.
	turnDone := make(chan struct{})
	go func() {
		defer close(turnDone)
		_, _ = u.agent.UtilityPrompt(context.Background(), "slow", "")
	}()
	// Wait until the turn's Call is actually in flight.
	deadline := time.Now().Add(2 * time.Second)
	for countCalls(br, api.MethodPrompt) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("prompt Call never started")
		}
		time.Sleep(time.Millisecond)
	}

	// An RPC read must complete while the turn is still blocked.
	rpcCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := u.session.accountUsageRaw(rpcCtx); err != nil {
		t.Fatalf("accountUsageRaw during an in-flight turn: %v", err)
	}
	select {
	case <-turnDone:
		t.Fatal("text turn finished before the RPC asserted concurrency; test is vacuous")
	default:
	}

	close(release)
	<-turnDone
}
