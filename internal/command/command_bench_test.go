package command

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// benchDeps is a minimal host double for benchmarking dispatch overhead.
type benchDeps struct {
	dedup map[string][]byte
}

func newBenchDeps() *benchDeps { return &benchDeps{dedup: make(map[string][]byte)} }

func (d *benchDeps) CheckDedup(reqID string) ([]byte, bool)         { v, ok := d.dedup[reqID]; return v, ok }
func (d *benchDeps) RecordDedup(reqID string, data []byte)          { d.dedup[reqID] = data }
func (d *benchDeps) ChatStore() ChatStore                           { return nil }
func (d *benchDeps) Broadcast(context.Context, vibekit.ServerEvent) {}
func (d *benchDeps) GetBridge(vibekit.ChatID) Bridge                { return nil }
func (d *benchDeps) GetOrCreateBridge(context.Context, vibekit.ChatID, string) (Bridge, error) {
	return nil, nil
}
func (d *benchDeps) CloseBridge(vibekit.ChatID)                    {}
func (d *benchDeps) ClearPendingPermsForChat(vibekit.ChatID)       {}
func (d *benchDeps) TakePendingPerm(int64, vibekit.SettledBy) bool { return true }
func (d *benchDeps) TurnContext(reqCtx context.Context) (context.Context, context.CancelFunc) {
	return context.WithCancel(context.WithoutCancel(reqCtx))
}
func (d *benchDeps) InflightAdd(int)                                       {}
func (d *benchDeps) InflightDone()                                         {}
func (d *benchDeps) CleanupChatState(context.Context, vibekit.ChatID)      {}
func (d *benchDeps) CloseChatState(context.Context, vibekit.ChatID)        {}
func (d *benchDeps) CancelChatRuns(context.Context, vibekit.ChatID)        {}
func (d *benchDeps) KillTurnTerminals(vibekit.ChatID)                      {}
func (d *benchDeps) MCPWaitForReady(context.Context, time.Duration) bool   { return true }
func (d *benchDeps) PrimeIfNeeded(context.Context, vibekit.ChatID, Bridge) {}
func (d *benchDeps) PrimeFromChat(vibekit.ChatID, vibekit.ChatID)          {}
func (d *benchDeps) IsEmptyTurn(*vibekit.RPCResponse, vibekit.ChatID) bool { return false }
func (d *benchDeps) EmitTurnEndedWithStats(context.Context, vibekit.ChatID, *vibekit.RPCResponse, TurnStats) {
}

func (d *benchDeps) AbandonInFlightTurn(context.Context, vibekit.ChatID) {}
func (d *benchDeps) LatchTurnModel(vibekit.ChatID, string)               {}

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

	// Boolean/scalar returns. The workspace paths are not here any more: they
	// are a Workspace value, so there is no double method to exercise.
	if turnCtx, cancel := d.TurnContext(t.Context()); turnCtx == nil {
		t.Error("TurnContext() returned a nil context")
	} else {
		cancel()
	}
	if !d.MCPWaitForReady(t.Context(), time.Millisecond) {
		t.Error("MCPWaitForReady returned false")
	}

	// No-op methods must not panic.
	d.Broadcast(t.Context(), vibekit.ServerEvent{})
	d.CloseBridge("x")
	d.ClearPendingPermsForChat("x")
	d.TakePendingPerm(0, vibekit.SettledByUser)
	d.InflightAdd(1)
	d.InflightDone()
	d.CleanupChatState(t.Context(), "x")
	d.PrimeIfNeeded(t.Context(), "x", nil)
	d.LatchTurnModel("x", "sonnet-4")
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
		if turnCtx, cancel := d.TurnContext(t.Context()); turnCtx == nil {
			t.Error("TurnContext must return a non-nil context")
		} else {
			cancel()
		}
	})

	// --- Intentionally nil (safe only for dispatch-overhead benchmarks) ---
	t.Run("intentionally_nil", func(t *testing.T) {
		if d.ChatStore() != nil {
			t.Error("ChatStore expected nil for bench stub")
		}
		if d.GetBridge("any") != nil {
			t.Error("GetBridge expected nil for bench stub")
		}
	})

	// --- No-panic on zero-value calls ---
	t.Run("no_panic_zero_value_calls", func(t *testing.T) {
		d.Broadcast(t.Context(), vibekit.ServerEvent{})
		d.CloseBridge("x")
		d.ClearPendingPermsForChat("x")
		d.TakePendingPerm(0, vibekit.SettledByUser)
		d.InflightAdd(1)
		d.InflightDone()
		d.CleanupChatState(t.Context(), "x")
		d.PrimeIfNeeded(t.Context(), "x", nil)
		if d.IsEmptyTurn(nil, "x") {
			t.Error("IsEmptyTurn should be false")
		}
		d.EmitTurnEndedWithStats(t.Context(), "x", nil, TurnStats{})
		if _, err := d.GetOrCreateBridge(t.Context(), "x", ""); err != nil {
			t.Errorf("GetOrCreateBridge returned error: %v", err)
		}
	})
}

func BenchmarkDispatcherServeHTTP(b *testing.B) {
	deps := newBenchDeps()
	d := New(deps)
	d.Register("create_chat", func(_ context.Context, w http.ResponseWriter, _ *vibekit.ClientCommand) {
		httpreply.WriteRawJSON(w, []byte(`{"ok":true}`))
	})

	body, _ := json.Marshal(vibekit.ClientCommand{
		Type:      "create_chat",
		RequestID: "req-bench-1",
		ChatID:    "chat-bench-1",
	})

	b.Run("cache_miss", func(b *testing.B) {
		b.ReportAllocs()
		i := 0
		for b.Loop() {
			// Use unique request IDs to avoid dedup cache hits.
			reqBody, _ := json.Marshal(vibekit.ClientCommand{
				Type:      "create_chat",
				RequestID: "req-" + string(rune('A'+i%26)) + string(rune('0'+i%10)),
				ChatID:    "chat-bench-1",
			})
			req := httptest.NewRequest(http.MethodPost, "/api/command", bytes.NewReader(reqBody))
			w := httptest.NewRecorder()
			d.ServeHTTP(w, req)
			i++
		}
	})

	b.Run("cache_hit", func(b *testing.B) {
		// Prime the dedup cache.
		req := httptest.NewRequest(http.MethodPost, "/api/command", bytes.NewReader(body))
		w := httptest.NewRecorder()
		d.ServeHTTP(w, req)

		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			req := httptest.NewRequest(http.MethodPost, "/api/command", bytes.NewReader(body))
			w := httptest.NewRecorder()
			d.ServeHTTP(w, req)
		}
	})

	b.Run("unknown_command", func(b *testing.B) {
		unknownBody, _ := json.Marshal(vibekit.ClientCommand{
			Type:      "nonexistent_cmd",
			RequestID: "req-unknown",
			ChatID:    "chat-bench-1",
		})
		b.ReportAllocs()
		for b.Loop() {
			req := httptest.NewRequest(http.MethodPost, "/api/command", bytes.NewReader(unknownBody))
			w := httptest.NewRecorder()
			d.ServeHTTP(w, req)
		}
	})
}

// hostDouble is what a single all-in-one test double answers: every role a
// handler declares, plus the envelope seam. It exists ONLY for the doubles in
// this package's tests — production code has no aggregate over the roles, and
// the shape pin in shape_test.go reads production files only, for exactly this
// reason. A double stands in for the whole host, so naming every role once is
// what lets one value fill every slot a handler asks for.
type hostDouble interface {
	DedupGate
	BridgeAccess
	ChatAccess
	PendingPermAccess
	TerminalAccess
	LifecycleAccess
	MCPAccess
	TurnOutcomeAccess
}

var _ hostDouble = (*benchDeps)(nil)

// promptRolesOf wires one double into the prompt path's role set, the way
// RegisterDefaults wires the Hub into it.
func promptRolesOf(d hostDouble) *promptRoles {
	return &promptRoles{
		bridges:     d,
		chats:       d,
		workspace:   Workspace{Dir: "/tmp", ConfigDir: "/tmp"},
		lifecycle:   d,
		mcp:         d,
		turnOutcome: d,
	}
}
