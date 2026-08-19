package vibekit

import (
	"encoding/json"
	"testing"
)

// The permission reply is the ONE wire shape a turn approval shares with an
// ordinary tool permission, so these tests pin both directions of that overlap:
// an ordinary reply must not grow a `_meta` it never had, and an approval's
// decisions must land at exactly `_meta.kiro.fileDecisions` — the path KAS
// reads. A misspelled key here does not fail: KAS finds no decisions, treats
// every action as unaccepted, and rolls back the whole turn the user just
// approved.

func TestPermissionOutcomeSelected_HasNoMeta(t *testing.T) {
	got := mustMarshal(t, PermissionOutcomeSelected("allow_once"))
	if _, ok := got["_meta"]; ok {
		t.Errorf("ordinary permission reply carries _meta: %v", got)
	}
	outcome, ok := got["outcome"].(map[string]any)
	if !ok {
		t.Fatalf("outcome is %T, want object", got["outcome"])
	}
	if outcome["outcome"] != "selected" || outcome["optionId"] != "allow_once" {
		t.Errorf("outcome = %v, want selected/allow_once", outcome)
	}
}

func TestPermissionOutcomeWithFileDecisions_LandsUnderMetaKiro(t *testing.T) {
	got := mustMarshal(t, PermissionOutcomeWithFileDecisions("accept", map[string]bool{
		"act-1": true,
		"act-2": false,
	}))

	meta, ok := got["_meta"].(map[string]any)
	if !ok {
		t.Fatalf("_meta is %T, want object: %v", got["_meta"], got)
	}
	kiro, ok := meta["kiro"].(map[string]any)
	if !ok {
		t.Fatalf("_meta.kiro is %T, want object", meta["kiro"])
	}
	decisions, ok := kiro["fileDecisions"].(map[string]any)
	if !ok {
		t.Fatalf("_meta.kiro.fileDecisions is %T, want object", kiro["fileDecisions"])
	}
	if decisions["act-1"] != true || decisions["act-2"] != false {
		t.Errorf("fileDecisions = %v, want act-1 true / act-2 false", decisions)
	}
	// A false decision must be PRESENT, not omitted: KAS restores anything not
	// in the accepted set, so `omitempty` on the value would be indistinguishable
	// from a reject by accident rather than by choice.
	if len(decisions) != 2 {
		t.Errorf("fileDecisions has %d entries, want 2 (a false verdict must be sent, not dropped)", len(decisions))
	}
	// The option id still rides the ordinary place.
	outcome, _ := got["outcome"].(map[string]any)
	if outcome["optionId"] != "accept" {
		t.Errorf("optionId = %v, want accept", outcome["optionId"])
	}
}

// An empty map means "this was not a turn approval", so the reply must be
// byte-identical to the ordinary one. Emitting an empty _meta.kiro object would
// put a turn-approval envelope on every permission answer in the app.
func TestPermissionOutcomeWithFileDecisions_EmptyMapOmitsMeta(t *testing.T) {
	for name, decisions := range map[string]map[string]bool{
		"nil":   nil,
		"empty": {},
	} {
		t.Run(name, func(t *testing.T) {
			withDecisions := mustMarshalBytes(t, PermissionOutcomeWithFileDecisions("allow_once", decisions))
			plain := mustMarshalBytes(t, PermissionOutcomeSelected("allow_once"))
			if string(withDecisions) != string(plain) {
				t.Errorf("reply = %s, want identical to the plain reply %s", withDecisions, plain)
			}
		})
	}
}

func TestPermissionOutcomeCancelled_IsCancelled(t *testing.T) {
	got := mustMarshal(t, PermissionOutcomeCancelled())
	outcome, ok := got["outcome"].(map[string]any)
	if !ok {
		t.Fatalf("outcome is %T, want object", got["outcome"])
	}
	if outcome["outcome"] != string(StopReasonCancelled) {
		t.Errorf("outcome = %v, want %q", outcome["outcome"], StopReasonCancelled)
	}
	if _, ok := outcome["optionId"]; ok {
		t.Errorf("cancelled reply carries an optionId: %v", outcome)
	}
}

func mustMarshalBytes(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func mustMarshal(t *testing.T, v any) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(mustMarshalBytes(t, v), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}
