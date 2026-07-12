package hub

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/pending"
)

// TestForward_BridgeExitFlushesPending pins fix #3: when a chat's bridge
// exits (Forward's NotifCh range returns) any staged supervised write is
// flushed — the parked fs-handler waiter is released and a
// pending_changes_cleared(bridge_exited) event is broadcast — so a dead
// bridge can't leave a parked goroutine and a phantom "awaiting approval"
// op that replays to reconnecting clients. Cancel/delete/mode-disable
// already flush; this is the bridge-exit sibling.
func TestForward_BridgeExitFlushesPending(t *testing.T) {
	h, _, br := newTestHub()
	ctx := context.Background()

	wait, _, err := h.perm.pending.Add(ctx, &pending.AddParams{
		ToolCallID: "fs-c1-1", ChatID: "c1", Path: "a.go", Kind: pending.KindEdit, NewText: "x",
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if h.perm.pending.CountForChat("c1") != 1 {
		t.Fatalf("precondition: op not staged; count=%d", h.perm.pending.CountForChat("c1"))
	}

	// Simulate bridge exit: closing notifCh makes Forward's range return,
	// then Forward flushes the chat's pending ops.
	br.Stop()
	h.coord.Forward("c1", br)

	if got := h.perm.pending.CountForChat("c1"); got != 0 {
		t.Fatalf("pending count after bridge exit = %d, want 0 (flushed)", got)
	}
	select {
	case <-wait:
	case <-time.After(time.Second):
		t.Fatal("staged op waiter not released on bridge exit")
	}

	ev := lastReplayEventOfType(h, api.EventPendingChangesCleared)
	if ev == nil {
		t.Fatal("no pending_changes_cleared broadcast on bridge exit")
	}
	raw, err := json.Marshal(ev.Payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var p api.PendingChangesClearedPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if p.Reason != api.ClearReasonBridgeExited {
		t.Errorf("cleared reason = %q, want %q", p.Reason, api.ClearReasonBridgeExited)
	}
}

// TestForward_BridgeExitNoPendingIsQuiet confirms the flush is a clean
// no-op when the chat has no staged ops: no pending_changes_cleared
// event is broadcast (flushPendingForChat only emits when it rejected
// at least one op), so the idempotent double-flush from the chat-delete
// path (which flushes before CloseBridge) stays silent here.
func TestForward_BridgeExitNoPendingIsQuiet(t *testing.T) {
	h, _, br := newTestHub()

	br.Stop()
	h.coord.Forward("c1", br)

	if ev := lastReplayEventOfType(h, api.EventPendingChangesCleared); ev != nil {
		t.Errorf("pending_changes_cleared broadcast with no staged ops: %+v", ev)
	}
}
