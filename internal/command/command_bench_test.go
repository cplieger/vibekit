package command

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// benchDeps is a minimal host double for benchmarking dispatch overhead.
type benchDeps struct {
	// primeOpen is what PrimeTurnOpen answers, so a test can put the chat in the
	// PRIME window a steer must be refused in.
	primeOpen bool
}

func newBenchDeps() *benchDeps { return &benchDeps{} }

// The store methods are answered directly now: Roles holds ChatStore, so the
// ChatStore() getter it used to return is gone.
func (d *benchDeps) Get(context.Context, vibekit.ChatID) (*vibekit.Chat, bool) { return nil, false }

func (d *benchDeps) Mutate(context.Context, vibekit.ChatID, func(*vibekit.Chat, bool) bool) error {
	return nil
}

func (d *benchDeps) AppendMessage(context.Context, vibekit.ChatID, *vibekit.Message) error {
	return nil
}

func (d *benchDeps) SetDraft(context.Context, vibekit.ChatID, string) (*vibekit.ComposerState, error) {
	return nil, nil
}

func (d *benchDeps) SetAttachments(context.Context, vibekit.ChatID, []string) (*vibekit.ComposerState, error) {
	return nil, nil
}
func (d *benchDeps) Delete(context.Context, vibekit.ChatID) error   { return nil }
func (d *benchDeps) Broadcast(context.Context, vibekit.ServerEvent) {}
func (d *benchDeps) Bridge(vibekit.ChatID) Bridge                   { return nil }
func (d *benchDeps) OpenBridge(context.Context, vibekit.ChatID, string) (Bridge, error) {
	return nil, nil
}
func (d *benchDeps) CloseBridge(vibekit.ChatID)                                    {}
func (d *benchDeps) ClearPendingPermsForChat(vibekit.ChatID)                       {}
func (d *benchDeps) TakePendingPerm(vibekit.ChatID, int64, vibekit.SettledBy) bool { return true }
func (d *benchDeps) TurnContext(reqCtx context.Context) (context.Context, context.CancelFunc) {
	return context.WithCancel(context.WithoutCancel(reqCtx))
}
func (d *benchDeps) InflightAdd(int)                                  {}
func (d *benchDeps) InflightDone()                                    {}
func (d *benchDeps) DeleteChatState(context.Context, vibekit.ChatID)  {}
func (d *benchDeps) CloseChatState(context.Context, vibekit.ChatID)   {}
func (d *benchDeps) KillForTurn(vibekit.ChatID)                       {}
func (d *benchDeps) WaitForReady(context.Context, time.Duration) bool { return true }
func (d *benchDeps) PendingSummary(context.Context) MCPPendingSummary { return MCPPendingSummary{} }
func (d *benchDeps) PrimeIfNeeded(context.Context, vibekit.ChatID)    {}
func (d *benchDeps) PrimeFromChat(vibekit.ChatID, vibekit.ChatID)     {}

// OpenTurn answers a real epoch, not zero. Zero is the REFUSAL — the local-shell
// source rule declines while another turn is open — so a stub returning it makes
// every `!cmd` test 409.
func (d *benchDeps) OpenTurn(context.Context, vibekit.ChatID, vibekit.TurnOpenSource) vibekit.TurnEpoch {
	return 1
}

func (d *benchDeps) AwaitTurn(context.Context, vibekit.ChatID, vibekit.TurnEpoch) (vibekit.TurnResult, error) {
	return vibekit.TurnResult{}, vibekit.ErrNoSuchTurn
}

func (d *benchDeps) ReleaseTurn(vibekit.ChatID, vibekit.TurnEpoch) {}
func (d *benchDeps) SettleTurnOnResponse(context.Context, vibekit.ChatID, vibekit.TurnEpoch, uint64, *vibekit.RPCResponse) {
}

func (d *benchDeps) TurnOpenedAfter(vibekit.ChatID, vibekit.TurnEpoch) bool { return false }

func (d *benchDeps) PrimeTurnOpen(vibekit.ChatID) bool { return d.primeOpen }
func (d *benchDeps) FinalizeLocalShellTurn(context.Context, vibekit.ChatID, vibekit.TurnEpoch) {
}

func (d *benchDeps) AbandonInFlightTurn(context.Context, vibekit.ChatID, vibekit.TurnEpoch, string) {
}

// TestBenchDeps_NoPanic verifies that every benchDeps method can be called
// with zero-value arguments without panicking.
func TestBenchDeps_NoPanic(t *testing.T) {
	d := newBenchDeps()

	// Boolean/scalar returns. The workspace paths are not here any more: they
	// are a Workspace value, so there is no double method to exercise.
	if turnCtx, cancel := d.TurnContext(t.Context()); turnCtx == nil {
		t.Error("TurnContext() returned a nil context")
	} else {
		cancel()
	}
	if !d.WaitForReady(t.Context(), time.Millisecond) {
		t.Error("MCPWaitForReady returned false")
	}

	// No-op methods must not panic.
	d.Broadcast(t.Context(), vibekit.ServerEvent{})
	d.CloseBridge("x")
	d.ClearPendingPermsForChat("x")
	d.TakePendingPerm("x", 0, vibekit.SettledByUser)
	d.InflightAdd(1)
	d.InflightDone()
	d.DeleteChatState(t.Context(), "x")
	d.PrimeIfNeeded(t.Context(), "x")
	d.OpenTurn(t.Context(), "x", vibekit.TurnSourcePrompt)
	d.ReleaseTurn("x", 0)
}

// TestBenchDeps_Contract documents which methods intentionally return nil
// (safe only because benchmarks don't invoke handlers that call them) vs.
// which return usable values needed by the dispatch path.
func TestBenchDeps_Contract(t *testing.T) {
	d := newBenchDeps()

	// --- Safe usable values (dispatch path depends on these) ---
	t.Run("usable_values", func(t *testing.T) {
		if turnCtx, cancel := d.TurnContext(t.Context()); turnCtx == nil {
			t.Error("TurnContext must return a non-nil context")
		} else {
			cancel()
		}
	})

	// --- Intentionally nil (safe only for dispatch-overhead benchmarks) ---
	t.Run("intentionally_nil", func(t *testing.T) {
		if d.Bridge("any") != nil {
			t.Error("Bridge expected nil for bench stub")
		}
	})

	// --- No-panic on zero-value calls ---
	t.Run("no_panic_zero_value_calls", func(t *testing.T) {
		d.Broadcast(t.Context(), vibekit.ServerEvent{})
		d.CloseBridge("x")
		d.ClearPendingPermsForChat("x")
		d.TakePendingPerm("x", 0, vibekit.SettledByUser)
		d.InflightAdd(1)
		d.InflightDone()
		d.DeleteChatState(t.Context(), "x")
		d.PrimeIfNeeded(t.Context(), "x")
		if _, err := d.AwaitTurn(t.Context(), "x", 0); err == nil {
			t.Error("AwaitTurn on the stub should report no such turn")
		}
		d.ReleaseTurn("x", 0)
		d.SettleTurnOnResponse(t.Context(), "x", 0, 0, nil)
		d.FinalizeLocalShellTurn(t.Context(), "x", 0)
		if d.TurnOpenedAfter("x", 0) {
			t.Error("TurnOpenedAfter on the stub should report false")
		}
		if _, err := d.OpenBridge(t.Context(), "x", ""); err != nil {
			t.Errorf("OpenBridge returned error: %v", err)
		}
	})
}

// BenchmarkDispatcherServeHTTP measures the envelope path: decode, validate,
// table lookup, handler. It had cache_miss / cache_hit sub-benchmarks when the
// dispatcher ran its own request_id dedup; both now measure the identical path,
// so there is one. Replay cost belongs to the header middleware and is
// benchmarked with it.
func BenchmarkDispatcherServeHTTP(b *testing.B) {
	d := New()
	d.Register("create_chat", func(context.Context, *vibekit.ClientCommand) (any, error) {
		return responseOK, nil
	})

	body, _ := json.Marshal(vibekit.ClientCommand{
		Type:   "create_chat",
		ChatID: "chat-bench-1",
	})

	b.Run("dispatch", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			req := httptest.NewRequest(http.MethodPost, "/api/command", bytes.NewReader(body))
			w := httptest.NewRecorder()
			d.ServeHTTP(w, req)
		}
	})

	b.Run("unknown_command", func(b *testing.B) {
		unknownBody, _ := json.Marshal(vibekit.ClientCommand{
			Type:   "nonexistent_cmd",
			ChatID: "chat-bench-1",
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
// handler declares. It exists ONLY for the doubles in
// this package's tests — production code has no aggregate over the roles, and
// the shape pin in shape_test.go reads production files only, for exactly this
// reason. A double stands in for the whole host, so naming every role once is
// what lets one value fill every slot a handler asks for.
type hostDouble interface {
	BridgeAccess
	ChatStore
	Broadcaster
	ChatTeardown
	PendingPermAccess
	TerminalAccess
	LifecycleAccess
	MCPAccess
	TurnOutcomeAccess
}

var _ hostDouble = (*benchDeps)(nil)

// promptRolesOf wires one double into the prompt path's role set, the way
// RegisterDefaults wires the Runtime into it.
func promptRolesOf(d hostDouble) *promptRoles {
	return &promptRoles{
		bridges:     d,
		chats:       d,
		bus:         d,
		workspace:   Workspace{Dir: "/tmp", ConfigDir: "/tmp"},
		lifecycle:   d,
		mcp:         d,
		turnOutcome: d,
	}
}
