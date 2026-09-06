package agent

// The cancel's CARRIER, and the deadline's fate when that cancel is refused.
//
// One file rather than a share of run_bounds_test.go and run_control_test.go,
// because the two halves are one defect: a cancel sent on the wrong session is
// refused by KAS, and a refused cancel used to unbound its run permanently.

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// spawnRecorder hands out a DISTINCT fake per spawn and remembers them, which the
// shared newTestHub cannot do: one instance serves the utility session and every
// chat bridge there, so its call log answers "did the RPC go out" and never "on
// which session". Routing is the whole subject here, so the fixture has to tell
// two carriers apart.
type spawnRecorder struct {
	results map[string]json.RawMessage
	errs    map[string]error
	spawned []*fakeBridge
	mu      sync.Mutex
}

func (s *spawnRecorder) factory() ACPBridge {
	br := newFakeBridge()
	br.callResults = s.results
	br.callErrs = s.errs
	s.mu.Lock()
	defer s.mu.Unlock()
	s.spawned = append(s.spawned, br)
	return br
}

// sawCall reports how many of the spawned bridges took the method, and how many
// times in total. Both numbers, because "the utility session was spared" and "the
// cancel was not sent twice" are different claims.
func (s *spawnRecorder) sawCall(method string) (bridges, calls int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, br := range s.spawned {
		hits := 0
		for _, m := range br.callLog() {
			if m == method {
				hits++
			}
		}
		if hits > 0 {
			bridges++
			calls += hits
		}
	}
	return bridges, calls
}

// chatBridge is the fake the coordinator actually handed this chat, resolved
// through the bridge map rather than by spawn order — the utility session starts
// lazily on its first RPC, so which index it takes depends on the call sequence.
func chatBridge(t *testing.T, h *Runtime, chatID vibekit.ChatID) *fakeBridge {
	t.Helper()
	sb := h.runs.bridges.get(chatID)
	if sb == nil {
		t.Fatalf("chat %q holds no bridge", chatID)
	}
	br, ok := sb.bridge.(*fakeBridge)
	if !ok {
		t.Fatalf("chat %q holds a %T, not a fake", chatID, sb.bridge)
	}
	return br
}

// agentLaunchedRun stages the population the defect lives in: a chat with a live
// bridge, and one run KAS reports as parented on that chat's session. Returns the
// recorder so a test can ask which carriers saw what.
func agentLaunchedRun(t *testing.T, results map[string]json.RawMessage, errs map[string]error) (*Runtime, *spawnRecorder) {
	t.Helper()
	rec := &spawnRecorder{results: results, errs: errs}
	cs := newFakeChatStore()
	h := New(t.Context(), "/tmp/work", rec.factory, cs)
	cs.Bus = h
	h.mcpRegistry.SignalReady()
	if err := cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		c.RecordSession("sess_owner")
		return true
	}); err != nil {
		t.Fatalf("seed the chat: %v", err)
	}
	if _, err := h.coord.OpenBridge(t.Context(), "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	return h, rec
}

// agentRunList is what `workflow/list` answers for one agent-launched run.
func agentRunList(t *testing.T, status string) json.RawMessage {
	t.Helper()
	return kasRuns(t, map[string]any{
		"workflowId": "wf_1", "name": "publish", "status": status,
		"parentSessionId": "sess_owner",
	})
}

// TestCancel_IsCarriedOnTheOWNINGChatsBridge is the routing defect, and the
// assertion is on the CARRIER rather than on the outcome.
//
// An agent-launched run has no `run:<id>` bridge and never will — KAS parents it on
// the calling chat's session — so `control` fell through to the UTILITY session for
// that population, which is nearly all of them. KAS's cancel handler tries its own
// registry first and cancels a hit with no ownership check; a miss reaches the write
// helper, which opens with `ensureRunOwnership` and throws while the owner is live.
// Measured: 35 of 36 bound-driven cancels refused over 2026-08-20 → 09-05.
//
// Asserting only that the run stopped would pass on the utility session too, because
// the fake answers every carrier identically. So this counts carriers: exactly one
// bridge takes the cancel, and it is the one the chat holds.
func TestCancel_IsCarriedOnTheOWNINGChatsBridge(t *testing.T) {
	h, rec := agentLaunchedRun(t, map[string]json.RawMessage{
		methodKiroWorkflowList:    agentRunList(t, "running"),
		methodKiroWorkflowCancel:  json.RawMessage(`{}`),
		methodKiroWorkflowInspect: inspectReply(t, "wf_1", "aborted", ""),
	}, nil)
	owner := chatBridge(t, h, "c1")
	leased(t, h.runs, "wf_1")

	if err := h.runs.Cancel(t.Context(), "wf_1"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	if !slices.Contains(owner.callLog(), methodKiroWorkflowCancel) {
		t.Error("the cancel did not reach the launching chat's bridge, so KAS answers it " +
			"from the write helper and refuses while that process is alive")
	}
	if bridges, calls := rec.sawCall(methodKiroWorkflowCancel); bridges != 1 || calls != 1 {
		t.Errorf("the cancel reached %d bridge(s) in %d call(s), want exactly 1 and 1: "+
			"a second carrier means the utility session was used as well", bridges, calls)
	}
}

// TestDelete_IsCarriedOnTheOWNINGChatsBridge: delete takes the same carrier for the
// same reason. KAS's delete cancels a non-terminal run itself, so it meets the same
// ownership check the cancel does.
func TestDelete_IsCarriedOnTheOWNINGChatsBridge(t *testing.T) {
	h, rec := agentLaunchedRun(t, map[string]json.RawMessage{
		methodKiroWorkflowList:   agentRunList(t, "paused"),
		methodKiroWorkflowDelete: json.RawMessage(`{}`),
	}, nil)
	owner := chatBridge(t, h, "c1")
	leased(t, h.runs, "wf_1")

	if err := h.runs.Delete(t.Context(), "wf_1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if !slices.Contains(owner.callLog(), methodKiroWorkflowDelete) {
		t.Error("the delete did not reach the launching chat's bridge")
	}
	if bridges, _ := rec.sawCall(methodKiroWorkflowDelete); bridges != 1 {
		t.Errorf("the delete reached %d bridges, want exactly the owner's", bridges)
	}
}

// TestCancelForSessions_ReadsTheRunInventoryOnce is the routing fix's COST, one layer
// up from the fix itself.
//
// The tab-close route enumerates the inventory and then cancels per run, and through
// the plain Cancel each of those re-entered `workflow/list` to recover the parent
// session the loop was already holding — N+1 sequential trips against the utility
// session, each with its own sessionListTimeout, during a teardown the user is
// watching. The carrier now comes off the row, so the TRIP COUNT is the subject here;
// which carrier it is belongs to the routing tests above.
//
// The chat's bridge is INSERTED rather than opened: OpenBridge fires the rehydrate
// hook, whose own resumeInterruptedRuns reads the inventory on another goroutine, so a
// count taken after it would be measuring a race rather than this path.
func TestCancelForSessions_ReadsTheRunInventoryOnce(t *testing.T) {
	h, cs, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowList: kasRuns(t,
			map[string]any{
				"workflowId": "wf_1", "name": "publish", "status": "running",
				"parentSessionId": "sess_owner",
			},
			map[string]any{
				"workflowId": "wf_2", "name": "publish", "status": "paused",
				"parentSessionId": "sess_owner",
			},
		),
		methodKiroWorkflowCancel: json.RawMessage(`{}`),
		// Named for neither run, so releaseIfOver's identity check declines both and
		// the leases stay put — nothing here turns on them.
		methodKiroWorkflowInspect: inspectReply(t, "wf_other", "running", ""),
	}
	if err := cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		c.RecordSession("sess_owner")
		return true
	}); err != nil {
		t.Fatalf("seed the chat: %v", err)
	}
	h.bridge.mgr.insert("c1", &sharedBridge{bridge: br, state: bridgeIdle})
	leased(t, h.runs, "wf_1")
	leased(t, h.runs, "wf_2")

	h.runs.CancelForSessions(t.Context(), "c1", []string{"sess_owner"})

	if got := callsOf(br, methodKiroWorkflowCancel); got != 2 {
		t.Errorf("the cancel went out %d times for 2 live runs, want 2", got)
	}
	if got := callsOf(br, methodKiroWorkflowList); got != 1 {
		t.Errorf("`workflow/list` went out %d times to cancel 2 runs, want 1: the loop "+
			"already holds every row the carrier resolver would re-read", got)
	}
}

// TestCancelForSessions_RoutesWithTheChatRecordALREADYDELETED is the one door the
// routing did not reach, and the assertion is on the CARRIER rather than on the
// outcome for TestCancel_IsCarriedOnTheOWNINGChatsBridge's reason.
//
// With retention off, Membership.CloseTab deletes the doomed chat's record at its
// commit point and only then dispatches deleteChatTeardown → DeleteChatStateByChain
// → here. The record-matching resolver reads chat.Store.Get, a disk read with no
// cache, so on that path every row resolved a NIL carrier and its cancel went to the
// utility session — where KAS refuses it while the owner lives. The bridge was in the
// map the whole time: present and unreachable.
//
// So the fixture is the delete-grade state exactly: a live bridge under "c1" and NO
// chat record. The carrier now comes off the chat ID this loop was handed, which by
// construction is the launching chat for every run it cancels and needs no record.
func TestCancelForSessions_RoutesWithTheChatRecordALREADYDELETED(t *testing.T) {
	rec := &spawnRecorder{results: map[string]json.RawMessage{
		methodKiroWorkflowList:   agentRunList(t, "running"),
		methodKiroWorkflowCancel: json.RawMessage(`{}`),
		// Named for no run here, so releaseIfOver's identity check declines and the
		// lease stays put — nothing in this test turns on it.
		methodKiroWorkflowInspect: inspectReply(t, "wf_other", "running", ""),
	}}
	cs := newFakeChatStore()
	h := New(t.Context(), "/tmp/work", rec.factory, cs)
	cs.Bus = h
	owner, ok := rec.factory().(*fakeBridge)
	if !ok {
		t.Fatal("Setup: the recorder handed back something other than a fake")
	}
	h.bridge.mgr.insert("c1", &sharedBridge{bridge: owner, state: bridgeIdle})
	if _, found := cs.Get(t.Context(), "c1"); found {
		t.Fatal("Setup: the chat record exists, so this is not the delete-grade state")
	}
	leased(t, h.runs, "wf_1")

	h.runs.CancelForSessions(t.Context(), "c1", []string{"sess_owner"})

	if !slices.Contains(owner.callLog(), methodKiroWorkflowCancel) {
		t.Error("the cancel did not reach the launching chat's bridge for a chat whose " +
			"record is already deleted, so KAS answers it from the write helper and " +
			"refuses while that process is alive")
	}
	if bridges, calls := rec.sawCall(methodKiroWorkflowCancel); bridges != 1 || calls != 1 {
		t.Errorf("the cancel reached %d bridge(s) in %d call(s), want exactly 1 and 1: "+
			"a second carrier means the utility session was used as well", bridges, calls)
	}
}

// TestCancelForSessions_PrefersTheRunsOWNProcess pins the `run:<id>`-first half of the
// preference at its SECOND composition site.
//
// The preference has one owner (runOwnBridge) because it is load-bearing rather than
// tidy: a re-hosted run's registry entry lives in the process that re-hosted it, so
// consulting the chat's bridge first sends the verb to a process that has forgotten
// the run and KAS refuses it from the write helper. Reachable exactly here — a run
// re-hosted while its chat's bridge was down, whose chat was then reopened.
func TestCancelForSessions_PrefersTheRunsOWNProcess(t *testing.T) {
	rec := &spawnRecorder{results: map[string]json.RawMessage{
		methodKiroWorkflowList:    agentRunList(t, "paused"),
		methodKiroWorkflowCancel:  json.RawMessage(`{}`),
		methodKiroWorkflowInspect: inspectReply(t, "wf_other", "running", ""),
	}}
	cs := newFakeChatStore()
	h := New(t.Context(), "/tmp/work", rec.factory, cs)
	cs.Bus = h
	chatBr, ok := rec.factory().(*fakeBridge)
	if !ok {
		t.Fatal("Setup: the recorder handed back something other than a fake")
	}
	runBr, ok := rec.factory().(*fakeBridge)
	if !ok {
		t.Fatal("Setup: the recorder handed back something other than a fake")
	}
	// Both carriers live at once, which is what makes the ORDER observable.
	h.bridge.mgr.insert("c1", &sharedBridge{bridge: chatBr, state: bridgeIdle})
	h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: runBr, state: bridgeIdle})
	leased(t, h.runs, "wf_1")

	h.runs.CancelForSessions(t.Context(), "c1", []string{"sess_owner"})

	if !slices.Contains(runBr.callLog(), methodKiroWorkflowCancel) {
		t.Error("the cancel did not reach the run's own re-hosted process, which is the " +
			"one holding its registry entry")
	}
	if slices.Contains(chatBr.callLog(), methodKiroWorkflowCancel) {
		t.Error("the cancel went to the launching chat's process while a re-hosted " +
			"carrier existed, so KAS meets the ownership check and refuses it")
	}
}

// TestCancel_FallsBackToTheUtilitySessionWhenNothingHostsTheRun keeps the fallback
// honest, which is what makes the routing a preference rather than a requirement: a
// run whose owner is gone has no carrier to prefer, and KAS's ownership check passes
// on a stale stamp. That population is the one measured success in the record —
// wf_5fa90abea7328028's tab-close cancel landed on the utility session because by
// then its owner was stale.
func TestCancel_FallsBackToTheUtilitySessionWhenNothingHostsTheRun(t *testing.T) {
	h, rec := agentLaunchedRun(t, map[string]json.RawMessage{
		// Parented on a session no open chat owns, so nothing here hosts it.
		methodKiroWorkflowList: kasRuns(t, map[string]any{
			"workflowId": "wf_1", "status": "paused", "parentSessionId": "sess_stranger",
		}),
		methodKiroWorkflowCancel:  json.RawMessage(`{}`),
		methodKiroWorkflowInspect: inspectReply(t, "wf_1", "aborted", ""),
	}, nil)
	owner := chatBridge(t, h, "c1")
	leased(t, h.runs, "wf_1")

	if err := h.runs.Cancel(t.Context(), "wf_1"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	if slices.Contains(owner.callLog(), methodKiroWorkflowCancel) {
		t.Error("the cancel went to a chat whose session does not own the run")
	}
	if bridges, _ := rec.sawCall(methodKiroWorkflowCancel); bridges != 1 {
		t.Errorf("the cancel reached %d bridges, want the utility session alone", bridges)
	}
}

// TestFinishTermination_ARefusedCancelKEEPSTheDeadline is the ordering fix.
//
// The disarm used to run BEFORE the cancel, so a refusal left the run executing with
// `Bounded() == false` — and armDeadline's idempotence check then had nothing to
// protect, so nothing ever re-armed it. Each of the 35 measured refusals unbounded
// its run for the rest of its life; two were still on disk when the defect was found.
//
// The deadline is the record that vibekit is bounding a run, and a run whose cancel
// was refused is still a run vibekit is bounding.
func TestFinishTermination_ARefusedCancelKEEPSTheDeadline(t *testing.T) {
	h, _, br := newTestHub()
	br.callErrs = map[string]error{methodKiroWorkflowCancel: errors.New("owner pid 979344 is live")}
	leased(t, h.runs, "wf_1")
	h.runs.armDeadline(t.Context(), "wf_1")
	if !h.runs.bounded("wf_1") {
		t.Fatal("the fixture is not bounded, so it cannot show a deadline surviving")
	}

	if err := h.runs.Cancel(t.Context(), "wf_1"); err == nil {
		t.Fatal("Cancel reported success for a refused cancel")
	}

	if !h.runs.bounded("wf_1") {
		t.Error("a REFUSED cancel parked the run's deadline, so the run keeps executing " +
			"with nothing bounding it and no later arm will ever restore one")
	}
}

// TestFinishTermination_ALandedCancelRELEASESTheDeadline is the other half, and it
// is what stops the fix above from simply never disarming: the deadline is a
// statement about a run this process is bounding, so a run that has stopped must not
// keep one.
func TestFinishTermination_ALandedCancelRELEASESTheDeadline(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowCancel: json.RawMessage(`{}`),
		// Still running, so releaseIfOver leaves the lease and `bounded` stays
		// readable — the deadline's own fate is what this pins.
		methodKiroWorkflowInspect: inspectReply(t, "wf_1", "running", ""),
	}
	leased(t, h.runs, "wf_1")
	h.runs.armDeadline(t.Context(), "wf_1")

	if err := h.runs.Cancel(t.Context(), "wf_1"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	if h.runs.bounded("wf_1") {
		t.Error("a LANDED cancel left the run bounded, so the ceiling would fire again " +
			"against a run this process has already stopped")
	}
}

// TestRecordEnd_IsNotStampedOnARunThatDidNotStop.
//
// `endReason`'s one consumer is toWire, so a reason recorded before the cancel made a
// parentless run's History row read `overran` while KAS reported it `running` — the
// row claimed an outcome the run had not reached. The reason is what vibekit DID, so
// it is recorded when the stop lands and not when it is attempted.
func TestRecordEnd_IsNotStampedOnARunThatDidNotStop(t *testing.T) {
	t.Run("a refused ceiling cancel records nothing", func(t *testing.T) {
		h, _, br := newTestHub()
		br.callErrs = map[string]error{methodKiroWorkflowCancel: errors.New("owner is live")}
		leased(t, h.runs, "wf_1")

		h.runs.cancelBounded("wf_1", runEndOverran)

		if got := h.runs.endReason("wf_1"); got != "" {
			t.Errorf("endReason = %q after a REFUSED cancel, want \"\": the History row would "+
				"read as ended while KAS still reports the run running", got)
		}
	})

	t.Run("a landed ceiling cancel records overran", func(t *testing.T) {
		h, _, br := newTestHub()
		br.callResults = map[string]json.RawMessage{
			methodKiroWorkflowCancel:  json.RawMessage(`{}`),
			methodKiroWorkflowInspect: inspectReply(t, "wf_1", "aborted", ""),
		}
		leased(t, h.runs, "wf_1")

		h.runs.cancelBounded("wf_1", runEndOverran)

		if got := h.runs.endReason("wf_1"); got != runEndOverran {
			t.Errorf("endReason = %q after a LANDED ceiling cancel, want %q: the row would "+
				"fall back to plain aborted and lose why the run stopped", got, runEndOverran)
		}
	})
}

// restoreCancelRetryDelay points the re-attempt backoff at d for one test, over the
// package default TestMain parks it at.
//
// A `var` for the same reason healBaseDelay is one: the production ladder is 5s,
// 10s, 20s, and a test that waited it out would take 35 seconds. Restored through
// t.Cleanup rather than defer, so a subtest's failure path cannot leak it.
func restoreCancelRetryDelay(t *testing.T, d time.Duration) {
	t.Helper()
	prev := cancelRetryBaseDelay
	cancelRetryBaseDelay = d
	t.Cleanup(func() { cancelRetryBaseDelay = prev })
}

// TestRetryTermination_IsBoundedAndDoesNotReFireForever.
//
// Keeping the deadline is what makes this necessary: the timer that fired is spent
// and claimExpiredDeadline would pass again against a lease this path did not
// change, so the next attempt has to be installed deliberately. Deliberately means
// bounded — a permanently unkillable run would otherwise attempt a cancel for the
// life of the container.
//
// The bound is on the RETRY and not on the deadline, which is what the second
// assertion pins: the budget runs out and the run is STILL bounded, because vibekit
// is still bounding a run it could not stop.
func TestRetryTermination_IsBoundedAndDoesNotReFireForever(t *testing.T) {
	h, _, br := newTestHub()
	br.callErrs = map[string]error{methodKiroWorkflowCancel: errors.New("owner is live")}
	restoreCancelRetryDelay(t, time.Millisecond)
	leased(t, h.runs, "wf_1")
	h.runs.armDeadline(t.Context(), "wf_1")

	// The first attempt is the caller's; every later one is a re-attempt this
	// schedules for itself.
	if err := h.runs.Cancel(t.Context(), "wf_1"); err == nil {
		t.Fatal("Cancel reported success for a refused cancel")
	}

	// One caller plus maxCancelRetries re-attempts. The ladder is 1ms, 2ms, 4ms, so
	// it is spent well inside this poll — which fails CLOSED with a diagnostic rather
	// than sleeping a fixed span and hoping.
	want := 1 + maxCancelRetries
	deadline := time.Now().Add(5 * time.Second)
	for callsOf(br, methodKiroWorkflowCancel) < want {
		if time.Now().After(deadline) {
			t.Fatalf("the cancel was attempted %d times, want %d (one caller plus %d "+
				"bounded re-attempts): the ladder is not re-installing itself at all",
				callsOf(br, methodKiroWorkflowCancel), want, maxCancelRetries)
		}
		time.Sleep(time.Millisecond)
	}

	// And no more, however long the process lives. A settling window rather than an
	// instant read: the last re-attempt's own timer can still be in flight.
	time.Sleep(100 * time.Millisecond)
	if got := callsOf(br, methodKiroWorkflowCancel); got != want {
		t.Errorf("the cancel was attempted %d times, want %d: an unbounded ladder "+
			"retries for the life of the container", got, want)
	}
	if !h.runs.bounded("wf_1") {
		t.Error("the spent retry budget unbounded the run; the bound belongs on OUR " +
			"attempts to end it, never on the record that we are bounding it")
	}
}

// awaitCalls polls until br has taken method at least want times, failing CLOSED
// with a diagnostic rather than sleeping a fixed span and hoping.
func awaitCalls(t *testing.T, br *fakeBridge, method string, want int, why string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for callsOf(br, method) < want {
		if time.Now().After(deadline) {
			t.Fatalf("%s was attempted %d times, want %d: %s",
				method, callsOf(br, method), want, why)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestHealProgress_RefillsTheCancelRetryBudget: the retry budget has TWO spenders,
// so a run's own Cancel button can spend the ceiling's re-attempts.
//
// finishTermination's error path is reached by cancelOn as well as cancelBounded, and
// nothing that a still-EXECUTING run reaches ever cleared the counter — clearCancelRetries
// fired only on a landed cancel, in forgetBounds and in clearEnd. So three refused
// presses left the ceiling firing hours later, being refused once, and logging
// logMsgCancelUnretried having never re-attempted.
//
// A refusal is evidence about a MOMENT, so progress makes the earlier refusals stale:
// a completed node returns the budget, exactly as it returns the heal budget beside it.
func TestHealProgress_RefillsTheCancelRetryBudget(t *testing.T) {
	h, _, br := newTestHub()
	br.callErrs = map[string]error{methodKiroWorkflowCancel: errors.New("owner is live")}
	restoreCancelRetryDelay(t, time.Millisecond)
	leased(t, h.runs, "wf_1")
	h.runs.armDeadline(t.Context(), "wf_1")

	// Spend the whole ladder on the USER's button: one press plus the re-attempts it
	// schedules for itself.
	if err := h.runs.Cancel(t.Context(), "wf_1"); err == nil {
		t.Fatal("Cancel reported success for a refused cancel")
	}
	spent := 1 + maxCancelRetries
	awaitCalls(t, br, methodKiroWorkflowCancel, spent,
		"the ladder is not re-installing itself, so the budget was never spent and "+
			"this test cannot observe a refill")

	// The run completes a node: it has moved on, whatever our refusals said.
	h.translateACPEvent("c1", runNotif(methodWFNodeComplete, map[string]any{
		"workflowId": "wf_1", "nodeId": "n1", "status": "completed",
	}))

	// A later refused cancel now gets a FRESH ladder rather than one attempt and
	// logMsgCancelUnretried.
	if err := h.runs.Cancel(t.Context(), "wf_1"); err == nil {
		t.Fatal("Cancel reported success for a refused cancel")
	}
	awaitCalls(t, br, methodKiroWorkflowCancel, spent+1+maxCancelRetries,
		"the budget was not refilled by the completed node, so the ceiling's own "+
			"re-attempts stay spent by the user's earlier presses")
}

// callsOf counts how many times a fake took one method.
func callsOf(br *fakeBridge, method string) int {
	n := 0
	for _, m := range br.callLog() {
		if m == method {
			n++
		}
	}
	return n
}

// TestRetryTermination_ARunNoLongerBoundedIsLeftAlone: the re-attempt re-reads
// before it acts, which is what makes an untracked AfterFunc safe here (healPaused's
// precedent). A pause parks the deadline and a terminal frame releases the lease;
// in both cases vibekit has stopped bounding the run, so it is not one this path may
// cancel.
func TestRetryTermination_ARunNoLongerBoundedIsLeftAlone(t *testing.T) {
	h, _, br := newTestHub()
	br.callErrs = map[string]error{methodKiroWorkflowCancel: errors.New("owner is live")}
	// TWO ordering constraints on this base, and the test is vacuous if either breaks:
	// the park below has to land inside it, and the settle after it has to OUTLAST it
	// or the timer never fires and the guard is never exercised.
	const base = 200 * time.Millisecond
	restoreCancelRetryDelay(t, base)
	leased(t, h.runs, "wf_1")
	h.runs.armDeadline(t.Context(), "wf_1")

	if err := h.runs.Cancel(t.Context(), "wf_1"); err == nil {
		t.Fatal("Cancel reported success for a refused cancel")
	}
	before := callsOf(br, methodKiroWorkflowCancel)
	// The run parks before the re-attempt comes due.
	h.runs.disarmDeadline(t.Context(), "wf_1")

	time.Sleep(2 * base)

	if got := callsOf(br, methodKiroWorkflowCancel); got != before {
		t.Errorf("a run vibekit had stopped bounding was cancelled anyway (%d → %d calls)",
			before, got)
	}
}

// TestResumeIfInterrupted_ArmsTheDeadline.
//
// This path calls `_kiro/workflow/resume` DIRECTLY on the chat's bridge rather than
// through Runs.Resume, so it did not arm — it was saved only by the `run_start`
// frame landing on that same live bridge and reaching observeStart. A frame lost
// between the resume and the arm left the run executing with no ceiling and nothing
// able to notice, which is Resume's own reason for arming explicitly.
func TestResumeIfInterrupted_ArmsTheDeadline(t *testing.T) {
	h, cs, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		// stalePauseReason is what a restart-reconciled run carries, and
		// involuntarilyPaused reads the run's own state rather than the frame.
		methodKiroWorkflowInspect: inspectReply(t, "wf_1", "paused", stalePauseReason),
		methodKiroWorkflowResume:  json.RawMessage(`{}`),
	}
	if err := cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		c.RecordSession("sess_owner")
		return true
	}); err != nil {
		t.Fatalf("seed the chat: %v", err)
	}
	if _, err := h.coord.OpenBridge(t.Context(), "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	// Leased and PARKED, which is the state a resume finds: the pause zeroed the
	// deadline, so an arm here is a fresh budget rather than a remainder.
	leased(t, h.runs, "wf_1")
	if h.runs.bounded("wf_1") {
		t.Fatal("the fixture is already bounded, so an arm would be unobservable")
	}

	h.runs.resumeIfInterrupted(t.Context(), "c1", "wf_1")

	if !slices.Contains(br.callLog(), methodKiroWorkflowResume) {
		t.Fatalf("the resume never went out, so the arm is untested: %v", br.callLog())
	}
	if !h.runs.bounded("wf_1") {
		t.Error("the rehydrate resume did not arm the run's deadline: a lost `run_start` " +
			"then leaves it executing unbounded with nothing able to notice")
	}
}

// TestResumeIfInterrupted_DoesNotArmARefusedResume: the arm follows the verb, like
// Resume's own. A resume KAS refused re-drove nothing, so bounding it would start a
// clock on a run that is still parked and cancel it while it waits.
func TestResumeIfInterrupted_DoesNotArmARefusedResume(t *testing.T) {
	h, cs, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowInspect: inspectReply(t, "wf_1", "paused", stalePauseReason),
	}
	br.callErrs = map[string]error{methodKiroWorkflowResume: errors.New("registry.require threw")}
	if err := cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		c.RecordSession("sess_owner")
		return true
	}); err != nil {
		t.Fatalf("seed the chat: %v", err)
	}
	if _, err := h.coord.OpenBridge(t.Context(), "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	leased(t, h.runs, "wf_1")

	h.runs.resumeIfInterrupted(t.Context(), "c1", "wf_1")

	if h.runs.bounded("wf_1") {
		t.Error("a REFUSED resume armed a deadline, so a run that is still parked would " +
			"be cancelled for overrunning a budget it never started spending")
	}
}

// TestCancelUnretriedMessage_IsGreppable pins the one line an operator has to act
// on. It names no Loki rule, deliberately — no rule keys on it yet — but a run
// nothing can stop is the condition worth alerting on, so the string is a constant
// rather than an inline format.
//
// And it must claim nothing it has not verified: retryTermination fires for every
// non-nil cancel error, so a transport fault and a workflow id KAS does not know both
// reach this line, where neither "still executing" nor "holds its recipe" is known.
func TestCancelUnretriedMessage_IsGreppable(t *testing.T) {
	for _, unverified := range []string{"still executing", "holds its recipe"} {
		if strings.Contains(logMsgCancelUnretried, unverified) {
			t.Errorf("logMsgCancelUnretried = %q, want no claim about the run's own state: "+
				"%q is unverified for a transport fault or an unknown workflow id, and this "+
				"is the line an alert would key on", logMsgCancelUnretried, unverified)
		}
	}
	for _, other := range []string{
		logMsgRunStalled, logMsgRunBackstop, logMsgStepCap, logMsgRunOrphaned, logMsgRunYieldedToSlot,
	} {
		if logMsgCancelUnretried == other {
			t.Errorf("logMsgCancelUnretried duplicates %q, so a rule reading one would "+
				"page on the other", other)
		}
	}
}
