package agent

// Tests for the run read handler's own guards.
//
// The passthrough is not tested here — there is nothing to assert about `raw in,
// raw out` that is not a restatement — and the step-session seeding it performs is
// tested where the registry lives (translate.RecordRunSteps), because that is
// where the assertion can be about the observable consequence rather than about a
// map reached through a test-only accessor.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/runlease"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// TestHandleRun_RejectsNonGET pins that the surface is read-only at the method
// level too. Runs have no controls at all (user decision), so a POST here is not
// a missing feature to route somewhere.
func TestHandleRun_RejectsNonGET(t *testing.T) {
	h, _, _ := newTestHub()
	rec := httptest.NewRecorder()
	h.runRoutes.handleRun(rec, httptest.NewRequest(http.MethodPost, "/api/runs/wf_1", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /api/runs/{id} = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandleRun_RejectsAMissingID(t *testing.T) {
	h, _, _ := newTestHub()
	rec := httptest.NewRecorder()
	// No path value set: the route cannot match this, but a hand-built request
	// can, and answering 400 beats calling KAS with an empty id.
	h.runRoutes.handleRun(rec, httptest.NewRequest(http.MethodGet, "/api/runs/", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("GET with no id = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// getLiveRuns serves GET /api/runs/live off rr and decodes the envelope.
func getLiveRuns(t *testing.T, rr *runRoutes) vibekit.LiveRunsResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	rr.handleLiveRuns(rec, httptest.NewRequest(http.MethodGet, "/api/runs/live", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/runs/live = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var out vibekit.LiveRunsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode live-runs reply: %v", err)
	}
	return out
}

func TestHandleLiveRuns_RejectsNonGET(t *testing.T) {
	h, _, _ := newTestHub()
	rec := httptest.NewRecorder()
	h.runRoutes.handleLiveRuns(rec, httptest.NewRequest(http.MethodPost, "/api/runs/live", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /api/runs/live = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

// TestHandleLiveRuns_ProjectsEveryLiveLeaseWithItsChat is the projection's
// contract: a chat-parented run carries the chat its `run_start` frame arrived
// on, a parentless run carries none, and the whole thing is served off
// vibekit-local state — no KAS round trip, no utility-bridge spawn.
func TestHandleLiveRuns_ProjectsEveryLiveLeaseWithItsChat(t *testing.T) {
	h, _, br := newTestHub()
	h.runs.observeStart(t.Context(), "c-live", runNotif(methodWFRunStart, map[string]any{
		"workflowId": "wf_agent", "workflowName": "publish",
	}))
	h.runs.observeStart(t.Context(), "", runNotif(methodWFRunStart, map[string]any{
		"workflowId": "wf_manual", "workflowName": "nightly",
	}))
	calls := len(br.callLog())

	out := getLiveRuns(t, h.runRoutes)

	want := map[string]string{"wf_agent": "c-live", "wf_manual": ""}
	if len(out.Runs) != len(want) {
		t.Fatalf("GET /api/runs/live returned %d rows, want %d: %+v", len(out.Runs), len(want), out.Runs)
	}
	for _, r := range out.Runs {
		wantChat, ok := want[r.WorkflowID]
		if !ok {
			t.Errorf("unexpected row %+v", r)
			continue
		}
		if r.ChatID != wantChat {
			t.Errorf("chat_id for %s = %q, want %q", r.WorkflowID, r.ChatID, wantChat)
		}
	}
	if got := len(br.callLog()); got != calls {
		t.Errorf("the endpoint put %d call(s) on the wire; the projection must be presence-based",
			got-calls)
	}
}

// TestHandleLiveRuns_ATerminalRunLeavesTheProjection: the terminal frame
// releases the lease (forgetBounds), so presence stays the non-terminal claim
// the eviction exemption needs it to be.
func TestHandleLiveRuns_ATerminalRunLeavesTheProjection(t *testing.T) {
	h, _, _ := newTestHub()
	h.runs.observeStart(t.Context(), "c-live", runNotif(methodWFRunStart, map[string]any{
		"workflowId": "wf_agent", "workflowName": "publish",
	}))
	if out := getLiveRuns(t, h.runRoutes); len(out.Runs) != 1 {
		t.Fatalf("the live run is not in the projection: %+v", out.Runs)
	}

	h.runs.observeComplete(t.Context(), "c-live", runNotif(methodWFRunComplete, map[string]any{
		"workflowId": "wf_agent", "status": "completed",
	}))

	if out := getLiveRuns(t, h.runRoutes); len(out.Runs) != 0 {
		t.Errorf("a terminal run is still in the projection, so its chat can never be evicted: %+v",
			out.Runs)
	}
}

// TestHandleLiveRuns_HistoryStaysParentlessOnly pins that the projection is a
// NEW surface, not a change to History: the same chat-parented run appears in
// /api/runs/live (it is live, its chat is exempt) while History's toWire drops
// it (that conversation's work already renders in the chat's transcript).
func TestHandleLiveRuns_HistoryStaysParentlessOnly(t *testing.T) {
	h, _, _ := newTestHub()
	h.runs.observeStart(t.Context(), "c-live", runNotif(methodWFRunStart, map[string]any{
		"workflowId": "wf_agent", "workflowName": "publish",
	}))

	if out := getLiveRuns(t, h.runRoutes); len(out.Runs) != 1 || out.Runs[0].ChatID != "c-live" {
		t.Fatalf("the chat-parented run is not projected with its chat: %+v", out.Runs)
	}

	rows := h.runs.toWire(
		map[string]vibekit.ChatID{"sess-1": "c-live"},
		[]kasWorkflowRun{
			{WorkflowID: "wf_agent", Name: "publish", Status: "running", ParentSessionID: "sess-1"},
			{WorkflowID: "wf_manual", Name: "nightly", Status: "completed"},
		},
	)
	for i := range rows {
		if rows[i].WorkflowID == "wf_agent" {
			t.Errorf("History listed a chat-parented run; the live-runs projection must not "+
				"have widened it: %+v", rows[i])
		}
	}
	if len(rows) != 1 || rows[0].WorkflowID != "wf_manual" {
		t.Errorf("History dropped the parentless run it exists to list: %+v", rows)
	}
}

// TestHandleLiveRuns_ServesPersistedLeasesAcrossARestart is the reload story:
// a chat-parented run survives a vibekit restart inside KAS, its lease
// survives in runs.json, and the projection must serve it from those persisted
// bytes with ZERO frames observed — a paused run emits nothing until it is
// resumed, and the client needs the row to paint its dot and to nest its tab.
//
// It is served NOT-EXECUTING, and that is a deliberate change of contract from the
// one this test used to assert ("its chat must be exempt from eviction the whole
// time"). NewStore parks every loaded deadline, because a deadline read back from
// disk is a fact about a process that no longer exists — and that is the honest
// answer to the eviction question too: the bridge carrying this run's frames died
// with that process, so nothing is being written into the chat's window.
func TestHandleLiveRuns_ServesPersistedLeasesAcrossARestart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	before, err := runlease.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := before.Put(t.Context(), &runlease.Lease{
		WorkflowID: "wf_agent", Recipe: "publish", ChatID: "c-live", Origin: runlease.OriginAgent,
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// The restart: a fresh store over the same directory, wired into a fresh
	// run surface. Nothing has replayed a frame.
	reopened, err := runlease.NewStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	out := getLiveRuns(t, &runRoutes{runs: &Runs{leases: reopened}})

	if len(out.Runs) != 1 || out.Runs[0].WorkflowID != "wf_agent" || out.Runs[0].ChatID != "c-live" {
		t.Fatalf("the projection after a restart = %+v, want the persisted lease with its chat",
			out.Runs)
	}
	if out.Runs[0].Executing {
		t.Error("a restart-surviving lease reports executing; NewStore parks every loaded " +
			"deadline, and the bridge that carried this run's frames died with the process")
	}
}

// TestHandleLiveRuns_ExecutingFollowsTheLeasesOwnClock is the Stage 2 wire field,
// and it is the lease's own knowledge rather than a KAS status: armed on every start
// and resume, parked on every pause, because the bound is on EXECUTING time.
//
// The pause half is what the narrowing exists for. A run parked on a question writes
// nothing into its launching chat's transcript and can sit there for hours, so the
// client's eviction exemption must lapse while the ROW survives — the dot painter and
// the tab-parent resolver still need it.
func TestHandleLiveRuns_ExecutingFollowsTheLeasesOwnClock(t *testing.T) {
	h, _, _ := newTestHub()
	h.runs.observeStart(t.Context(), "c-live", runNotif(methodWFRunStart, map[string]any{
		"workflowId": "wf_agent", "workflowName": "publish",
	}))

	out := getLiveRuns(t, h.runRoutes)
	if len(out.Runs) != 1 || !out.Runs[0].Executing {
		t.Fatalf("a run that just started is not projected as executing: %+v", out.Runs)
	}

	// The run-level pause frame, which is what parks the deadline.
	h.runs.observePaused(func(context.Context, vibekit.ChatID, *vibekit.RPCResponse) {})(
		t.Context(), "c-live", runNotif(methodWFPaused, map[string]any{"workflowId": "wf_agent"}),
	)

	out = getLiveRuns(t, h.runRoutes)
	if len(out.Runs) != 1 {
		t.Fatalf("a paused run left the projection, so its tab loses its dot and its parent: %+v",
			out.Runs)
	}
	if out.Runs[0].Executing {
		t.Error("a parked run still reports executing, so its chat's whole message window " +
			"stays pinned for the life of the page")
	}
}

// TestHandleLiveRuns_APreUpgradeLeaseRowProjectsWithNoChat: a version-1 file
// written before Lease.ChatID existed still loads (the field is additive), and
// its rows project with an empty chat_id — "no chat to exempt", exactly what
// the parentless launch verbs mint.
func TestHandleLiveRuns_APreUpgradeLeaseRowProjectsWithNoChat(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	body := `{"version":1,"leases":[{"started_at":"2026-08-01T03:00:00Z",` +
		`"workflow_id":"wf_old","recipe":"nightly","origin":"scheduled","unattended":true}]}`
	if err := os.WriteFile(filepath.Join(dir, runlease.FileName), []byte(body), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	st, err := runlease.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore over a pre-upgrade file: %v", err)
	}

	out := getLiveRuns(t, &runRoutes{runs: &Runs{leases: st}})

	if len(out.Runs) != 1 || out.Runs[0].WorkflowID != "wf_old" {
		t.Fatalf("the pre-upgrade lease is not projected: %+v", out.Runs)
	}
	if out.Runs[0].ChatID != "" {
		t.Errorf("chat_id = %q for a pre-upgrade row, want empty (no chat to exempt)",
			out.Runs[0].ChatID)
	}
}

// answerReq builds a POST /api/runs/{id}/answer request with its path value set,
// which the handler reads rather than parsing the URL.
func answerReq(t *testing.T, id, askID, text string) *http.Request {
	t.Helper()
	body, err := json.Marshal(vibekit.RunAnswerRequest{AskID: askID, Text: text})
	if err != nil {
		t.Fatalf("Setup: marshalling the answer body: %s", err)
	}
	req := httptest.NewRequest(http.MethodPost,
		"/api/runs/"+id+"/answer", bytes.NewReader(body))
	req.SetPathValue("id", id)
	return req
}

// TestHandleAnswer pins the endpoint's own guards and the ONE status that is not
// a fault: a 409 means another surface answered first or the step moved on, which
// is a state of the world rather than something the reader can redo.
func TestHandleAnswer(t *testing.T) {
	t.Run("it refuses a non-POST", func(t *testing.T) {
		h, _, _ := newTestHub()
		rec := httptest.NewRecorder()
		h.runRoutes.handleAnswer(rec, httptest.NewRequest(http.MethodGet, "/api/runs/wf_1/answer", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
		}
	})

	t.Run("it refuses a missing id", func(t *testing.T) {
		h, _, _ := newTestHub()
		rec := httptest.NewRecorder()
		h.runRoutes.handleAnswer(rec, httptest.NewRequest(http.MethodPost, "/api/runs//answer", nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("no id = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("an empty answer is a 400", func(t *testing.T) {
		h, _, _ := newTestHub()
		rec := httptest.NewRecorder()
		h.runRoutes.handleAnswer(rec, answerReq(t, "wf_1", "a1", "  "))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("an empty answer = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("an unknown ask is a 409 naming the situation", func(t *testing.T) {
		h, _, _ := newTestHub()
		rec := httptest.NewRecorder()
		h.runRoutes.handleAnswer(rec, answerReq(t, "wf_1", "a1", "the main branch"))
		if rec.Code != http.StatusConflict {
			t.Fatalf("an unknown ask = %d, want %d: %s", rec.Code, http.StatusConflict, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "already been answered") {
			t.Errorf("the 409 body = %s, want it to name the situation", rec.Body.String())
		}
	})

	// The second 409 cause, and the client tells the two apart by the server's own
	// sentence alone: this one is RETRYABLE and the card is already back, so it must
	// not reach the 400 arm that tells the reader they asked wrongly.
	t.Run("a run between steps is a 409 that says to retry", func(t *testing.T) {
		h, _, br := newTestHub()
		br.callResults = map[string]json.RawMessage{
			methodKiroWorkflowList:    json.RawMessage(`{"runs":[]}`),
			methodKiroWorkflowInspect: inspectReply(t, "wf_1", "running", ""),
		}
		h.runs.asks.Add(&runAsk{
			chatID: "run:wf_1",
			payload: vibekit.RunInputNeededPayload{
				WorkflowID: "wf_1", AskID: "a1", NodeID: "review", StepSessionID: "sess_step",
			},
		})

		rec := httptest.NewRecorder()
		h.runRoutes.handleAnswer(rec, answerReq(t, "wf_1", "a1", "the main branch"))

		if rec.Code != http.StatusConflict {
			t.Fatalf("a run between steps = %d, want %d: %s",
				rec.Code, http.StatusConflict, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "try again") {
			t.Errorf("the 409 body = %s, want the retry sentence: the client shows the "+
				"server's sentence alone, so this is the only place it can say so",
				rec.Body.String())
		}
	})

	// A run with no bridge answers 200, and the route's own 409 for it is GONE. A
	// park now drops the process, so "nothing hosts this run" is the ordinary state
	// of every run an ask is raised on; refusing there made the card the reader was
	// shown unanswerable, which is the state forgetBounds calls worse than no card.
	t.Run("a run with no bridge is re-hosted and answers 200", func(t *testing.T) {
		h, _, _ := newTestHub()
		h.runs.asks.Add(&runAsk{
			chatID: "run:wf_1",
			payload: vibekit.RunInputNeededPayload{
				WorkflowID: "wf_1", AskID: "a1", StepSessionID: "sess_step",
			},
		})
		rec := httptest.NewRecorder()
		h.runRoutes.handleAnswer(rec, answerReq(t, "wf_1", "a1", "the main branch"))
		if rec.Code != http.StatusOK {
			t.Errorf("an unhosted run = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
		}
	})

	// handleStepStatus's arm, on the route that shares its shape: with the re-host in
	// place a failed SPAWN reaches this handler, and the default arm reads it as the
	// caller's mistake and echoes an internal path back to them.
	t.Run("a failed spawn answers a generic 500", func(t *testing.T) {
		h, _, br := newTestHub()
		br.callResults = map[string]json.RawMessage{
			methodKiroWorkflowList: json.RawMessage(`{"runs":[]}`),
		}
		if _, err := h.runs.listRaw(t.Context()); err != nil {
			t.Fatalf("Setup: warming the utility session: %s", err)
		}
		br.startErr = errors.New("fork/exec: no such file or directory")
		h.runs.asks.Add(&runAsk{
			chatID: "run:wf_1",
			payload: vibekit.RunInputNeededPayload{
				WorkflowID: "wf_1", AskID: "a1", StepSessionID: "sess_step",
			},
		})

		rec := httptest.NewRecorder()
		h.runRoutes.handleAnswer(rec, answerReq(t, "wf_1", "a1", "the main branch"))

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("a failed spawn = %d, want %d: %s",
				rec.Code, http.StatusInternalServerError, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "fork/exec") {
			t.Errorf("the body = %s, want a generic sentinel", rec.Body.String())
		}
		// The other benefit of hosting BEFORE the claim: a spawn that never reached
		// KAS leaves the card on every surface rather than consuming it.
		if !h.runs.asks.HasRun("wf_1") {
			t.Error("the ask was consumed by a failure that never reached KAS, so the card " +
				"is gone from every surface with the question still open")
		}
	})

	t.Run("the happy path answers 200", func(t *testing.T) {
		h, _, br := newTestHub()
		h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})
		h.runs.asks.Add(&runAsk{
			chatID: "run:wf_1",
			payload: vibekit.RunInputNeededPayload{
				WorkflowID: "wf_1", AskID: "a1", StepSessionID: "sess_step",
			},
		})
		rec := httptest.NewRecorder()
		h.runRoutes.handleAnswer(rec, answerReq(t, "wf_1", "a1", "the main branch"))
		if rec.Code != http.StatusOK {
			t.Fatalf("the happy path = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
		}
	})
}

// pauseReq builds the request the pause route takes.
func pauseReq(id string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/runs/"+id+"/pause", nil)
	req.SetPathValue("id", id)
	return req
}

// TestControlHandler_ClassifiesAKASRefusalApartFromAStartFailure. The re-host made
// KAS's own refusal the answer a reader gets most often here, and a failed spawn is
// the other arm; which status each earns and why is in vibekit-runtime.md's
// liveness-split block.
func TestControlHandler_ClassifiesAKASRefusalApartFromAStartFailure(t *testing.T) {
	// `from` gates pause on a RUNNING run, which is the population the re-host is
	// reached for: KAS says running, this process holds nothing.
	running := func(t *testing.T, br *fakeBridge) {
		t.Helper()
		br.callResults = map[string]json.RawMessage{
			methodKiroWorkflowList:    json.RawMessage(`{"runs":[]}`),
			methodKiroWorkflowInspect: inspectReply(t, "wf_1", "running", ""),
		}
	}

	t.Run("KAS's own refusal answers 409 carrying its reason", func(t *testing.T) {
		h, _, br := newTestHub()
		running(t, br)
		// The shape KAS actually refuses in: -32603 with the reason in `error.data`,
		// which is why the client is handed rpcerr.Text rather than error.Message.
		br.callRPCErrs = map[string]*vibekit.RPCError{
			methodKiroWorkflowPause: {
				Code:    -32603,
				Message: "Internal error",
				Data:    json.RawMessage(`{"details":"Workflow 'wf_1' is not registered"}`),
			},
		}

		rec := httptest.NewRecorder()
		h.runRoutes.handlePause(rec, pauseReq("wf_1"))

		if rec.Code != http.StatusConflict {
			t.Fatalf("a refused pause = %d, want %d: %s",
				rec.Code, http.StatusConflict, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "not registered") {
			t.Errorf("the body = %s, want KAS's own reason; without it the reader is told "+
				"only that the verb failed and the reason reaches the log alone",
				rec.Body.String())
		}
	})

	t.Run("a failed spawn answers a generic 500", func(t *testing.T) {
		h, _, br := newTestHub()
		running(t, br)
		// Armed AFTER the status read, which needs the utility session, so what fails
		// is the re-host and not the gate.
		if _, err := h.runRoutes.status(t.Context(), "wf_1"); err != nil {
			t.Fatalf("Setup: the status gate could not read the run: %s", err)
		}
		br.startErr = errors.New("fork/exec: no such file or directory")

		rec := httptest.NewRecorder()
		h.runRoutes.handlePause(rec, pauseReq("wf_1"))

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("a failed spawn = %d, want %d: %s",
				rec.Code, http.StatusInternalServerError, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "fork/exec") {
			t.Errorf("the body = %s, want a generic sentinel: a spawn failure reads as the "+
				"caller's fault and echoes an internal path", rec.Body.String())
		}
	})

	// The arm ORDER is what this case pins, and a plain startErr cannot: KAS
	// refusing the re-host's own session door puts an *RPCError UNDER
	// errRunHostStart, so a handler testing the type first reports a failed spawn as
	// a state of the run and hands the reader KAS's session-door text.
	t.Run("a spawn KAS refused is still a 500, not its refusal", func(t *testing.T) {
		h, _, br := newTestHub()
		running(t, br)
		if _, err := h.runRoutes.status(t.Context(), "wf_1"); err != nil {
			t.Fatalf("Setup: the status gate could not read the run: %s", err)
		}
		// The exact nesting a refused handshake produces: bridge_rpc.go wraps the
		// JSON-RPC error object, bridge_session.go wraps that again, and rehost puts
		// errRunHostStart over the pair.
		br.startErr = fmt.Errorf("session/new: %w",
			fmt.Errorf("ACP error %d: %w", -32000, &vibekit.RPCError{
				Code:    -32000,
				Message: "unknown security preset 'read-workspace'",
			}))

		rec := httptest.NewRecorder()
		h.runRoutes.handlePause(rec, pauseReq("wf_1"))

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("a spawn KAS refused = %d, want %d: a start failure is this "+
				"server's fault whoever refused it, and 409 says the RUN is in a state "+
				"the reader can act on: %s",
				rec.Code, http.StatusInternalServerError, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "unknown security preset") {
			t.Errorf("the body = %s, want a generic sentinel: KAS's session-door text "+
				"describes vibekit's spawn, not the run the reader asked about",
				rec.Body.String())
		}
	})
}

// TestHandleStepStatus_SplitsAValidationRefusalFromAStartFailure.
//
// The re-host put a SERVER fault on a path that had only ever carried a caller's own
// mistake, so `errRunHostStart` reached the 400 arm with `run bridge start: …` as its
// body. 400 for a KAS refusal here is unchanged and deliberate.
func TestHandleStepStatus_SplitsAValidationRefusalFromAStartFailure(t *testing.T) {
	post := func(t *testing.T, h *Runtime, nodeID, status string) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(map[string]string{"node_id": nodeID, "status": status})
		if err != nil {
			t.Fatalf("Setup: marshalling the step-status body: %s", err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/runs/wf_1/step", bytes.NewReader(body))
		req.SetPathValue("id", "wf_1")
		rec := httptest.NewRecorder()
		h.runRoutes.handleStepStatus(rec, req)
		return rec
	}

	t.Run("an unknown status is a 400 naming the allowlist", func(t *testing.T) {
		h, _, _ := newTestHub()
		rec := post(t, h, "review", "paused")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("an unknown status = %d, want %d: %s",
				rec.Code, http.StatusBadRequest, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), runStepCompleted) {
			t.Errorf("the body = %s, want the accepted statuses", rec.Body.String())
		}
	})

	t.Run("a failed spawn answers a generic 500", func(t *testing.T) {
		h, _, br := newTestHub()
		// The pre-send target read has to LAND, or the handler answers its own 409 and
		// the spawn failure this case is about is never reached.
		addressableStep(t, h, br, "wf_1", "review")
		br.startErr = errors.New("fork/exec: no such file or directory")

		rec := post(t, h, "review", runStepCompleted)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("a failed spawn = %d, want %d: %s",
				rec.Code, http.StatusInternalServerError, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "fork/exec") {
			t.Errorf("the body = %s, want a generic sentinel", rec.Body.String())
		}
	})
}

// stepTargetInspect is the `inspect` reply SetStepStatus's pre-send read needs: one
// run parked at ONE step, so statusUpdateTarget resolves to nodeID and the write goes.
//
// Every node carries `type`, because KAS's own resolver considers `type: "step"` alone
// and its tree builder stamps the field on every node it makes — a fixture that omits
// it describes a wire KAS does not send, which is the trap vibekit-acp.md records for
// the step-metering gate.
func stepTargetInspect(t *testing.T, workflowID, nodeID string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"workflowId": workflowID,
		"state": map[string]any{
			"status": runStatusPaused,
			"root": map[string]any{
				"nodeId": "root", "type": "sequence", "status": runStatusPaused,
				"children": []any{map[string]any{
					"nodeId": nodeID, "type": stepNodeType, "status": runStatusPaused,
				}},
			},
		},
	})
	if err != nil {
		t.Fatalf("Setup: marshalling the inspect reply: %s", err)
	}
	return raw
}

// addressableStep points the fake's `inspect` at a run parked on nodeID and warms the
// utility session, so a SetStepStatus case exercises the arm it is about rather than
// the pre-send read's own refusal.
func addressableStep(t *testing.T, h *Runtime, br *fakeBridge, workflowID, nodeID string) {
	t.Helper()
	br.setCallResult(methodKiroWorkflowInspect, stepTargetInspect(t, workflowID, nodeID))
	if _, err := h.runs.rawInspect(t.Context(), workflowID); err != nil {
		t.Fatalf("Setup: warming the utility session: %s", err)
	}
}

// TestSetStepStatus pins the allowlist and the bridge resolution.
func TestSetStepStatus(t *testing.T) {
	t.Run("it accepts running as the continue-without-answering verb", func(t *testing.T) {
		h, _, br := newTestHub()
		addressableStep(t, h, br, "wf_1", "review")
		h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})
		h.runs.asks.Add(&runAsk{
			chatID: "run:wf_1",
			payload: vibekit.RunInputNeededPayload{
				WorkflowID: "wf_1", AskID: "a1", NodeID: "review",
			},
		})

		if err := h.runs.SetStepStatus(t.Context(), "wf_1", "review", runStepRunning); err != nil {
			t.Fatalf("SetStepStatus(running) = %v, want nil", err)
		}
		// `running` clears KAS's completionSignal and re-drives the step with its
		// DEFAULT continuation, so whatever it asked is no longer answerable and
		// every surface still showing that card has to be told.
		if h.runs.asks.HasRun("wf_1") {
			t.Error("continuing the step left its question live")
		}
		settled := settledPayloads(t, h)
		if len(settled) != 1 {
			t.Fatalf("run_input_settled events = %d, want 1", len(settled))
		}
		// SettledByUser HERE, unlike the node-completion door: this verb is only ever
		// reached by a reader clicking Continue-without-answering, so it IS their
		// decision and another window is correctly told a person made it.
		if got := settled[0]["settled_by"]; got != string(vibekit.SettledByUser) {
			t.Errorf("settled_by = %q, want %q", got, vibekit.SettledByUser)
		}
	})

	t.Run("an unknown status is still refused", func(t *testing.T) {
		h, _, br := newTestHub()
		h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})
		if err := h.runs.SetStepStatus(t.Context(), "wf_1", "review", "paused"); err == nil {
			t.Error("SetStepStatus(paused) = nil, want a refusal")
		}
	})

	t.Run("a chat-parented run resolves the launching chat's bridge", func(t *testing.T) {
		h, cs, br := newTestHub()
		// An AGENT-launched run has no bridge of its own: KAS parents it on the
		// calling chat's session. Resolving that chat's bridge is what keeps the verb
		// off a needless re-host for the whole population an agent creates.
		cs.Chats["c1"] = &vibekit.Chat{ID: "c1", ACPSessionID: "sess_parent"}
		h.bridge.mgr.insert("c1", &sharedBridge{bridge: br, state: bridgeIdle})
		br.callResults = map[string]json.RawMessage{
			methodKiroWorkflowList: json.RawMessage(
				`{"runs":[{"workflowId":"wf_1","name":"publish","status":"paused","parentSessionId":"sess_parent"}]}`,
			),
			methodKiroWorkflowInspect: stepTargetInspect(t, "wf_1", "review"),
		}

		if err := h.runs.SetStepStatus(t.Context(), "wf_1", "review", runStepCompleted); err != nil {
			t.Errorf("SetStepStatus on an agent-launched run = %v, want nil", err)
		}
	})
}

// TestSetStepStatus_WithholdsAMistargetedWrite pins the guard against KAS's own target
// resolver: the verb carries no node id, so a client naming node X can have KAS mark
// node Y, and this is the read that refuses instead of aiming a write positionally.
//
// The parallel case is the one this exists for and it is the shape item B's ask card
// now produces: two paused branches with the SIGNAL-bearing one second, where the
// resolver takes the FIRST. `completed` there is not cosmetic — KAS publishes that
// node's capture and stamps it finished.
func TestSetStepStatus_WithholdsAMistargetedWrite(t *testing.T) {
	// Two paused branches under one parallel node, `plan` carrying the need-input
	// signal and `verify` first in document order. `type` on every node, because the
	// resolver considers `type: "step"` alone — without it the paused PARALLEL is what
	// a naive walk would name.
	branched := func(t *testing.T) json.RawMessage {
		t.Helper()
		raw, err := json.Marshal(map[string]any{
			"workflowId": "wf_1",
			"state": map[string]any{
				"status": runStatusPaused,
				"root": map[string]any{
					"nodeId": "fan", "type": "parallel", "status": runStatusPaused,
					"children": []any{
						map[string]any{
							"nodeId": "verify", "type": stepNodeType, "status": runStatusPaused,
						},
						map[string]any{
							"nodeId": "plan", "type": stepNodeType, "status": runStatusPaused,
							"completionSignal": needInputSignal,
						},
					},
				},
			},
		})
		if err != nil {
			t.Fatalf("Setup: marshalling the inspect reply: %s", err)
		}
		return raw
	}

	cases := []struct {
		name string
		// reply is the state tree the fake answers with; nil when readErr drives the
		// case instead, since those are two different arms of stepStatusAddress.
		reply   func(*testing.T) json.RawMessage
		readErr error
		nodeID  string
		wantErr error
	}{{
		// The whole defect: the ask card names the signal-bearing branch and KAS
		// would mark the first paused one instead.
		name:    "the signal-bearing branch is not the node KAS would mark",
		reply:   branched,
		nodeID:  "plan",
		wantErr: errStepStatusMistargeted,
	}, {
		// The other side of the same tree: naming the node the resolver DOES pick
		// must still go through, or the guard would refuse every parallel run.
		name:   "the node KAS would mark is sent",
		reply:  branched,
		nodeID: "verify",
	}, {
		name: "a run with no running or paused step is withheld",
		reply: func(t *testing.T) json.RawMessage {
			t.Helper()
			return json.RawMessage(`{"workflowId":"wf_1","state":{"status":"completed",` +
				`"root":{"nodeId":"root","type":"sequence","status":"completed",` +
				`"children":[{"nodeId":"review","type":"step","status":"completed"}]}}}`)
		},
		nodeID:  "review",
		wantErr: errStepStatusMistargeted,
	}, {
		// A RUNNING step outranks every paused one in KAS's resolver, so a reader
		// marking a parked branch of a run that has moved on is refused.
		name: "a running step outranks the parked node being named",
		reply: func(t *testing.T) json.RawMessage {
			t.Helper()
			return json.RawMessage(`{"workflowId":"wf_1","state":{"status":"running",` +
				`"root":{"nodeId":"root","type":"sequence","status":"running","children":[` +
				`{"nodeId":"plan","type":"step","status":"paused"},` +
				`{"nodeId":"build","type":"step","status":"running"}]}}}`)
		},
		nodeID:  "plan",
		wantErr: errStepStatusMistargeted,
	}, {
		// FAIL CLOSED on an unreadable run, the opposite of answerAddress's own
		// fallback: a failed read there costs a prompt, here it would cost a write
		// nothing undoes.
		name: "an undecodable state is withheld rather than sent",
		reply: func(t *testing.T) json.RawMessage {
			t.Helper()
			return json.RawMessage(`{"workflowId":"wf_1"}`)
		},
		nodeID:  "review",
		wantErr: errStepStatusUnreadable,
	}, {
		// The OTHER unreadable shape, and a separate arm in the code: the inspect
		// CALL fails rather than answering something that will not decode. Both have
		// to withhold, or the fail-closed direction holds for one of them only.
		name:    "a failed read is withheld rather than sent",
		readErr: errors.New("bridge exited"),
		nodeID:  "review",
		wantErr: errStepStatusUnreadable,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _, br := newTestHub()
			if tc.readErr != nil {
				br.callErrs = map[string]error{methodKiroWorkflowInspect: tc.readErr}
			} else {
				br.setCallResult(methodKiroWorkflowInspect, tc.reply(t))
			}
			h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})

			err := h.runs.SetStepStatus(t.Context(), "wf_1", tc.nodeID, runStepCompleted)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("SetStepStatus(%q) = %v, want nil", tc.nodeID, err)
				}
				if br.paramsFor(methodKiroWorkflowUpdate) == nil {
					t.Errorf("no %s call, calls were %v: the addressable node must still "+
						"be sent, or the guard refuses every parallel run",
						methodKiroWorkflowUpdate, br.callLog())
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("SetStepStatus(%q) = %v, want %v", tc.nodeID, err, tc.wantErr)
			}
			// A state-of-the-world refusal, so the REST layer answers 409 rather than
			// telling the reader they asked wrongly.
			if !errors.Is(err, errStepStatusRefused) {
				t.Errorf("SetStepStatus(%q) = %v, want it to wrap errStepStatusRefused",
					tc.nodeID, err)
			}
			if params := br.paramsFor(methodKiroWorkflowUpdate); params != nil {
				t.Errorf("%s was called with %v, want no call at all: the write is "+
					"WITHHELD, and KAS marking the wrong node is what that prevents",
					methodKiroWorkflowUpdate, params)
			}
		})
	}
}

// TestSetStepStatus_ParamsAreFlat pins the shape `_kiro/workflow/update` accepts,
// against the schema the pinned KAS bundle declares for it — `action` required and
// enumerated, `status` enumerated, both read at the TOP level with no `update` object
// and no node id anywhere (quoted in vibekit-acp.md).
//
// Both halves are asserted: the keys that must be there, and the two that must NOT,
// because either one reappearing is the nested shape returning — and that shape fell
// through to `if (r.status === void 0) throw` on every call.
func TestSetStepStatus_ParamsAreFlat(t *testing.T) {
	h, _, br := newTestHub()
	addressableStep(t, h, br, "wf_1", "review")
	h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})

	if err := h.runs.SetStepStatus(t.Context(), "wf_1", "review", runStepCompleted); err != nil {
		t.Fatalf("SetStepStatus = %v, want nil", err)
	}
	params := br.paramsFor(methodKiroWorkflowUpdate)
	if params == nil {
		t.Fatalf("no %s call, calls were %v", methodKiroWorkflowUpdate, br.callLog())
	}
	if got := params[keyWorkflowID]; got != "wf_1" {
		t.Errorf("params[%q] = %v, want wf_1", keyWorkflowID, got)
	}
	if got := params["action"]; got != "update_status" {
		t.Errorf(`params["action"] = %v, want update_status: the schema declares it `+
			`required, so an absent value is a shape the verb accepts only by accident`, got)
	}
	if got := params["status"]; got != runStepCompleted {
		t.Errorf(`params["status"] = %v, want %q at the TOP level`, got, runStepCompleted)
	}
	if got, ok := params["update"]; ok {
		t.Errorf(`params["update"] = %v, want absent: nesting the fields is what made `+
			`the verb throw on "`+"`status` is required for action `update_status`"+`"`, got)
	}
	if got, ok := params["nodeId"]; ok {
		t.Errorf(`params["nodeId"] = %v, want absent: the verb takes no node id — it `+
			`targets the run's own current running-or-paused step`, got)
	}
}

// TestSetStepStatus_ReadsTheReply pins the half a param fix alone would miss: KAS
// DECLINES with a 200 rather than throwing when the run is already terminal or holds
// no step to mark, so a caller that ignored the result would report the write as
// landed and leave the reader clicking a control that changed nothing.
func TestSetStepStatus_ReadsTheReply(t *testing.T) {
	// The three replies KAS produces, plus the shape a build that stops sending
	// `updated` would produce. `updated` is a *bool for that last row: absent is NO
	// CLAIM and must read as taken, because an unstated field making a working verb
	// report a refusal is the worse direction.
	cases := []struct {
		name    string
		reply   string
		wantErr bool
		wantMsg string
	}{
		{
			name:  "a taken update",
			reply: `{"workflowId":"wf_1","updated":true,"queued":false,"message":"Step marked completed; the workflow will advance."}`,
		},
		{
			// applyStatusUpdate's queued arm: the signal is recorded and lands at the
			// current turn's end, so the update was taken.
			name:  "a queued update",
			reply: `{"workflowId":"wf_1","updated":true,"queued":true,"message":"Marked completed; the step finalizes when its current turn ends."}`,
		},
		{
			name:    "a terminal run is declined",
			reply:   `{"workflowId":"wf_1","updated":false,"queued":false,"message":"Cannot update step status: the workflow has already finished (status: completed)."}`,
			wantErr: true,
			wantMsg: "the workflow has already finished",
		},
		{
			name:    "a run with no current step is declined",
			reply:   `{"workflowId":"wf_1","updated":false,"queued":false,"message":"No current step to update: the workflow has no running or paused step."}`,
			wantErr: true,
			wantMsg: "no running or paused step",
		},
		{
			name:  "a reply that states nothing reads as taken",
			reply: `{"workflowId":"wf_1"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _, br := newTestHub()
			h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})
			br.callResults = map[string]json.RawMessage{
				methodKiroWorkflowUpdate:  json.RawMessage(tc.reply),
				methodKiroWorkflowInspect: stepTargetInspect(t, "wf_1", "review"),
			}

			err := h.runs.SetStepStatus(t.Context(), "wf_1", "review", runStepCompleted)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("SetStepStatus = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, errStepStatusRefused) {
				t.Fatalf("SetStepStatus = %v, want errStepStatusRefused", err)
			}
			// KAS's own sentence has to reach the reader: it names which of the two
			// declines happened, and nothing this server knows can reconstruct it.
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error = %q, want it to carry %q", err, tc.wantMsg)
			}
		})
	}
}

// TestHandleStepStatus_DeclineIsAConflict pins the REST arm the refusal earns. A
// state of the world answered 400 tells the reader they asked wrongly, which is the
// misattribution the answer route's own errRunNotParked arm already avoids.
func TestHandleStepStatus_DeclineIsAConflict(t *testing.T) {
	h, _, br := newTestHub()
	h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowUpdate: json.RawMessage(
			`{"workflowId":"wf_1","updated":false,"queued":false,` +
				`"message":"No current step to update: the workflow has no running or paused step."}`,
		),
		methodKiroWorkflowInspect: stepTargetInspect(t, "wf_1", "review"),
	}
	rr := &runRoutes{runs: h.runs}
	req := httptest.NewRequest(http.MethodPost, "/api/runs/wf_1/step",
		strings.NewReader(`{"node_id":"review","status":"completed"}`))
	req.SetPathValue("id", "wf_1")
	rec := httptest.NewRecorder()

	rr.handleStepStatus(rec, req)

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
	if !strings.Contains(rec.Body.String(), "no running or paused step") {
		t.Errorf("body = %s, want KAS's own reason", rec.Body.String())
	}
}
