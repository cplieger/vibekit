package translate

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
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
	tr := New(deps)

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

// --- session_info_update: the unconsumed-sub-kind fallback ---

// infoKindFrame builds a session_info_update whose _meta.kiro carries only a
// kind — no focus, summarization, promptTurnSummaries or contextUsage block —
// which is the shape every sub-kind vibekit does not consume arrives in.
func infoKindFrame(t *testing.T, kind string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"_meta": map[string]any{"kiro": map[string]any{"kind": kind}},
	})
	if err != nil {
		t.Fatalf("marshal info frame: %v", err)
	}
	return b
}

// TestSessionInfoUpdate_UnknownKindWarns pins the observability contract on
// session_info_update, which is a CARRIER multiplexing 22+ sub-kinds under
// one wire type.
//
// The dispatch cascade keys on which sub-BLOCK is present, not on the kind
// string, so everything vibekit does not consume falls through to the end.
// That is fine for the 20-odd kinds we ignore on purpose and NOT fine for a
// sub-kind KAS adds later: the failure mode of a multiplexed carrier is that
// new payloads vanish leaving no trace at all. So an unrecognised kind must
// reach the log at Warn, while a known-but-ignored one must not (it would be
// noise on every turn — `turn_start` alone fires once per prompt).
//
// Asserted through captured slog output because a log line IS the behaviour
// here; there is no domain event to observe. Serial by necessity: slog's
// default is process-global.
func TestSessionInfoUpdate_UnknownKindWarns(t *testing.T) {
	tests := []struct {
		name     string
		kind     string
		wantWarn bool
	}{
		{name: "a kind KAS added since this was written", kind: "quantum_entanglement_update", wantWarn: true},
		{name: "known and deliberately ignored", kind: "turn_start", wantWarn: false},
		{name: "known compaction marker", kind: "summarization_separator", wantWarn: false},
		{name: "known, carries the persisted permission history", kind: "pending_interaction", wantWarn: false},
		{name: "reaches the wire via SessionInfoEmitter, not a build call site", kind: "repositories_update", wantWarn: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			restore := captureSlog(&buf)
			defer restore()

			deps, _, _ := depsWithStore(t, "c1")
			New(deps).HandleSessionInfoUpdate(context.Background(), "c1", infoKindFrame(t, tt.kind), "")

			out := buf.String()
			gotWarn := strings.Contains(out, "level=WARN") && strings.Contains(out, "UNKNOWN kind")
			if gotWarn != tt.wantWarn {
				t.Errorf("kind %q: warned = %v, want %v (log was: %s)", tt.kind, gotWarn, tt.wantWarn, out)
			}
			if !strings.Contains(out, tt.kind) {
				t.Errorf("kind %q never reached the log at any level; a dropped sub-kind must leave a trace. log was: %s",
					tt.kind, out)
			}
		})
	}
}

// TestSessionInfoUpdate_NoKindIsSilent pins that a frame carrying no kind at
// all logs nothing. Without this guard the fallback would fire on every
// well-formed frame whose sub-block the cascade already consumed and
// returned early from — and on any frame KAS sends with an absent kind.
func TestSessionInfoUpdate_NoKindIsSilent(t *testing.T) {
	var buf bytes.Buffer
	restore := captureSlog(&buf)
	defer restore()

	deps, _, _ := depsWithStore(t, "c1")
	New(deps).HandleSessionInfoUpdate(context.Background(), "c1",
		json.RawMessage(`{"_meta":{"kiro":{}}}`), "")

	if out := buf.String(); out != "" {
		t.Errorf("a kindless session_info_update logged %q, want silence", out)
	}
}
