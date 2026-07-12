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
	if h.bridge.utility != nil && h.bridge.utility.started {
		t.Error("utility bridge started before first call")
	}
	h.lifecycle.mu.Unlock()

	// No chunks; the drain loop will exit on the idle timer since
	// kiro-cli's real behaviour is "response is the turn-end signal".
	// This test only cares that the bridge started.
	_, err := h.UtilityPrompt(t.Context(), "test prompt")
	if err != nil {
		t.Fatalf("UtilityPrompt error = %v", err)
	}
	_ = br // notifCh unused in this test

	h.lifecycle.mu.Lock()
	if h.bridge.utility == nil || !h.bridge.utility.started {
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

	result, err := h.UtilityPrompt(t.Context(), "test")
	if err != nil {
		t.Fatalf("UtilityPrompt error = %v", err)
	}
	if result != "hello world" {
		t.Errorf("result = %q, want %q", result, "hello world")
	}
}

func TestUtilityBridge_StopAndRestart(t *testing.T) {
	h, _, _ := newTestHub()

	// Manually set up a utility bridge.
	h.lifecycle.mu.Lock()
	h.bridge.utility = &utilityBridge{started: true, bridge: newFakeBridge()}
	h.lifecycle.mu.Unlock()

	h.stopUtilityBridge()

	h.lifecycle.mu.Lock()
	if h.bridge.utility != nil {
		t.Error("utility bridge not nil after stop")
	}
	h.lifecycle.mu.Unlock()
}

func TestDrainUtilityResponse_NilResponse(t *testing.T) {
	_, _, _ = newTestHub()
	ub := &utilityBridge{bridge: newFakeBridge(), started: true}
	_, err := ub.drainResponse(context.Background(), nil)
	if err == nil {
		t.Error("expected error for nil response")
	}
}

func TestDrainUtilityResponse_ChannelClose(t *testing.T) {
	_, _, _ = newTestHub()
	fb := newFakeBridge()
	ub := &utilityBridge{bridge: fb, started: true}

	// Close the channel immediately.
	go func() {
		time.Sleep(10 * time.Millisecond)
		fb.Stop()
	}()

	resp := &api.RPCResponse{Result: json.RawMessage(`{}`)}
	result, _ := ub.drainResponse(context.Background(), resp)
	// Should return whatever was collected (empty in this case).
	_ = result
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
				ch := make(chan *api.RPCResponse, n)
				fb := &fakeBridge{
					sessionID: "bench-sess",
					modelID:   "bench-model",
					notifCh:   ch,
				}
				ub := &utilityBridge{bridge: fb, started: true}

				for _, m := range msgs {
					ch <- m
				}

				resp := &api.RPCResponse{Result: json.RawMessage(`{}`)}
				result, _ := ub.drainResponse(context.Background(), resp)
				_ = result
			}
		})
	}
}

func TestUtilityBridge_ConcurrentPrompts(t *testing.T) {
	h, _, br := newTestHub()

	// Pre-start the utility bridge so concurrent calls don't race on start.
	_, _ = h.UtilityPrompt(t.Context(), "warmup")

	// Replace the bridge with a fresh one that won't be stopped.
	freshBr := newFakeBridge()
	h.bridge.utility.mu.Lock()
	h.bridge.utility.bridge = freshBr
	h.bridge.utility.promptCount = 0
	h.bridge.utility.mu.Unlock()
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
			results[idx], errs[idx] = h.UtilityPrompt(t.Context(), fmt.Sprintf("prompt-%d", idx))
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

// newTestUtilBridge builds a utilityBridge whose factory hands out a
// fresh fakeBridge on each call (so a recycle visibly swaps the
// instance) and whose model catalog is empty.
func newTestUtilBridge() *utilityBridge {
	return newUtilityBridge(
		context.Background(),
		func() api.ACPBridge { return newFakeBridge() },
		func() []api.SessionModel { return nil },
	)
}

// At the prompt cap (promptCount == maxUtilityPrompts) with the bridge
// already started, the next UtilityPrompt recycles: reset() stops the
// old bridge and zeroes the counter, start() swaps in a fresh bridge,
// then the increment lands at 1.
func TestUtilityPrompt_RecyclesAtPromptCap(t *testing.T) {
	ub := newTestUtilBridge()
	br0 := newFakeBridge()
	ub.bridge = br0
	ub.started = true
	ub.promptCount = maxUtilityPrompts // exact boundary
	defer ub.Stop()

	if _, err := ub.UtilityPrompt(context.Background(), "p"); err != nil {
		t.Fatalf("UtilityPrompt error = %v, want nil", err)
	}

	if ub.bridge == br0 {
		t.Errorf("UtilityPrompt at the prompt cap did not recycle the bridge")
	}
	if ub.promptCount != 1 {
		t.Errorf("promptCount after recycle = %d, want 1", ub.promptCount)
	}
}

// A fresh bridge (started=false) skips the recycle branch; after one
// prompt the counter is 1.
func TestUtilityPrompt_IncrementsPromptCount(t *testing.T) {
	ub := newTestUtilBridge()
	defer ub.Stop()

	if _, err := ub.UtilityPrompt(context.Background(), "p"); err != nil {
		t.Fatalf("UtilityPrompt error = %v, want nil", err)
	}

	if ub.promptCount != 1 {
		t.Errorf("promptCount after one prompt = %d, want 1", ub.promptCount)
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
		(&utilityBridge{}).answerHostRequest(rb, &api.RPCResponse{ID: &id, Method: methodKiroShellType})
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
		(&utilityBridge{}).answerHostRequest(rb, &api.RPCResponse{ID: &id, Method: methodKiroGetAccessToken})
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
	ub := newTestUtilBridge()
	defer ub.Stop()
	ctx := context.Background()

	// Warm up so the bridge + responseCh exist and are empty.
	if _, err := ub.UtilityPrompt(ctx, "warmup"); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	// Inject a residual chunk from a "prior turn" directly into responseCh.
	var stale utilityChunkPayload
	stale.Content.Text = "STALE"
	ub.responseCh <- stale

	// The next prompt (no chunks delivered) must return empty — the stale
	// chunk was drained at the top, not collected by drainResponse.
	got, err := ub.UtilityPrompt(ctx, "real")
	if err != nil {
		t.Fatalf("UtilityPrompt: %v", err)
	}
	if got != "" {
		t.Errorf("result = %q, want empty; stale chunk bled into this task's output", got)
	}
}

// TestUtilityBridge_PromptCountResetOnRestart verifies start() zeroes the
// prompt counter, so a culled-then-restarted bridge doesn't recycle after
// fewer than maxUtilityPrompts. The cull marks the bridge stopped WITHOUT
// going through reset() (which would zero the counter), so start() must.
func TestUtilityBridge_PromptCountResetOnRestart(t *testing.T) {
	ub := newTestUtilBridge()
	defer ub.Stop()
	ctx := context.Background()

	if _, err := ub.UtilityPrompt(ctx, "p1"); err != nil {
		t.Fatalf("p1: %v", err)
	}
	if ub.promptCount != 1 {
		t.Fatalf("promptCount after p1 = %d, want 1", ub.promptCount)
	}

	// Mimic the cull: stop the current bridge and mark not-started, without
	// calling reset() (which would also zero promptCount).
	ub.mu.Lock()
	victim := ub.bridge
	ub.started = false
	ub.mu.Unlock()
	victim.Stop()

	// The next prompt restarts the bridge; start() must reset the counter.
	if _, err := ub.UtilityPrompt(ctx, "p2"); err != nil {
		t.Fatalf("p2: %v", err)
	}
	if ub.promptCount != 1 {
		t.Errorf("promptCount after cull+restart = %d, want 1 (start must zero it)", ub.promptCount)
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
// the idle utility bridge under ub.mu and stops that exact instance.
func TestCullIdleBridgesOnce_StopsIdleUtilityBridge(t *testing.T) {
	h, _, _ := newTestHub()
	ub := h.ensureUtility()
	if _, err := ub.UtilityPrompt(context.Background(), "warm"); err != nil {
		t.Fatalf("warm: %v", err)
	}
	victim, ok := ub.bridge.(*fakeBridge)
	if !ok {
		t.Fatal("utility bridge is not a *fakeBridge")
	}
	// Backdate activity so the cull considers it idle.
	ub.mu.Lock()
	ub.lastActiveAt = time.Now().Add(-bridgeIdleTimeout - time.Minute)
	ub.mu.Unlock()

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
	ub.mu.Lock()
	started := ub.started
	ub.mu.Unlock()
	if started {
		t.Error("cull did not mark the utility bridge stopped")
	}
}

// TestStopUtilityBridge_ConcurrentWithCull_NoRace exercises stopUtilityBridge
// concurrently with the cull. Both must coordinate on h.lifecycle.mu for the
// h.bridge.utility field read/nil; -race flags the fix's absence.
func TestStopUtilityBridge_ConcurrentWithCull_NoRace(t *testing.T) {
	h, _, _ := newTestHub()
	ub := h.ensureUtility()
	if _, err := ub.UtilityPrompt(context.Background(), "warm"); err != nil {
		t.Fatalf("warm: %v", err)
	}
	ub.mu.Lock()
	ub.lastActiveAt = time.Now().Add(-bridgeIdleTimeout - time.Minute)
	ub.mu.Unlock()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); h.cullIdleBridgesOnce() }()
	go func() { defer wg.Done(); h.stopUtilityBridge() }()
	wg.Wait()
}
