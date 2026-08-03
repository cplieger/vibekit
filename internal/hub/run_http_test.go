package hub

// Tests for the run read surface's two side concerns: seeding step attribution
// from a read, and the route's own registration.
//
// The passthrough itself is not tested here — there is nothing to assert about
// `raw in, raw out` that is not a restatement — but the seeding IS the recovery
// path for step-frame classification, and getting it wrong is silent: a resumed
// run's frames would be attributed to a subagent that does not exist, and the
// only symptom is a step's prose appearing in the wrong place.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRecordRunStepSessions_SeedsFromARead pins the recovery path.
//
// `node_start` announces a step's session id live, but a container restart empties
// that registry while the run carries on — so reading the run is the only other
// moment the mapping is in hand, and `inspect` carries it on every node.
func TestRecordRunStepSessions_SeedsFromARead(t *testing.T) {
	h, _, _ := newTestHub()
	raw := json.RawMessage(`{
	  "workflowId": "wf_1",
	  "state": {"workflowId": "wf_1", "root": {"nodeId": "wf_1", "type": "sequence", "children": [
	    {"nodeId": "build", "type": "step", "sessionId": "sess_build"},
	    {"nodeId": "test", "type": "step", "sessionId": "sess_test"},
	    {"nodeId": "later", "type": "step"}
	  ]}}
	}`)

	h.recordRunStepSessions(raw)

	ref, ok := h.translator.StepOf("sess_build")
	if !ok {
		t.Fatal("reading a run did not seed its step sessions")
	}
	if ref.WorkflowID != "wf_1" || ref.NodeID != "build" {
		t.Errorf("StepOf(sess_build) = %+v, want {wf_1 build}", ref)
	}
	if _, ok := h.translator.StepOf("sess_test"); !ok {
		t.Error("only the first step was seeded")
	}
	// A step with no session has not started; recording an empty key would make
	// every unattributed frame look like a step.
	if _, ok := h.translator.StepOf(""); ok {
		t.Error("a pending step seeded an empty-keyed entry")
	}
}

// TestRecordRunStepSessions_ToleratesJunk pins that seeding is best-effort. The
// response is passed through raw and is useful whether or not the side effect
// landed, so a decode failure must not be able to fail the read.
func TestRecordRunStepSessions_ToleratesJunk(t *testing.T) {
	h, _, _ := newTestHub()
	for _, raw := range []string{`{`, `null`, `[]`, `{"state":null}`, `{"state":{"root":null}}`, `"a string"`} {
		h.recordRunStepSessions(json.RawMessage(raw))
	}
}

// TestHandleRun_RejectsNonGET pins that the surface is read-only at the method
// level too. Runs have no controls at all (user decision), so a POST here is not
// a missing feature to route somewhere.
func TestHandleRun_RejectsNonGET(t *testing.T) {
	h, _, _ := newTestHub()
	rec := httptest.NewRecorder()
	h.handleRun(rec, httptest.NewRequest(http.MethodPost, "/api/runs/wf_1", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /api/runs/{id} = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandleRun_RejectsAMissingID(t *testing.T) {
	h, _, _ := newTestHub()
	rec := httptest.NewRecorder()
	// No path value set: the route cannot match this, but a hand-built request
	// can, and answering 400 beats calling KAS with an empty id.
	h.handleRun(rec, httptest.NewRequest(http.MethodGet, "/api/runs/", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("GET with no id = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}
