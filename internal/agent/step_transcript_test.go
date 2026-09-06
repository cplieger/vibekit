package agent

// Tests for serving one workflow step's transcript.
//
// The fixture is the measured `inspect` shape: a repeat whose two iterations hold
// the SAME step id under differently-spelled iteration containers, which is what
// makes "address by path, never by id" a property with teeth rather than a
// preference.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// stepInspect is one run's inspect reply, trimmed to what this path decodes: a
// repeat with two iterations, each holding a step whose node id is `build`, plus a
// pending step that has no session at all.
//
// Note the iteration containers: the tree spells them `loop#0` / `loop#1` while
// every path below reads `iter-0` / `iter-1`. That divergence is the reason
// workflow.pathSegment exists, and asserting on the wire spelling here is what
// keeps this endpoint addressable by the ids the client actually holds.
const stepInspect = `{
  "workflowId": "wf_1",
  "state": {
    "workflowId": "wf_1",
    "status": "completed",
    "root": {
      "nodeId": "wf_1", "type": "sequence", "status": "completed",
      "children": [
        {"nodeId": "loop", "type": "repeat", "status": "completed", "children": [
          {"nodeId": "loop#0", "type": "sequence", "status": "completed", "iteration": 0, "children": [
            {"nodeId": "build", "type": "step", "status": "completed", "iteration": 0, "sessionId": "sess_pass0"}
          ]},
          {"nodeId": "loop#1", "type": "sequence", "status": "completed", "iteration": 1, "children": [
            {"nodeId": "build", "type": "step", "status": "completed", "iteration": 1, "sessionId": "sess_pass1"}
          ]}
        ]},
        {"nodeId": "later", "type": "step", "status": "pending"}
      ]
    }
  }
}`

// armStepInspect makes the utility bridge answer `_kiro/workflow/inspect` with the
// fixture above.
func armStepInspect(br *fakeBridge) {
	br.mu.Lock()
	defer br.mu.Unlock()
	if br.callResults == nil {
		br.callResults = map[string]json.RawMessage{}
	}
	br.callResults[methodKiroWorkflowInspect] = json.RawMessage(stepInspect)
}

// armStepReplay makes a `session/load` reply with a replay of `texts`, each frame
// stamped with sessionID — the id of the session being LOADED, which is what makes
// them foreign to the utility session's own connection and therefore the frames
// this feature exists to route.
func armStepReplay(br *fakeBridge, sessionID string, texts ...string) {
	frames := make([]*vibekit.RPCResponse, 0, len(texts))
	for _, tx := range texts {
		frames = append(frames, newSessionChunkMsg(sessionID, tx))
	}
	br.mu.Lock()
	defer br.mu.Unlock()
	if br.notifsOnCall == nil {
		br.notifsOnCall = map[string][]*vibekit.RPCResponse{}
	}
	br.notifsOnCall[vibekit.MethodSessionLoad] = frames
}

// shortStepBudget drives the budget in milliseconds so an expiry test does not
// wait out a real minute.
func shortStepBudget(t *testing.T, d time.Duration) {
	t.Helper()
	prev := stepTranscriptBudget
	stepTranscriptBudget = d
	t.Cleanup(func() { stepTranscriptBudget = prev })
}

func TestStepTranscript_AStepsFramesProject(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	armStepInspect(br)
	armStepReplay(br, "sess_pass0", "first half ", "second half")

	got, err := h.Runs().StepTranscript(t.Context(), "wf_1", "wf_1/loop/iter-0/build")
	if err != nil {
		t.Fatalf("StepTranscript: %v", err)
	}
	if got.State != vibekit.RunStepTranscriptReady {
		t.Fatalf("state = %q, want ready", got.State)
	}
	if len(got.Messages) != 1 {
		t.Fatalf("got %d messages, want 1: %+v", len(got.Messages), got.Messages)
	}
	m := got.Messages[0]
	if m.Role != vibekit.RoleAssistant {
		t.Errorf("role = %q, want assistant", m.Role)
	}
	if m.Content != "first half second half" {
		t.Errorf("content = %q, want %q", m.Content, "first half second half")
	}
	if len(m.Blocks) == 0 {
		t.Error("the projected message carries no blocks, so nothing would render")
	}
	if got.WorkflowID != "wf_1" || got.NodePath != "wf_1/loop/iter-0/build" {
		t.Errorf("echoed identity = %q/%q, want wf_1/wf_1/loop/iter-0/build",
			got.WorkflowID, got.NodePath)
	}
}

// TestStepTranscript_ARepeatsIterationsAreDistinct is the property the whole
// address-by-path decision rests on. Both paths name a step whose NODE ID is
// `build`; only the path separates them, and each must reach its own session.
func TestStepTranscript_ARepeatsIterationsAreDistinct(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	armStepInspect(br)

	for _, tc := range []struct {
		path    string
		session string
		want    string
	}{
		{path: "wf_1/loop/iter-0/build", session: "sess_pass0", want: "pass zero"},
		{path: "wf_1/loop/iter-1/build", session: "sess_pass1", want: "pass one"},
	} {
		armStepReplay(br, tc.session, tc.want)
		got, err := h.Runs().StepTranscript(t.Context(), "wf_1", tc.path)
		if err != nil {
			t.Fatalf("StepTranscript(%s): %v", tc.path, err)
		}
		if got.State != vibekit.RunStepTranscriptReady {
			t.Fatalf("%s: state = %q, want ready", tc.path, got.State)
		}
		if len(got.Messages) != 1 || got.Messages[0].Content != tc.want {
			t.Errorf("%s projected %+v, want one message %q", tc.path, got.Messages, tc.want)
		}
		// The session actually loaded is the other half of the claim: a path
		// resolving to the right CONTENT off the wrong session id would only be
		// right because the fake answered the same frames either way.
		if id := br.lastParamsFor(vibekit.MethodSessionLoad)[vibekit.KeySessionID]; id != tc.session {
			t.Errorf("%s loaded session %v, want %s", tc.path, id, tc.session)
		}
	}
}

// TestStepTranscript_AStepThatNeverRanIsGone: KAS records no session for a step
// that has not started, so there is nothing to load and never was. `gone` rather
// than a 404, because the step is genuinely part of this run.
func TestStepTranscript_AStepThatNeverRanIsGone(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	armStepInspect(br)

	got, err := h.Runs().StepTranscript(t.Context(), "wf_1", "wf_1/later")
	if err != nil {
		t.Fatalf("StepTranscript: %v", err)
	}
	if got.State != vibekit.RunStepTranscriptGone {
		t.Errorf("state = %q, want gone", got.State)
	}
	if len(got.Messages) != 0 {
		t.Errorf("messages = %+v, want none", got.Messages)
	}
	// And nothing was loaded: a step with no session must not put a session/load
	// on the wire at all.
	if br.called(vibekit.MethodSessionLoad) {
		t.Error("a step with no session issued a session/load")
	}
}

// TestStepTranscript_AnUnknownPathIsAClientError separates the two absences.
// `gone` says the step exists and its transcript does not; errStepUnknown says
// this run has no such step, which is the caller's mistake.
func TestStepTranscript_AnUnknownPathIsAClientError(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	armStepInspect(br)

	for _, path := range []string{
		"wf_1/loop/iter-2/build", // an iteration that never ran
		"wf_1/loop/build",        // the tree's own shape, missing the iteration
		"wf_1/loop#0/build",      // the STATE-TREE spelling, which is not the wire's
		"wf_1/nope",
	} {
		got, err := h.Runs().StepTranscript(t.Context(), "wf_1", path)
		if err == nil {
			t.Errorf("StepTranscript(%q) = %+v, want errStepUnknown", path, got)
			continue
		}
		if !strings.Contains(err.Error(), "no step at that path") {
			t.Errorf("StepTranscript(%q) error = %v, want errStepUnknown", path, err)
		}
	}
}

// TestStepTranscript_AFailedLoadIsUnavailable: KAS answers an id it does not hold
// and a transient fault with the same shape, so this is `unavailable` and never
// `gone` — the two want opposite client behaviour, and only one is retryable.
func TestStepTranscript_AFailedLoadIsUnavailable(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	armStepInspect(br)
	br.mu.Lock()
	br.callRPCErrs = map[string]*vibekit.RPCError{
		vibekit.MethodSessionLoad: {Code: -32603, Message: "Internal error"},
	}
	br.mu.Unlock()

	got, err := h.Runs().StepTranscript(t.Context(), "wf_1", "wf_1/loop/iter-0/build")
	if err != nil {
		t.Fatalf("a refused load must not be an error the handler 500s on: %v", err)
	}
	if got.State != vibekit.RunStepTranscriptUnavailable {
		t.Errorf("state = %q, want unavailable", got.State)
	}
	if len(got.Messages) != 0 {
		t.Errorf("messages = %+v, want none", got.Messages)
	}
}

// TestStepTranscript_AnUnreadableRunIsUnavailable: a run whose own state cannot be
// read has no step transcript either, and it is a state of the world rather than a
// client error — so it is a 200 carrying `unavailable`, not a 404.
//
// Both arms of "unreadable" are driven, because they arrive by different routes and
// only one of them looks like a failure: KAS's refusal reaches this side as an EMPTY
// result (rawCall drops the in-band error), while a wire change reaches it as bytes
// that will not decode. Neither is the caller's fault, so neither may 404.
func TestStepTranscript_AnUnreadableRunIsUnavailable(t *testing.T) {
	for _, tc := range []struct {
		desc  string
		arm   func(*fakeBridge)
		wantN int
	}{
		{
			desc: "KAS refused the inspect, which arrives as an empty result",
			arm: func(br *fakeBridge) {
				br.callRPCErrs = map[string]*vibekit.RPCError{
					methodKiroWorkflowInspect: {Code: -32603, Message: "Internal error"},
				}
			},
		},
		{
			desc: "the reply is not JSON at all",
			arm: func(br *fakeBridge) {
				br.callResults = map[string]json.RawMessage{
					methodKiroWorkflowInspect: json.RawMessage(`not json`),
				}
			},
		},
		{
			desc: "the reply is JSON of the wrong shape",
			arm: func(br *fakeBridge) {
				br.callResults = map[string]json.RawMessage{
					methodKiroWorkflowInspect: json.RawMessage(`{"state":"a string, not an object"}`),
				}
			},
		},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			h, _, br := newTestHub()
			t.Cleanup(func() { shutdownHub(t, h) })
			br.mu.Lock()
			tc.arm(br)
			br.mu.Unlock()

			got, err := h.Runs().StepTranscript(t.Context(), "wf_1", "wf_1/loop/iter-0/build")
			if err != nil {
				t.Fatalf("an unreadable run must not be a client error: %v", err)
			}
			if got.State != vibekit.RunStepTranscriptUnavailable {
				t.Errorf("state = %q, want unavailable", got.State)
			}
		})
	}
}

// TestStepTranscript_TheBudgetBoundsTheBarrier is the reason this read has a budget
// at all: a bridge Call has no client-side timeout, so without one a step whose
// replay never drains holds the request forever.
//
// Driven with the utility session's forward goroutine NOT wired to the registry, so
// nothing ever observes the drain — which is exactly the shape a wedged replay has.
func TestStepTranscript_TheBudgetBoundsTheBarrier(t *testing.T) {
	shortStepBudget(t, 40*time.Millisecond)
	br := newFakeBridge()
	armStepInspect(br)
	armStepReplay(br, "sess_pass0", "never drained")
	rs := unwiredStepRuns(t, br)

	start := time.Now()
	got, err := rs.StepTranscript(t.Context(), "wf_1", "wf_1/loop/iter-0/build")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("StepTranscript: %v", err)
	}
	if got.State != vibekit.RunStepTranscriptUnavailable {
		t.Errorf("state = %q, want unavailable", got.State)
	}
	// The point is that it ANSWERS. A generous ceiling, because the assertion is
	// "bounded", not "bounded to the millisecond".
	if elapsed > 5*time.Second {
		t.Errorf("answered after %v, want inside the budget", elapsed)
	}
}

// TestStepTranscript_ARefusedLoadLeavesNoReplayOpen pins the cleanup: every exit
// takes the replay, so a second read of the same step is not refused as a
// duplicate by a corpse of the first.
func TestStepTranscript_ARefusedLoadLeavesNoReplayOpen(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	armStepInspect(br)
	br.mu.Lock()
	br.callRPCErrs = map[string]*vibekit.RPCError{
		vibekit.MethodSessionLoad: {Code: -32603, Message: "Internal error"},
	}
	br.mu.Unlock()

	if _, err := h.Runs().StepTranscript(t.Context(), "wf_1", "wf_1/loop/iter-0/build"); err != nil {
		t.Fatalf("first read: %v", err)
	}
	// The refusal cleared, the retry succeeds. Without the deferred take the
	// registry would still hold sess_pass0 and answer `unavailable` forever.
	br.mu.Lock()
	br.callRPCErrs = nil
	br.mu.Unlock()
	armStepReplay(br, "sess_pass0", "second time")

	got, err := h.Runs().StepTranscript(t.Context(), "wf_1", "wf_1/loop/iter-0/build")
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got.State != vibekit.RunStepTranscriptReady {
		t.Fatalf("retry state = %q, want ready — the first read left a replay open", got.State)
	}
	if len(got.Messages) != 1 || got.Messages[0].Content != "second time" {
		t.Errorf("retry projected %+v, want one message %q", got.Messages, "second time")
	}
}

// TestStepTranscript_OnlyAssistantRowsTravel: a step's own prompt is already the
// pane's Instruction row, and the stream this feeds renders ONE assistant
// transcript, so a user row would be the same text twice.
func TestStepTranscript_OnlyAssistantRowsTravel(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	armStepInspect(br)
	// The replay's own user row, in the shape KAS sends it — the frame kind the
	// projection turns into a RoleUser message.
	user, _ := json.Marshal(map[string]any{
		"sessionId": "sess_pass0",
		"update": json.RawMessage(`{"sessionUpdate":"user_message_chunk",` +
			`"content":{"type":"text","text":"the step's own instruction"}}`),
	})
	br.mu.Lock()
	br.notifsOnCall = map[string][]*vibekit.RPCResponse{
		vibekit.MethodSessionLoad: {
			{Method: vibekit.MethodSessionUpdate, Params: user},
			newSessionChunkMsg("sess_pass0", "the answer"),
		},
	}
	br.mu.Unlock()

	got, err := h.Runs().StepTranscript(t.Context(), "wf_1", "wf_1/loop/iter-0/build")
	if err != nil {
		t.Fatalf("StepTranscript: %v", err)
	}
	for _, m := range got.Messages {
		if m.Role != vibekit.RoleAssistant {
			t.Errorf("a %s row travelled: %q", m.Role, m.Content)
		}
		if strings.Contains(m.Content, "own instruction") {
			t.Errorf("the step's prompt reached the transcript: %q", m.Content)
		}
	}
	if len(got.Messages) != 1 || got.Messages[0].Content != "the answer" {
		t.Errorf("projected %+v, want one assistant message %q", got.Messages, "the answer")
	}
}

// TestStepTranscript_TheUtilitySessionKeepsItsOwnIdentity: a raw `session/load` Call must
// NOT rebind the utility bridge's own session id. If it did, liveID() would report a step's
// id, taking the real utility session out of the orphan reaper's keep-list — and the reaper
// would delete on-disk state from under a live subprocess.
func TestStepTranscript_TheUtilitySessionKeepsItsOwnIdentity(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	armStepInspect(br)
	armStepReplay(br, "sess_pass0", "hello")

	// Start the utility session first, so its id is settled before the read.
	if err := h.utility.get().session.ensureStarted(t.Context()); err != nil {
		t.Fatalf("start utility session: %v", err)
	}
	before := h.utility.get().session.liveID()
	if before == "" {
		t.Fatal("the utility session reports no id")
	}

	if _, err := h.Runs().StepTranscript(t.Context(), "wf_1", "wf_1/loop/iter-0/build"); err != nil {
		t.Fatalf("StepTranscript: %v", err)
	}

	if after := h.utility.get().session.liveID(); after != before {
		t.Errorf("the utility session's id moved from %q to %q across a step read — "+
			"the orphan reaper's keep-list now names the wrong session", before, after)
	}
	if after := before; after == "sess_pass0" {
		t.Error("the utility session adopted the step's id")
	}
}

// --- The HTTP surface ---

// getStepTranscript drives the real route table, so the wildcard pattern and the
// path-value decode are exercised rather than assumed.
func getStepTranscript(t *testing.T, h *Runtime, target string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	(&runRoutes{runs: h.Runs()}).register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestHandleStepTranscript_HTTP(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	armStepInspect(br)
	armStepReplay(br, "sess_pass0", "served")

	rec := getStepTranscript(t, h, "/api/runs/wf_1/steps/wf_1/loop/iter-0/build")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var out vibekit.RunStepTranscript
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if out.State != vibekit.RunStepTranscriptReady {
		t.Errorf("state = %q, want ready", out.State)
	}
	if len(out.Messages) != 1 || out.Messages[0].Content != "served" {
		t.Errorf("messages = %+v, want one %q", out.Messages, "served")
	}
	// The verdict must be PRESENT on the wire, not omitted. A `state` a client can
	// find absent is a client inventing "assume ready".
	if !strings.Contains(rec.Body.String(), `"state"`) {
		t.Error("the reply omits `state`")
	}
}

// TestHandleStepTranscript_AGoneVerdictIsA200 pins the status split: only a path
// this run does not name is an HTTP error. `gone` and `unavailable` are answers
// ABOUT the transcript, and a client has to be able to render each.
func TestHandleStepTranscript_AGoneVerdictIsA200(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	armStepInspect(br)

	rec := getStepTranscript(t, h, "/api/runs/wf_1/steps/wf_1/later")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var out vibekit.RunStepTranscript
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.State != vibekit.RunStepTranscriptGone {
		t.Errorf("state = %q, want gone", out.State)
	}
}

func TestHandleStepTranscript_Refusals(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	armStepInspect(br)

	for _, tc := range []struct {
		desc   string
		method string
		target string
		want   int
	}{
		{
			desc: "a path this run does not name is a 404",
			// The one HTTP error: the run and the endpoint are fine, the caller's
			// path is not.
			method: http.MethodGet,
			target: "/api/runs/wf_1/steps/wf_1/nope",
			want:   http.StatusNotFound,
		},
		{
			desc:   "no path at all is a 400",
			method: http.MethodGet,
			target: "/api/runs/wf_1/steps/",
			want:   http.StatusBadRequest,
		},
		{
			// The EXACT form is registered alongside the subtree precisely so this
			// is a 400 rather than ServeMux's own 307 to the trailing-slash form,
			// which internal/server's canonical-path gate cannot see.
			desc:   "the bare collection is a 400, never a redirect",
			method: http.MethodGet,
			target: "/api/runs/wf_1/steps",
			want:   http.StatusBadRequest,
		},
		{
			desc:   "a path belonging to another run is a 400",
			method: http.MethodGet,
			target: "/api/runs/wf_1/steps/wf_other/loop/iter-0/build",
			want:   http.StatusBadRequest,
		},
		{
			desc:   "a non-GET method is refused",
			method: http.MethodPost,
			target: "/api/runs/wf_1/steps/wf_1/loop/iter-0/build",
			want:   http.StatusMethodNotAllowed,
		},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			mux := http.NewServeMux()
			(&runRoutes{runs: h.Runs()}).register(mux)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.target, nil))
			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

// TestHandleStepTranscript_APercentEncodedSegmentDecodes pins the one encoding rule
// this route has: segments are encoded per segment and the separators stay raw, so
// a node id carrying a `#` or a space survives the trip.
//
// Measured against Go's own ServeMux: `{path...}` hands back the DECODED remainder,
// which is why the handler compares it to the tree's join as it stands.
func TestHandleStepTranscript_APercentEncodedSegmentDecodes(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	// A step whose node id is not URL-safe. `loop#0` is the shape a repeat child
	// falls back to when KAS sends no `iteration`.
	br.mu.Lock()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowInspect: json.RawMessage(`{"state":{"workflowId":"wf_1","root":{` +
			`"nodeId":"wf_1","type":"sequence","children":[` +
			`{"nodeId":"a b#0","type":"step","status":"completed","sessionId":"sess_odd"}]}}}`),
	}
	br.mu.Unlock()
	armStepReplay(br, "sess_odd", "odd id")

	rec := getStepTranscript(t, h, "/api/runs/wf_1/steps/wf_1/a%20b%230")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var out vibekit.RunStepTranscript
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.State != vibekit.RunStepTranscriptReady {
		t.Fatalf("state = %q, want ready", out.State)
	}
	if len(out.Messages) != 1 || out.Messages[0].Content != "odd id" {
		t.Errorf("messages = %+v, want one %q", out.Messages, "odd id")
	}
}

// --- The registry, on its own ---

// TestStepReplays_TheSettleSpansTheDrain is the barrier's whole argument, tested
// where it lives: a load that has returned settles only once the CONSUMER has
// folded everything that preceded the result, because the replay frames precede
// that result on the wire and the notification channel is buffered.
func TestStepReplays_TheSettleSpansTheDrain(t *testing.T) {
	var sr stepReplays
	if !sr.open("sess_a") {
		t.Fatal("open reported a duplicate on an empty registry")
	}
	if closedNow(sr.barrier("sess_a")) {
		t.Fatal("a fresh replay's barrier is already closed")
	}
	// Reaching the position is not enough on its own: the load has not returned,
	// so the position it will name is not known yet.
	sr.settleConsumed(atFrame(testLoadSeq), false)
	if closedNow(sr.barrier("sess_a")) {
		t.Fatal("settled before the load returned")
	}
	sr.markLoadedAt("sess_a", drainPoint{gen: testFwdGen, seq: testLoadSeq + 2})
	// The load has returned but the consumer is still behind its position.
	sr.settleConsumed(atFrame(testLoadSeq+1), false)
	if closedNow(sr.barrier("sess_a")) {
		t.Fatal("settled while frames were still unfolded: the drain was not spanned")
	}
	sr.settleConsumed(atFrame(testLoadSeq+2), false)
	if !closedNow(sr.barrier("sess_a")) {
		t.Error("did not settle with the load returned and its position reached")
	}
}

// TestStepReplays_ADrainedReplaySettlesWhenTheLoadReturns is defect (a) on the
// STEP route: the read holds a barrier nothing will close.
//
// The settle used to run only from the utility session's drain loop, so a replay
// whose frames were all folded before the `session/load` RPC returned had no
// trigger left — no frame was coming, and the loop only ends when the bridge does.
// The read then held its whole 60s budget and answered `unavailable` for a
// transcript that existed and was fully projected.
func TestStepReplays_ADrainedReplaySettlesWhenTheLoadReturns(t *testing.T) {
	var sr stepReplays
	if !sr.open("sess_a") {
		t.Fatal("open reported a duplicate on an empty registry")
	}
	// The drain loop folded every replayed frame while the RPC was still in flight.
	sr.settleConsumed(atFrame(testLoadSeq), false)
	if closedNow(sr.barrier("sess_a")) {
		t.Fatal("settled before the load returned")
	}

	sr.markLoadedAt("sess_a", atLoad())

	if !closedNow(sr.barrier("sess_a")) {
		t.Error("the barrier is still open on a replay the consumer had already " +
			"folded: nothing else will close it, so the read waits out its budget " +
			"and answers unavailable for a transcript it has in hand")
	}
}

// TestStepReplays_AStragglerFromAPreviousAttachmentSettlesNothing: the utility
// session is recycled every 20 prompts and culled after 30 minutes idle, so a
// previous subprocess's forward goroutine draining its closed channel is ordinary
// rather than exotic — and its positions run arbitrarily far ahead of a fresh
// session's, because each read loop restarts its sequence at zero.
func TestStepReplays_AStragglerFromAPreviousAttachmentSettlesNothing(t *testing.T) {
	var sr stepReplays
	if !sr.open("sess_a") {
		t.Fatal("open reported a duplicate on an empty registry")
	}
	const live = testFwdGen + 1
	sr.markLoadedAt("sess_a", drainPoint{gen: live, seq: testLoadSeq})

	sr.settleConsumed(drainPoint{gen: testFwdGen, seq: 900}, false)
	if closedNow(sr.barrier("sess_a")) {
		t.Fatal("a straggling observation from the previous subprocess settled a " +
			"replay loaded on the live one")
	}
	// Nor may its EXIT seal, which arrives when that closed channel runs out.
	sr.settleConsumed(drainPoint{gen: testFwdGen}, true)
	if closedNow(sr.barrier("sess_a")) {
		t.Fatal("the previous subprocess's exit seal settled the live replay")
	}
	// Nor may its position have been ADOPTED, which refusing to settle on it does
	// not prove: the live subprocess's own first frame is one frame in, so a stored
	// 900 would settle this read on a transcript nothing has folded.
	sr.settleConsumed(drainPoint{gen: live, seq: 1}, false)
	if closedNow(sr.barrier("sess_a")) {
		t.Fatal("the live subprocess's FIRST frame settled the replay: the straggler's " +
			"position was adopted")
	}

	sr.settleConsumed(drainPoint{gen: live, seq: testLoadSeq}, false)
	if !closedNow(sr.barrier("sess_a")) {
		t.Error("the live attachment reaching the load position did not settle it")
	}
}

// TestStepReplays_NoReplayOpenAnswersImmediately: a reader must never wait on a
// load that is not happening.
func TestStepReplays_NoReplayOpenAnswersImmediately(t *testing.T) {
	var sr stepReplays
	if !closedNow(sr.barrier("nobody")) {
		t.Error("an unknown session's barrier is not already closed")
	}
	if got := sr.take("nobody"); got != nil {
		t.Errorf("take of an unknown session = %+v, want nil", got)
	}
	// markLoadedAt and settleConsumed must tolerate it too: the drain loop calls
	// settleConsumed on every frame, whether or not anything is being read.
	sr.markLoadedAt("nobody", atLoad())
	sr.settleConsumed(atFrame(testLoadSeq), false)
}

// TestStepReplays_ASecondReadOfOneStepIsRefused: two readers of one barrier is a
// lifecycle this registry does not carry, so the second is refused rather than
// joined — and the refusal must not disturb the first.
func TestStepReplays_ASecondReadOfOneStepIsRefused(t *testing.T) {
	var sr stepReplays
	if !sr.open("sess_a") {
		t.Fatal("the first open was refused")
	}
	if sr.open("sess_a") {
		t.Error("a second open of the same session was accepted")
	}
	sr.markLoadedAt("sess_a", atLoad())
	sr.settleConsumed(atFrame(testLoadSeq), false)
	if !closedNow(sr.barrier("sess_a")) {
		t.Error("the refused second open disturbed the first replay")
	}
	// Once taken, the session is open for a new read again.
	_ = sr.take("sess_a")
	if !sr.open("sess_a") {
		t.Error("a session cannot be read again after its replay was taken")
	}
}

// TestStepReplays_TakeIsIdempotentAndClosesTheBarrier: take is the reader's own
// cleanup and runs on the timeout path too, so it must close a barrier nothing
// else will and survive being called twice.
func TestStepReplays_TakeIsIdempotentAndClosesTheBarrier(t *testing.T) {
	var sr stepReplays
	sr.open("sess_a")
	b := sr.barrier("sess_a")
	_ = sr.take("sess_a") // the abandoned path: never settled
	if !closedNow(b) {
		t.Error("take left a barrier nothing will ever close")
	}
	if got := sr.take("sess_a"); got != nil {
		t.Errorf("a second take = %+v, want nil", got)
	}
	// And a settle reaching a taken entry must not panic on an already-closed
	// channel.
	sr.settleConsumed(atFrame(testLoadSeq), false)
}

// TestStepReplays_IngestReportsWhetherItConsumed is what lets the utility session
// tell an expected replay frame from a genuinely foreign one.
func TestStepReplays_IngestReportsWhetherItConsumed(t *testing.T) {
	var sr stepReplays
	raw := json.RawMessage(`{"content":{"type":"text","text":"x"}}`)
	if sr.ingest("sess_a", vibekit.ACPUpdateAgentChunk, raw) {
		t.Error("ingest claimed a frame with no replay open")
	}
	sr.open("sess_a")
	if !sr.ingest("sess_a", vibekit.ACPUpdateAgentChunk, raw) {
		t.Error("ingest dropped a frame for an open replay")
	}
	if sr.ingest("sess_b", vibekit.ACPUpdateAgentChunk, raw) {
		t.Error("ingest claimed a frame for a session nobody is reading")
	}
	msgs := sr.take("sess_a")
	if len(msgs) != 1 || msgs[0].Content != "x" {
		t.Errorf("projected %+v, want one message %q", msgs, "x")
	}
}

// TestStepTranscript_SettlesOnTheBarrierRatherThanTheBudget drives the read under a budget
// too short to hide behind: on the real 60s budget, a read that waits it out and answers
// `unavailable` is indistinguishable from one that settles. At 50ms only a closed barrier can
// answer `ready`, so the load's read-loop position, the attachment it names and the utility
// session's per-frame report all have to agree.
func TestStepTranscript_SettlesOnTheBarrierRatherThanTheBudget(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	armStepInspect(br)
	armStepReplay(br, "sess_pass0", "settled ", "in time")
	shortStepBudget(t, 50*time.Millisecond)

	got, err := h.Runs().StepTranscript(t.Context(), "wf_1", "wf_1/loop/iter-0/build")
	if err != nil {
		t.Fatalf("StepTranscript: %v", err)
	}
	if got.State != vibekit.RunStepTranscriptReady {
		t.Fatalf("state = %q, want ready — the replay did not settle inside a 50ms "+
			"budget, so the read is waiting out its clock rather than the drain", got.State)
	}
	if len(got.Messages) != 1 || got.Messages[0].Content != "settled in time" {
		t.Errorf("messages = %+v, want one %q", got.Messages, "settled in time")
	}
}

// TestUtilityRawCallAt_CarriesTheResponsePosition: the step read's whole barrier
// rests on this one number, so it must be a real position rather than a zero.
//
// A load answering at 0 while the frames it must wait for are still queued would
// make the completion condition trivially true — the consumer's position starts at
// 0 too — and the read would return a short projection with nothing reporting the
// shortfall. It also names the ATTACHMENT, because the utility session is recycled
// every 20 prompts and each subprocess restarts its sequence at zero.
func TestUtilityRawCallAt_CarriesTheResponsePosition(t *testing.T) {
	br := newFakeBridge()
	armStepReplay(br, "sess_pass0", "one ", "two ", "three")
	rs := unwiredStepRuns(t, br)

	raw, at, err := rs.utility().session.rawCallAt(t.Context(), "step transcript load",
		vibekit.MethodSessionLoad,
		callerParams(map[string]any{vibekit.KeySessionID: "sess_pass0"}))
	if err != nil {
		t.Fatalf("rawCallAt: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("rawCallAt returned an empty result, so the load did not answer")
	}
	if at.seq != 3 {
		t.Errorf("load position = %d, want 3 — the three replay frames all precede "+
			"the result on the wire, so the consumer has to reach their position", at.seq)
	}
	if at.gen == 0 {
		t.Error("the load position names no attachment, so a straggling observation " +
			"from a recycled subprocess could satisfy it")
	}
}

// --- helpers ---

// closedNow reports whether a barrier is already closed, without waiting.
func closedNow(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// unwiredStepRuns builds a Runs over a utility session with NO forward goroutine
// feeding the step registry — the shape a wedged replay has, and the only way to
// drive the barrier's own budget deterministically.
func unwiredStepRuns(t *testing.T, br *fakeBridge) *Runs {
	t.Helper()
	session := &utilitySession{
		shutdownCtx:   context.Background(),
		bridgeFactory: func() ACPBridge { return br },
		models:        func() []vibekit.SessionModel { return nil },
	}
	ur := &utilityRuntime{session: session, textgen: newUtilityAgent(session)}
	t.Cleanup(session.Stop)
	return &Runs{
		translate: noopRunTranslator{},
		utility:   func() *utilityRuntime { return ur },
	}
}

// lastParamsFor returns the params of the most recent Call of one method.
func (b *fakeBridge) lastParamsFor(method string) map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastParams[method]
}

// called reports whether a method was ever Called.
func (b *fakeBridge) called(method string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Contains(b.calls, method)
}
