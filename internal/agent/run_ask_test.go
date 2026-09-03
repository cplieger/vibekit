package agent

// Tests for the pending-run-ask registry and the two doors a workflow step's
// question arrives through.
//
// What is pinned here is vibekit's own bookkeeping, not KAS's behaviour: which
// frames become an answerable ask, that exactly one surface can answer one, that
// a failed send hands the ask back, and that nothing ends up holding a card for a
// run whose wait is over.

import (
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// notifyAsk builds a `_kiro/session/notify` frame KAS would send for a step's
// question.
func notifyAsk(workflowID, nodeID, message, notifyID string) *vibekit.RPCResponse {
	return runNotif(methodKiroSessionNotify, map[string]any{
		"sessionId":       "sess_parent",
		"callerSessionId": "sess_step",
		"message":         message,
		"severity":        "warning",
		"workflowId":      workflowID,
		"nodeId":          nodeID,
		"agentName":       "reviewer",
		"notifyId":        notifyID,
	})
}

// askOf builds a recorded ask directly, for the registry's own table tests.
//
// By POINTER, matching the registry: an entry is immutable after Add and every
// method that hands one back has already deleted it, so nothing shares one.
func askOf(chatID vibekit.ChatID, workflowID, askID, nodeID string) *runAsk {
	return &runAsk{
		chatID: chatID,
		payload: vibekit.RunInputNeededPayload{
			WorkflowID: workflowID,
			AskID:      askID,
			NodeID:     nodeID,
		},
	}
}

func TestPendingRunAsks_AddReportsNewOnly(t *testing.T) {
	t.Parallel()
	r := &pendingRunAsks{}
	if !r.Add(askOf("c1", "wf_1", "a1", "review")) {
		t.Error("Add(a fresh ask) = false, want true")
	}
	// A redelivered frame must not re-broadcast: the dock would de-duplicate it,
	// but the log line and the wire traffic are both avoidable.
	if r.Add(askOf("c1", "wf_1", "a1", "review")) {
		t.Error("Add(the same ask twice) = true, want false")
	}
	// Missing identity is not an ask: it could never be answered or retired.
	if r.Add(askOf("c1", "", "a1", "review")) {
		t.Error("Add(no workflow id) = true, want false")
	}
	if r.Add(askOf("c1", "wf_1", "", "review")) {
		t.Error("Add(no ask id) = true, want false")
	}
}

func TestPendingRunAsks_TakeIsOncePerAsk(t *testing.T) {
	t.Parallel()
	r := &pendingRunAsks{}
	r.Add(askOf("c1", "wf_1", "a1", "review"))

	got, ok := r.TakeIfPresent("wf_1", "a1")
	if !ok {
		t.Fatal("TakeIfPresent(the recorded ask) ok = false, want true")
	}
	if got.payload.NodeID != "review" {
		t.Errorf("the claimed ask's node = %q, want review", got.payload.NodeID)
	}
	// The take-once claim: KAS accepts exactly one answer, and the loser's
	// session/prompt would fall through to an ORDINARY prompt on the step's
	// session — a message injected into a step nobody asked to steer.
	if _, second := r.TakeIfPresent("wf_1", "a1"); second {
		t.Error("TakeIfPresent twice ok = true, want false on the second claim")
	}
}

func TestPendingRunAsks_TakeIsKeyedOnThePair(t *testing.T) {
	t.Parallel()
	r := &pendingRunAsks{}
	// A synthesised ask id is derived from a node path, which two concurrent runs
	// of one recipe SHARE. Keyed on the ask id alone, one run's reconcile would
	// overwrite the other's ask and an answer to either would retire whichever
	// survived.
	r.Add(askOf("c1", "wf_1", "reconciled:root/review", "review"))
	r.Add(askOf("c2", "wf_2", "reconciled:root/review", "review"))

	a, ok := r.TakeIfPresent("wf_1", "reconciled:root/review")
	if !ok {
		t.Fatal("TakeIfPresent(wf_1) ok = false, want true")
	}
	if a.chatID != "c1" {
		t.Errorf("claimed the ask of chat %q, want c1", a.chatID)
	}
	if _, still := r.TakeIfPresent("wf_2", "reconciled:root/review"); !still {
		t.Error("the OTHER run's ask was taken too, want it left in place")
	}
}

func TestPendingRunAsks_RestorePutsAClaimBack(t *testing.T) {
	t.Parallel()
	r := &pendingRunAsks{}
	a := askOf("c1", "wf_1", "a1", "review")
	r.Add(a)
	claimed, _ := r.TakeIfPresent("wf_1", "a1")
	r.Restore(claimed)
	// Without the restore, a transport failure on the answer leaves the run
	// parked with its card gone from every surface and nothing to bring it back.
	if _, ok := r.TakeIfPresent("wf_1", "a1"); !ok {
		t.Error("Restore then TakeIfPresent ok = false, want the ask answerable again")
	}
}

func TestPendingRunAsks_TakeNodeIsNodeScoped(t *testing.T) {
	t.Parallel()
	r := &pendingRunAsks{}
	r.Add(askOf("c1", "wf_1", "a1", "review"))
	r.Add(askOf("c1", "wf_1", "a2", "build"))
	// An ask carrying no node cannot be matched, and the terminal clear collects it.
	r.Add(askOf("c1", "wf_1", "a3", ""))

	got := r.TakeNode("wf_1", "review")
	if len(got) != 1 || got[0].payload.AskID != "a1" {
		t.Fatalf("TakeNode(review) = %+v, want just a1", got)
	}
	// A PARALLEL branch's node can complete while a sibling branch's step is
	// still parked, so a run-scoped clear here would take a live ask with it.
	if _, ok := r.TakeIfPresent("wf_1", "a2"); !ok {
		t.Error("the sibling node's ask was dropped, want it left in place")
	}
	if _, ok := r.TakeIfPresent("wf_1", "a3"); !ok {
		t.Error("the node-less ask was dropped, want it left for the terminal clear")
	}
}

func TestPendingRunAsks_TakeRunAndClearChat(t *testing.T) {
	t.Parallel()
	r := &pendingRunAsks{}
	r.Add(askOf("c1", "wf_1", "a1", "review"))
	r.Add(askOf("c1", "wf_1", "a2", "build"))
	r.Add(askOf("c2", "wf_2", "a3", "review"))

	// It RETURNS the claims rather than dropping them, because the caller has to
	// announce each one: dropping an entry takes no card off any screen.
	got := r.TakeRun("wf_1")
	if len(got) != 2 {
		t.Fatalf("TakeRun(wf_1) returned %d asks, want 2", len(got))
	}
	if r.HasRun("wf_1") {
		t.Error("HasRun(wf_1) after TakeRun = true, want false")
	}
	if !r.HasRun("wf_2") {
		t.Error("TakeRun(wf_1) also dropped wf_2's ask")
	}
	// Idempotent, which is load-bearing: the answer path claims its own entry and
	// the lifecycle path clears the rest, so both run for one ask.
	if again := r.TakeRun("wf_1"); len(again) != 0 {
		t.Errorf("TakeRun(wf_1) a second time returned %d asks, want 0", len(again))
	}

	r.ClearChat("c2")
	if r.HasRun("wf_2") {
		t.Error("HasRun(wf_2) after ClearChat(c2) = true, want false")
	}
}

func TestPendingRunAsks_ListFiltersByChatButKeepsRunKeyedAsks(t *testing.T) {
	t.Parallel()
	r := &pendingRunAsks{}
	r.Add(askOf("c1", "wf_1", "a1", "review"))
	r.Add(askOf("run:wf_2", "wf_2", "a2", "review"))
	r.Add(askOf("", "wf_3", "a3", "review"))

	// A chat-filtered SSE stream sees only its own chat's ask. `run:<id>` is not a
	// chat, so a parentless run's ask does not leak onto a chat's stream.
	got := r.List("c1")
	if len(got) != 2 {
		t.Fatalf("List(c1) returned %d events, want 2 (c1's ask and the topicless one)", len(got))
	}
	// An unfiltered connection gets everything: that is the run tab's stream.
	if all := r.List(""); len(all) != 3 {
		t.Errorf("List(\"\") returned %d events, want 3", len(all))
	}
	for _, evt := range got {
		if evt.Type != vibekit.EventRunInputNeeded {
			t.Errorf("List emitted %q, want %q", evt.Type, vibekit.EventRunInputNeeded)
		}
	}
}

// TestRunDispatch_SessionNotifyBecomesAnAsk pins the RUN bridge's door.
//
// It sits BEFORE the `_kiro/workflow/` prefix test, because this method is not
// under that prefix and would otherwise reach the Debug tail — which is exactly
// where it used to go. It keeps the run bridge's OWN chat id rather than being
// flattened to workspace-global like the lifecycle frames: an ask is answerable,
// so it has to land on a surface.
func TestRunDispatch_SessionNotifyBecomesAnAsk(t *testing.T) {
	h, _, _ := newTestHub()

	h.dispatch(t.Context(), "run:wf_1", notifyAsk("wf_1", "review", "which branch?", "n1"))

	events := bufferedEvents(h)
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1: %+v", len(events), events)
	}
	if events[0].Type != string(vibekit.EventRunInputNeeded) {
		t.Fatalf("type = %q, want run_input_needed", events[0].Type)
	}
	if events[0].ChatID != "run:wf_1" {
		t.Errorf("chat_id = %q, want run:wf_1 (the run tab's dock key)", events[0].ChatID)
	}
	p := marshalPayload(t, events[0].Payload)
	if p["workflow_id"] != "wf_1" || p["node_id"] != "review" {
		t.Errorf("payload = %+v, want wf_1/review", p)
	}
	if p["question"] != "which branch?" {
		t.Errorf("question = %q, want the message verbatim", p["question"])
	}
	// The registry holds it too, or a client connecting a moment later gets
	// nothing: the event does not re-fire.
	if !h.runs.asks.HasRun("wf_1") {
		t.Error("the ask was broadcast but not recorded, so a reconnect would lose it")
	}
}

// TestTranslateACPEvent_SessionNotifyBecomesAnAsk pins the CHAT bridge's door,
// which is where an AGENT-launched run's step asks: KAS parents such a run on
// the calling chat's session, so the frame arrives on that chat's connection and
// the ask belongs in that chat's own dock.
func TestTranslateACPEvent_SessionNotifyBecomesAnAsk(t *testing.T) {
	h, cs, _ := newTestHub()
	cs.Chats["c1"] = &vibekit.Chat{ID: "c1"}

	h.translateACPEvent("c1", notifyAsk("wf_1", "review", "which branch?", "n1"))

	events := bufferedEvents(h)
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1: %+v", len(events), events)
	}
	if events[0].ChatID != "c1" {
		t.Errorf("chat_id = %q, want c1 (the launching chat's dock key)", events[0].ChatID)
	}
}

// TestRunDispatch_SessionNotifyDropsNonWarnings pins the gate, and it is the
// half worth pinning: `info`, `success` and `error` change the step's lifecycle
// without leaving anybody waiting, so a card for one asks the reader to answer a
// step that has already moved on.
func TestRunDispatch_SessionNotifyDropsNonWarnings(t *testing.T) {
	for _, severity := range []string{"info", "success", "error"} {
		t.Run(severity, func(t *testing.T) {
			h, _, _ := newTestHub()
			h.dispatch(t.Context(), "run:wf_1", runNotif(methodKiroSessionNotify, map[string]any{
				"callerSessionId": "sess_step",
				"message":         "something happened",
				"severity":        severity,
				"workflowId":      "wf_1",
			}))
			if events := bufferedEvents(h); len(events) != 0 {
				t.Errorf("severity %q produced %+v, want no event", severity, events)
			}
			if h.runs.asks.HasRun("wf_1") {
				t.Errorf("severity %q recorded an ask, want none", severity)
			}
		})
	}
}

// TestRunAskCleared pins that no ask outlives the wait it describes.
func TestRunAskCleared(t *testing.T) {
	t.Run("a terminal run_complete clears the run", func(t *testing.T) {
		h, _, _ := newTestHub()
		h.dispatch(t.Context(), "run:wf_1", notifyAsk("wf_1", "review", "which branch?", "n1"))
		if !h.runs.asks.HasRun("wf_1") {
			t.Fatal("Setup: the ask was not recorded")
		}
		h.dispatch(t.Context(), "run:wf_1", runNotif(methodWFRunComplete,
			map[string]any{"workflowId": "wf_1", "status": "completed"}))
		if h.runs.asks.HasRun("wf_1") {
			t.Error("a completed run still holds an ask, want it cleared")
		}
	})

	t.Run("a terminal run_complete announces what it retired", func(t *testing.T) {
		h, _, _ := newTestHub()
		h.dispatch(t.Context(), "run:wf_1", notifyAsk("wf_1", "review", "which branch?", "n1"))
		h.dispatch(t.Context(), "run:wf_1", runNotif(methodWFRunComplete,
			map[string]any{"workflowId": "wf_1", "status": "failed"}))

		// Dropping the entry takes no card off any screen: every surface the ask was
		// offered to still holds it, and while a stale run card sits at the head of a
		// per-chat dock queue it also hides every later ask for that chat.
		settled := settledPayloads(t, h)
		if len(settled) != 1 {
			t.Fatalf("run_input_settled events = %d, want 1", len(settled))
		}
		// NOBODY answered, so the reason must not claim anybody did — SettledByUser
		// makes every other window read "answered in another window" for a question
		// that was discarded.
		if got := settled[0]["settled_by"]; got != string(vibekit.SettledByMoot) {
			t.Errorf("settled_by = %q, want %q", got, vibekit.SettledByMoot)
		}
	})

	t.Run("a non-terminal run_complete keeps it", func(t *testing.T) {
		h, _, _ := newTestHub()
		h.dispatch(t.Context(), "run:wf_1", notifyAsk("wf_1", "review", "which branch?", "n1"))
		// KAS reports an onMaxIterations policy stop through this same frame, and
		// that run is still this process's to resume — so its ask is still live.
		h.dispatch(t.Context(), "run:wf_1", runNotif(methodWFRunComplete,
			map[string]any{"workflowId": "wf_1", "status": "paused"}))
		if !h.runs.asks.HasRun("wf_1") {
			t.Error("a paused run lost its ask, want it kept")
		}
	})

	t.Run("the asking node completing retires it and says so", func(t *testing.T) {
		h, cs, _ := newTestHub()
		cs.Chats["c1"] = &vibekit.Chat{ID: "c1"}
		h.translateACPEvent("c1", notifyAsk("wf_1", "review", "which branch?", "n1"))

		h.translateACPEvent("c1", runNotif(methodWFNodeComplete, map[string]any{
			"workflowId": "wf_1", "nodeId": "review", "status": "completed",
		}))
		if h.runs.asks.HasRun("wf_1") {
			t.Error("the asking node completed and its ask survived")
		}
		// The announcement is not optional: the card is on every surface the ask
		// was offered to, and a registry deletion changes nothing anybody sees.
		settled := settledPayloads(t, h)
		if len(settled) != 1 {
			t.Fatalf("run_input_settled events = %d, want 1", len(settled))
		}
		// MOOT, not user: vibekit's own answer path settles the entry it claimed
		// before it sends, so nothing reaching this door was answered here — and this
		// frame fires for a failed and an aborted node just as readily.
		if got := settled[0]["settled_by"]; got != string(vibekit.SettledByMoot) {
			t.Errorf("settled_by = %q, want %q", got, vibekit.SettledByMoot)
		}
	})

	t.Run("a node that FAILED still does not claim an answer", func(t *testing.T) {
		h, cs, _ := newTestHub()
		cs.Chats["c1"] = &vibekit.Chat{ID: "c1"}
		h.translateACPEvent("c1", notifyAsk("wf_1", "review", "which branch?", "n1"))

		h.translateACPEvent("c1", runNotif(methodWFNodeComplete, map[string]any{
			"workflowId": "wf_1", "nodeId": "review", "status": "failed",
		}))
		settled := settledPayloads(t, h)
		if len(settled) != 1 {
			t.Fatalf("run_input_settled events = %d, want 1", len(settled))
		}
		if got := settled[0]["settled_by"]; got != string(vibekit.SettledByMoot) {
			t.Errorf("settled_by = %q, want %q — nobody answered a step that failed",
				got, vibekit.SettledByMoot)
		}
	})

	t.Run("a sibling node completing leaves it alone", func(t *testing.T) {
		h, cs, _ := newTestHub()
		cs.Chats["c1"] = &vibekit.Chat{ID: "c1"}
		h.translateACPEvent("c1", notifyAsk("wf_1", "review", "which branch?", "n1"))

		h.translateACPEvent("c1", runNotif(methodWFNodeComplete, map[string]any{
			"workflowId": "wf_1", "nodeId": "build", "status": "completed",
		}))
		if !h.runs.asks.HasRun("wf_1") {
			t.Error("a sibling node's completion dropped a live ask")
		}
	})
}

func hasEventType(events []bufferedEvent, want string) bool {
	for _, e := range events {
		if e.Type == want {
			return true
		}
	}
	return false
}

// settledPayloads decodes every `run_input_settled` payload a dispatch produced.
//
// The ATTRIBUTION is what these cases assert, not merely that a frame fired: the
// three settle reasons read differently to the person whose card vanished, and one
// of them asserts an answer nobody gave.
func settledPayloads(t *testing.T, h *Runtime) []map[string]string {
	t.Helper()
	var out []map[string]string
	for _, e := range bufferedEvents(h) {
		if e.Type == string(vibekit.EventRunInputSettled) {
			out = append(out, marshalPayload(t, e.Payload))
		}
	}
	return out
}

// pauseFixture mirrors testdata/need_input_pauses.json. See that file's _comment
// for why the table is SHARED with the TypeScript side rather than written twice.
type pauseFixture struct {
	Cases []struct {
		Name   string `json:"name"`
		Reason string `json:"reason"`
		Want   bool   `json:"want"`
	} `json:"cases"`
}

// TestNeedInputPauseContract is one half of a cross-language pin:
// run-store-pause.node.test.ts runs the same table against the TypeScript
// implementation. A KAS wording change applied in only one language fails in the
// other, which is the only thing keeping the two predicates in agreement.
//
// The table lives in the fixture rather than here so there is ONE statement of the
// cases. An inline copy beside it would be the same duplication one level down.
func TestNeedInputPauseContract(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("testdata/need_input_pauses.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fx pauseFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if len(fx.Cases) == 0 {
		t.Fatal("fixture carries no cases; a silently-empty table would pass forever")
	}
	// Both verdicts have to be represented, or half the rule is unpinned: a
	// predicate returning a constant satisfies a single-verdict table.
	var trues, falses int
	for _, tc := range fx.Cases {
		if tc.Want {
			trues++
		} else {
			falses++
		}
	}
	if trues == 0 || falses == 0 {
		t.Fatalf("fixture has %d true and %d false cases; both are needed", trues, falses)
	}
	for _, tc := range fx.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			if got := needInputPause(tc.Reason); got != tc.Want {
				t.Errorf("needInputPause(%q) = %v, want %v", tc.Reason, got, tc.Want)
			}
		})
	}
}

func TestPausedLeaf(t *testing.T) {
	t.Parallel()
	// A run reports `paused` at the ROOT while the step actually holding the
	// question is somewhere below it, and that step's session id is the answer
	// address — so the leaf, not the tree's own status, is what a reconciled ask
	// has to be built from.
	root := &askNode{
		NodeID: "root",
		Status: "paused",
		Children: []askNode{
			{NodeID: "build", Status: "completed"},
			{
				NodeID: "loop",
				Status: "paused",
				Children: []askNode{
					{NodeID: "iter", Status: "completed"},
					{NodeID: "iter", Status: "paused", SessionID: "sess_step", AgentName: "reviewer"},
				},
			},
		},
	}
	leaf, path := pausedLeaf(root, nil)
	if leaf == nil {
		t.Fatal("pausedLeaf found nothing, want the parked step")
	}
	if leaf.SessionID != "sess_step" {
		t.Errorf("leaf session = %q, want sess_step", leaf.SessionID)
	}
	// The PATH rather than the node id, because a repeat's iterations share an id
	// and the synthesised ask id has to distinguish two passes of one loop body.
	want := []string{"root", "loop", "iter"}
	if len(path) != len(want) {
		t.Fatalf("path = %v, want %v", path, want)
	}
	for i := range want {
		if path[i] != want[i] {
			t.Fatalf("path = %v, want %v", path, want)
		}
	}
	// A tree with nothing parked yields nothing, so a run paused for another
	// reason cannot produce an ask.
	if l, _ := pausedLeaf(&askNode{NodeID: "root", Status: "running"}, nil); l != nil {
		t.Errorf("pausedLeaf(a running run) = %+v, want nil", l)
	}
}

// parkedInspect builds a workflow/inspect reply for a run parked at one leaf:
// paused at the root, paused at the `review` leaf, and that leaf carrying the
// session id which IS the answer address.
//
// One builder for both readers of this shape — the reconcile, which varies the
// status and the pause reason, and the answer path's resolve-from-inspect
// fallback, which varies the session id — so the two cannot disagree about what a
// parked run's state looks like.
func parkedInspect(t *testing.T, status, pauseReason, stepSession string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"state": map[string]any{
			"status":      status,
			"pauseReason": pauseReason,
			"pauseDetail": map[string]any{"occurredAt": "2026-09-03T10:00:00Z"},
			"root": map[string]any{
				"nodeId": "root", "status": "paused",
				"children": []any{map[string]any{
					"nodeId": "review", "status": "paused",
					"sessionId": stepSession, "agentName": "reviewer",
				}},
			},
		},
	})
	if err != nil {
		t.Fatalf("Setup: marshalling the inspect reply: %s", err)
	}
	return raw
}

// eventOfType returns the first buffered event of a type, so a case that opens a
// bridge asserts on the frame it means rather than on an index into whatever else
// the setup broadcast.
func eventOfType(t *testing.T, h *Runtime, want string) bufferedEvent {
	t.Helper()
	for _, e := range bufferedEvents(h) {
		if e.Type == want {
			return e
		}
	}
	t.Fatalf("no %s event; got %+v", want, bufferedEvents(h))
	return bufferedEvent{}
}

// TestReconcileNeedInput pins the container-restart path: the registry is in
// memory while the run is not, so a restart leaves the run parked with the
// question gone. Without a reconstructed ask the only recourse would be
// cancelling work one sentence from finishing.
func TestReconcileNeedInput(t *testing.T) {
	inspect := func(status, pauseReason string) json.RawMessage {
		return parkedInspect(t, status, pauseReason, "sess_step")
	}

	t.Run("a need_input pause with no ask gets one", func(t *testing.T) {
		h, _, _ := newTestHub()
		h.runs.reconcileNeedInput(t.Context(), "wf_1", inspect("paused", needInputPauseReason))

		events := bufferedEvents(h)
		if len(events) != 1 || events[0].Type != string(vibekit.EventRunInputNeeded) {
			t.Fatalf("got %+v, want one run_input_needed", events)
		}
		p := marshalPayload(t, events[0].Payload)
		if p["step_session_id"] != "sess_step" {
			t.Errorf("step_session_id = %q, want sess_step (the answer address)", p["step_session_id"])
		}
		// Deliberately empty: the text was in the registry this process lost, and
		// inventing a question would put words in the step's mouth.
		if p["question"] != "" {
			t.Errorf("question = %q, want empty on a reconstructed ask", p["question"])
		}
	})

	t.Run("it is idempotent across reads", func(t *testing.T) {
		h, _, _ := newTestHub()
		raw := inspect("paused", needInputPauseReason)
		// The read path runs this on EVERY refetch, so a fresh id per read would
		// stack duplicate cards on the dock.
		h.runs.reconcileNeedInput(t.Context(), "wf_1", raw)
		h.runs.reconcileNeedInput(t.Context(), "wf_1", raw)
		if n := len(bufferedEvents(h)); n != 1 {
			t.Errorf("two reads produced %d events, want 1", n)
		}
	})

	// The reader's requirement is the prompt in the PARENT tab, and the composer
	// dock matches on chat id alone — a `run:` key can never match it, so keying
	// every reconstructed ask there put the card on the one surface that cannot
	// answer it (answering needs the launching chat's bridge). Keyed to the chat, it
	// renders in BOTH: the run tab's dock matches the payload's run id as well.
	t.Run("it keys the ask to the launching chat when one still hosts the run", func(t *testing.T) {
		h, cs, br := newTestHub()
		br.callResults = map[string]json.RawMessage{
			methodKiroWorkflowList: kasRuns(t, map[string]any{
				"workflowId": "wf_1", "status": "paused", "parentSessionId": "sess_owned",
			}),
		}
		if err := cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
			c.Name = "A"
			c.RecordSession("sess_owned")
			return true
		}); err != nil {
			t.Fatalf("Setup: seeding the launching chat: %s", err)
		}
		if _, err := h.coord.OpenBridge(t.Context(), "c1", ""); err != nil {
			t.Fatalf("Setup: opening the launching chat's bridge: %s", err)
		}
		h.runs.reconcileNeedInput(t.Context(), "wf_1", inspect("paused", needInputPauseReason))

		evt := eventOfType(t, h, string(vibekit.EventRunInputNeeded))
		if evt.ChatID != "c1" {
			t.Errorf("chat_id = %q, want c1: keyed to run:wf_1 the card renders only in "+
				"the run tab and never in the launching chat's composer dock", evt.ChatID)
		}
	})

	// The fallback, and it is the honest answer rather than a lesser one: after a
	// restart every bridge died with the process, so no launching chat's dock exists
	// to key to, and the run tab is how a person reaches such a run at all.
	t.Run("it keys the ask to the run when nothing hosts it", func(t *testing.T) {
		h, _, _ := newTestHub()
		h.runs.reconcileNeedInput(t.Context(), "wf_1", inspect("paused", needInputPauseReason))

		evt := eventOfType(t, h, string(vibekit.EventRunInputNeeded))
		if evt.ChatID != string(runChatID("wf_1")) {
			t.Errorf("chat_id = %q, want run:wf_1", evt.ChatID)
		}
	})

	// The window AnswerInput opens before it claims. Without the answering arm of
	// the gate a refetch landing between the claim and the send passes it, mints a
	// text-less twin, and the settle that follows names the ORIGINAL ask id — so
	// nothing retires the twin and the reader is told the question was lost
	// immediately after answering it.
	t.Run("an answer in flight gets no twin", func(t *testing.T) {
		h, _, _ := newTestHub()
		h.runs.asks.beginAnswer("wf_1")
		t.Cleanup(func() { h.runs.asks.endAnswer("wf_1") })
		h.runs.reconcileNeedInput(t.Context(), "wf_1", inspect("paused", needInputPauseReason))
		if events := bufferedEvents(h); len(events) != 0 {
			t.Errorf("got %+v, want no event while an answer is in flight", events)
		}
	})

	t.Run("a run paused for a transient error gets none", func(t *testing.T) {
		h, _, _ := newTestHub()
		h.runs.reconcileNeedInput(t.Context(), "wf_1",
			inspect("paused", "Transient connection error (EAI_AGAIN); the run is paused and can be resumed."))
		if events := bufferedEvents(h); len(events) != 0 {
			t.Errorf("got %+v, want no event for an involuntary pause", events)
		}
	})

	t.Run("a running run gets none", func(t *testing.T) {
		h, _, _ := newTestHub()
		h.runs.reconcileNeedInput(t.Context(), "wf_1", inspect("running", ""))
		if events := bufferedEvents(h); len(events) != 0 {
			t.Errorf("got %+v, want no event for a running run", events)
		}
	})
}

// TestAnswerInput pins the answer verb: the RPC it sends, the take-once refusal,
// and the restore on a failed send.
func TestAnswerInput(t *testing.T) {
	// setup wires a run bridge holding one recorded ask, which is the shape a
	// parentless run has.
	setup := func(t *testing.T) (*Runtime, *fakeBridge) {
		t.Helper()
		h, _, br := newTestHub()
		h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})
		h.runs.asks.Add(&runAsk{
			chatID: "run:wf_1",
			payload: vibekit.RunInputNeededPayload{
				WorkflowID: "wf_1", AskID: "a1", NodeID: "review", StepSessionID: "sess_step",
			},
		})
		return h, br
	}

	t.Run("it prompts the paused step's own session", func(t *testing.T) {
		h, br := setup(t)
		if err := h.runs.AnswerInput(t.Context(), "wf_1", "a1", "the main branch"); err != nil {
			t.Fatalf("AnswerInput = %v, want nil", err)
		}
		// The answer verb is a plain session/prompt addressed to the STEP's own
		// session, which KAS reroutes into the run (tryResumeStepWithMessage).
		// Addressed anywhere else it would be an ordinary prompt on a chat.
		params := br.paramsFor(vibekit.MethodPrompt)
		if params == nil {
			t.Fatalf("no %s call, calls were %v", vibekit.MethodPrompt, br.callLog())
		}
		if params["sessionId"] != "sess_step" {
			t.Errorf("sessionId = %v, want sess_step", params["sessionId"])
		}
		blocks, ok := params["prompt"].([]any)
		if !ok || len(blocks) != 1 {
			t.Fatalf("prompt = %#v, want one content block", params["prompt"])
		}
		block, ok := blocks[0].(map[string]any)
		if !ok || block["type"] != "text" || block["text"] != "the main branch" {
			t.Errorf("prompt block = %#v, want a text block carrying the answer", blocks[0])
		}
		if !hasEventType(bufferedEvents(h), string(vibekit.EventRunInputSettled)) {
			t.Error("no run_input_settled event, so the card on every other surface stays live")
		}
	})

	t.Run("only one surface may answer", func(t *testing.T) {
		h, _ := setup(t)
		if err := h.runs.AnswerInput(t.Context(), "wf_1", "a1", "the main branch"); err != nil {
			t.Fatalf("Setup: the first answer failed: %v", err)
		}
		// KAS accepts one answer, and the loser's prompt would fall through to an
		// ORDINARY prompt on the step's session — a message injected into a step
		// nobody asked to steer. So the claim has to be decided here.
		err := h.runs.AnswerInput(t.Context(), "wf_1", "a1", "no, the release branch")
		if !errors.Is(err, errAskAlreadySettled) {
			t.Errorf("the second answer = %v, want errAskAlreadySettled", err)
		}
	})

	t.Run("a failed send hands the ask back AND re-offers it", func(t *testing.T) {
		h, br := setup(t)
		br.callErrs = map[string]error{vibekit.MethodPrompt: errors.New("bridge died")}
		if err := h.runs.AnswerInput(t.Context(), "wf_1", "a1", "the main branch"); err == nil {
			t.Fatal("AnswerInput = nil, want the transport error")
		}
		// Without the restore a blip leaves the run parked with its card gone from
		// every surface and no way to bring it back short of another restart.
		if !h.runs.asks.HasRun("wf_1") {
			t.Error("the ask was lost on a failed send, want it restored")
		}
		// Restoring the ENTRY is only half of it: the click already spliced the card
		// from every dock that held it, so an entry with no frame behind it is
		// visible to nobody until the next SSE connect refills it from the replay —
		// which is the outcome the restore exists to prevent, one layer up. And it
		// must NOT be a settle: the question is still open.
		if !hasEventType(bufferedEvents(h), string(vibekit.EventRunInputNeeded)) {
			t.Error("no run_input_needed event, so the restored ask reaches no surface")
		}
		if hasEventType(bufferedEvents(h), string(vibekit.EventRunInputSettled)) {
			t.Error("a failed send announced a settle, want the ask re-offered instead")
		}
	})

	// The SUCCESS arm of the resolve-from-inspect fallback, which is what makes an
	// address-less ask answerable at all. Only a RECONCILED ask reaches it (a live
	// notify frame carries its own callerSessionId), and until this case existed the
	// arm had no test: a regression there would refuse every reconstructed ask
	// forever, with a sentence about the step not being addressable, and nothing
	// would have gone red.
	t.Run("an address-less ask resolves the step from a fresh inspect", func(t *testing.T) {
		h, _, br := newTestHub()
		h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})
		br.callResults = map[string]json.RawMessage{
			methodKiroWorkflowInspect: parkedInspect(
				t, runStatusPaused, needInputPauseReason, "sess_from_inspect",
			),
		}
		h.runs.asks.Add(&runAsk{
			chatID: runChatID("wf_1"),
			payload: vibekit.RunInputNeededPayload{
				WorkflowID: "wf_1", AskID: "reconciled:root/review", NodeID: "review",
			},
		})
		if err := h.runs.AnswerInput(
			t.Context(), "wf_1", "reconciled:root/review", "the main branch",
		); err != nil {
			t.Fatalf("AnswerInput = %v, want nil", err)
		}
		params := br.paramsFor(vibekit.MethodPrompt)
		if params == nil {
			t.Fatalf("no %s call, calls were %v", vibekit.MethodPrompt, br.callLog())
		}
		// The paused LEAF's session, not the root's and not the chat's: KAS reroutes a
		// prompt into the run only when it is addressed to the parked step itself.
		if params["sessionId"] != "sess_from_inspect" {
			t.Errorf("sessionId = %v, want sess_from_inspect (resolved from inspect)",
				params["sessionId"])
		}
		if h.runs.asks.HasRun("wf_1") {
			t.Error("the ask survived a successful answer, so its card is offered again")
		}
	})

	t.Run("an unaddressable step re-offers the ask too", func(t *testing.T) {
		// Same restore, other refusal arm: the ask carries no step session and no
		// fresh inspect can supply one, so the claim has to go back.
		h, _, br := newTestHub()
		h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})
		h.runs.asks.Add(&runAsk{
			chatID:  "run:wf_1",
			payload: vibekit.RunInputNeededPayload{WorkflowID: "wf_1", AskID: "a1"},
		})
		if err := h.runs.AnswerInput(t.Context(), "wf_1", "a1", "the main branch"); err == nil {
			t.Fatal("AnswerInput with no answer address = nil, want a refusal")
		}
		if !h.runs.asks.HasRun("wf_1") {
			t.Fatal("the ask was consumed by a refusal, want it left answerable")
		}
		if !hasEventType(bufferedEvents(h), string(vibekit.EventRunInputNeeded)) {
			t.Error("no run_input_needed event, so the restored ask reaches no surface")
		}
	})

	t.Run("an empty answer is refused", func(t *testing.T) {
		h, _ := setup(t)
		// Continuing without an answer is a DIFFERENT verb, because it drives the
		// step with KAS's default continuation rather than the user's words. A
		// reader must not reach it by submitting an empty box.
		if err := h.runs.AnswerInput(t.Context(), "wf_1", "a1", "   "); err == nil {
			t.Error("AnswerInput(whitespace) = nil, want a refusal")
		}
		if !h.runs.asks.HasRun("wf_1") {
			t.Error("a refused empty answer consumed the ask")
		}
	})

	t.Run("an unhosted run is refused without losing the ask", func(t *testing.T) {
		h, _, _ := newTestHub()
		h.runs.asks.Add(&runAsk{
			chatID: "run:wf_1",
			payload: vibekit.RunInputNeededPayload{
				WorkflowID: "wf_1", AskID: "a1", StepSessionID: "sess_step",
			},
		})
		err := h.runs.AnswerInput(t.Context(), "wf_1", "a1", "the main branch")
		if !errors.Is(err, errRunNotHosted) {
			t.Errorf("AnswerInput with no bridge = %v, want errRunNotHosted", err)
		}
		if !h.runs.asks.HasRun("wf_1") {
			t.Error("the ask was consumed by a refusal, want it left answerable")
		}
	})
}
