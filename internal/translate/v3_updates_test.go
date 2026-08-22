package translate

import (
	"bytes"
	"encoding/json"
	"slices"
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
	tr := New(rolesOf(deps))

	raw := configModelUpdate(t, "model-a", []map[string]any{
		{"value": "model-a", "name": "Model A", "_meta": map[string]any{"kiro": map[string]any{"hasEffort": true, "rateMultiplier": 1.0}}},
		{"value": "model-b", "name": "Model B", "_meta": map[string]any{"kiro": map[string]any{"hasEffort": false, "rateMultiplier": 2.0}}},
		{"value": "model-c", "name": "Model C"}, // no _meta at all
	})

	tr.HandleConfigOptionUpdate(t.Context(), "c1", raw)

	c, ok := store.Get(t.Context(), "c1")
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

// configEffortUpdate builds a config_option_update carrying the `effortLevel`
// option — the tier vocabulary plus the level the session runs at. This is the
// option kiro-cli's own TUI builds its effort picker from; there is no per-model
// tier list on the wire.
func configEffortUpdate(t *testing.T, current string, choices []map[string]any) []byte {
	t.Helper()
	return mustJSON(t, map[string]any{
		"configOptions": []map[string]any{
			{
				"id":           "effortLevel",
				"type":         "select",
				"currentValue": current,
				"options":      choices,
			},
		},
	})
}

// TestHandleConfigOptionUpdate_PlumbsEffortOption pins the fix for a control that
// rendered five tiers with none marked: the effortLevel option's own choices are
// the tier list, and its currentValue is the level the session is RUNNING at,
// which is what the UI marks for a chat that has chosen nothing of its own.
func TestHandleConfigOptionUpdate_PlumbsEffortOption(t *testing.T) {
	deps, _, store := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))

	raw := configEffortUpdate(t, "medium", []map[string]any{
		{"value": "low", "name": "Low"},
		{"value": "medium", "name": "Medium"},
		{"value": "high", "name": "High"},
	})
	tr.HandleConfigOptionUpdate(t.Context(), "c1", raw)

	c, ok := store.Get(t.Context(), "c1")
	if !ok {
		t.Fatal("chat c1 missing after config_option_update")
	}
	if c.EffortActive != "medium" {
		t.Errorf("EffortActive = %q, want medium", c.EffortActive)
	}
	wantIDs := []string{"low", "medium", "high"}
	gotIDs := make([]string, 0, len(c.EffortLevels))
	for _, l := range c.EffortLevels {
		gotIDs = append(gotIDs, l.ID)
	}
	if !slices.Equal(gotIDs, wantIDs) {
		t.Errorf("EffortLevels = %v, want %v", gotIDs, wantIDs)
	}
	if len(c.EffortLevels) > 0 && c.EffortLevels[0].Name != "Low" {
		t.Errorf("level name = %q, want Low", c.EffortLevels[0].Name)
	}
	// The chat's own CHOICE is untouched: the option reports what the session is
	// doing, and adopting it as the choice would pin a service default into every
	// later session through StartOpts.Effort.
	if c.Effort != "" {
		t.Errorf("Effort = %q, want empty (the option is not a choice)", c.Effort)
	}
}

// TestHandleConfigOptionUpdate_EmptyEffortOptionApplies covers the answer that is
// not a missing answer: kiro-cli reports an EMPTY option list for a model with no
// effort tiers, and its own TUI reads that as "effort is not available on the
// current model". So an empty list has to land, or a model without tiers keeps
// showing the previous model's.
func TestHandleConfigOptionUpdate_EmptyEffortOptionApplies(t *testing.T) {
	deps, _, store := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))

	tr.HandleConfigOptionUpdate(t.Context(), "c1",
		configEffortUpdate(t, "high", []map[string]any{{"value": "high", "name": "High"}}))
	tr.HandleConfigOptionUpdate(t.Context(), "c1", configEffortUpdate(t, "", nil))

	c, _ := store.Get(t.Context(), "c1")
	if len(c.EffortLevels) != 0 {
		t.Errorf("EffortLevels = %v, want empty", c.EffortLevels)
	}
	if c.EffortActive != "" {
		t.Errorf("EffortActive = %q, want empty", c.EffortActive)
	}
}

// TestChoiceEffort covers the _meta.kiro effort extractor on a model choice.
// Two fields only, and the tier LIST is deliberately not one of them: kiro-cli
// 2.18.0 stamps `defaultEffortLevel` per model and stamps no `hasEffort` at all
// (measured against the shipped chat sidecar), and the tiers belong to the
// `effortLevel` option. Absent and malformed meta both decode to the zero value,
// which the client reads as "not plumbed".
func TestChoiceEffort(t *testing.T) {
	cases := []struct {
		name        string
		meta        string
		wantHas     bool
		wantDefault string
	}{
		{name: "nil meta"},
		{name: "empty object", meta: `{}`},
		{name: "hasEffort only", meta: `{"kiro":{"hasEffort":true}}`, wantHas: true},
		{name: "hasEffort false", meta: `{"kiro":{"hasEffort":false}}`},
		{name: "kiro without hasEffort", meta: `{"kiro":{"rateMultiplier":2}}`},
		{
			// The 2.18.0 shape: a default tier, no capability flag. Opus 4.7's
			// service default is xhigh, which other models do not even offer.
			name:        "default tier, no hasEffort",
			meta:        `{"kiro":{"rateMultiplier":1,"effortSchemaPath":"reasoning","defaultEffortLevel":"xhigh"}}`,
			wantDefault: "xhigh",
		},
		{name: "malformed", meta: `{not json`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var meta []byte
			if tc.meta != "" {
				meta = []byte(tc.meta)
			}
			got := choiceEffort(meta)
			if got.HasEffort != tc.wantHas {
				t.Errorf("HasEffort = %v, want %v", got.HasEffort, tc.wantHas)
			}
			if got.Default != tc.wantDefault {
				t.Errorf("Default = %q, want %q", got.Default, tc.wantDefault)
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
			New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), "c1", infoKindFrame(t, tt.kind), "")

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
	New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), "c1",
		json.RawMessage(`{"_meta":{"kiro":{}}}`), "")

	if out := buf.String(); out != "" {
		t.Errorf("a kindless session_info_update logged %q, want silence", out)
	}
}

// TestMaterialPctDelta pins the gate that decides whether a context-percentage
// move is worth a full transcript rewrite. The exact-inequality gate this
// replaced meant KAS's per-model-response frames each rewrote the whole chat
// file: roughly 40 load-Unmarshal-Marshal-fsync cycles for a 20-tool-call turn,
// serialized on the per-chat mutex, on files measured up to 21 MB.
//
// Two properties have to hold together, which is why the tier cases are here
// rather than folded into the epsilon cases: a move under one point is dropped
// BECAUSE the ring cannot render it, and a move that crosses 80 or 95 is kept
// even when it is tiny, because the tier is what the UI colours on. An
// epsilon-only gate would round away exactly the crossing that matters.
func TestMaterialPctDelta(t *testing.T) {
	cases := map[string]struct {
		old, new float64
		want     bool
	}{
		"identical":                      {50, 50, false},
		"sub-point up is not material":   {50, 50.4, false},
		"sub-point down is not material": {50, 49.6, false},
		"exactly one point up":           {50, 51, true},
		"exactly one point down":         {51, 50, true},
		"large jump":                     {10, 90, true},
		// The tiers are vibekit's OWN client thresholds, not KAS's: 70 and 90 are
		// where status.ts recolours the context ring, 95 is context-ui.ts's
		// DEFAULT_CUTOFF_PCT where the composer stops accepting input. An earlier
		// revision used KAS's 80/95 TUI boundaries, which this client never
		// renders, so a sub-point crossing of 70, 90 or 95 could be rounded away
		// — including the one that disables the composer.
		"tiny move crossing 70":              {69.9, 70.0, true},
		"tiny move crossing 70 downward":     {70.0, 69.9, true},
		"tiny move crossing 90":              {89.9, 90.0, true},
		"tiny move crossing 95":              {94.9, 95.0, true},
		"tiny move crossing 95 downward":     {95.0, 94.9, true},
		"tiny move inside the warning band":  {75.0, 75.2, false},
		"tiny move inside the critical band": {91.0, 91.3, false},
		"tiny move above the cutoff":         {96.0, 96.3, false},
		// 80 is KAS's boundary and NOT one of vibekit's, so a sub-point move
		// across it is correctly ignored. This row is what would fail if someone
		// reinstated KAS's tiers here.
		"tiny move crossing KAS's 80 is not material": {79.9, 80.0, false},
		"from zero is material":                       {0, 1, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := materialPctDelta(tc.old, tc.new); got != tc.want {
				t.Errorf("materialPctDelta(%v, %v) = %v, want %v", tc.old, tc.new, got, tc.want)
			}
		})
	}
}

// turnSummaryInfo builds a session_info_update carrying the turn-end metering
// block: the promptTurnSummaries list plus elapsedTime in milliseconds. A
// summary entry with an empty unit is spelled by omitting the key, which is
// what KAS does when it reports the default dimension.
func turnSummaryInfo(t *testing.T, elapsedMs float64, summaries []map[string]any) json.RawMessage {
	t.Helper()
	return mustJSON(t, map[string]any{
		"_meta": map[string]any{"kiro": map[string]any{
			"promptTurnSummaries": summaries,
			"elapsedTime":         elapsedMs,
		}},
	})
}

// contextUsageInfo builds a session_info_update carrying a context percentage
// on one of the two channels KAS mirrors it across: the contextUsage
// sub-block, or the bare _meta.kiro.usagePercentage.
func contextUsageInfo(t *testing.T, key string, pct float64) json.RawMessage {
	t.Helper()
	kiro := map[string]any{}
	switch key {
	case "contextUsage":
		kiro["contextUsage"] = map[string]any{"usagePercentage": pct}
	case "usagePercentage":
		kiro["usagePercentage"] = pct
	}
	return mustJSON(t, map[string]any{"_meta": map[string]any{"kiro": kiro}})
}

// KAS mirrors the context percentage across two channels — the contextUsage
// sub-block and a bare usagePercentage — and whichever arrives has to keep the
// ring fresh. Reading only one of them leaves the context popup at zero for
// every frame KAS happened to send the other way.
func TestHandleSessionInfoUpdate_ContextPctArrivesOnEitherChannel(t *testing.T) {
	for _, key := range []string{"contextUsage", "usagePercentage"} {
		t.Run(key, func(t *testing.T) {
			deps, _, store := depsWithStore(t, "c1")
			tr := New(rolesOf(deps))

			tr.HandleSessionInfoUpdate(t.Context(), "c1", contextUsageInfo(t, key, 42.5), "")

			c, ok := store.Get(t.Context(), "c1")
			if !ok {
				t.Fatal("chat c1 missing after session_info_update")
			}
			if c.Usage.ContextPct != 42.5 {
				t.Errorf("Usage.ContextPct after a %s frame = %v, want 42.5", key, c.Usage.ContextPct)
			}
			if !c.Usage.HasRealData {
				t.Errorf("Usage.HasRealData after a %s frame = false, want true", key)
			}
		})
	}
}

// The metering summary counts the default dimension as spend: KAS reports
// credits either as unit "credit" or with the unit key absent, and both are the
// same money. Counting only the spelled-out form makes a chat's credit readout
// silently stop at whatever the last labelled frame said.
func TestPersistTurnSummary_AnEmptyUnitCountsAsCredits(t *testing.T) {
	for _, unit := range []string{"", "credit"} {
		t.Run("unit_"+unit, func(t *testing.T) {
			deps, _, store := depsWithStore(t, "c1")
			tr := New(rolesOf(deps))
			summary := map[string]any{"usage": 0.5}
			if unit != "" {
				summary["unit"] = unit
			}

			tr.HandleSessionInfoUpdate(t.Context(), "c1", turnSummaryInfo(t, 1200, []map[string]any{summary}), "")

			c, ok := store.Get(t.Context(), "c1")
			if !ok {
				t.Fatal("chat c1 missing after session_info_update")
			}
			if c.Usage.Credits != 0.5 {
				t.Errorf("Usage.Credits after a summary with unit %q = %v, want 0.5", unit, c.Usage.Credits)
			}
			if !c.Usage.HasRealData {
				t.Errorf("Usage.HasRealData after a summary with unit %q = false, want true", unit)
			}
		})
	}
}

// A turn that reported no elapsed time leaves the previous duration alone.
// "Last turn took 0 ms" is not an answer the popup can render, and overwriting
// a real measurement with it loses the only duration the chat had.
func TestPersistTurnSummary_ZeroElapsedKeepsThePreviousDuration(t *testing.T) {
	deps, _, store := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))
	credit := []map[string]any{{"unit": "credit", "usage": 0.25}}

	tr.HandleSessionInfoUpdate(t.Context(), "c1", turnSummaryInfo(t, 1200, credit), "")
	tr.HandleSessionInfoUpdate(t.Context(), "c1", turnSummaryInfo(t, 0, credit), "")

	c, ok := store.Get(t.Context(), "c1")
	if !ok {
		t.Fatal("chat c1 missing after session_info_update")
	}
	if c.Usage.LastTurnMs != 1200 {
		t.Errorf("Usage.LastTurnMs after a zero-elapsed turn = %v, want 1200 (the previous measurement)", c.Usage.LastTurnMs)
	}
	if c.Usage.TurnCount != 2 {
		t.Errorf("Usage.TurnCount = %d, want 2 (both turns counted)", c.Usage.TurnCount)
	}
}

// A turn that spent nothing is not evidence of real spend. HasRealData is what
// switches the context popup from "unknown" to a figure, so flipping it on a
// zero-credit summary reports a measured 0.00 the account never confirmed.
func TestPersistTurnSummary_ZeroCreditsIsNotRealSpend(t *testing.T) {
	deps, _, store := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))

	tr.HandleSessionInfoUpdate(t.Context(), "c1",
		turnSummaryInfo(t, 1200, []map[string]any{{"unit": "credit", "usage": 0.0}}), "")

	c, ok := store.Get(t.Context(), "c1")
	if !ok {
		t.Fatal("chat c1 missing after session_info_update")
	}
	if c.Usage.Credits != 0 {
		t.Errorf("Usage.Credits after a zero-credit turn = %v, want 0", c.Usage.Credits)
	}
	if c.Usage.HasRealData {
		t.Error("Usage.HasRealData after a zero-credit turn = true, want false (nothing was spent)")
	}
	if c.Usage.TurnCount != 1 {
		t.Errorf("Usage.TurnCount = %d, want 1 (the turn still happened)", c.Usage.TurnCount)
	}
}

// An effort-only config frame must leave the model catalog standing. KAS sends
// the two selects independently, so treating a frame with no model option as
// "the model list is now empty" empties the picker the moment the user changes
// effort.
func TestHandleConfigOptionUpdate_EffortOnlyFrameKeepsTheModelCatalog(t *testing.T) {
	deps, _, store := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))

	tr.HandleConfigOptionUpdate(t.Context(), "c1", configModelUpdate(t, "model-a", []map[string]any{
		{"value": "model-a", "name": "Model A"},
		{"value": "model-b", "name": "Model B"},
	}))
	tr.HandleConfigOptionUpdate(t.Context(), "c1", configEffortUpdate(t, "high", []map[string]any{
		{"value": "high", "name": "High"},
	}))

	c, ok := store.Get(t.Context(), "c1")
	if !ok {
		t.Fatal("chat c1 missing after config_option_update")
	}
	gotIDs := make([]string, 0, len(c.AvailableModels))
	for _, m := range c.AvailableModels {
		gotIDs = append(gotIDs, m.ID)
	}
	if !slices.Equal(gotIDs, []string{"model-a", "model-b"}) {
		t.Errorf("AvailableModels after an effort-only frame = %v, want [model-a model-b]", gotIDs)
	}
	if c.EffortActive != "high" {
		t.Errorf("EffortActive = %q, want high (the effort half still applied)", c.EffortActive)
	}
}
