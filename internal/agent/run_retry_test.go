package agent

// Tests for retry: the verb, its addressing, and what the route answers.

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

// retryReq builds POST /api/runs/{id}/retry with the path value the handler reads
// instead of parsing the URL.
func retryReq(id string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/runs/"+id+"/retry", bytes.NewReader(nil))
	req.SetPathValue("id", id)
	return req
}

// seedChatParentedRun stages one ABORTED run parented on a chat's session, that
// chat's bridge optionally live here, and KAS ready to answer inspect, list, retry.
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

// gateAnswer resolves the affordance the retry ROUTE's gate hands the verb, for
// real rather than hand-built, checking BOTH threaded facts: a stand-in would keep
// passing once the affordance stopped carrying either, and the verb would then
// re-host a run whose own process is alive or re-arm a nameless lease.
func gateAnswer(t *testing.T, h *Runtime, workflowID string) runAffordance {
	t.Helper()
	aff := h.runs.affordance(t.Context(), workflowID, "aborted")
	if aff.ParentChat != "c1" {
		t.Fatalf("Setup: the gate resolved parent %q, want the launching chat c1", aff.ParentChat)
	}
	if aff.Recipe != "publish" {
		t.Fatalf("Setup: the gate resolved recipe %q, want publish off KAS's run list", aff.Recipe)
	}
	return aff
}

// TestHandleRetry_AnswersTheOutcomeRatherThanOk: the reply IS the outcome report,
// so it carries KAS's status and reset node ids. A content-free body makes a retry
// that reset zero nodes and one that reset five the same result on the wire.
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
			// A reply carrying `ok` as well would look correct while leaving the
			// ambiguity in place for anything still reading it.
			if strings.Contains(rec.Body.String(), `"ok"`) {
				t.Errorf("the reply still carries `ok`: %s", rec.Body.String())
			}
		})
	}
}

// TestRetry_AddressesTheRunsRealHost: a chat-parented run has no bridge under
// `runChatID(workflowID)`, so keying on that alone re-hosts every one of them and
// spawns a second engine for a run whose process is still alive. The load call is
// the tell — the fake hands out one bridge, and `load` marks the re-host branch.
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

// TestRetry_LoadsBeforeItRetriesAReHostedRun: `_kiro/workflow/retry` does not
// rehydrate — it requires the run in the calling process's live registry and
// refuses otherwise ("not registered. Load or create it first."), so recovery after
// a process death is `load` then `retry`, as kiro-cli's own client does.
func TestRetry_LoadsBeforeItRetriesAReHostedRun(t *testing.T) {
	// No chat bridge and no run bridge: nothing in this process holds the run.
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

// TestRetry_AFailedLoadLeavesNothingBehind: a bridge left registered for a run that
// never re-drove holds a kiro-cli subprocess and a lease nothing releases.
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

// TestRetry_AnInBandRefusalIsAFailure: KAS refuses with a well-formed response
// carrying an `error` member, so the transport succeeds and the reason travels in
// band. Reading only the transport error answers 200 for a run never re-driven,
// then re-arms its clock and clears its recorded termination.
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

// TestHandleRetry_ForwardsKASsOwnSentence: httpreply.InternalError sends the
// CONSTANT "internal error" and leaves the actionable sentence in the container log,
// so a reader given it cannot tell a refusal from a fault.
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

// TestHandleRetry_AnEngineThatWillNotStartAnswersInsideTheClientsWindow: the browser
// aborts every apiAction at 30s, so the verb's budget must stay below the client's
// or the CLIENT kills an in-flight retry, tearing down the bridge it just minted
// with nobody watching. Inside the window the answer is a 503 the reader can act on.
func TestHandleRetry_AnEngineThatWillNotStartAnswersInsideTheClientsWindow(t *testing.T) {
	if retryTimeout >= clientRequestBudget {
		t.Errorf("retryTimeout = %v, want it below the client's %v request budget, or the "+
			"browser aborts the handoff mid-flight", retryTimeout, clientRequestBudget)
	}

	h, br := seedChatParentedRun(t, false)
	// The utility bridge FIRST: the fake's factory hands out one bridge, so a gate
	// armed before the utility session exists parks the status read instead of the
	// retry's own spawn, and the test hangs rather than failing.
	if _, err := h.runs.rawInspect(t.Context(), "wf_1"); err != nil {
		t.Fatalf("Setup: warming the utility bridge: %s", err)
	}
	// A start that never returns is an engine unpacking a cold runtime tree: the gate
	// is never closed, so Start parks until the verb's own budget expires.
	// Milliseconds rather than the real 25s keep that inside a unit test's budget.
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

// TestHandleRetry_RefusesWhatTheAffordanceRefuses: the gate is server-side, because
// a rule living in one client boolean accepts the request whatever the client drew.
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

	// A gate that re-introduced the parentless-only rule server-side would leave an
	// aborted chat-parented run unreachable in both products.
	t.Run("an aborted CHAT-PARENTED run is accepted", func(t *testing.T) {
		h, _ := seedChatParentedRun(t, true, "phase-c-loop")
		rec := httptest.NewRecorder()
		h.runRoutes.handleRetry(rec, retryReq("wf_1"))
		if rec.Code != http.StatusOK {
			t.Fatalf("retry on an aborted chat-parented run = %d, want %d: %s",
				rec.Code, http.StatusOK, rec.Body.String())
		}
	})

	// The two arms of a status the gate cannot use are DIFFERENT answers.
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
		// The engine answers and has no workflow verb, so rr.status reports "" rather
		// than an error: no status to gate on, not a fault.
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

// TestRetry_AnUnreadableOutcomeIsNotReportedAsAFailedRetry: KAS accepted the verb
// and only its report is unusable, so this is its own class — reporting an ordinary
// failure asks for work that may already be running, and releasing the lease or
// closing the bridge abandons a run that just started. Both branches re-arm it.
func TestRetry_AnUnreadableOutcomeIsNotReportedAsAFailedRetry(t *testing.T) {
	// One reply per shape KAS can answer that carries no usable outcome.
	unreadable := map[string]json.RawMessage{
		"a reply that is not an object": json.RawMessage(`"retried"`),
		"a reply with no outcome":       json.RawMessage(``),
		"a reply about another run":     retryReply(t, "wf_other", "running", "plan"),
	}

	for name, reply := range unreadable {
		t.Run(name+", on the run's own host", func(t *testing.T) {
			// One fake bridge serves the whole runtime, so this is the chat's own.
			h, br := seedChatParentedRun(t, true)
			h.runs.claimTermination("wf_1")
			h.runs.recordEnd("wf_1", runEndOverran)
			br.setCallResult(methodKiroWorkflowRetry, reply)

			_, err := h.runs.Retry(t.Context(), "wf_1", gateAnswer(t, h, "wf_1"))
			if !errors.Is(err, errRetryOutcomeUnreadable) {
				t.Fatalf("Retry = %v, want errRetryOutcomeUnreadable: the verb LANDED, so "+
					"telling the reader to retry would ask for the work twice", err)
			}
			// The run may be executing, so leaving the recorded termination would keep
			// the page saying aborted while the run advanced.
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

// TestHandleRetry_AnUnreadableOutcomeTellsTheReaderToRefresh: the answer must not be
// the 500 that says "this failed, try again".
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

// TestRetry_ReadsTheParentTheGateResolved: the verb consumes the gate's answer
// instead of resolving the host again, because the two reads can DISAGREE — the
// inventory here loses the run's parent session in between, so a verb that re-asks
// re-hosts a run whose own process is alive. Asserted as a mechanism rather than a
// call count: the runtime lists in the background, so round trips are not stable.
func TestRetry_ReadsTheParentTheGateResolved(t *testing.T) {
	h, br := seedChatParentedRun(t, true, "final-verify")
	// Resolved while the inventory still carries the parent session.
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
