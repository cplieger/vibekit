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
	if got.Catalog != vibekit.CatalogReady {
		t.Errorf("catalog = %q, want %q: a decoded `model` option is a catalog KAS answered with",
			got.Catalog, vibekit.CatalogReady)
	}
}

// KAS omits the `model` config option entirely when its model cache holds nothing, so that
// option's PRESENCE is the only signal separating "this is the catalog" from "there was no
// catalog".
func TestTemplateToResponse_DistinguishesAnAbsentModelOptionFromAPopulatedOne(t *testing.T) {
	const present = `{"configOptions": [
	  {"id": "model", "currentValue": "m-1", "options": [{"value": "m-1", "name": "One"}]}
	]}`
	// What KAS really sends for an unresolved cache: no `model` entry, never an empty one.
	const absent = `{"configOptions": [
	  {"id": "effortLevel", "currentValue": "high", "options": [{"value": "high", "name": "High"}]}
	]}`

	verdict := func(raw string) vibekit.CatalogState {
		t.Helper()
		var tpl kasConfigTemplate
		if err := json.Unmarshal([]byte(raw), &tpl); err != nil {
			t.Fatalf("unmarshal fixture: %v", err)
		}
		return templateToResponse(&tpl).Catalog
	}

	if got := verdict(present); got != vibekit.CatalogReady {
		t.Errorf("a template carrying a model option: catalog = %q, want %q", got, vibekit.CatalogReady)
	}
	if got := verdict(absent); got != vibekit.CatalogEmpty {
		t.Errorf("a template with no model option: catalog = %q, want %q. The two outcomes must "+
			"differ, or a client cannot tell a cache that answered nothing from one that answered",
			got, vibekit.CatalogEmpty)
	}
}

// The verdict is the option's PRESENCE, not len(Models): an all-[Deprecated] template is
// still a catalog KAS answered with, and deriving the verdict from the filtered list sends
// the client into a retry loop over a read that can never change.
func TestTemplateToResponse_APresentOptionWhoseEntriesAllFilterOutIsStillReady(t *testing.T) {
	const raw = `{"configOptions": [
	  {"id": "model", "currentValue": "m-old", "options": [
	    {"value": "m-old", "name": "Old", "description": "[Deprecated] gone"}
	  ]}
	]}`
	var tpl kasConfigTemplate
	if err := json.Unmarshal([]byte(raw), &tpl); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	got := templateToResponse(&tpl)
	if len(got.Models) != 0 {
		t.Fatalf("models = %+v, want the deprecated entry filtered out", got.Models)
	}
	if got.Catalog != vibekit.CatalogReady {
		t.Errorf("catalog = %q, want %q", got.Catalog, vibekit.CatalogReady)
	}
}

func TestTemplateToResponseEmpty(t *testing.T) {
	got := templateToResponse(&kasConfigTemplate{})
	if got.DefaultModel != "" || len(got.Modes) != 0 || len(got.Models) != 0 {
		t.Errorf("empty template must yield empty catalog: %+v", got)
	}
	// Arrays stay non-null so the client can index without null checks.
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	if s != `{"catalog":"empty","modes":[],"models":[],"effort_levels":[]}` {
		t.Errorf("empty response JSON: %s", s)
	}
}

// The client keeps static fallbacks for a 200 carrying empty lists, so a failure here is
// INVISIBLE in the UI and the log line is the only evidence — which also means a guard
// flipped to fire on success reports a broken catalog on every page load.
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

	serve := func(t *testing.T, h *Runtime) vibekit.ConfigTemplateResponse {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/config-template", nil)
		rec := httptest.NewRecorder()
		h.handleConfigTemplate(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: the client has no error path here, it reads the body",
				rec.Code)
		}
		var got vibekit.ConfigTemplateResponse
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
		if got.Catalog != vibekit.CatalogReady || got.CatalogReason != "" {
			t.Errorf("catalog = %q/%q, want %q with no reason", got.Catalog, got.CatalogReason,
				vibekit.CatalogReady)
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
		if got.Catalog != vibekit.CatalogUnavailable || got.CatalogReason != vibekit.CatalogReasonRPC {
			t.Errorf("catalog = %q/%q, want %q/%q: a failed read is the one outcome retrying can fix",
				got.Catalog, got.CatalogReason, vibekit.CatalogUnavailable, vibekit.CatalogReasonRPC)
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
		if got.Catalog != vibekit.CatalogUnavailable || got.CatalogReason != vibekit.CatalogReasonDecode {
			t.Errorf("catalog = %q/%q, want %q/%q: a contract that moved is a different failure "+
				"from a bridge that died, and only one of them is worth retrying",
				got.Catalog, got.CatalogReason, vibekit.CatalogUnavailable, vibekit.CatalogReasonDecode)
		}
		const wantLine = "config template decode failed"
		if out := logs.String(); !strings.Contains(out, `"msg":"`+wantLine+`"`) {
			t.Errorf("a template vibekit could not read said nothing; want a line reading %q. "+
				"Got: %s", wantLine, out)
		}
	})
}

// Asserted on the raw BYTES, because decoding into the struct cannot see this
// (len(nil) == 0): a degrade branch omitting a slice puts `null` on the wire where success
// puts `[]`, and a generated decoder requiring an array fails on the failure path alone.
func TestHandleConfigTemplate_EveryBodyKeepsItsArraysNonNull(t *testing.T) {
	cases := map[string]func(*fakeBridge){
		"an unreachable bridge": func(br *fakeBridge) {
			br.callErrs = map[string]error{methodKiroConfigTemplate: errors.New("kas gone")}
		},
		"an undecodable template": func(br *fakeBridge) {
			br.callResults = map[string]json.RawMessage{
				methodKiroConfigTemplate: json.RawMessage(`["not the template shape"]`),
			}
		},
		"an empty template": func(br *fakeBridge) {
			br.callResults = map[string]json.RawMessage{
				methodKiroConfigTemplate: json.RawMessage(`{}`),
			}
		},
	}
	for name, arm := range cases {
		t.Run(name, func(t *testing.T) {
			_ = captureLogs(t)
			h, _, br := newTestHub()
			arm(br)

			req := httptest.NewRequest(http.MethodGet, "/api/config-template", nil)
			rec := httptest.NewRecorder()
			h.handleConfigTemplate(rec, req)

			body := rec.Body.String()
			for _, key := range []string{"modes", "models", "effort_levels"} {
				if !strings.Contains(body, `"`+key+`":[]`) {
					t.Errorf("body has no empty %s array: %s", key, body)
				}
			}
			if strings.Contains(body, "null") {
				t.Errorf("body carries a null: %s", body)
			}
		})
	}
}
