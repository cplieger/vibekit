package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
	"pgregory.net/rapid"
)

func TestUtilityBridge_LazyStart(t *testing.T) {
	h, _, br := newTestHub()

	// Utility bridge should not exist yet.
	h.lifecycle.mu.Lock()
	if u := h.utility.peek(); u != nil && u.session.started {
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
	if u := h.utility.peek(); u == nil || !u.session.started {
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
	br.chunksOnCall = map[string][]string{vibekit.MethodPrompt: {"hello ", "world"}}

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
	s := &utilitySession{shutdownCtx: t.Context(), started: true, bridge: newFakeBridge()}
	h.lifecycle.mu.Lock()
	h.utility = &utilityLease{rt: &utilityRuntime{session: s, textgen: newUtilityAgent(s)}}
	h.lifecycle.mu.Unlock()

	h.stopUtilityBridge()

	h.lifecycle.mu.Lock()
	if h.utility.peek() != nil {
		t.Error("utility bridge not nil after stop")
	}
	h.lifecycle.mu.Unlock()
}

// agentForDrainTest builds an agent over a preset started session, for
// exercising drainResponse directly.
// context.Background() rather than t.Context(): no *testing.T is in scope here.
func agentForDrainTest(bridge ACPBridge) *utilityAgent {
	s := &utilitySession{shutdownCtx: context.Background(), bridge: bridge, started: true, gen: 1}
	return newUtilityAgent(s)
}

func TestDrainUtilityResponse_NilResponse(t *testing.T) {
	_, _, _ = newTestHub()
	ua := agentForDrainTest(newFakeBridge())
	_, err := ua.drainResponse(t.Context(), sessionLease{gen: 1}, nil)
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

	resp := &vibekit.RPCResponse{Result: json.RawMessage(`{}`)}
	result, err := ua.drainResponse(t.Context(), sessionLease{gen: 1, chunks: chunks}, resp)
	if err != nil {
		t.Fatalf("drainResponse on closed channel: %v", err)
	}
	if result != "" {
		t.Errorf("result = %q, want empty", result)
	}
}

func BenchmarkUtilityBridge_DrainResponse(b *testing.B) {
	chunk := func(text string) *vibekit.RPCResponse {
		return newChunkMsg(text)
	}

	payload := "x]x]x]x]x]x]x]x]x]x]x]x]x]x]x]x]x]x]x]x]x]x]x]x]x]" // 50 bytes

	for _, n := range []int{5, 20, 100, 500, 1000} {
		b.Run(fmt.Sprintf("chunks=%d", n), func(b *testing.B) {
			msgs := make([]*vibekit.RPCResponse, n)
			for i := range msgs {
				msgs[i] = chunk(payload)
			}

			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
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

				resp := &vibekit.RPCResponse{Result: json.RawMessage(`{}`)}
				result, _ := ua.drainResponse(b.Context(), sessionLease{gen: 1, chunks: ch}, resp)
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
	u := h.utility.peek()
	u.session.mu.Lock()
	u.session.bridge = freshBr
	u.session.mu.Unlock()
	u.textgen.turnMu.Lock()
	u.textgen.promptCount = 0
	u.textgen.turnMu.Unlock()
	_ = br

	const goroutines = 5
	var wg sync.WaitGroup
	results := make([]string, goroutines)
	errs := make([]error, goroutines)

	// Feed chunks for each goroutine's prompt. Since calls serialize,
	// each prompt drains the idle timer before the next starts. The frames are
	// built HERE, on the test's own goroutine: newChunkMsg can t.Fatalf, and a
	// Fatal off the test goroutine ends the wrong one (go-rulebook §7).
	frames := make([]*vibekit.RPCResponse, goroutines)
	for i := range frames {
		frames[i] = newChunkMsg(fmt.Sprintf("resp-%d", i))
	}
	go func() {
		for i := range goroutines {
			// Pace the feed so each chunk lands inside a live drain window
			// rather than between two of them (the fixture, not a wait for an
			// async effect: 20ms against drainResponse's 50ms idle debounce).
			time.Sleep(20 * time.Millisecond)
			freshBr.mu.Lock()
			stopped := freshBr.stopped
			freshBr.mu.Unlock()
			if stopped {
				return
			}
			freshBr.deliver(frames[i])
		}
	}()

	for i := range goroutines {
		wg.Go(func() {
			results[i], errs[i] = h.UtilityPrompt(t.Context(), fmt.Sprintf("prompt-%d", i), "")
		})
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
		catalog := make([]vibekit.SessionModel, n)
		for i := range n {
			catalog[i] = vibekit.SessionModel{
				ID:             rapid.StringMatching(`[a-z0-9-]{1,20}`).Draw(rt, fmt.Sprintf("id_%d", i)),
				Name:           rapid.String().Draw(rt, fmt.Sprintf("name_%d", i)),
				Description:    rapid.String().Draw(rt, fmt.Sprintf("desc_%d", i)),
				RateMultiplier: rapid.Float64Range(0, 10).Draw(rt, fmt.Sprintf("rate_%d", i)),
			}
		}

		ctx := t.Context()
		result := cheapestModel(ctx, catalog)

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
// context.Background() rather than t.Context(): no *testing.T is in scope here.
func newTestUtilityRuntime() *utilityRuntime {
	return newUtilityRuntime(
		context.Background(),
		func() ACPBridge { return newFakeBridge() },
		func() []vibekit.SessionModel { return nil },
		utilitySessionHooks{},
		nil, // secrets: no credential store in tests
		false,
	)
}

// presetStartedSession marks the runtime's session as started on the given
// bridge at generation 1 and syncs the agent's counters to it, so a test
// can preset counters without the generation-mismatch resync zeroing them.
func presetStartedSession(u *utilityRuntime, bridge ACPBridge) {
	u.session.bridge = bridge
	u.session.started = true
	u.session.gen = 1
	u.textgen.counterGen = 1
}

// At the prompt cap (promptCount == maxUtilityPrompts) with the session
// already started, the next UtilityPrompt recycles: resetIf stops the
// old subprocess, the re-acquire starts a fresh one (new generation), the
// counter resync zeroes, then the increment lands at 1.
func TestUtilityPrompt_RecyclesAtPromptCap(t *testing.T) {
	u := newTestUtilityRuntime()
	br0 := newFakeBridge()
	presetStartedSession(u, br0)
	u.textgen.promptCount = maxUtilityPrompts // exact boundary
	defer u.session.Stop()

	if _, err := u.textgen.UtilityPrompt(t.Context(), "p", ""); err != nil {
		t.Fatalf("UtilityPrompt error = %v, want nil", err)
	}

	if u.session.bridge == br0 {
		t.Errorf("UtilityPrompt at the prompt cap did not recycle the bridge")
	}
	if u.textgen.promptCount != 1 {
		t.Errorf("promptCount after recycle = %d, want 1", u.textgen.promptCount)
	}
}

// A fresh runtime (session not started) skips the recycle branch; after
// one prompt the counter is 1.
func TestUtilityPrompt_IncrementsPromptCount(t *testing.T) {
	u := newTestUtilityRuntime()
	defer u.session.Stop()

	if _, err := u.textgen.UtilityPrompt(t.Context(), "p", ""); err != nil {
		t.Fatalf("UtilityPrompt error = %v, want nil", err)
	}

	if u.textgen.promptCount != 1 {
		t.Errorf("promptCount after one prompt = %d, want 1", u.textgen.promptCount)
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
		(&utilitySession{}).answerHostRequest(rb, &vibekit.RPCResponse{ID: &id, Method: methodKiroShellType})
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

	t.Run("getAccessToken answered even when no token source", func(t *testing.T) {
		rb := newRespondingBridge()
		id := int64(9)
		(&utilitySession{}).answerHostRequest(rb, &vibekit.RPCResponse{ID: &id, Method: methodKiroGetAccessToken})
		rb.respMu.Lock()
		defer rb.respMu.Unlock()
		// Answered as a JSON-RPC error (no source wired) — never dropped.
		if rb.response.id != id {
			t.Fatalf("auth request not answered: got id %d, want %d", rb.response.id, id)
		}
		if rb.response.err == nil {
			t.Errorf("expected an error result when no token source is wired")
		}
	})

	t.Run("getAccessToken forwards the wired source's result", func(t *testing.T) {
		rb := newRespondingBridge()
		id := int64(11)
		us := &utilitySession{hooks: utilitySessionHooks{
			tokenSource: func(context.Context) (map[string]any, error) {
				return map[string]any{"accessToken": "tok", "expiresAt": "2027-01-01T00:00:00Z"}, nil
			},
		}}
		us.answerHostRequest(rb, &vibekit.RPCResponse{ID: &id, Method: methodKiroGetAccessToken})
		rb.respMu.Lock()
		defer rb.respMu.Unlock()
		if rb.response.id != id {
			t.Fatalf("auth request not answered: got id %d, want %d", rb.response.id, id)
		}
		m, ok := rb.response.result.(map[string]any)
		if !ok || m["accessToken"] != "tok" {
			t.Errorf("result = %v, want the source's token map", rb.response.result)
		}
	})
}

// TestForward_RoutesPolicyNotifications pins that the utility session hands
// _kiro/policy/{changed,error} to its hook instead of dropping them.
//
// The utility session's notifications bypass the main dispatch table, so this
// routing is the ONLY path a policy reload has to the client when no chat bridge
// is alive — and Settings -> Permissions is exactly the surface someone uses with
// no chat open. Without it, vibekit wrote permissions.yaml, KAS rebuilt from its
// own file watcher ~0.5s later, and nothing told the panel: the switch it had
// just been clicked on painted itself back off from the pre-write read and stayed
// wrong until the page was reloaded.
func TestForward_RoutesPolicyNotifications(t *testing.T) {
	notifCh := make(chan vibekit.Notification, 2)
	responseCh := make(chan utilityChunkPayload, 4)
	done := make(chan struct{})

	var mu sync.Mutex
	var seen []string
	us := &utilitySession{hooks: utilitySessionHooks{
		onPolicyNotification: func(msg *vibekit.RPCResponse) {
			mu.Lock()
			defer mu.Unlock()
			seen = append(seen, msg.Method)
		},
	}}

	go us.forward(nil, notifCh, responseCh, done)
	notifCh <- vibekit.Notification{Msg: &vibekit.RPCResponse{Method: methodV3PolicyChanged}, Seq: 1}
	notifCh <- vibekit.Notification{Msg: &vibekit.RPCResponse{Method: methodV3PolicyError}, Seq: 2}
	close(notifCh)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("forward did not exit after notifCh closed")
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{methodV3PolicyChanged, methodV3PolicyError}
	if !slices.Equal(seen, want) {
		t.Errorf("hook saw %v, want %v", seen, want)
	}
}

// TestForwardChunk_ForwardsAssistantText is the positive half its sibling
// below cannot supply: it pins that a real KAS frame reaches the channel with
// its text intact. Without it, forwardChunk could reject every frame and the
// whole file stayed green — which is what happened. The kind discriminator sits
// on the `update` object, so a fixture that flattens it tests nothing.
func TestForwardChunk_ForwardsAssistantText(t *testing.T) {
	ch := make(chan utilityChunkPayload, 4)
	forwardChunk(newChunkMsg("feat/branch-name"), ch)
	select {
	case got := <-ch:
		if got.Content.Text != "feat/branch-name" {
			t.Errorf("forwardChunk text = %q, want %q", got.Content.Text, "feat/branch-name")
		}
	default:
		t.Fatal("forwardChunk forwarded nothing; an agent_message_chunk must reach responseCh")
	}
}

// TestForwardChunk_IgnoresOtherKinds keeps the filter honest in the other
// direction: only agent_message_chunk is assistant text, and a tool_call
// forwarded as one would splice tool metadata into a generated commit message.
func TestForwardChunk_IgnoresOtherKinds(t *testing.T) {
	ch := make(chan utilityChunkPayload, 4)
	forwardChunk(newToolCallMsg(t, "tc-1", "readFile", "pending"), ch)
	if len(ch) != 0 {
		t.Errorf("responseCh len = %d, want 0 (only agent_message_chunk is text)", len(ch))
	}
}

// TestForwardChunk_NonBlockingDropsWhenFull verifies forwardChunk never
// blocks on a full responseCh. A blocking send would park the forward
// goroutine so it never observes notifCh closing, deadlocking reset()'s
// <-forwardDone (taken under ub.mu) and the whole utility subsystem.
func TestForwardChunk_NonBlockingDropsWhenFull(t *testing.T) {
	// Buffered size 1, pre-filled: the next chunk has nowhere to go.
	ch := make(chan utilityChunkPayload, 1)
	ch <- utilityChunkPayload{}

	msg := newChunkMsg("dropped")

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
	ctx := t.Context()

	// Warm up so the bridge + responseCh exist and are empty.
	if _, err := u.textgen.UtilityPrompt(ctx, "warmup", ""); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	// Inject a residual chunk from a "prior turn" directly into responseCh.
	var stale utilityChunkPayload
	stale.Content.Text = "STALE"
	u.session.responseCh <- stale

	// The next prompt (no chunks delivered) must return empty — the stale
	// chunk was drained at the top, not collected by drainResponse.
	got, err := u.textgen.UtilityPrompt(ctx, "real", "")
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
	ctx := t.Context()

	if _, err := u.textgen.UtilityPrompt(ctx, "p1", ""); err != nil {
		t.Fatalf("p1: %v", err)
	}
	if u.textgen.promptCount != 1 {
		t.Fatalf("promptCount after p1 = %d, want 1", u.textgen.promptCount)
	}

	// Mimic the cull: stop the session without touching agent counters.
	if !u.session.stopIfIdle(time.Now().Add(time.Minute)) {
		t.Fatal("stopIfIdle did not stop the just-active session with a future cutoff")
	}

	// The next prompt restarts the session (new generation); syncCounters
	// must zero the stale count so it lands at 1, not 2.
	if _, err := u.textgen.UtilityPrompt(ctx, "p2", ""); err != nil {
		t.Fatalf("p2: %v", err)
	}
	if u.textgen.promptCount != 1 {
		t.Errorf("promptCount after cull+restart = %d, want 1 (generation resync must zero it)", u.textgen.promptCount)
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
	if _, err := h.AccountUsage(t.Context()); err != nil {
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
	if _, err := h.config.PolicyList(t.Context(), ""); err != nil {
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
	if _, err := h.config.PolicyExplain(t.Context(), vibekit.PolicyExplainRequest{Capability: "fs_write"}); err != nil {
		t.Fatalf("PolicyExplain: %v", err)
	}
	if !br.callHadDeadline(methodV3PermissionsExplain) {
		t.Error("permissions/explain Call ran without a deadline")
	}
}

// TestCullIdleUtilityBridgeOnce_StopsIdleUtilityBridge verifies the sweep
// captures the idle utility session's bridge under the session mutex and
// stops that exact instance. The utility session is the ONLY bridge with an
// idle timer — a chat bridge is owned by its tab and never swept.
func TestCullIdleUtilityBridgeOnce_StopsIdleUtilityBridge(t *testing.T) {
	h, _, _ := newTestHub()
	u := h.utility.get()
	if _, err := u.textgen.UtilityPrompt(t.Context(), "warm", ""); err != nil {
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

	h.cullIdleUtilityBridgeOnce()

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
// the utility slot's read/nil; -race flags the fix's absence.
func TestStopUtilityBridge_ConcurrentWithCull_NoRace(t *testing.T) {
	h, _, _ := newTestHub()
	u := h.utility.get()
	if _, err := u.textgen.UtilityPrompt(t.Context(), "warm", ""); err != nil {
		t.Fatalf("warm: %v", err)
	}
	u.session.mu.Lock()
	u.session.lastActiveAt = time.Now().Add(-bridgeIdleTimeout - time.Minute)
	u.session.mu.Unlock()

	var wg sync.WaitGroup
	wg.Go(h.cullIdleUtilityBridgeOnce)
	wg.Go(h.stopUtilityBridge)
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
	u.textgen.promptCount = 3
	u.textgen.promptBytes = maxUtilityPromptBytes // exact boundary
	defer u.session.Stop()

	if _, err := u.textgen.UtilityPrompt(t.Context(), "p", ""); err != nil {
		t.Fatalf("UtilityPrompt error = %v, want nil", err)
	}

	if u.session.bridge == br0 {
		t.Errorf("UtilityPrompt at the byte budget did not recycle the bridge")
	}
	if u.textgen.promptBytes != 1 { // len("p") accumulated on the fresh session
		t.Errorf("promptBytes after recycle = %d, want 1", u.textgen.promptBytes)
	}
}

// Per-task effort: the first prompt at a level issues one effortLevel
// set_config_option; a same-level follow-up issues none; a different
// level issues one more. Params carry configId=effortLevel + the value.
func TestUtilityPrompt_AppliesEffortPerTask(t *testing.T) {
	br := newFakeBridge()
	u := newUtilityRuntime(
		t.Context(),
		func() ACPBridge { return br },
		func() []vibekit.SessionModel { return nil },
		utilitySessionHooks{},
		nil, // secrets: no credential store in tests
		false,
	)
	defer u.session.Stop()

	for _, effort := range []vibekit.EffortLevel{vibekit.EffortLow, vibekit.EffortLow, vibekit.EffortMedium} {
		if _, err := u.textgen.UtilityPrompt(t.Context(), "p", effort); err != nil {
			t.Fatalf("UtilityPrompt(%s) error = %v, want nil", effort, err)
		}
	}

	if n := countCalls(br, vibekit.MethodSetConfigOption); n != 2 {
		t.Errorf("set_config_option calls = %d, want 2 (low once, medium once)", n)
	}
	p := br.paramsFor(vibekit.MethodSetConfigOption)
	if p["configId"] != vibekit.ConfigOptionEffort || p["value"] != string(vibekit.EffortMedium) {
		t.Errorf("last set_config_option params = %v, want configId=%s value=%s", p, vibekit.ConfigOptionEffort, vibekit.EffortMedium)
	}
	if u.textgen.currentEffort != vibekit.EffortMedium {
		t.Errorf("currentEffort = %q, want %q", u.textgen.currentEffort, vibekit.EffortMedium)
	}
}

// A failed effortLevel set_config_option (model without a reasoning-effort
// config) latches effortUnsupported: the prompt still succeeds, and later
// tasks skip the round-trip entirely until the next session start.
func TestUtilityPrompt_EffortUnsupportedLatches(t *testing.T) {
	br := newFakeBridge()
	br.callErrs = map[string]error{vibekit.MethodSetConfigOption: fmt.Errorf("no such config option")}
	u := newUtilityRuntime(
		t.Context(),
		func() ACPBridge { return br },
		func() []vibekit.SessionModel { return nil },
		utilitySessionHooks{},
		nil, // secrets: no credential store in tests
		false,
	)
	defer u.session.Stop()

	for range 2 {
		if _, err := u.textgen.UtilityPrompt(t.Context(), "p", vibekit.EffortMedium); err != nil {
			t.Fatalf("UtilityPrompt error = %v, want nil (effort failure must not fail the task)", err)
		}
	}

	if !u.textgen.effortUnsupported {
		t.Error("effortUnsupported not latched after a failed set_config_option")
	}
	if n := countCalls(br, vibekit.MethodSetConfigOption); n != 1 {
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
		{method: vibekit.MethodRequestPermission, cancelled: true},
		{method: vibekit.MethodFSRead, wantErr: true},
		{method: vibekit.MethodFSWrite, wantErr: true},
		{method: "terminal/create", wantErr: true},
		{method: "_kiro/some/future_request", wantErr: true},
		// The security property D69 bought, asserted where a regression would
		// land: executeHook asks vibekit to run a shell command a hook FILE
		// specifies, and this session used to answer it for the Run-now trigger.
		// It must now reach the default refusal branch like any other capability
		// vibekit does not offer. A re-added special case would return a result
		// here and fail this row.
		{method: "_kiro/hooks/executeHook", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			rb := newRespondingBridge()
			id := int64(11)
			(&utilitySession{}).answerHostRequest(rb, &vibekit.RPCResponse{ID: &id, Method: tc.method})
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
				o, ok := rb.response.result.(*vibekit.PermissionOutcome)
				if !ok {
					t.Fatalf("%s: result type %T, want *vibekit.PermissionOutcome", tc.method, rb.response.result)
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
	if got := u.session.liveID(); got != "" {
		t.Errorf("liveID before start = %q, want empty", got)
	}
	if _, err := u.textgen.UtilityPrompt(t.Context(), "p", ""); err != nil {
		t.Fatalf("UtilityPrompt error = %v", err)
	}
	if got := u.session.liveID(); got == "" {
		t.Error("liveID after start = empty, want the fake session id")
	}
	u.session.Stop()
	if got := u.session.liveID(); got != "" {
		t.Errorf("liveID after Stop = %q, want empty", got)
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
	br.blockOn = map[string]chan struct{}{vibekit.MethodPrompt: release}
	u := newUtilityRuntime(
		t.Context(),
		func() ACPBridge { return br },
		func() []vibekit.SessionModel { return nil },
		utilitySessionHooks{},
		nil, // secrets: no credential store in tests
		false,
	)
	defer u.session.Stop()

	// Start a text turn that parks inside its prompt Call.
	turnDone := make(chan struct{})
	go func() {
		defer close(turnDone)
		_, _ = u.textgen.UtilityPrompt(t.Context(), "slow", "")
	}()
	// Wait until the turn's Call is actually in flight.
	deadline := time.Now().Add(2 * time.Second)
	for countCalls(br, vibekit.MethodPrompt) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("prompt Call never started")
		}
		time.Sleep(time.Millisecond)
	}

	// An RPC read must complete while the turn is still blocked.
	rpcCtx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
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

// The sweep must leave a session that was active a moment ago alone. Its cutoff
// is NOW MINUS the idle timeout, so a sign error puts the cutoff in the future
// and every sweep stops the live bridge — which the next prompt then has to
// respawn, one process per tick.
func TestCullIdleUtilityBridgeOnce_LeavesARecentlyActiveBridgeAlone(t *testing.T) {
	h, _, _ := newTestHub()
	u := h.utility.get()
	if _, err := u.textgen.UtilityPrompt(t.Context(), "warm", ""); err != nil {
		t.Fatalf("warm: %v", err)
	}
	live, ok := u.session.bridge.(*fakeBridge)
	if !ok {
		t.Fatal("utility bridge is not a *fakeBridge")
	}

	h.cullIdleUtilityBridgeOnce()

	// The cull stops its victim in a goroutine, so a stop would not be visible
	// immediately; give it the same window the idle-cull test allows before
	// concluding nothing was stopped.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		live.mu.Lock()
		stopped := live.stopped
		live.mu.Unlock()
		if stopped {
			t.Fatal("the sweep stopped a bridge used a moment ago; its idle cutoff is in the future")
		}
		time.Sleep(2 * time.Millisecond)
	}
	u.session.mu.Lock()
	started := u.session.started
	u.session.mu.Unlock()
	if !started {
		t.Error("session.started = false after a sweep of a recently-active session, want true")
	}
}

// TestUtilityBridge_DeclaresSecretStorageOnlyWhenThisProcessHoldsAStore is the
// utility session's half of the capability the chat spawn already pins.
//
// It is a separate spawn with its own StartOpts, so the two can disagree — and this
// is the bridge MCP's OAuth actually runs on, which makes it the half that decides
// whether a credential survives. KAS builds its AcpSecretStorage only for a client
// that declared the capability and then asks that client to persist every
// credential: declaring it without a store drops each one silently and re-runs the
// OAuth dance on the next spawn, while withholding it where a store exists is the
// same regression reached from the other side. The store is opened best-effort from
// the config dir, so this is a runtime question rather than a build-time constant.
func TestUtilityBridge_DeclaresSecretStorageOnlyWhenThisProcessHoldsAStore(t *testing.T) {
	startedUtilityBridge := func(t *testing.T, opts ...Option) *fakeBridge {
		t.Helper()
		cs := newFakeChatStore()
		br := newFakeBridge()
		h := New(context.Background(), t.TempDir(), func() ACPBridge { return br }, cs, opts...)
		cs.Bus = h
		h.mcpRegistry.SignalReady()
		br.callResults = map[string]json.RawMessage{
			methodKiroGetUsage: json.RawMessage(`{"success":true,"message":"ok"}`),
		}
		// Any utility call spins the session up; usage is the cheapest.
		if _, err := h.AccountUsage(t.Context()); err != nil {
			t.Fatalf("AccountUsage: %v", err)
		}
		if br.lastStartOpts() == nil {
			t.Fatal("the utility bridge never started, so there is nothing to assert on")
		}
		return br
	}

	t.Run("a runtime with a credential store declares the capability", func(t *testing.T) {
		br := startedUtilityBridge(t, WithConfigDir(t.TempDir()))
		if !br.lastStartOpts().SecretStorage {
			t.Error("the utility bridge did not declare secretStorage although this process holds " +
				"a store, so KAS never asks it to persist an MCP credential and every OAuth " +
				"registration is redone on the next spawn")
		}
	})

	t.Run("a runtime without one declares it off", func(t *testing.T) {
		br := startedUtilityBridge(t)
		if br.lastStartOpts().SecretStorage {
			t.Error("the utility bridge declared secretStorage with no store behind it, so KAS " +
				"hands it credentials that go nowhere")
		}
	})
}
