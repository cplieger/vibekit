package command

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"vibekit/internal/api"
	"vibekit/internal/pending"
)

// benchDeps is a minimal Dependencies stub for benchmarking dispatch overhead.
type benchDeps struct {
	dedup map[string][]byte
}

func newBenchDeps() *benchDeps { return &benchDeps{dedup: make(map[string][]byte)} }

func (d *benchDeps) CheckDedup(reqID string) ([]byte, bool) { v, ok := d.dedup[reqID]; return v, ok }
func (d *benchDeps) RecordDedup(reqID string, data []byte)  { d.dedup[reqID] = data }
func (d *benchDeps) Draining() bool                         { return false }
func (d *benchDeps) ChatStore() api.ChatStore               { return nil }
func (d *benchDeps) Broadcast(context.Context, api.ServerEvent) {}
func (d *benchDeps) GetBridge(api.ChatID) Bridge            { return nil }
func (d *benchDeps) GetOrCreateBridge(context.Context, api.ChatID, string, string) (Bridge, error) {
	return nil, nil
}
func (d *benchDeps) CloseBridge(api.ChatID)                                                    {}
func (d *benchDeps) PendingStore() *pending.Store                                              { return nil }
func (d *benchDeps) SupervisedSetTrust(api.ChatID)                                             {}
func (d *benchDeps) SupervisedClearTrust(api.ChatID, api.ClearReason)                          {}
func (d *benchDeps) ChatInSupervisedMode(context.Context, api.ChatID) bool                     { return false }
func (d *benchDeps) FlushPendingForChat(context.Context, api.ChatID, api.ClearReason)          {}
func (d *benchDeps) ClearPendingPermsForChat(api.ChatID)                                       {}
func (d *benchDeps) RemovePendingPerm(int64)                                                   {}
func (d *benchDeps) Checkpoints() api.CheckpointService                                       { return nil }
func (d *benchDeps) AdvanceCheckpointTurn(context.Context, api.ChatID)                         {}
func (d *benchDeps) WorkDir() string                                                           { return "/tmp" }
func (d *benchDeps) ConfigDir() string                                                         { return "/tmp" }
func (d *benchDeps) ShutdownCtx() context.Context                                              { return context.Background() }
func (d *benchDeps) InflightAdd(int)                                                           {}
func (d *benchDeps) InflightDone()                                                             {}
func (d *benchDeps) InflightGo(func())                                                         {}
func (d *benchDeps) CleanupChatState(context.Context, api.ChatID)                              {}
func (d *benchDeps) MCPWaitForReady(context.Context, time.Duration) bool                       { return true }
func (d *benchDeps) ResolveInsideWorkDir(string) (string, error)                               { return "", nil }
func (d *benchDeps) PrimeIfNeeded(context.Context, api.ChatID, Bridge)                         {}
func (d *benchDeps) IsEmptyTurn(*api.RPCResponse, api.ChatID) bool                             { return false }
func (d *benchDeps) EmitTurnEndedWithStats(context.Context, api.ChatID, *api.RPCResponse, float64, float64) {
}

// TestBenchDeps_NoPanic verifies that every benchDeps method can be called
// with zero-value arguments without panicking.
func TestBenchDeps_NoPanic(t *testing.T) {
	d := newBenchDeps()

	// Dedup round-trip.
	if _, ok := d.CheckDedup("unknown"); ok {
		t.Error("CheckDedup returned true for unknown key")
	}
	d.RecordDedup("k", []byte("v"))
	if got, ok := d.CheckDedup("k"); !ok || string(got) != "v" {
		t.Errorf("RecordDedup+CheckDedup round-trip failed: got %q, ok=%v", got, ok)
	}

	// Boolean/scalar returns.
	if d.Draining() {
		t.Error("Draining() = true, want false")
	}
	if d.WorkDir() == "" {
		t.Error("WorkDir() returned empty")
	}
	if d.ConfigDir() == "" {
		t.Error("ConfigDir() returned empty")
	}
	if d.ShutdownCtx() == nil {
		t.Error("ShutdownCtx() returned nil")
	}
	if !d.MCPWaitForReady(context.Background(), time.Millisecond) {
		t.Error("MCPWaitForReady returned false")
	}

	// No-op methods must not panic.
	d.Broadcast(context.Background(), api.ServerEvent{})
	d.CloseBridge("x")
	d.SupervisedSetTrust("x")
	d.SupervisedClearTrust("x", "")
	d.FlushPendingForChat(context.Background(), "x", "")
	d.ClearPendingPermsForChat("x")
	d.RemovePendingPerm(0)
	d.AdvanceCheckpointTurn(context.Background(), "x")
	d.InflightAdd(1)
	d.InflightDone()
	d.InflightGo(func() {})
	d.CleanupChatState(context.Background(), "x")
	d.PrimeIfNeeded(context.Background(), "x", nil)
}

// TestBenchDeps_Contract documents which methods intentionally return nil
// (safe only because benchmarks don't invoke handlers that call them) vs.
// which return usable values needed by the dispatch path.
func TestBenchDeps_Contract(t *testing.T) {
	d := newBenchDeps()

	// --- Safe usable values (dispatch path depends on these) ---
	t.Run("usable_values", func(t *testing.T) {
		if _, ok := d.CheckDedup("miss"); ok {
			t.Error("CheckDedup should return false for unknown")
		}
		d.RecordDedup("r1", []byte("data"))
		if got, ok := d.CheckDedup("r1"); !ok || string(got) != "data" {
			t.Errorf("dedup round-trip failed: %q ok=%v", got, ok)
		}
		if d.Draining() {
			t.Error("Draining must be false for benchmarks")
		}
		if d.WorkDir() == "" {
			t.Error("WorkDir must be non-empty")
		}
		if d.ShutdownCtx() == nil {
			t.Error("ShutdownCtx must be non-nil")
		}
	})

	// --- Intentionally nil (safe only for dispatch-overhead benchmarks) ---
	t.Run("intentionally_nil", func(t *testing.T) {
		if d.ChatStore() != nil {
			t.Error("ChatStore expected nil for bench stub")
		}
		if d.PendingStore() != nil {
			t.Error("PendingStore expected nil for bench stub")
		}
		if d.GetBridge("any") != nil {
			t.Error("GetBridge expected nil for bench stub")
		}
		if d.Checkpoints() != nil {
			t.Error("Checkpoints expected nil for bench stub")
		}
	})

	// --- No-panic on zero-value calls ---
	t.Run("no_panic_zero_value_calls", func(t *testing.T) {
		d.Broadcast(context.Background(), api.ServerEvent{})
		d.CloseBridge("x")
		d.SupervisedSetTrust("x")
		d.SupervisedClearTrust("x", "")
		if d.ChatInSupervisedMode(context.Background(), "x") {
			t.Error("ChatInSupervisedMode should be false")
		}
		d.FlushPendingForChat(context.Background(), "x", "")
		d.ClearPendingPermsForChat("x")
		d.RemovePendingPerm(0)
		d.AdvanceCheckpointTurn(context.Background(), "x")
		d.InflightAdd(1)
		d.InflightDone()
		d.InflightGo(func() {})
		d.CleanupChatState(context.Background(), "x")
		d.PrimeIfNeeded(context.Background(), "x", nil)
		if d.IsEmptyTurn(nil, "x") {
			t.Error("IsEmptyTurn should be false")
		}
		d.EmitTurnEndedWithStats(context.Background(), "x", nil, 0, 0)
		if _, err := d.GetOrCreateBridge(context.Background(), "x", "", ""); err != nil {
			t.Errorf("GetOrCreateBridge returned error: %v", err)
		}
		if _, err := d.ResolveInsideWorkDir(""); err != nil {
			t.Errorf("ResolveInsideWorkDir returned error: %v", err)
		}
	})
}

func BenchmarkDispatcherServeHTTP(b *testing.B) {
	deps := newBenchDeps()
	d := New(deps)
	d.Register("create_chat", func(_ context.Context, w http.ResponseWriter, _ *api.ClientCommand) {
		api.WriteRawJSON(w, []byte(`{"ok":true}`))
	})

	body, _ := json.Marshal(api.ClientCommand{
		Type:      "create_chat",
		RequestID: "req-bench-1",
		ChatID:    "chat-bench-1",
	})

	b.Run("cache_miss", func(b *testing.B) {
		b.ReportAllocs()
		for i := range b.N {
			// Use unique request IDs to avoid dedup cache hits.
			reqBody, _ := json.Marshal(api.ClientCommand{
				Type:      "create_chat",
				RequestID: "req-" + string(rune('A'+i%26)) + string(rune('0'+i%10)),
				ChatID:    "chat-bench-1",
			})
			req := httptest.NewRequest(http.MethodPost, "/api/command", bytes.NewReader(reqBody))
			w := httptest.NewRecorder()
			d.ServeHTTP(w, req)
		}
	})

	b.Run("cache_hit", func(b *testing.B) {
		// Prime the dedup cache.
		req := httptest.NewRequest(http.MethodPost, "/api/command", bytes.NewReader(body))
		w := httptest.NewRecorder()
		d.ServeHTTP(w, req)

		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			req := httptest.NewRequest(http.MethodPost, "/api/command", bytes.NewReader(body))
			w := httptest.NewRecorder()
			d.ServeHTTP(w, req)
		}
	})

	b.Run("unknown_command", func(b *testing.B) {
		unknownBody, _ := json.Marshal(api.ClientCommand{
			Type:      "nonexistent_cmd",
			RequestID: "req-unknown",
			ChatID:    "chat-bench-1",
		})
		b.ReportAllocs()
		for range b.N {
			req := httptest.NewRequest(http.MethodPost, "/api/command", bytes.NewReader(unknownBody))
			w := httptest.NewRecorder()
			d.ServeHTTP(w, req)
		}
	})
}
