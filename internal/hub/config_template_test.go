package hub

import (
	"encoding/json"
	"testing"
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
