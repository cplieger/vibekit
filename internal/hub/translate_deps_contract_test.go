package hub

import (
	"testing"

	"github.com/cplieger/vibekit/internal/translate"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// TranslateRolesContractTest exercises every method of every translate role
// against a concrete wiring. Purpose: catch drift between Hub's implementations
// and the translate package's expectations.
//
// It takes the Roles value rather than one aggregate interface, so the subject
// is the wiring hub actually performs: a role Hub stopped satisfying fails to
// compile at the literal, and a method that regresses fails here.
func TranslateRolesContractTest(t *testing.T, newRoles func(t *testing.T) *translate.Roles) {
	t.Helper()

	t.Run("ChatRecords_non_nil", func(t *testing.T) {
		r := newRoles(t)
		if r.Streaming.ChatRecords() == nil {
			t.Error("ChatRecords() returned nil")
		}
	})

	t.Run("WorkDir_non_empty", func(t *testing.T) {
		r := newRoles(t)
		if r.Streaming.WorkDir() == "" {
			t.Error("WorkDir() returned empty string")
		}
	})

	t.Run("Broadcast_does_not_panic", func(t *testing.T) {
		r := newRoles(t)
		r.Streaming.Broadcast(t.Context(), vibekit.ServerEvent{Type: "test_event", ChatID: "chat-1"})
	})

	t.Run("ParentACPSession_empty_for_unknown_chat", func(t *testing.T) {
		r := newRoles(t)
		if s := r.Streaming.ParentACPSession("unknown-chat"); s != "" {
			t.Errorf("ParentACPSession(unknown) = %q, want empty", s)
		}
	})

	t.Run("IsHookStatusEnabled_returns_bool", func(t *testing.T) {
		r := newRoles(t)
		_ = r.Streaming.IsHookStatusEnabled()
	})

	t.Run("TerminalOutput_unknown_terminal_is_not_ok", func(t *testing.T) {
		// The false direction is the one that matters: an unknown terminal must
		// report not-known, because adoption logs a miss on exactly that.
		r := newRoles(t)
		if _, _, ok := r.Streaming.TerminalOutput("term-never-created"); ok {
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
		r.Perms.PendingPermsAdd(42, vibekit.ServerEvent{Type: "permission_needed", ChatID: "c1"})
	})

	t.Run("NotifyPush_does_not_panic", func(t *testing.T) {
		r := newRoles(t)
		r.Perms.NotifyPush(t.Context(), "test body", vibekit.PushKindPermission, "")
	})

	t.Run("BufferStore_non_nil", func(t *testing.T) {
		r := newRoles(t)
		if r.Streaming.BufferStore() == nil {
			t.Error("BufferStore() returned nil")
		}
	})

	t.Run("LineTracker_non_nil", func(t *testing.T) {
		r := newRoles(t)
		if r.Streaming.LineTracker() == nil {
			t.Error("LineTracker() returned nil")
		}
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
		if r.RunOrigin.IsScheduledRun("wf-never-launched") {
			t.Error("IsScheduledRun(unlaunched) = true, want false")
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
		return &translate.Roles{
			Streaming:  h,
			Perms:      h,
			MCP:        h.MCPRecorder(),
			Governance: h.config,
			RunOrigin:  h.runs,
			RunBounds:  h.runs,
		}
	})
}
