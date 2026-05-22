package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"vibekit/internal/api"
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

	// Pre-buffer the chunks so they're available as soon as the drain
	// loop starts consuming notifCh. The fakeBridge's notifCh has a
	// buffer large enough for this; sending before the Call means the
	// chunks land before idleDebounce can fire.
	br.notifCh <- &api.RPCResponse{
		Method: "session/update",
		Params: mustJSON(t, map[string]any{
			"sessionUpdate": "agent_message_chunk",
			"content":       map[string]any{"type": "text", "text": "hello "},
		}),
	}
	br.notifCh <- &api.RPCResponse{
		Method: "session/update",
		Params: mustJSON(t, map[string]any{
			"sessionUpdate": "agent_message_chunk",
			"content":       map[string]any{"type": "text", "text": "world"},
		}),
	}

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
