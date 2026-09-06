package agent

// Tests for the run read handler's own guards. The passthrough is not tested here —
// nothing about `raw in, raw out` is assertable without restating it — and its
// step-session seeding is tested where the registry lives (translate.RecordRunSteps).

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/runlease"
	"github.com/cplieger/vibekit/internal/vibekit"
	"github.com/cplieger/vibekit/internal/workflow"
)

// TestHandleRun_RejectsNonGET pins that the surface is read-only at the method level too:
// runs have no controls, so a POST here is not a missing feature to route somewhere.
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
	// The route cannot match this, but a hand-built request can: 400 beats calling KAS.
	h.runRoutes.handleRun(rec, httptest.NewRequest(http.MethodGet, "/api/runs/", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("GET with no id = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

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

// TestHandleLiveRuns_ProjectsEveryLiveLeaseWithItsChat is the projection's contract: a
// chat-parented run carries the chat its `run_start` frame arrived on, a parentless run
// carries none, and it is served off vibekit-local state — no KAS round trip.
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

// TestHandleLiveRuns_ATerminalRunLeavesTheProjection: the terminal frame releases the
// lease, so presence stays the non-terminal claim the eviction exemption needs.
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

// Both surfaces carry a chat-parented run, and what separates them is the answer each is
// FOR: /api/runs/live projects the run with the chat it belongs to, because its consumer
// is that chat's eviction exemption, while History attributes the run to the chat so the
// row's door can nest the run's tab under it.
func TestHandleLiveRuns_AndHistoryBothCarryAChatParentedRun(t *testing.T) {
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
	if len(rows) != 2 {
		t.Fatalf("History listed %d rows, want both runs: %+v", len(rows), rows)
	}
	byID := map[string]string{}
	for i := range rows {
		byID[rows[i].WorkflowID] = rows[i].ParentChatID
	}
	if got, ok := byID["wf_agent"]; !ok || got != "c-live" {
		t.Errorf("History's chat-parented row carries parent_chat_id %q (present=%v), want %q: "+
			"the row's door nests the run's tab under that chat", got, ok, "c-live")
	}
	if got, ok := byID["wf_manual"]; !ok || got != "" {
		t.Errorf("History's parentless row carries parent_chat_id %q (present=%v), want empty",
			got, ok)
	}
}

// TestHandleLiveRuns_ServesPersistedLeasesAcrossARestart is the reload story: the lease
// survives in runs.json and the projection must serve it from those persisted bytes with
// ZERO frames observed — a paused run emits nothing, and its chat stays exempt.
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

	// The restart: a fresh store over the same directory. Nothing has replayed a frame.
	reopened, err := runlease.NewStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	out := getLiveRuns(t, &runRoutes{runs: &Runs{leases: reopened}})

	if len(out.Runs) != 1 || out.Runs[0].WorkflowID != "wf_agent" || out.Runs[0].ChatID != "c-live" {
		t.Errorf("the projection after a restart = %+v, want the persisted lease with its chat",
			out.Runs)
	}
}

// TestHandleLiveRuns_APreUpgradeLeaseRowProjectsWithNoChat: a version-1 file written
// before Lease.ChatID existed still loads (the field is additive), and its rows project
// with an empty chat_id — "no chat to exempt".
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

// TestHandleControls serves the affordance route and pins its envelope. The endpoint
// exists because the CLIENT cannot answer the question: it decided the control row from a
// map written only by SSE frames, so a reloaded client read every run as parentless.
func TestHandleControls(t *testing.T) {
	controlsReq := func(id string) *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/api/runs/"+id+"/controls", nil)
		req.SetPathValue("id", id)
		return req
	}

	t.Run("it refuses a non-GET", func(t *testing.T) {
		h, _ := seedChatParentedRun(t, true)
		rec := httptest.NewRecorder()
		h.runRoutes.handleControls(rec, httptest.NewRequest(http.MethodPost, "/api/runs/wf_1/controls", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
		}
	})

	t.Run("it refuses a missing id", func(t *testing.T) {
		h, _ := seedChatParentedRun(t, true)
		rec := httptest.NewRecorder()
		h.runRoutes.handleControls(rec, httptest.NewRequest(http.MethodGet, "/api/runs//controls", nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("no id = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	// Retry is offered and the parent chat travels with it, because the run page's
	// step-transcript note asks the same question.
	t.Run("an aborted chat-parented run offers retry and names its parent chat", func(t *testing.T) {
		h, _ := seedChatParentedRun(t, true)
		rec := httptest.NewRecorder()
		h.runRoutes.handleControls(rec, controlsReq("wf_1"))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET controls = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
		}
		var got vibekit.RunControlsResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decoding the controls reply: %s", err)
		}
		if !slices.Contains(got.Verbs, "retry") {
			t.Errorf("verbs = %v, want retry offered", got.Verbs)
		}
		if got.ParentChatID != "c1" {
			t.Errorf("parent_chat_id = %q, want c1; the run page reads it to say where a step's "+
				"live transcript went", got.ParentChatID)
		}
	})

	t.Run("a live run whose engine is gone carries the refusal sentence", func(t *testing.T) {
		// Chat closed, so nothing in this process holds the run.
		h, br := seedChatParentedRun(t, false)
		br.setCallResult(methodKiroWorkflowInspect, inspectReply(t, "wf_1", "running", ""))
		rec := httptest.NewRecorder()
		h.runRoutes.handleControls(rec, controlsReq("wf_1"))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET controls = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
		}
		var got vibekit.RunControlsResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decoding the controls reply: %s", err)
		}
		if slices.Contains(got.Verbs, "pause") {
			t.Errorf("verbs = %v, want pause withheld from a run nothing hosts", got.Verbs)
		}
		if !strings.Contains(got.Refused["pause"], "Findings cleanup") {
			t.Errorf("refused[pause] = %q, want the sentence to name the chat to open",
				got.Refused["pause"])
		}
	})

	t.Run("an unreadable run is a 404 rather than an empty row", func(t *testing.T) {
		h, br := seedChatParentedRun(t, true)
		// An engine with no workflow verb: rr.status reports "" rather than an error,
		// which means "no status to gate on".
		br.setCallErr(methodKiroWorkflowInspect, workflow.ErrUnknownMethod)
		rec := httptest.NewRecorder()
		h.runRoutes.handleControls(rec, controlsReq("wf_1"))
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET controls for an unreadable run = %d, want %d: %s",
				rec.Code, http.StatusNotFound, rec.Body.String())
		}
	})
}

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

// TestHandleAnswer pins the endpoint's own guards and the ONE status that is not a fault:
// a 409 means another surface answered first or the step moved on, not a retryable error.
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

	t.Run("a run with no bridge is a 409 too", func(t *testing.T) {
		h, _, _ := newTestHub()
		h.runs.asks.Add(&runAsk{
			chatID: "run:wf_1",
			payload: vibekit.RunInputNeededPayload{
				WorkflowID: "wf_1", AskID: "a1", StepSessionID: "sess_step",
			},
		})
		rec := httptest.NewRecorder()
		h.runRoutes.handleAnswer(rec, answerReq(t, "wf_1", "a1", "the main branch"))
		if rec.Code != http.StatusConflict {
			t.Errorf("an unhosted run = %d, want %d: %s", rec.Code, http.StatusConflict, rec.Body.String())
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

// TestSetStepStatus pins the allowlist and the bridge resolution.
func TestSetStepStatus(t *testing.T) {
	t.Run("it accepts running as the continue-without-answering verb", func(t *testing.T) {
		h, _, br := newTestHub()
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
		// `running` clears KAS's completionSignal and re-drives the step with its DEFAULT
		// continuation, so what it asked is no longer answerable and every surface is told.
		if h.runs.asks.HasRun("wf_1") {
			t.Error("continuing the step left its question live")
		}
		settled := settledPayloads(t, h)
		if len(settled) != 1 {
			t.Fatalf("run_input_settled events = %d, want 1", len(settled))
		}
		// SettledByUser HERE, unlike the node-completion door: this verb is only reached by
		// a reader clicking Continue-without-answering, so it IS their decision.
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
		// An AGENT-launched run has no bridge of its own: KAS parents it on the calling
		// chat's session, so keying on `run:<id>` alone answers errRunNotHosted always.
		cs.Chats["c1"] = &vibekit.Chat{ID: "c1", ACPSessionID: "sess_parent"}
		h.bridge.mgr.insert("c1", &sharedBridge{bridge: br, state: bridgeIdle})
		br.callResults = map[string]json.RawMessage{
			methodKiroWorkflowList: json.RawMessage(
				`{"runs":[{"workflowId":"wf_1","name":"publish","status":"paused","parentSessionId":"sess_parent"}]}`,
			),
		}

		if err := h.runs.SetStepStatus(t.Context(), "wf_1", "review", runStepCompleted); err != nil {
			t.Errorf("SetStepStatus on an agent-launched run = %v, want nil", err)
		}
	})
}
