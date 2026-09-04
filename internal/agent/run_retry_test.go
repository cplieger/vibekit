package agent

// Tests for retry: the verb, its addressing, and what the route answers.
//
// Every case here fails against the shape this replaced, and each names which of
// the defects it pins. The report that produced them is one aborted,
// chat-parented run whose Retry button was drawn by mistake, dispatched a real
// request, and then had its outcome thrown away — so the assertions are about the
// OUTCOME reaching the reader as much as about the verb reaching KAS.

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
	"github.com/cplieger/vibekit/internal/workflow"
)

// retryReply builds `_kiro/workflow/retry`'s own reply shape:
// `{workflowId, status, retriedNodeIds[]}`.
func retryReply(t *testing.T, workflowID, status string, nodes ...string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"workflowId": workflowID, "status": status, "retriedNodeIds": nodes,
	})
	if err != nil {
		t.Fatalf("Setup: marshalling the retry reply: %s", err)
	}
	return raw
}

// retryReq builds POST /api/runs/{id}/retry with its path value set, which the
// handler reads rather than parsing the URL.
func retryReq(id string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/runs/"+id+"/retry", bytes.NewReader(nil))
	req.SetPathValue("id", id)
	return req
}

// seedChatParentedRun stages the reported situation: one ABORTED run parented on
// a chat's session, with that chat's bridge live in this process, and KAS ready to
// answer inspect, list and retry.
func seedChatParentedRun(t *testing.T, openChat bool, nodes ...string) (*Runtime, *fakeBridge) {
	t.Helper()
	h, cs, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowInspect: inspectReply(t, "wf_1", "aborted", ""),
		methodKiroWorkflowList: kasRuns(t, map[string]any{
			"workflowId": "wf_1", "name": "publish",
			"status": "aborted", "parentSessionId": "sess_owned",
		}),
		methodKiroWorkflowLoad:  json.RawMessage(`{}`),
		methodKiroWorkflowRetry: retryReply(t, "wf_1", "running", nodes...),
	}
	if err := cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "Findings cleanup"
		c.RecordSession("sess_owned")
		return true
	}); err != nil {
		t.Fatalf("Setup: seeding the chat: %s", err)
	}
	if openChat {
		if _, err := h.coord.OpenBridge(t.Context(), "c1", ""); err != nil {
			t.Fatalf("Setup: opening the chat's bridge: %s", err)
		}
	}
	return h, br
}

// gateAnswer resolves the affordance the retry ROUTE's gate hands the verb.
//
// Resolved for real rather than hand-built, because it is the thread the verb
// depends on: a stand-in carrying `ParentChat: "c1"` would keep passing after the
// affordance stopped travelling with the parent, and then the verb would re-host a
// run whose own process is alive — the defect below.
func gateAnswer(t *testing.T, h *Runtime, workflowID string) runAffordance {
	t.Helper()
	aff := h.runs.affordance(t.Context(), workflowID, "aborted")
	if aff.ParentChat != "c1" {
		t.Fatalf("Setup: the gate resolved parent %q, want the launching chat c1", aff.ParentChat)
	}
	return aff
}

// TestHandleRetry_AnswersTheOutcomeRatherThanOk is RC3, the defect that made a
// no-op retry indistinguishable from a real one.
//
// `_kiro/workflow/retry` answers `{workflowId, status, retriedNodeIds[]}` and the
// route discarded the whole reply, answering a content-free `{"ok":true}`. A retry
// that reset zero nodes and one that reset five were therefore the same result on
// the wire, and since the success path emitted no notification either, "nothing
// happened" was what a no-op retry was DESIGNED to look like.
func TestHandleRetry_AnswersTheOutcomeRatherThanOk(t *testing.T) {
	for name, nodes := range map[string][]string{
		"five nodes reset": {"phase-c-loop", "phase-d-loop", "final-verify", "plan", "setup"},
		"one node reset":   {"final-verify"},
		"NOTHING reset":    {},
	} {
		t.Run(name, func(t *testing.T) {
			h, _ := seedChatParentedRun(t, true, nodes...)
			rec := httptest.NewRecorder()
			h.runRoutes.handleRetry(rec, retryReq("wf_1"))
			if rec.Code != http.StatusOK {
				t.Fatalf("POST retry = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
			}
			var got vibekit.RunRetriedResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decoding the retry reply: %s", err)
			}
			if got.Status != "running" {
				t.Errorf("status = %q, want %q", got.Status, "running")
			}
			if !slices.Equal(got.RetriedNodeIDs, nodes) {
				t.Errorf("retried_node_ids = %v, want %v; the reply IS the outcome report, and "+
					"a caller that cannot count the reset nodes cannot tell a retry from a no-op",
					got.RetriedNodeIDs, nodes)
			}
			// The old shape. Asserted explicitly because it is what the client used
			// to receive, and a reply carrying both would look correct while leaving
			// the ambiguity in place for anything still reading `ok`.
			if strings.Contains(rec.Body.String(), `"ok"`) {
				t.Errorf("the reply still carries `ok`: %s", rec.Body.String())
			}
		})
	}
}

// TestRetry_AddressesTheRunsRealHost is RC5's first half.
//
// Retry keyed on `runChatID(workflowID)` alone, and a chat-parented run never has
// a bridge under that key — so it ALWAYS took the re-host branch and spawned a
// second engine for a run whose parentSessionId lives in a process that is
// frequently still alive. SetStepStatus's comment names this exact bug class for
// its own verb ("keyed on `run:<id>` alone answered errRunNotHosted for that whole
// population unconditionally, which is exactly the population an agent creates");
// retry never got that fix.
//
// The load call is the tell, and it is a better one than a bridge-start count: the
// fake's factory hands out one bridge, so only the branch taken is observable, and
// `load` is issued on the re-host branch alone.
func TestRetry_AddressesTheRunsRealHost(t *testing.T) {
	h, br := seedChatParentedRun(t, true, "final-verify")

	out, err := h.runs.Retry(t.Context(), "wf_1", gateAnswer(t, h, "wf_1"))
	if err != nil {
		t.Fatalf("Retry on a chat-parented run = %v, want nil", err)
	}
	if len(out.RetriedNodeIDs) != 1 {
		t.Errorf("outcome = %+v, want the one node KAS reported", out)
	}
	calls := br.callLog()
	if !slices.Contains(calls, methodKiroWorkflowRetry) {
		t.Fatalf("the verb never reached KAS; calls were %v", calls)
	}
	if slices.Contains(calls, methodKiroWorkflowLoad) {
		t.Errorf("the run was re-hosted (a load was issued) although the process holding it is "+
			"alive; that starts a second engine for one run. Calls were %v", calls)
	}
	if sb := h.bridge.mgr.get(runChatID("wf_1")); sb != nil {
		t.Error("a bridge was registered under the run's synthetic id for a run its launching " +
			"chat already hosts")
	}
}

// TestRetry_LoadsBeforeItRetriesAReHostedRun is RC5's second half, and the one
// that made the re-host branch suspect for EVERY population it served.
//
// `_kiro/workflow/retry` does not rehydrate: it requires the run in the calling
// process's live registry and refuses otherwise ("not registered. Load or create
// it first."). Recovery after a process death is `load` then `retry`, which is
// what kiro-cli's own client does before touching a run it does not hold. Without
// the load, the only branch that could ever have worked is the already-hosted one
// — which the code itself called "Not the expected path".
func TestRetry_LoadsBeforeItRetriesAReHostedRun(t *testing.T) {
	// No chat bridge and no run bridge: nothing in this process holds the run,
	// which is retry's own legality window.
	h, br := seedChatParentedRun(t, false, "phase-c-loop")

	if _, err := h.runs.Retry(t.Context(), "wf_1", gateAnswer(t, h, "wf_1")); err != nil {
		t.Fatalf("Retry on a run nothing hosts = %v, want nil", err)
	}
	calls := br.callLog()
	load := slices.Index(calls, methodKiroWorkflowLoad)
	retry := slices.Index(calls, methodKiroWorkflowRetry)
	if load < 0 {
		t.Fatalf("no load was issued before retrying a run this process has never seen; KAS "+
			"refuses that retry as unregistered. Calls were %v", calls)
	}
	if retry < 0 || load > retry {
		t.Errorf("load/retry order = %v, want the load first", calls)
	}
}

// TestRetry_AFailedLoadLeavesNothingBehind: the load is a real failure point, so
// it gets the teardown the retry call already had. A bridge left registered for a
// run that never re-drove would hold a kiro-cli subprocess and a lease nothing
// would release.
func TestRetry_AFailedLoadLeavesNothingBehind(t *testing.T) {
	h, br := seedChatParentedRun(t, false)
	br.setCallRPCErr(methodKiroWorkflowLoad,
		&vibekit.RPCError{Code: -32603, Message: "Workflow wf_1 not found on disk"})

	if _, err := h.runs.Retry(t.Context(), "wf_1", gateAnswer(t, h, "wf_1")); err == nil {
		t.Fatal("Retry = nil after the load failed; nothing was registered, so nothing can run")
	}
	if slices.Contains(br.callLog(), methodKiroWorkflowRetry) {
		t.Error("the verb was issued against a process that failed to load the run")
	}
	if sb := h.bridge.mgr.get(runChatID("wf_1")); sb != nil {
		t.Error("the bridge was left registered for a run that never re-drove")
	}
	if _, held := h.runs.lease("wf_1"); held {
		t.Error("the lease minted for the attempt was not given back")
	}
}

// TestRetry_AnInBandRefusalIsAFailure pins the channel the hosted branch used to
// ignore.
//
// KAS refuses by answering a well-formed response carrying an `error` member —
// the transport succeeds and the reason travels in band. The hosted branch read
// only the transport error, so such a refusal returned nil and the route answered
// 200 for a run that was never re-driven, THEN re-armed its clock and cleared its
// recorded termination. Every other verb folds through runCallErr; this one did
// not.
func TestRetry_AnInBandRefusalIsAFailure(t *testing.T) {
	h, br := seedChatParentedRun(t, true)
	br.setCallRPCErr(methodKiroWorkflowRetry,
		&vibekit.RPCError{Code: -32603, Message: "Cannot retry a completed workflow"})

	out, err := h.runs.Retry(t.Context(), "wf_1", gateAnswer(t, h, "wf_1"))
	if err == nil {
		t.Fatalf("Retry = (%+v, nil) for a refusal KAS answered in band; the run was not "+
			"re-driven and its previous terminal reason is still the truth about it", out)
	}
	if !isRPCRefusal(err) {
		t.Errorf("Retry error = %v, want a JSON-RPC refusal the route can classify as a 409", err)
	}
}

// TestHandleRetry_ForwardsKASsOwnSentence is RC4.
//
// controlHandler's only informative refusal was errRunNotHosted; every other
// failure — every KAS refusal included — became httpreply.InternalError, which
// sends the CONSTANT "internal error" while the actionable sentence goes to the
// container log. So the reader was told "Couldn't retry the run: internal error"
// and could not tell a refusal from a fault. handleLaunch already forwarded
// rpcerr.Text for exactly this reason.
func TestHandleRetry_ForwardsKASsOwnSentence(t *testing.T) {
	h, br := seedChatParentedRun(t, true)
	br.setCallRPCErr(methodKiroWorkflowRetry, &vibekit.RPCError{
		Code:    -32603,
		Message: "Internal error",
		Data:    json.RawMessage(`{"details":"Workflow wf_1 is not registered. Load or create it first."}`),
	})

	rec := httptest.NewRecorder()
	h.runRoutes.handleRetry(rec, retryReq("wf_1"))

	if rec.Code != http.StatusConflict {
		t.Fatalf("a KAS refusal = %d, want %d: %s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "not registered") {
		t.Errorf("the refusal body = %s, want KAS's own sentence", body)
	}
	if strings.Contains(body, "internal error") {
		t.Errorf("the refusal body = %s, want the diagnostic rather than the constant", body)
	}
}

// TestHandleRetry_AnEngineThatWillNotStartAnswersInsideTheClientsWindow is RC6,
// the deadline inversion.
//
// Retry ran on the request context's 120s budget while the browser aborts every
// apiAction at 30s, so a slow engine start meant the CLIENT killed the server's
// in-flight retry — which then tore down the bridge it had just minted and
// released the lease, with nobody watching. The verb's own budget is now below the
// client's, so the answer is a 503 the reader can act on.
func TestHandleRetry_AnEngineThatWillNotStartAnswersInsideTheClientsWindow(t *testing.T) {
	if retryTimeout >= clientRequestBudget {
		t.Errorf("retryTimeout = %v, want it below the client's %v request budget, or the "+
			"browser aborts the handoff mid-flight", retryTimeout, clientRequestBudget)
	}

	h, br := seedChatParentedRun(t, false)
	// The utility bridge FIRST, and this ordering is the fixture's whole trick: the
	// fake's factory hands out one bridge, so a gate armed before the utility session
	// exists parks the status read instead of the retry's own spawn and the test
	// hangs rather than failing. One read warms it.
	if _, err := h.runs.rawInspect(t.Context(), "wf_1"); err != nil {
		t.Fatalf("Setup: warming the utility bridge: %s", err)
	}
	// Now a start that never returns, which is what an engine unpacking a cold
	// runtime tree looks like. The gate is never closed, so Start parks until the
	// verb's own budget expires. Milliseconds rather than the real 25s, so the whole
	// path runs inside a unit test's budget; restored by Cleanup, not defer, since a
	// defer does not run on a failure path and would leak the override into the
	// package.
	br.setStartGate(make(chan struct{}))
	prev := retryTimeout
	retryTimeout = time.Millisecond
	t.Cleanup(func() { retryTimeout = prev })
	before := len(br.callLog())

	rec := httptest.NewRecorder()
	h.runRoutes.handleRetry(rec, retryReq("wf_1"))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("a retry whose engine never started = %d, want %d: %s",
			rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "try again") {
		t.Errorf("the 503 body = %s, want it to name the remedy", rec.Body.String())
	}
	if slices.Contains(br.callLog()[before:], methodKiroWorkflowRetry) {
		t.Error("the verb was issued on a bridge that never started")
	}
	if _, held := h.runs.lease("wf_1"); held {
		t.Error("the abandoned attempt kept the lease it minted")
	}
}

// TestHandleRetry_RefusesWhatTheAffordanceRefuses: the gate exists server-side
// now, which is RC2. The rule lived in ONE client boolean, so the request was
// accepted whatever the client drew — and the client's copy was reading an empty
// cache.
func TestHandleRetry_RefusesWhatTheAffordanceRefuses(t *testing.T) {
	t.Run("a completed run is refused, naming its status", func(t *testing.T) {
		h, br := seedChatParentedRun(t, true)
		br.setCallResult(methodKiroWorkflowInspect, inspectReply(t, "wf_1", "completed", ""))
		rec := httptest.NewRecorder()
		h.runRoutes.handleRetry(rec, retryReq("wf_1"))
		if rec.Code != http.StatusConflict {
			t.Fatalf("retry on a completed run = %d, want %d", rec.Code, http.StatusConflict)
		}
		if !strings.Contains(rec.Body.String(), "completed") {
			t.Errorf("the refusal = %s, want it to name the status", rec.Body.String())
		}
		if slices.Contains(br.callLog(), methodKiroWorkflowRetry) {
			t.Error("the verb reached KAS for a status it refuses")
		}
	})

	// The reported run: aborted and chat-parented. It must be ACCEPTED — that is
	// the ruling this change rests on, and a gate that quietly re-introduced the
	// parentless-only rule server-side would leave the run unreachable in both
	// products.
	t.Run("an aborted CHAT-PARENTED run is accepted", func(t *testing.T) {
		h, _ := seedChatParentedRun(t, true, "phase-c-loop")
		rec := httptest.NewRecorder()
		h.runRoutes.handleRetry(rec, retryReq("wf_1"))
		if rec.Code != http.StatusOK {
			t.Fatalf("retry on an aborted chat-parented run = %d, want %d: %s",
				rec.Code, http.StatusOK, rec.Body.String())
		}
	})

	// The two arms of a status the gate cannot use, which are DIFFERENT answers.
	// One subtest used to claim the 404 and drive the 500's arm, leaving the 404
	// uncovered on this route under a name saying it was covered.
	t.Run("a status read that FAILS is a 500", func(t *testing.T) {
		h, br := seedChatParentedRun(t, true)
		br.setCallErr(methodKiroWorkflowInspect, errors.New("no such workflow"))
		rec := httptest.NewRecorder()
		h.runRoutes.handleRetry(rec, retryReq("wf_1"))
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("an unreadable status = %d, want %d", rec.Code, http.StatusInternalServerError)
		}
	})

	t.Run("a run KAS does not know is a 404", func(t *testing.T) {
		h, br := seedChatParentedRun(t, true)
		// The engine answers, and has no workflow verb: rr.status reports "" rather
		// than an error, which means "no status to gate on" and not a fault.
		br.setCallErr(methodKiroWorkflowInspect, workflow.ErrUnknownMethod)
		rec := httptest.NewRecorder()
		h.runRoutes.handleRetry(rec, retryReq("wf_1"))
		if rec.Code != http.StatusNotFound {
			t.Errorf("retry on an unknown run = %d, want %d: %s",
				rec.Code, http.StatusNotFound, rec.Body.String())
		}
		if slices.Contains(br.callLog(), methodKiroWorkflowRetry) {
			t.Error("the verb was issued for a run that has no status")
		}
	})
}

// TestRetry_AnUnreadableOutcomeIsNotReportedAsAFailedRetry: the mirror of the
// in-band refusal above, and the one failure that happens AFTER the work may have
// started.
//
// KAS accepted the verb; only its report is unusable. Reporting that as an ordinary
// retry failure told the reader to try again — asking for work that may already be
// running — while the re-host branch released the lease and closed the bridge that
// had just begun executing the run. So the class is its own, and both branches keep
// what they started and re-arm the run.
func TestRetry_AnUnreadableOutcomeIsNotReportedAsAFailedRetry(t *testing.T) {
	// One reply per shape KAS could answer that carries no usable outcome. All three
	// mean the same thing to the reader and must not mean "nothing happened".
	unreadable := map[string]json.RawMessage{
		"a reply that is not an object": json.RawMessage(`"retried"`),
		"a reply with no outcome":       json.RawMessage(``),
		"a reply about another run":     retryReply(t, "wf_other", "running", "plan"),
	}

	for name, reply := range unreadable {
		t.Run(name+", on the run's own host", func(t *testing.T) {
			// One fake bridge serves the whole runtime, so this is the chat's bridge:
			// the process that holds the run.
			h, br := seedChatParentedRun(t, true)
			h.runs.claimTermination("wf_1")
			h.runs.recordEnd("wf_1", runEndOverran)
			br.setCallResult(methodKiroWorkflowRetry, reply)

			_, err := h.runs.Retry(t.Context(), "wf_1", gateAnswer(t, h, "wf_1"))
			if !errors.Is(err, errRetryOutcomeUnreadable) {
				t.Fatalf("Retry = %v, want errRetryOutcomeUnreadable: the verb LANDED, so "+
					"telling the reader to retry would ask for the work twice", err)
			}
			// Re-armed like a retry that reported properly, because the run may be
			// executing: leaving the recorded termination would keep the page saying
			// aborted while the run advanced.
			if got := h.runs.endReason("wf_1"); got != "" {
				t.Errorf("the run still reads %q, so its row renders as aborted while it may "+
					"be running", got)
			}
			if !h.runs.bounded("wf_1") {
				t.Error("the run carries no deadline, so nothing bounds work that may be running")
			}
		})

		t.Run(name+", on a re-hosted run", func(t *testing.T) {
			h, br := seedChatParentedRun(t, false)
			br.setCallResult(methodKiroWorkflowRetry, reply)

			_, err := h.runs.Retry(t.Context(), "wf_1", gateAnswer(t, h, "wf_1"))
			if !errors.Is(err, errRetryOutcomeUnreadable) {
				t.Fatalf("Retry = %v, want errRetryOutcomeUnreadable", err)
			}
			if h.bridge.mgr.get(runChatID("wf_1")) == nil {
				t.Error("the bridge that just started re-driving the run was closed because its " +
					"reply could not be parsed")
			}
			if _, held := h.runs.lease("wf_1"); !held {
				t.Error("the lease was released for a run that may be executing, so the wall " +
					"clock no longer bounds it and its recipe reads as free")
			}
		})
	}
}

// TestHandleRetry_AnUnreadableOutcomeTellsTheReaderToRefresh is the route half:
// the answer must not be the 500 that says "this failed, try again".
func TestHandleRetry_AnUnreadableOutcomeTellsTheReaderToRefresh(t *testing.T) {
	h, br := seedChatParentedRun(t, true)
	br.setCallResult(methodKiroWorkflowRetry, json.RawMessage(`"retried"`))

	rec := httptest.NewRecorder()
	h.runRoutes.handleRetry(rec, retryReq("wf_1"))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("an unreadable outcome = %d, want %d: %s",
			rec.Code, http.StatusBadGateway, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "refresh") {
		t.Errorf("the body = %s, want the remedy that is actually the reader's; a retry that "+
			"landed must not be reported as one to repeat", body)
	}
	if strings.Contains(body, "internal error") {
		t.Errorf("the body = %s, want the diagnostic rather than the constant", body)
	}
}

// TestRetry_ReadsTheParentTheGateResolved: the gate and the verb asked the same
// question independently.
//
// `permits` resolves the affordance — one `workflow/list` round trip plus a full
// chat-directory scan — and the verb then resolved the host all over again, a second
// of each, on top of the recipe read. Three uncached lists per click, every one
// spent before the engine starts and inside a budget narrower than the request's.
//
// Asserted as a MECHANISM rather than a call count, and deliberately: the runtime
// lists in the background too, so counting round trips answers differently under
// `-race` than without it. What is deterministic is that the two reads can DISAGREE,
// which is the sharper reason not to make them twice: the inventory here loses the
// run's parent session between the gate and the verb, so a verb that re-asks
// concludes nothing hosts the run and re-hosts one whose own process is alive.
func TestRetry_ReadsTheParentTheGateResolved(t *testing.T) {
	h, br := seedChatParentedRun(t, true, "final-verify")
	// The gate's answer, resolved for real while the inventory still carries the
	// parent session — the state every request starts from.
	aff := gateAnswer(t, h, "wf_1")
	// The inventory a LATER read gets: the same run, no parent session.
	br.setCallResult(methodKiroWorkflowList, kasRuns(t, map[string]any{
		"workflowId": "wf_1", "name": "publish", "status": "aborted",
	}))

	if _, err := h.runs.Retry(t.Context(), "wf_1", aff); err != nil {
		t.Fatalf("Retry = %v, want nil", err)
	}
	if slices.Contains(br.callLog(), methodKiroWorkflowLoad) {
		t.Errorf("the run was re-hosted although the gate had already resolved its live host; "+
			"the verb re-asked and got a different answer. Calls were %v", br.callLog())
	}
	if sb := h.bridge.mgr.get(runChatID("wf_1")); sb != nil {
		t.Error("a second engine was registered under the run's synthetic id for a run its " +
			"launching chat already hosts")
	}
}

func TestHandleRetry_RejectsNonPOSTAndAMissingID(t *testing.T) {
	h, _ := seedChatParentedRun(t, true)
	rec := httptest.NewRecorder()
	h.runRoutes.handleRetry(rec, httptest.NewRequest(http.MethodGet, "/api/runs/wf_1/retry", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
	rec = httptest.NewRecorder()
	h.runRoutes.handleRetry(rec, httptest.NewRequest(http.MethodPost, "/api/runs//retry", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("no id = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}
