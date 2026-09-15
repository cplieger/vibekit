package agent

// The REST envelopes this package serves carry the digest stamp with the hub epoch:
// GET /api/runs/live (`runs`, the lease store's own collection version) and
// GET /api/config-template (`catalog`).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cplieger/vibekit/internal/runlease"
	"github.com/cplieger/vibekit/internal/subject"
	"github.com/cplieger/vibekit/internal/vibekit"
)

func TestEpoch_IsTheHubsCurrentEpoch(t *testing.T) {
	h, _, _ := newTestHub()
	if got, want := h.Epoch(), h.bus.fanout.Position().Epoch; got != want || got == "" {
		t.Errorf("Epoch() = %q, want the hub's non-empty %q", got, want)
	}
}

func TestLiveRuns_SubjectIsTheLeaseStoresVersionWithTheEpoch(t *testing.T) {
	h, _, _ := newTestHub()
	st := runlease.NewMemory()
	if err := st.Put(t.Context(), &runlease.Lease{WorkflowID: "wf1", ChatID: "c1", Origin: runlease.OriginAgent}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	rr := &runRoutes{runs: &Runs{leases: st}, epoch: h.Epoch}

	out := getLiveRuns(t, rr)

	_, version := st.ListStamped()
	want := vibekit.SubjectStamp{Kind: string(subject.KindRuns), Version: version, Epoch: h.Epoch()}
	if out.Subject == nil || *out.Subject != want {
		t.Errorf("GET /api/runs/live subject = %+v, want %+v", out.Subject, want)
	}
	if version == "0" || h.Epoch() == "" {
		t.Errorf("version %q / epoch %q: the assertion above is vacuous", version, h.Epoch())
	}
}

func TestConfigTemplate_SubjectIsTheCatalogVersionWithTheEpoch(t *testing.T) {
	h, _, _ := newTestHub()
	h.catalog.SetModes([]vibekit.SessionMode{{ID: "vibe", Name: "Default"}})
	h.catalog.SetModels([]vibekit.SessionModel{{ID: "m1", Name: "One"}})

	rec := httptest.NewRecorder()
	h.handleConfigTemplate(rec, httptest.NewRequest(http.MethodGet, "/api/config-template", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var got vibekit.ConfigTemplateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	current, _ := h.versions.Current(subject.KindCatalog, "")
	want := vibekit.SubjectStamp{Kind: string(subject.KindCatalog), Version: current, Epoch: h.Epoch()}
	if got.Subject == nil || *got.Subject != want {
		t.Errorf("GET /api/config-template subject = %+v, want %+v", got.Subject, want)
	}
	if current != "2" {
		t.Errorf("catalog version after SetModes and SetModels = %q, want 2", current)
	}
}

// Nothing has reloaded the catalog: the envelope stamps Unminted, matching what
// the resolver answers for the same counter.
func TestConfigTemplate_UnmintedCatalogStampsZero(t *testing.T) {
	h, _, _ := newTestHub()
	rec := httptest.NewRecorder()
	h.handleConfigTemplate(rec, httptest.NewRequest(http.MethodGet, "/api/config-template", nil))
	var got vibekit.ConfigTemplateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Subject == nil || got.Subject.Version != subject.Unminted || got.Subject.Epoch != h.Epoch() {
		t.Errorf("subject = %+v, want version %q with epoch %q", got.Subject, subject.Unminted, h.Epoch())
	}
}
