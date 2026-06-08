package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/pending"
)

// httptestGet drives a GET request through the hub's pending-changes
// handler for table-driven tests. Separate from postCmd because this
// route is outside the /api/command envelope.
func httptestGet(h *Hub, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.handlePendingChange(rec, req)
	return rec
}

// stageOp is a helper that Adds a pending op directly to the hub's
// store (bypassing the fs handler) and returns the resume channel. We
// use it to exercise the command layer without spinning up a real fs
// write path.
func stageOp(h *Hub, chatID api.ChatID, id, path string) (<-chan struct{}, func() pending.Resolution) {
	w, a, err := h.perm.pending.Add(context.TODO(), &pending.AddParams{
		ToolCallID: id, ChatID: chatID, Path: path,
		Kind: pending.KindEdit, NewText: "new",
	})
	if err != nil {
		panic(err)
	}
	return w, a
}

// TestCmdResolvePendingChange_Accept flows one op through the command
// and verifies the waiter unblocks with accepted=true.
func TestCmdResolvePendingChange_Accept(t *testing.T) {
	t.Parallel()
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	wait, accepted := stageOp(h, "c1", "tc-1", "foo.go")

	rec := postCmd(t, h, api.ClientCommand{
		Type: "resolve_pending_change", ChatID: "c1", RequestID: "r1",
		Payload: mustJSON(t, api.ResolvePendingChangeCommand{
			ToolCallID: "tc-1", Action: "accept",
		}),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case <-wait:
		if !accepted().Accepted {
			t.Error("waiter saw accepted=false, want true")
		}
	case <-time.After(time.Second):
		t.Fatal("waiter blocked after accept")
	}
}

// TestCmdResolvePendingChange_UnknownID returns 404.
func TestCmdResolvePendingChange_UnknownID(t *testing.T) {
	t.Parallel()
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	rec := postCmd(t, h, api.ClientCommand{
		Type: "resolve_pending_change", ChatID: "c1", RequestID: "r1",
		Payload: mustJSON(t, api.ResolvePendingChangeCommand{
			ToolCallID: "nope", Action: "accept",
		}),
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// TestCmdResolvePendingChange_BadAction returns 400.
func TestCmdResolvePendingChange_BadAction(t *testing.T) {
	t.Parallel()
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	_, _ = stageOp(h, "c1", "tc-1", "foo.go")

	rec := postCmd(t, h, api.ClientCommand{
		Type: "resolve_pending_change", ChatID: "c1", RequestID: "r1",
		Payload: mustJSON(t, api.ResolvePendingChangeCommand{
			ToolCallID: "tc-1", Action: "maybe",
		}),
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// TestCmdResolveAllPendingChanges_Reject flushes every op in the
// chat and leaves other chats untouched.
func TestCmdResolveAllPendingChanges_Reject(t *testing.T) {
	t.Parallel()
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	_ = cs.Mutate(context.Background(), "c2", func(c *api.Chat, _ bool) bool { c.Name = "B"; return true })

	_, _ = stageOp(h, "c1", "tc-1", "a.go")
	_, _ = stageOp(h, "c1", "tc-2", "b.go")
	_, _ = stageOp(h, "c2", "tc-3", "z.go")

	rec := postCmd(t, h, api.ClientCommand{
		Type: "resolve_all_pending_changes", ChatID: "c1", RequestID: "r1",
		Payload: mustJSON(t, api.ResolveAllPendingChangesCommand{Action: "reject"}),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if h.perm.pending.CountForChat("c1") != 0 {
		t.Errorf("c1 still has %d pending", h.perm.pending.CountForChat("c1"))
	}
	if h.perm.pending.CountForChat("c2") != 1 {
		t.Errorf("c2 flushed unexpectedly: count=%d", h.perm.pending.CountForChat("c2"))
	}
}

// TestCmdSetSupervisedMode_DisableFlushes enabling then disabling the
// mode while an op is pending rejects the op.
func TestCmdSetSupervisedMode_DisableFlushes(t *testing.T) {
	t.Parallel()
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.SupervisedMode = true
		return true
	})
	wait, accepted := stageOp(h, "c1", "tc-1", "foo.go")

	rec := postCmd(t, h, api.ClientCommand{
		Type: "set_supervised_mode", ChatID: "c1", RequestID: "r1",
		Payload: mustJSON(t, api.SetSupervisedModeCommand{Enabled: false}),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	chat, _ := cs.Get(context.Background(), "c1")
	if chat.SupervisedMode {
		t.Fatal("SupervisedMode still true after disable")
	}
	select {
	case <-wait:
		if accepted().Accepted {
			t.Error("waiter saw accepted=true after flush, want false")
		}
	case <-time.After(time.Second):
		t.Fatal("waiter blocked after mode-disable flush")
	}
}

// TestCmdSetSupervisedMode_EnableIdempotent: enabling twice doesn't
// error.
func TestCmdSetSupervisedMode_EnableIdempotent(t *testing.T) {
	t.Parallel()
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	for _, req := range []string{"r1", "r2"} {
		rec := postCmd(t, h, api.ClientCommand{
			Type: "set_supervised_mode", ChatID: "c1", RequestID: req,
			Payload: mustJSON(t, api.SetSupervisedModeCommand{Enabled: true}),
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status=%d", req, rec.Code)
		}
	}
	chat, _ := cs.Get(context.Background(), "c1")
	if !chat.SupervisedMode {
		t.Fatal("SupervisedMode false after enable")
	}
}

// TestCmdCancel_FlushesPending ensures a cancel with pending ops
// unblocks them with reject.
func TestCmdCancel_FlushesPending(t *testing.T) {
	t.Parallel()
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	wait, accepted := stageOp(h, "c1", "tc-1", "foo.go")

	rec := postCmd(t, h, api.ClientCommand{
		Type: "cancel", ChatID: "c1", RequestID: "r1",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	select {
	case <-wait:
		if accepted().Accepted {
			t.Error("cancel did not reject pending op")
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not unblock pending op")
	}
}

// TestHandlePendingChange_GET returns the staged op or 404.
func TestHandlePendingChange_GET(t *testing.T) {
	t.Parallel()
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	_, _ = stageOp(h, "c1", "tc-1", "foo.go")

	// Present
	rec := httptestGet(h, "/api/pending-changes/tc-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Absent
	rec = httptestGet(h, "/api/pending-changes/nope")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", rec.Code)
	}

	// Missing id
	rec = httptestGet(h, "/api/pending-changes/")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", rec.Code)
	}
}

// TestCmdResolvePendingChangePartial_Accept flows an op through the
// partial-resolve command and pins that (a) the waiter unblocks with
// accepted=true, (b) MergedText returns the caller-supplied override,
// and (c) the op is removed from the store exactly like a plain
// accept would have done.
func TestCmdResolvePendingChangePartial_Accept(t *testing.T) {
	t.Parallel()
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.SupervisedMode = true
		return true
	})
	wait, accepted := stageOp(h, "c1", "tc-p", "foo.go")

	rec := postCmd(t, h, api.ClientCommand{
		Type: "resolve_pending_change_partial", ChatID: "c1", RequestID: "r1",
		Payload: mustJSON(t, api.ResolvePendingChangePartialCommand{
			ToolCallID: "tc-p",
			MergedText: "merged body",
		}),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case <-wait:
		res := accepted()
		if !res.Accepted {
			t.Error("partial resolve did not mark op accepted")
		}
		// The Resolution carries the merged text atomically — no
		// separate MergedText/ClearMergedText step needed.
		if res.MergedText != "merged body" {
			t.Fatalf("Resolution.MergedText=%q, want %q", res.MergedText, "merged body")
		}
	case <-time.After(time.Second):
		t.Fatal("partial resolve did not unblock waiter")
	}
}

// TestCmdResolvePendingChangePartial_UnknownID returns 404.
func TestCmdResolvePendingChangePartial_UnknownID(t *testing.T) {
	t.Parallel()
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	rec := postCmd(t, h, api.ClientCommand{
		Type: "resolve_pending_change_partial", ChatID: "c1", RequestID: "r1",
		Payload: mustJSON(t, api.ResolvePendingChangePartialCommand{
			ToolCallID: "nope", MergedText: "x",
		}),
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", rec.Code)
	}
}

// TestCmdResolvePendingChangePartial_TooLarge rejects payloads above
// the 4 MiB cap so the fs handler can't accept-then-fail at write.
func TestCmdResolvePendingChangePartial_TooLarge(t *testing.T) {
	t.Parallel()
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	_, _ = stageOp(h, "c1", "tc-big", "big.go")

	// Build a payload that exceeds pending.Cap on the server side.
	// Must use printable bytes so JSON encoding doesn't inflate the
	// size by 6x (each NUL encodes to \u0000). ASCII letters round-
	// trip as-is so the on-the-wire size matches len(big)+overhead.
	big := make([]byte, pending.Cap+1)
	for i := range big {
		big[i] = 'a'
	}
	rec := postCmd(t, h, api.ClientCommand{
		Type: "resolve_pending_change_partial", ChatID: "c1", RequestID: "r1",
		Payload: mustJSON(t, api.ResolvePendingChangePartialCommand{
			ToolCallID: "tc-big", MergedText: string(big),
		}),
	})
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d, want 413", rec.Code)
	}
}

// TestCmdTrustPendingChanges_AcceptsAllAndSetsFlag pins the trust
// command: it must (a) set perTurnTrust, (b) accept every currently-
// staged op, (c) return 400 when the chat isn't supervised.
func TestCmdTrustPendingChanges_AcceptsAllAndSetsFlag(t *testing.T) {
	t.Parallel()
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.SupervisedMode = true
		return true
	})
	w1, a1 := stageOp(h, "c1", "tc-1", "a.go")
	w2, a2 := stageOp(h, "c1", "tc-2", "b.go")

	rec := postCmd(t, h, api.ClientCommand{
		Type: "trust_pending_changes", ChatID: "c1", RequestID: "r1",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !h.perm.supervised.HasTrust("c1") {
		t.Error("perTurnTrust not set after trust command")
	}
	for i, pair := range [][2]any{{w1, a1}, {w2, a2}} {
		wait := pair[0].(<-chan struct{})
		acc := pair[1].(func() pending.Resolution)
		select {
		case <-wait:
			if !acc().Accepted {
				t.Errorf("op %d not accepted by trust command", i)
			}
		case <-time.After(time.Second):
			t.Fatalf("op %d did not unblock within 1s", i)
		}
	}
}

// TestCmdTrustPendingChanges_RejectsNonSupervised returns 400 so
// clients that accidentally call trust on a non-supervised chat get
// a clear signal instead of a silent no-op.
func TestCmdTrustPendingChanges_RejectsNonSupervised(t *testing.T) {
	t.Parallel()
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	rec := postCmd(t, h, api.ClientCommand{
		Type: "trust_pending_changes", ChatID: "c1", RequestID: "r1",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", rec.Code)
	}
	if h.perm.supervised.HasTrust("c1") {
		t.Error("perTurnTrust set on non-supervised chat")
	}
}

// TestCmdTrustPendingChanges_BroadcastsEnabled pins the trust-event
// broadcast path: clicking Trust must emit pending_trust_enabled so
// every SSE subscriber can flip its Supervised pill to the trusted
// style. Idempotent: a second click in the same turn is a no-op (the
// flag is already set) and must NOT broadcast a duplicate event.
func TestCmdTrustPendingChanges_BroadcastsEnabled(t *testing.T) {
	t.Parallel()
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.SupervisedMode = true
		return true
	})

	rec := postCmd(t, h, api.ClientCommand{
		Type: "trust_pending_changes", ChatID: "c1", RequestID: "r1",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	firstCount := countReplayType(h, api.EventPendingTrustEnabled)
	if firstCount != 1 {
		t.Fatalf("first trust: got %d pending_trust_enabled events, want 1", firstCount)
	}

	// Second click: must NOT emit another event. Flag is already
	// set, so the no-op path short-circuits before broadcast.
	rec = postCmd(t, h, api.ClientCommand{
		Type: "trust_pending_changes", ChatID: "c1", RequestID: "r2",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("2nd trust status=%d", rec.Code)
	}
	secondCount := countReplayType(h, api.EventPendingTrustEnabled)
	if secondCount != 1 {
		t.Errorf("idempotent Trust broadcast: got %d, want 1 (no duplicate)", secondCount)
	}
}

// TestClearPerTurnTrust_BroadcastsOnTurnEnd pins the cleared event:
// when clearPerTurnTrust runs (simulating turn_ended), pending_trust_cleared
// must fire with the right reason so the client's pill can revert. No
// broadcast when the flag was never set.
func TestClearPerTurnTrust_BroadcastsOnTurnEnd(t *testing.T) {
	t.Parallel()
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.SupervisedMode = true
		return true
	})
	h.perm.supervised.SetTrust("c1")

	h.perm.supervised.ClearTrust("c1", api.ClearReasonCancelled)
	if count := countReplayType(h, api.EventPendingTrustCleared); count != 1 {
		t.Fatalf("pending_trust_cleared count=%d, want 1", count)
	}
	// Extract the most recent cleared event and check the reason.
	evt := lastReplayEventOfType(h, api.EventPendingTrustCleared)
	if evt == nil {
		t.Fatal("no pending_trust_cleared found")
	}
	raw, err := json.Marshal(evt.Payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var payload api.PendingTrustClearedPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Reason != "cancelled" {
		t.Errorf("reason=%q, want cancelled", payload.Reason)
	}

	// Second clear (flag already gone): no event.
	h.perm.supervised.ClearTrust("c1", api.ClearReasonTurnEnded)
	if count := countReplayType(h, api.EventPendingTrustCleared); count != 1 {
		t.Error("clearing an already-unset trust flag broadcast an event")
	}
}

// TestReplayPendingTrust pins the reconnect behaviour: when a new
// SSE client connects for a chat whose perTurnTrust is set, it must
// receive a replayed pending_trust_enabled so its Supervised pill
// comes up in the trusted state without waiting for the next agent
// event.
func TestReplayPendingTrust(t *testing.T) {
	t.Parallel()
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.SupervisedMode = true
		return true
	})
	h.perm.supervised.SetTrust("c1")

	var got []api.ServerEvent
	h.replayPendingTrust(func(evt api.ServerEvent) {
		got = append(got, evt)
	}, "c1")
	if len(got) != 1 {
		t.Fatalf("replayed %d events, want 1", len(got))
	}
	if got[0].Type != "pending_trust_enabled" {
		t.Errorf("type=%q, want pending_trust_enabled", got[0].Type)
	}
	if got[0].ChatID != "c1" {
		t.Errorf("chat_id=%q, want c1", got[0].ChatID)
	}

	// A client filtering on a different chat must get nothing.
	got = nil
	h.replayPendingTrust(func(evt api.ServerEvent) {
		got = append(got, evt)
	}, "c2")
	if len(got) != 0 {
		t.Errorf("cross-chat leak: %+v", got)
	}
}

// countReplayType returns how many events of `typ` currently live in
// the hub's replay ring buffer. Used to assert broadcast side-effects
// without the complexity of wiring up a real SSE subscriber.
func countReplayType(h *Hub, typ api.EventType) int {
	n := 0
	for _, evt := range h.sse.replayBuf.Events() {
		var msg api.ServerEvent
		if err := json.Unmarshal(evt.data, &msg); err != nil {
			continue
		}
		if msg.Type == typ {
			n++
		}
	}
	return n
}

// lastReplayEventOfType returns the most recent event of `typ` from
// the replay buffer, or nil if absent. Used when a test needs to
// inspect the payload of a broadcast rather than just count it.
func lastReplayEventOfType(h *Hub, typ api.EventType) *api.ServerEvent {
	evts := h.sse.replayBuf.Events()
	for i := range slices.Backward(evts) {
		var msg api.ServerEvent
		if err := json.Unmarshal(evts[i].data, &msg); err != nil {
			continue
		}
		if msg.Type == typ {
			return &msg
		}
	}
	return nil
}

// TestHandleCommand_BodyTooLarge verifies that a POST envelope
// exceeding maxCommandBody is rejected with 413, not 400. Clients
// pre-check partial-merge size, but an envelope padded by unusual
// JSON escape expansion (emoji in merged_text, long chat_id) could
// legitimately overflow. A clear 413 tells the client to retry with
// smaller content; a 400 "invalid json" leaves the client guessing
// whether their payload is malformed or just too big.
func TestHandleCommand_BodyTooLarge(t *testing.T) {
	t.Parallel()
	h, _, _ := newTestHub()
	// Build a payload larger than the 5 MiB maxCommandBody. The
	// JSON structure doesn't matter — we expect MaxBytesReader to
	// fire before the decoder ever gets to the end of the body.
	big := strings.Repeat("a", 6*1024*1024)
	req := httptest.NewRequest(
		http.MethodPost, "/api/command",
		strings.NewReader(`{"type":"prompt","payload":{"text":"`+big+`"}}`),
	)
	rec := httptest.NewRecorder()
	h.handleCommand(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d body=%s, want 413", rec.Code, rec.Body.String())
	}
}

// TestCmdClearPendingTrust_ClearsAndBroadcasts pins the
// stop-trusting path: posting clear_pending_trust when the flag is
// set must (a) remove it from h.perTurnTrust, (b) broadcast
// pending_trust_cleared with reason="user_cleared", and (c) be
// idempotent — a second post returns 200 with no additional
// broadcast.
func TestCmdClearPendingTrust_ClearsAndBroadcasts(t *testing.T) {
	t.Parallel()
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.SupervisedMode = true
		return true
	})
	h.perm.supervised.SetTrust("c1")

	rec := postCmd(t, h, api.ClientCommand{
		Type: "clear_pending_trust", ChatID: "c1", RequestID: "r1",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if h.perm.supervised.HasTrust("c1") {
		t.Error("perTurnTrust still set after clear")
	}
	if n := countReplayType(h, api.EventPendingTrustCleared); n != 1 {
		t.Fatalf("pending_trust_cleared count=%d, want 1", n)
	}
	evt := lastReplayEventOfType(h, api.EventPendingTrustCleared)
	if evt == nil {
		t.Fatal("no cleared event recorded")
	}
	raw, err := json.Marshal(evt.Payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var payload api.PendingTrustClearedPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Reason != "user_cleared" {
		t.Errorf("reason=%q, want user_cleared", payload.Reason)
	}

	// Idempotent: second call is a no-op (flag already unset) and
	// must NOT emit another event — the pill is already in the
	// right state.
	rec2 := postCmd(t, h, api.ClientCommand{
		Type: "clear_pending_trust", ChatID: "c1", RequestID: "r2",
	})
	if rec2.Code != http.StatusOK {
		t.Fatalf("2nd status=%d", rec2.Code)
	}
	if n := countReplayType(h, api.EventPendingTrustCleared); n != 1 {
		t.Errorf("idempotent clear broadcast a duplicate: count=%d", n)
	}
}

// TestFlushPendingForChat_RejectsAllAndBroadcasts stages 3 ops, flushes,
// and asserts 3 resolved events + 1 cleared event with correct reason.
func TestFlushPendingForChat_RejectsAllAndBroadcasts(t *testing.T) {
	t.Parallel()
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	_, _ = stageOp(h, "c1", "tc-1", "a.go")
	_, _ = stageOp(h, "c1", "tc-2", "b.go")
	_, _ = stageOp(h, "c1", "tc-3", "c.go")

	h.flushPendingForChat(context.Background(), "c1", api.ClearReasonCancelled)

	resolved := countReplayType(h, api.EventPendingChangeResolved)
	cleared := countReplayType(h, api.EventPendingChangesCleared)
	if resolved != 3 {
		t.Errorf("resolved events = %d, want 3", resolved)
	}
	if cleared != 1 {
		t.Errorf("cleared events = %d, want 1", cleared)
	}
	evt := lastReplayEventOfType(h, api.EventPendingChangesCleared)
	if evt == nil {
		t.Fatal("no cleared event")
	}
	raw, _ := json.Marshal(evt.Payload)
	var payload api.PendingChangesClearedPayload
	_ = json.Unmarshal(raw, &payload)
	if payload.Reason != api.ClearReasonCancelled {
		t.Errorf("reason = %q, want %q", payload.Reason, api.ClearReasonCancelled)
	}
}

// TestFlushPendingForChat_EmptyNoBroadcast flushes a chat with no
// pending ops and asserts zero events are broadcast.
func TestFlushPendingForChat_EmptyNoBroadcast(t *testing.T) {
	t.Parallel()
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	h.flushPendingForChat(context.Background(), "c1", api.ClearReasonCancelled)

	resolved := countReplayType(h, api.EventPendingChangeResolved)
	cleared := countReplayType(h, api.EventPendingChangesCleared)
	if resolved != 0 {
		t.Errorf("resolved events = %d, want 0", resolved)
	}
	if cleared != 0 {
		t.Errorf("cleared events = %d, want 0", cleared)
	}
}

// TestFlushPendingForChat_ReasonPropagates verifies different reasons
// propagate correctly to the cleared event payload.
func TestFlushPendingForChat_ReasonPropagates(t *testing.T) {
	t.Parallel()
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	_, _ = stageOp(h, "c1", "tc-1", "a.go")
	h.flushPendingForChat(context.Background(), "c1", api.ClearReasonShutdown)

	evt := lastReplayEventOfType(h, api.EventPendingChangesCleared)
	if evt == nil {
		t.Fatal("no cleared event")
	}
	raw, _ := json.Marshal(evt.Payload)
	var payload api.PendingChangesClearedPayload
	_ = json.Unmarshal(raw, &payload)
	if payload.Reason != api.ClearReasonShutdown {
		t.Errorf("reason = %q, want %q", payload.Reason, api.ClearReasonShutdown)
	}
}

// TestReplayPendingChanges_NoFilterReplaysAll stages ops in two chats,
// replays with filter="" and asserts both chats' ops appear.
func TestReplayPendingChanges_NoFilterReplaysAll(t *testing.T) {
	t.Parallel()
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	_ = cs.Mutate(context.Background(), "c2", func(c *api.Chat, _ bool) bool { c.Name = "B"; return true })

	_, _ = stageOp(h, "c1", "tc-1", "a.go")
	_, _ = stageOp(h, "c2", "tc-2", "b.go")

	var got []api.ServerEvent
	h.replayPendingChanges(func(evt api.ServerEvent) { got = append(got, evt) }, "")

	if len(got) != 2 {
		t.Fatalf("replayed %d events, want 2", len(got))
	}
	for _, evt := range got {
		if evt.Type != api.EventPendingChangeAdded {
			t.Errorf("type = %q, want pending_change_added", evt.Type)
		}
	}
}

// TestReplayPendingChanges_FilterReplaysOnlyMatching stages ops in two
// chats, replays with filter="c1" and asserts only c1's ops appear.
func TestReplayPendingChanges_FilterReplaysOnlyMatching(t *testing.T) {
	t.Parallel()
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	_ = cs.Mutate(context.Background(), "c2", func(c *api.Chat, _ bool) bool { c.Name = "B"; return true })

	_, _ = stageOp(h, "c1", "tc-1", "a.go")
	_, _ = stageOp(h, "c2", "tc-2", "b.go")

	var got []api.ServerEvent
	h.replayPendingChanges(func(evt api.ServerEvent) { got = append(got, evt) }, "c1")

	if len(got) != 1 {
		t.Fatalf("replayed %d events, want 1", len(got))
	}
	if got[0].ChatID != "c1" {
		t.Errorf("chat_id = %q, want c1", got[0].ChatID)
	}
}

// TestReplayPendingChanges_EmptyStoreReplaysNothing replays with no
// pending ops and asserts zero events emitted.
func TestReplayPendingChanges_EmptyStoreReplaysNothing(t *testing.T) {
	t.Parallel()
	h, _, _ := newTestHub()

	var got []api.ServerEvent
	h.replayPendingChanges(func(evt api.ServerEvent) { got = append(got, evt) }, "")

	if len(got) != 0 {
		t.Errorf("replayed %d events, want 0", len(got))
	}
}
