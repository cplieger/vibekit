package translate

import (
	"context"
	"testing"
)

// configModelUpdate builds a config_option_update payload carrying a "model"
// select option whose choices optionally stamp _meta.kiro.hasEffort — the
// shape KAS sends (each model choice gets `_meta: { kiro: { hasEffort } }`
// when the model has effort levels or a rate multiplier).
func configModelUpdate(t *testing.T, current string, choices []map[string]any) []byte {
	t.Helper()
	return mustJSON(t, map[string]any{
		"configOptions": []map[string]any{
			{
				"id":           "model",
				"category":     "model",
				"type":         "select",
				"currentValue": current,
				"options":      choices,
			},
		},
	})
}

// TestHandleConfigOptionUpdate_PlumbsHasEffort pins the server half of the
// model-aware effort gate: the per-model _meta.kiro.hasEffort KAS stamps on
// each config-catalog model choice must land on SessionModel.HasEffort, so the
// client picker can hide the effort row for a model that doesn't support it.
// A choice with no _meta (KAS omits it when the model has neither a rate
// multiplier nor effort) decodes as HasEffort=false.
func TestHandleConfigOptionUpdate_PlumbsHasEffort(t *testing.T) {
	deps, _, store := depsWithStore(t, "c1")
	tr := New(deps, "/tmp")

	raw := configModelUpdate(t, "model-a", []map[string]any{
		{"value": "model-a", "name": "Model A", "_meta": map[string]any{"kiro": map[string]any{"hasEffort": true, "rateMultiplier": 1.0}}},
		{"value": "model-b", "name": "Model B", "_meta": map[string]any{"kiro": map[string]any{"hasEffort": false, "rateMultiplier": 2.0}}},
		{"value": "model-c", "name": "Model C"}, // no _meta at all
	})

	tr.HandleConfigOptionUpdate(context.Background(), "c1", raw)

	c, ok := store.Get(context.Background(), "c1")
	if !ok {
		t.Fatal("chat c1 missing after config_option_update")
	}
	if c.Model != "model-a" {
		t.Errorf("current model = %q, want model-a", c.Model)
	}
	want := map[string]bool{"model-a": true, "model-b": false, "model-c": false}
	if len(c.AvailableModels) != len(want) {
		t.Fatalf("AvailableModels len = %d, want %d: %+v", len(c.AvailableModels), len(want), c.AvailableModels)
	}
	for _, m := range c.AvailableModels {
		exp, known := want[m.ID]
		if !known {
			t.Errorf("unexpected model %q in catalog", m.ID)
			continue
		}
		if m.HasEffort != exp {
			t.Errorf("model %q HasEffort = %v, want %v", m.ID, m.HasEffort, exp)
		}
	}
}

// TestChoiceHasEffort covers the _meta.kiro.hasEffort extractor directly,
// including the absent-meta and malformed-meta paths (both → false).
func TestChoiceHasEffort(t *testing.T) {
	cases := []struct {
		name string
		meta string
		want bool
	}{
		{"nil meta", "", false},
		{"empty object", `{}`, false},
		{"kiro.hasEffort true", `{"kiro":{"hasEffort":true}}`, true},
		{"kiro.hasEffort false", `{"kiro":{"hasEffort":false}}`, false},
		{"kiro without hasEffort", `{"kiro":{"rateMultiplier":2}}`, false},
		{"malformed", `{not json`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var meta []byte
			if tc.meta != "" {
				meta = []byte(tc.meta)
			}
			if got := choiceHasEffort(meta); got != tc.want {
				t.Errorf("choiceHasEffort(%q) = %v, want %v", tc.meta, got, tc.want)
			}
		})
	}
}
