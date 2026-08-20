package agent

import (
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/buffer"
	"github.com/cplieger/vibekit/internal/translate"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// TranslateRolesContractTest exercises every method of every translate role
// against a concrete wiring. Purpose: catch drift between Runtime's implementations
// and the translate package's expectations.
//
// It takes the Roles value rather than one aggregate interface, so the subject is
// the wiring hub actually performs: a role whose owner stopped satisfying it
// fails to compile at the literal, and a method that regresses fails here.
//
// Every field is now a different OWNER rather than two composites both filled
// with the runtime, which is what this test made visible: it used to reach eleven
// methods through r.Streaming and r.Perms, so it was really asserting that one
// type answered everything.
func TranslateRolesContractTest(t *testing.T, newRoles func(t *testing.T) *translate.Roles) {
	t.Helper()

	t.Run("chat_store_is_wired", func(t *testing.T) {
		r := newRoles(t)
		if _, ok := r.Chats.Get(t.Context(), "no-such-chat"); ok {
			t.Error("Chats.Get on an empty store reported found")
		}
	})

	t.Run("WorkDir_non_empty", func(t *testing.T) {
		r := newRoles(t)
		if r.WorkDir == "" {
			t.Error("WorkDir is empty")
		}
	})

	t.Run("Broadcast_does_not_panic", func(t *testing.T) {
		r := newRoles(t)
		r.Bus.Broadcast(t.Context(), vibekit.ServerEvent{Type: "test_event", ChatID: "chat-1"})
	})

	t.Run("ParentACPSession_empty_for_unknown_chat", func(t *testing.T) {
		r := newRoles(t)
		if s := r.Sessions.ParentACPSession("unknown-chat"); s != "" {
			t.Errorf("ParentACPSession(unknown) = %q, want empty", s)
		}
	})

	t.Run("IsHookStatusEnabled_returns_bool", func(t *testing.T) {
		r := newRoles(t)
		_ = r.HookStatus.IsHookStatusEnabled()
	})

	t.Run("TerminalOutput_unknown_terminal_is_not_ok", func(t *testing.T) {
		// The false direction is the one that matters: an unknown terminal must
		// report not-known, because adoption logs a miss on exactly that.
		r := newRoles(t)
		if _, _, ok := r.Terminals.TerminalOutput("term-never-created"); ok {
			t.Error("TerminalOutput(unknown) reported ok, want false")
		}
	})

	t.Run("MCPRecorder_does_not_panic", func(t *testing.T) {
		r := newRoles(t)
		if r.MCP == nil {
			t.Fatal("MCP role is nil")
		}
		r.MCP.RecordConnected(t.Context(), "test-server", nil, nil, nil)
		r.MCP.SignalReady()
	})

	t.Run("PendingPermsAdd_does_not_panic", func(t *testing.T) {
		r := newRoles(t)
		r.PendingPerms.PendingPermsAdd(42, vibekit.ServerEvent{Type: "permission_needed", ChatID: "c1"})
	})

	t.Run("NotifyPush_does_not_panic", func(t *testing.T) {
		r := newRoles(t)
		r.Push.NotifyPush(t.Context(), "test body", vibekit.PushKindPermission, "")
	})

	t.Run("buffers_and_lines_are_wired", func(t *testing.T) {
		r := newRoles(t)
		if r.Buffers.GetOrInit("c1") == nil {
			t.Error("Buffers.GetOrInit returned nil")
		}
		r.Lines.RecordFromDiffs("c1", nil, 0, "")
	})

	t.Run("SetGovernance_does_not_panic", func(t *testing.T) {
		r := newRoles(t)
		r.Governance.SetGovernance(vibekit.GovernanceStatePayload{})
	})

	t.Run("IsScheduledRun_false_for_an_unlaunched_run", func(t *testing.T) {
		// A run nothing launched is not scheduled. This is the direction that
		// matters: reporting a manual run as scheduled would put a start toast on
		// every launch the user made by hand.
		r := newRoles(t)
		if r.RunOrigin.IsScheduled("wf-never-launched") {
			t.Error("IsScheduled(unlaunched) = true, want false")
		}
	})

	t.Run("StepTurnCapExceeded_does_not_panic", func(t *testing.T) {
		r := newRoles(t)
		r.RunBounds.StepTurnCapExceeded("wf-never-launched", "node-1", 99)
	})
}

func TestHub_TranslateRolesContract(t *testing.T) {
	TranslateRolesContractTest(t, func(t *testing.T) *translate.Roles {
		t.Helper()
		h, _, _ := newTestHub()
		// The production wiring itself, not a copy of it: a copy keeps passing
		// after the real one changes.
		return h.translateRoles()
	})
}

// TestRequireWired_RefusesBothNils pins the constructor guard, and the second
// case is the one that shipped.
//
// A role assigned from a nil *T is a non-nil INTERFACE holding a nil pointer, so
// an IsNil() check on the field passes while the receiver is nil — the typed-nil
// trap. The first version of this guard had exactly that hole, and a
// deliberately-late h.lines assignment walked straight through it.
//
// The guard is production code rather than a test over New's output on purpose:
// a test that rebuilds the roles after New has returned sees every field
// populated, because by then they are. Only a check at the call site sees WHEN
// New read them, which is the whole bug.
func TestRequireWired_RefusesBothNils(t *testing.T) {
	full := func() *translate.Roles {
		h, _, _ := newTestHub()
		return h.translateRoles()
	}

	t.Run("a fully wired set is returned unchanged", func(t *testing.T) {
		r := full()
		if got := requireWired(r); got != r {
			t.Error("requireWired did not return its argument")
		}
	})

	t.Run("an outright nil role panics naming the field", func(t *testing.T) {
		r := full()
		r.Bus = nil
		defer func() {
			msg, _ := recover().(string)
			if !strings.Contains(msg, "Bus") || !strings.Contains(msg, "is nil") {
				t.Errorf("panic = %q, want it to name Bus as nil", msg)
			}
		}()
		requireWired(r)
		t.Error("requireWired accepted a nil role")
	})

	t.Run("a typed nil panics naming the concrete type", func(t *testing.T) {
		r := full()
		r.Lines = (*buffer.LineTracker)(nil)
		defer func() {
			msg, _ := recover().(string)
			if !strings.Contains(msg, "Lines") || !strings.Contains(msg, "*buffer.LineTracker") {
				t.Errorf("panic = %q, want it to name Lines and the nil concrete type", msg)
			}
		}()
		requireWired(r)
		t.Error("requireWired accepted an interface holding a nil pointer")
	})
}
