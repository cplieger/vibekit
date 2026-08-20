package agent

// Tests for the run host: the synthetic-id plumbing, the dispatch split, and
// the teardown rules. The launch flow's KAS half (new/invoke) is exercised
// against the fake bridge; what is pinned is vibekit's sequencing and
// bookkeeping, not KAS's behaviour.

import (
	"encoding/json"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/runlease"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// bufferedEvents decodes the SSE replay buffer back into typed envelopes, so a
// dispatch test asserts on what a client would actually receive.
func bufferedEvents(h *Runtime) []struct {
	Type   string `json:"type"`
	ChatID string `json:"chat_id"`
} {
	var out []struct {
		Type   string `json:"type"`
		ChatID string `json:"chat_id"`
	}
	for _, e := range h.bus.hub.Buffered() {
		var evt struct {
			Type   string `json:"type"`
			ChatID string `json:"chat_id"`
		}
		if json.Unmarshal(e.Event.Data, &evt) == nil {
			out = append(out, evt)
		}
	}
	return out
}

func runNotif(method string, params map[string]any) *vibekit.RPCResponse {
	raw, err := json.Marshal(params)
	if err != nil {
		panic(err)
	}
	return &vibekit.RPCResponse{Method: method, Params: raw}
}

// TestRunChatID_Namespace pins the synthetic id shape and that a real chat id
// can never read as a run's.
func TestRunChatID_Namespace(t *testing.T) {
	if got := runChatID("wf_1"); got != "run:wf_1" {
		t.Errorf("runChatID = %q, want run:wf_1", got)
	}
	if !isRunChat("run:wf_1") {
		t.Error("run:wf_1 not recognised as a run chat")
	}
	for _, id := range []vibekit.ChatID{"c-abc123", "", "wf_1", "running-jokes"} {
		if isRunChat(id) {
			t.Errorf("%q misread as a run chat", id)
		}
	}
}

// TestRunDispatch_LifecycleGoesWorkspaceGlobal pins the topic rule: a
// parentless run's lifecycle events carry an EMPTY chat id (workspace-global),
// never the synthetic one — the synthetic id is bridge-map plumbing and must
// not leak onto the wire as a topic.
func TestRunDispatch_LifecycleGoesWorkspaceGlobal(t *testing.T) {
	h, _, _ := newTestHub()

	h.runDispatch(t.Context(), "run:wf_1",
		runNotif("_kiro/workflow/run_start", map[string]any{"workflowId": "wf_1", "workflowName": "publish"}))

	events := bufferedEvents(h)
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1: %+v", len(events), events)
	}
	if events[0].Type != string(vibekit.EventRunStarted) {
		t.Errorf("type = %q, want run_started", events[0].Type)
	}
	if events[0].ChatID != "" {
		t.Errorf("chat_id = %q, want empty (workspace-global)", events[0].ChatID)
	}
}

// TestRunDispatch_StepContentIsDropped pins that a step's session/update never
// reaches the chunk handlers from a run bridge: there is no transcript here,
// and buffering into the synthetic id would open a phantom assistant message on
// a chat that must never exist.
func TestRunDispatch_StepContentIsDropped(t *testing.T) {
	h, _, _ := newTestHub()

	h.runDispatch(t.Context(), "run:wf_1", runNotif(vibekit.MethodSessionUpdate, map[string]any{
		"sessionId": "sess_step",
		"update": map[string]any{
			"sessionUpdate": "agent_message_chunk",
			"content":       map[string]any{"type": "text", "text": "step says"},
		},
	}))

	if events := bufferedEvents(h); len(events) != 0 {
		t.Fatalf("a step chunk on a run bridge produced %d events, want 0: %+v", len(events), events)
	}
	// And nothing opened an assistant buffer for the synthetic chat.
	if h.bridge.assistantBufs.Get("run:wf_1") != nil {
		t.Error("a step chunk opened an assistant buffer for the synthetic chat id")
	}
}

// TestRunDispatch_PermissionKeyedToRunChat pins the ask path: a step's
// permission on a run bridge broadcasts keyed to the synthetic chat id — which
// is what the client dock renders in the run tab, and what the reply's
// chat_id routes back through.
func TestRunDispatch_PermissionKeyedToRunChat(t *testing.T) {
	h, _, _ := newTestHub()

	id := int64(7)
	msg := runNotif(vibekit.MethodRequestPermission, map[string]any{
		"sessionId": "sess_step",
		"toolCall":  map[string]any{"toolCallId": "tc1", "title": "write file", "kind": "edit"},
		"options":   []map[string]any{{"optionId": "allow", "name": "Allow", "kind": "allow_once"}},
	})
	msg.ID = &id
	h.runDispatch(t.Context(), "run:wf_1", msg)

	found := false
	for _, e := range bufferedEvents(h) {
		if e.Type == string(vibekit.EventPermissionNeeded) {
			found = true
			if e.ChatID != "run:wf_1" {
				t.Errorf("permission chat_id = %q, want run:wf_1", e.ChatID)
			}
		}
	}
	if !found {
		t.Fatal("no permission_needed broadcast")
	}
}

// TestRunDispatch_UnknownRequestIsRefused pins that an unmatched A→C request is
// ANSWERED with an error rather than dropped — an unanswered request wedges the
// step's turn until a timeout nobody set fires.
func TestRunDispatch_UnknownRequestIsRefused(t *testing.T) {
	h, _, br := newTestHub()
	h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})

	id := int64(3)
	msg := runNotif("_kiro/spec/getTaskStatuses", map[string]any{})
	msg.ID = &id
	h.runDispatch(t.Context(), "run:wf_1", msg)

	if got := br.respondCount(); got != 1 {
		t.Fatalf("unknown request got %d responses, want 1 refusal", got)
	}
}

// TestLaunchRun_SequencesNewRegisterInvoke pins the launch ordering against the
// fake bridge: the run is created, REGISTERED, and only then invoked — a frame
// following invoke immediately must find the bridge in the map.
func TestLaunchRun_SequencesNewRegisterInvoke(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowListRecipes: json.RawMessage(`{"recipes":[{"name":"publish","source":"bundled://publish","builtIn":true}]}`),
		methodKiroWorkflowList:        json.RawMessage(`{"runs":[]}`),
		methodKiroWorkflowNew:         json.RawMessage(`{"workflowId":"wf_9"}`),
		methodKiroWorkflowInvoke:      json.RawMessage(`{}`),
	}

	id, name, err := h.runs.LaunchRun(t.Context(), "bundled://publish", nil)
	if err != nil {
		t.Fatalf("LaunchRun: %v", err)
	}
	if id != "wf_9" || name != "publish" {
		t.Errorf("LaunchRun = (%q, %q), want (wf_9, publish)", id, name)
	}
	if h.bridge.mgr.get("run:wf_9") == nil {
		t.Error("the run bridge is not registered under its synthetic id")
	}
	// invoke came after new, on the same bridge.
	calls := br.callLog()
	newIdx, invokeIdx := -1, -1
	for i, m := range calls {
		switch m {
		case methodKiroWorkflowNew:
			newIdx = i
		case methodKiroWorkflowInvoke:
			invokeIdx = i
		}
	}
	if newIdx == -1 || invokeIdx == -1 || invokeIdx < newIdx {
		t.Errorf("call order wrong: %v", calls)
	}
}

// TestLaunchRun_RefusesAnUnknownSource pins the validation posture: the launch
// source is re-checked against a fresh listRecipes reply, so this endpoint
// cannot be pointed at an arbitrary file even though the value looks like one.
func TestLaunchRun_RefusesAnUnknownSource(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowListRecipes: json.RawMessage(`{"recipes":[{"name":"publish","source":"bundled://publish"}]}`),
	}
	if _, _, err := h.runs.LaunchRun(t.Context(), "/etc/passwd", nil); err == nil {
		t.Fatal("an unlisted source launched")
	}
	if !strings.Contains(br.lastCall(), "listRecipes") {
		t.Errorf("last call = %q; the refusal happened after something other than validation", br.lastCall())
	}
}

// TestLaunchRun_SingleRunRule pins the 409 shape: one live run per recipe,
// globally, whoever launched it.
func TestLaunchRun_SingleRunRule(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowListRecipes: json.RawMessage(`{"recipes":[{"name":"publish","source":"bundled://publish"}]}`),
		methodKiroWorkflowList:        json.RawMessage(`{"runs":[{"workflowId":"wf_1","name":"publish","status":"running"}]}`),
	}
	_, _, err := h.runs.LaunchRun(t.Context(), "bundled://publish", nil)
	if err == nil || !strings.Contains(err.Error(), "live run") {
		t.Fatalf("err = %v, want the single-run refusal", err)
	}
	// A TERMINAL run of the same recipe does not block a relaunch.
	br.callResults[methodKiroWorkflowList] = json.RawMessage(`{"runs":[{"workflowId":"wf_1","name":"publish","status":"completed"}]}`)
	br.callResults[methodKiroWorkflowNew] = json.RawMessage(`{"workflowId":"wf_2"}`)
	br.callResults[methodKiroWorkflowInvoke] = json.RawMessage(`{}`)
	if _, _, err := h.runs.LaunchRun(t.Context(), "bundled://publish", nil); err != nil {
		t.Fatalf("a terminal run blocked a relaunch: %v", err)
	}
}

// TestCloseFinishedRunBridge_TerminalOnly pins the teardown rule: run_complete
// closes the bridge only on a TERMINAL status. A policy pause reports through
// the same frame and the run is still this process's to resume.
func TestCloseFinishedRunBridge_TerminalOnly(t *testing.T) {
	cases := []struct {
		status string
		closed bool
	}{
		{"completed", true},
		{"failed", true},
		{"aborted", true},
		{"cancelled", true},
		{"paused", false},
		{"", false},
	}
	for _, c := range cases {
		t.Run("status="+c.status, func(t *testing.T) {
			if got := terminalRunStatus(c.status); got != c.closed {
				t.Errorf("terminalRunStatus(%q) = %v, want %v", c.status, got, c.closed)
			}
		})
	}
}

// TestBridgeManagerInsert_RefusesReplacement pins that inserting over a live
// entry fails rather than orphaning the process the entry holds.
func TestBridgeManagerInsert_RefusesReplacement(t *testing.T) {
	h, _, br := newTestHub()
	first := &sharedBridge{bridge: br, state: bridgeIdle}
	if !h.bridge.mgr.insert("run:wf_1", first) {
		t.Fatal("first insert refused")
	}
	if h.bridge.mgr.insert("run:wf_1", &sharedBridge{bridge: br, state: bridgeIdle}) {
		t.Error("second insert over a live entry succeeded")
	}
	if got := h.bridge.mgr.get("run:wf_1"); got != first {
		t.Error("the original entry did not survive the refused insert")
	}
}

// TestKillForTurn_ScopedToTheOpenTurn pins the interrupt gate's scope (§5.6
// R3): a cancel kills the CURRENT turn's terminals and leaves a background
// command an earlier turn started on purpose alone.
func TestKillForTurn_ScopedToTheOpenTurn(t *testing.T) {
	at := bareTerminals()
	add := func(id string, chat vibekit.ChatID) {
		at.mu.Lock()
		term := newAgentTerminal(&exec.Cmd{}, chat, 1024)
		term.turnSeq = at.currentTurn(chat)
		at.terms[id] = term
		at.byChatID[chat] = append(at.byChatID[chat], id)
		at.mu.Unlock()
	}

	add("t1-old", "c1") // turn 0
	at.AdvanceTurn("c1")
	add("t2-cur", "c1") // turn 1, the open turn
	add("t2-cur-b", "c1")
	add("other-chat", "c2")

	at.KillForTurn("c1")

	at.mu.Lock()
	defer at.mu.Unlock()
	if _, ok := at.terms["t1-old"]; !ok {
		t.Error("an earlier turn's terminal was killed — that background command was not the cancel's to take")
	}
	if _, ok := at.terms["t2-cur"]; ok {
		t.Error("the open turn's terminal survived the interrupt")
	}
	if _, ok := at.terms["t2-cur-b"]; ok {
		t.Error("the open turn's second terminal survived the interrupt")
	}
	if _, ok := at.terms["other-chat"]; !ok {
		t.Error("another chat's terminal was killed")
	}
	if got := len(at.byChatID["c1"]); got != 1 {
		t.Errorf("c1's index holds %d ids, want 1 (the survivor)", got)
	}
}

// TestKillForTurn_NothingOpenIsANoOp pins that a cancel with no terminals (the
// overwhelmingly common case) touches nothing.
func TestKillForTurn_NothingOpenIsANoOp(t *testing.T) {
	at := bareTerminals()
	at.AdvanceTurn("c1")
	at.KillForTurn("c1") // must not panic or create entries
	at.mu.Lock()
	defer at.mu.Unlock()
	if len(at.terms) != 0 || len(at.byChatID["c1"]) != 0 {
		t.Errorf("no-op kill mutated the registry: %d terms", len(at.terms))
	}
}

// TestRetryRun_SuccessClearsTheOldTerminalReason is finding 9 on the hosted
// branch, end to end through the verb.
//
// Retry reuses the workflow id, so a run stopped as `overran` carried that reason
// into its retry — and history.ts deliberately lets a recognised end_reason
// outrank live status, so the running retry rendered as aborted and stayed that way
// after it succeeded.
func TestRetryRun_SuccessClearsTheOldTerminalReason(t *testing.T) {
	h, _, br := newTestHub()
	const id = "wf_1"
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowRetry: json.RawMessage(`{}`)}
	h.bridge.mgr.insert(runChatID(id), &sharedBridge{bridge: br, state: bridgeIdle})

	// The run as the bounds left it: terminated, reason recorded, claim taken.
	h.runs.claimRunTermination(id)
	h.runs.recordRunEnd(id, runEndOverran)

	if err := h.runs.RetryRun(t.Context(), id); err != nil {
		t.Fatalf("RetryRun: %v", err)
	}
	if got := h.runs.runEndReason(id); got != "" {
		t.Errorf("the retried run still reads %q, so its row renders as aborted", got)
	}
	if !h.runs.runBounded(id) {
		t.Error("the retried run holds no deadline, so nothing bounds it")
	}
	// The claim went with the reason, or no bound could ever stop the retry.
	if !h.runs.claimRunTermination(id) {
		t.Error("the retried run kept its termination claim")
	}
}

// TestRetryRun_FailureKeepsTheOldTerminalReason is the other half, and the reason
// the clear happens AFTER the RPC rather than before it: a retry KAS refused
// re-drove nothing, so the previous terminal reason is still the truth about that
// run and its row must keep saying so.
func TestRetryRun_FailureKeepsTheOldTerminalReason(t *testing.T) {
	h, _, br := newTestHub()
	const id = "wf_1"
	br.callErrs = map[string]error{methodKiroWorkflowRetry: errors.New("kas refused")}
	h.bridge.mgr.insert(runChatID(id), &sharedBridge{bridge: br, state: bridgeIdle})

	h.runs.claimRunTermination(id)
	h.runs.recordRunEnd(id, runEndOverran)

	if err := h.runs.RetryRun(t.Context(), id); err == nil {
		t.Fatal("a refused retry reported success")
	}
	if got := h.runs.runEndReason(id); got != runEndOverran {
		t.Errorf("the refused retry cleared the reason to %q; the run is still aborted", got)
	}
	if h.runs.runBounded(id) {
		t.Error("a refused retry bounded a run that is not executing")
	}
}

// TestRetryRun_AFrameArrivingDuringTheRetryCannotMakeTheRunUnsweepable is the
// interleaving that shipped the defect, forced deterministically.
//
// A retry re-hosts a PARENTLESS run, and its first lifecycle frame can arrive before
// the retry call returns — the code says so itself. The lease was granted after that
// call, so `run_start` landing first found no lease and the observer, inferring
// origin from lease ABSENCE, stamped OriginAgent on a run no chat owns. The rearm
// then saw a lease and kept it, and the agent exclusion made that run permanently
// unsweepable: if its bridge died or vibekit restarted, its restart-paused row was
// never cleared and blocked every later launch of the recipe forever.
//
// The fake bridge's blockOn seam holds the retry call open, so the frame is delivered
// strictly INSIDE the window rather than near it.
func TestRetryRun_AFrameArrivingDuringTheRetryCannotMakeTheRunUnsweepable(t *testing.T) {
	h, _, br := newTestHub()
	const id = "wf_1"
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowRetry: json.RawMessage(`{}`),
		// The run list is where a re-hosted run's recipe comes from.
		methodKiroWorkflowList: json.RawMessage(
			`{"runs":[{"workflowId":"wf_1","name":"nightly","status":"aborted"}]}`),
	}
	held := make(chan struct{})
	br.blockOn = map[string]chan struct{}{methodKiroWorkflowRetry: held}
	h.bridge.mgr.insert(runChatID(id), &sharedBridge{bridge: br, state: bridgeIdle})

	done := make(chan error, 1)
	go func() { done <- h.runs.RetryRun(t.Context(), id) }()

	// Wait until the retry is genuinely in flight, then deliver the frame the way
	// runDispatch does: a run bridge's workflow frames carry an EMPTY chat id.
	stop := time.Now().Add(5 * time.Second)
	for !slices.Contains(br.callLog(), methodKiroWorkflowRetry) {
		if time.Now().After(stop) {
			t.Fatalf("the retry never reached the bridge: %v", br.callLog())
		}
		time.Sleep(time.Millisecond)
	}
	h.runs.observeRunStart(t.Context(), "", runNotif(methodWFRunStart, map[string]any{
		"workflowId": id, "workflowName": "nightly",
	}))
	close(held)
	if err := <-done; err != nil {
		t.Fatalf("RetryRun: %v", err)
	}

	l, ok := h.runs.lease(id)
	if !ok {
		t.Fatal("the retried run holds no lease")
	}
	if l.Origin == runlease.OriginAgent {
		t.Error("a frame arriving mid-retry leased a parentless run as agent-origin, which " +
			"excludes it from the orphan sweep for good")
	}
	if l.Origin != runlease.OriginManual {
		t.Errorf("origin = %q, want manual", l.Origin)
	}
	if l.Recipe != "nightly" {
		t.Errorf("recipe = %q, want nightly off the run list; a nameless lease is invisible to "+
			"the single-run rule's comparison", l.Recipe)
	}
	if !l.Bounded() {
		t.Error("the retried run took no deadline")
	}
}

// TestRetryRun_ReHostedRunTakesItsRecipeFromTheRunList is the re-hosting branch —
// the one retry's legality window actually implies, since `closeFinishedRunBridge`
// tears the bridge down on exactly the statuses retry is legal from.
//
// Its lease used to be minted with an EMPTY recipe, on the reasoning that a
// re-hosted run's recipe is unknowable here. It is knowable: KAS's own run list
// reports it, and that is the same string the single-run rule compares against. The
// guess cost something real — a nameless lease cannot be recognised as the run
// holding its own recipe, so the admission backstop could not explain it.
func TestRetryRun_ReHostedRunTakesItsRecipeFromTheRunList(t *testing.T) {
	h, _, br := newTestHub()
	const id = "wf_1"
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowRetry: json.RawMessage(`{}`),
		methodKiroWorkflowList: json.RawMessage(
			`{"runs":[{"workflowId":"wf_1","name":"nightly","status":"aborted"}]}`),
	}
	// Deliberately NO bridge in the manager: that is what makes this the re-hosting
	// path rather than the already-hosted one.
	if h.bridge.mgr.get(runChatID(id)) != nil {
		t.Fatal("the fixture registered a bridge, so this exercises the wrong branch")
	}

	if err := h.runs.RetryRun(t.Context(), id); err != nil {
		t.Fatalf("RetryRun: %v", err)
	}
	l, ok := h.runs.lease(id)
	if !ok {
		t.Fatal("the re-hosted run holds no lease, so nothing bounds it")
	}
	if l.Recipe != "nightly" {
		t.Errorf("recipe = %q, want nightly off KAS's run list", l.Recipe)
	}
	if l.Origin != runlease.OriginManual {
		t.Errorf("origin = %q, want manual: the user clicked Retry, so this run is the user's "+
			"own and must stay sweepable", l.Origin)
	}
	if !l.Bounded() {
		t.Error("the re-hosted run took no deadline")
	}
}

// TestRetryRun_CancelsNothingAndKeepsNoLeaseWhenTheRetryIsRefused: the lease is now
// granted BEFORE the verb, so a refusal has to put it back. A lease left behind for
// a run that never re-drove would make its recipe read as busy to the admission
// backstop and hand a wall clock to a run that is not executing.
func TestRetryRun_CancelsNothingAndKeepsNoLeaseWhenTheRetryIsRefused(t *testing.T) {
	h, _, br := newTestHub()
	const id = "wf_1"
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowList: json.RawMessage(
			`{"runs":[{"workflowId":"wf_1","name":"nightly","status":"aborted"}]}`),
	}
	br.callErrs = map[string]error{methodKiroWorkflowRetry: errors.New("kas refused")}

	if err := h.runs.RetryRun(t.Context(), id); err == nil {
		t.Fatal("a refused retry reported success")
	}
	if _, ok := h.runs.lease(id); ok {
		t.Error("the refused retry kept the lease it minted, so the recipe reads as busy and a " +
			"run that is not executing carries a deadline")
	}
}

// TestCancelRun_LostClaimIssuesNoSecondCancel pins the loser's half of the
// termination claim on the public verb: something is already ending the run, so the
// user's Cancel must not send a second cancel or overwrite the winner's reason.
// It reports success because the outcome the caller asked for is the one happening.
func TestCancelRun_LostClaimIssuesNoSecondCancel(t *testing.T) {
	h, _, br := newTestHub()
	const id = "wf_1"
	h.bridge.mgr.insert(runChatID(id), &sharedBridge{bridge: br, state: bridgeIdle})

	// A bound got there first.
	if !h.runs.claimRunTermination(id) {
		t.Fatal("the fixture could not take the claim it needs to hold")
	}
	h.runs.recordRunEnd(id, runEndStepCap)

	if err := h.runs.CancelRun(t.Context(), id); err != nil {
		t.Errorf("CancelRun on an already-terminating run = %v, want nil", err)
	}
	if slices.Contains(br.callLog(), methodKiroWorkflowCancel) {
		t.Error("a second cancel went out for a run already being cancelled")
	}
	if got := h.runs.runEndReason(id); got != runEndStepCap {
		t.Errorf("the row reads %q; the losing cancel overwrote the winner's reason", got)
	}
}

// TestCancelRun_WinsTheClaimAndRecordsNothing: the user's cancel is the one
// terminal path that records NO reason, because its absence is what makes the two
// bounds distinguishable from a person on the History row.
func TestCancelRun_WinsTheClaimAndRecordsNothing(t *testing.T) {
	h, _, br := newTestHub()
	const id = "wf_1"
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
	h.bridge.mgr.insert(runChatID(id), &sharedBridge{bridge: br, state: bridgeIdle})
	h.runs.grantLease(t.Context(), id, "publish", manualLaunch())
	h.runs.armRunDeadline(t.Context(), id)

	if err := h.runs.CancelRun(t.Context(), id); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	if !slices.Contains(br.callLog(), methodKiroWorkflowCancel) {
		t.Error("the cancel verb never went out")
	}
	if got := h.runs.runEndReason(id); got != "" {
		t.Errorf("a user cancel recorded %q", got)
	}
	if h.runs.runBounded(id) {
		t.Error("the cancelled run kept its wall clock running")
	}
	// The claim is held, so a bound firing behind the cancel cannot relabel it.
	if h.runs.claimRunTermination(id) {
		t.Error("the cancelled run's claim was not held, so a late bound can still record over it")
	}
}

// TestCancelRun_FailedRPCHandsTheClaimBack: the claim means a termination is in
// flight or landed. A cancel KAS refused is neither, so holding the claim would
// make every later Cancel on a still-executing run silently do nothing.
func TestCancelRun_FailedRPCHandsTheClaimBack(t *testing.T) {
	h, _, br := newTestHub()
	const id = "wf_1"
	br.callErrs = map[string]error{methodKiroWorkflowCancel: errors.New("kas refused")}
	h.bridge.mgr.insert(runChatID(id), &sharedBridge{bridge: br, state: bridgeIdle})

	if err := h.runs.CancelRun(t.Context(), id); err == nil {
		t.Fatal("a refused cancel reported success")
	}
	if !h.runs.claimRunTermination(id) {
		t.Error("the run stayed claimed after its cancel failed, so nothing can stop it")
	}
}
