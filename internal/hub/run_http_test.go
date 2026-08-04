package hub

// Tests for the run read handler's own guards.
//
// The passthrough is not tested here — there is nothing to assert about `raw in,
// raw out` that is not a restatement — and the step-session seeding it performs is
// tested where the registry lives (translate.RecordRunSteps), because that is
// where the assertion can be about the observable consequence rather than about a
// map reached through a test-only accessor.

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

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
