package agent

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

func TestTemplateToResponse(t *testing.T) {
	raw := `{
	  "modes": {
	    "currentModeId": "vibe",
	    "availableModes": [
	      {"id": "vibe", "name": "Default", "description": "General", "_meta": {"kiro": {"source": "bundled"}}},
	      {"id": "my-agent", "name": "My Agent", "_meta": {"kiro": {"source": "global"}}},
	      {"id": "", "name": "bogus"}
	    ]
	  },
	  "configOptions": [
	    {"id": "mode", "currentValue": "vibe", "options": []},
	    {"id": "model", "currentValue": "m-default", "options": [
	      {"value": "m-default", "name": "Default Model", "description": "Fast", "_meta": {"kiro": {"rateMultiplier": 1}}},
	      {"value": "m-big", "name": "Big Model", "_meta": {"kiro": {"rateMultiplier": 2.5, "hasEffort": true}}},
	      {"value": "m-old", "name": "Old", "description": "[Deprecated] legacy"},
	      {"name": "Group", "options": [
	        {"value": "m-grouped", "name": "Grouped Model"}
	      ]}
	    ]}
	  ]
	}`
	var tpl kasConfigTemplate
	if err := json.Unmarshal([]byte(raw), &tpl); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	got := templateToResponse(&tpl)

	if got.DefaultModel != "m-default" {
		t.Errorf("default model: %q", got.DefaultModel)
	}
	// The empty-id mode is dropped; sources flow through.
	if len(got.Modes) != 2 || got.Modes[0].ID != "vibe" || got.Modes[1].Source != "global" {
		t.Errorf("modes: %+v", got.Modes)
	}
	// Deprecated filtered, group flattened, meta mapped.
	ids := make([]string, 0, len(got.Models))
	for _, m := range got.Models {
		ids = append(ids, m.ID)
	}
	if len(got.Models) != 3 || ids[0] != "m-default" || ids[1] != "m-big" || ids[2] != "m-grouped" {
		t.Fatalf("models: %v", ids)
	}
	if got.Models[1].RateMultiplier != 2.5 || !got.Models[1].HasEffort {
		t.Errorf("model meta mapping: %+v", got.Models[1])
	}
}

func TestTemplateToResponseEmpty(t *testing.T) {
	got := templateToResponse(&kasConfigTemplate{})
	if got.DefaultModel != "" || len(got.Modes) != 0 || len(got.Models) != 0 {
		t.Errorf("empty template must yield empty catalog: %+v", got)
	}
	// The JSON contract keeps arrays non-null so the client can index
	// without null checks.
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	if s != `{"modes":[],"models":[],"effort_levels":[]}` {
		t.Errorf("empty response JSON: %s", s)
	}
}

// TestHandleConfigTemplate_DegradesToEmptyListsAndSaysSo covers the endpoint's
// contract in all three directions, because the degradation is what makes the other
// two matter.
//
// The client keeps static fallbacks for a 200 carrying empty lists, so a failure
// here is INVISIBLE in the UI — the picker simply shows the built-in defaults. The
// log line is therefore the only evidence, which also means a guard flipped so it
// fires on success reports a broken catalog on every page load while the real
// failures look identical.
func TestHandleConfigTemplate_DegradesToEmptyListsAndSaysSo(t *testing.T) {
	const goodReply = `{
	  "modes": {"currentModeId": "vibe", "availableModes": [
	    {"id": "vibe", "name": "Default", "_meta": {"kiro": {"source": "bundled"}}}
	  ]},
	  "configOptions": [
	    {"id": "model", "currentValue": "m-default", "options": [
	      {"value": "m-default", "name": "Default Model"}
	    ]}
	  ]
	}`

	serve := func(t *testing.T, h *Runtime) configTemplateResponse {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/config-template", nil)
		rec := httptest.NewRecorder()
		h.handleConfigTemplate(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: the client has no error path here, it reads the body",
				rec.Code)
		}
		var got configTemplateResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode reply %q: %v", rec.Body.String(), err)
		}
		return got
	}

	t.Run("a good template becomes the catalog", func(t *testing.T) {
		logs := captureLogs(t)
		h, _, br := newTestHub()
		br.callResults = map[string]json.RawMessage{
			methodKiroConfigTemplate: json.RawMessage(goodReply),
		}

		got := serve(t, h)
		if len(got.Modes) != 1 || got.Modes[0].ID != "vibe" {
			t.Errorf("modes = %+v, want the template's one bundled mode", got.Modes)
		}
		if got.DefaultModel != "m-default" {
			t.Errorf("default model = %q, want m-default", got.DefaultModel)
		}
		out := logs.String()
		if strings.Contains(out, `"msg":"config template failed"`) ||
			strings.Contains(out, `"msg":"config template decode failed"`) {
			t.Errorf("a template that served fine reported a failure: %s", out)
		}
	})

	t.Run("an unreachable bridge degrades and is reported", func(t *testing.T) {
		logs := captureLogs(t)
		h, _, br := newTestHub()
		br.callErrs = map[string]error{methodKiroConfigTemplate: errors.New("kas gone")}

		got := serve(t, h)
		if len(got.Modes) != 0 || len(got.Models) != 0 {
			t.Errorf("got %+v, want empty lists so the client keeps its fallbacks", got)
		}
		const wantLine = "config template failed"
		if out := logs.String(); !strings.Contains(out, `"msg":"`+wantLine+`"`) {
			t.Errorf("a catalog nobody could fetch said nothing; want a line reading %q. Got: %s",
				wantLine, out)
		}
	})

	t.Run("an undecodable template degrades and is reported", func(t *testing.T) {
		logs := captureLogs(t)
		h, _, br := newTestHub()
		br.callResults = map[string]json.RawMessage{
			methodKiroConfigTemplate: json.RawMessage(`["not the template shape"]`),
		}

		got := serve(t, h)
		if len(got.Modes) != 0 || len(got.Models) != 0 {
			t.Errorf("got %+v, want empty lists so the client keeps its fallbacks", got)
		}
		const wantLine = "config template decode failed"
		if out := logs.String(); !strings.Contains(out, `"msg":"`+wantLine+`"`) {
			t.Errorf("a template vibekit could not read said nothing; want a line reading %q. "+
				"Got: %s", wantLine, out)
		}
	})

	// A live session's report beats the session-less template, per list. This is
	// the endpoint the client's ONLY copy of the vocabulary now comes from, so
	// serving the weaker of the two would lose both the workspace agents and the
	// shadowing KAS already resolved.
	t.Run("a live catalog wins over the template", func(t *testing.T) {
		h, _, br := newTestHub()
		br.callResults = map[string]json.RawMessage{
			methodKiroConfigTemplate: json.RawMessage(goodReply),
		}
		h.catalog.SetModes([]vibekit.SessionMode{
			{ID: "vibe", Name: "Default", Source: "bundled"},
			{ID: "reviewer", Name: "reviewer", Source: "workspace"},
		})

		got := serve(t, h)

		if len(got.Modes) != 2 || got.Modes[1].ID != "reviewer" {
			t.Errorf("modes = %+v, want the live catalog's two entries", got.Modes)
		}
		// The template still fills what no session has reported.
		if len(got.Models) != 1 || got.Models[0].ID != "m-default" {
			t.Errorf("models = %+v, want the template's, which the live catalog has not replaced",
				got.Models)
		}
		if got.DefaultModel != "m-default" {
			t.Errorf("default model = %q, want the template's m-default", got.DefaultModel)
		}
	})

	// A template outage must not hide a catalog vibekit already holds: this is now
	// the client's only source for it.
	t.Run("a live catalog survives a template outage", func(t *testing.T) {
		h, _, br := newTestHub()
		br.callErrs = map[string]error{methodKiroConfigTemplate: errors.New("kas gone")}
		h.catalog.SetModes([]vibekit.SessionMode{{ID: "reviewer", Name: "reviewer", Source: "workspace"}})
		h.catalog.SetModels([]vibekit.SessionModel{{ID: "m-live", Name: "Live"}})

		got := serve(t, h)

		if len(got.Modes) != 1 || got.Modes[0].ID != "reviewer" {
			t.Errorf("modes = %+v, want the live catalog's entry despite the template failing", got.Modes)
		}
		if len(got.Models) != 1 || got.Models[0].ID != "m-live" {
			t.Errorf("models = %+v, want the live catalog's entry", got.Models)
		}
	})
}
